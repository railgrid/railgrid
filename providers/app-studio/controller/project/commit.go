/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/hubmcp"
	"github.com/railgrid/provider-app-studio/internal/codecommit"
	"github.com/railgrid/provider-app-studio/workspace"
)

// Keeping git in step with the workspace.
//
// Edits land in the on-disk workspace (and stream into the dev sandbox)
// immediately; git is the durable copy, and it converges on this reconcile
// loop: the workspace store tracks uncommitted paths, and when the project is
// idle the controller commits them. No change feed, no post-write hook — a
// burst of edits collapses into a single commit, and a commit that fails is
// simply retried next pass. Interactive commits (the assistant's commit tool)
// keep working; both paths share the workspace settlement ledger so neither
// double-commits what the other already settled.
//
// Commits wait for the project to be idle: committing mid-turn would capture
// a half-written app and produce a history nobody wants to read.

// Annotation keys bridging the CR keyspace to the workspace/store keyspace
// (the reconciler only knows the cluster; scopes are keyed by org/workspace
// UUIDs the hub derives from the tenant path). Stamped at project creation.
const (
	orgUUIDAnnotation       = "ai.railgrid.ai/org-uuid"
	workspaceUUIDAnnotation = "ai.railgrid.ai/workspace-uuid"
)

// initializeRepositoryAnnotation asks for the project's existing working copy
// to be queued as the first commit of a repository the user just attached. The
// reconciler clears it once the paths are in the ledger; it is the only record
// that the seeding happened, because the receipt file it replaced lived on one
// replica's volume.
const initializeRepositoryAnnotation = "ai.railgrid.ai/initialize-repository"

// retryPendingCommitAnnotation is an explicit operator signal to replay one
// blocked immutable request. Its value is `<requestID>:<fresh-token>`; the
// request ID fences it to one intent and the token makes each retry distinct.
const retryPendingCommitAnnotation = "ai.railgrid.ai/retry-pending-commit"

const (
	repositoryCommitFailedReason     = "RepositoryCommitFailed"
	repositoryCommitFailedMessage    = "The RepositoryCommit failed. App Studio will automatically retry these dirty source files after a 30-second cooldown."
	repositoryCommitFailedRetryDelay = 30 * time.Second
)

var errPendingRepositoryCommitFailed = errors.New("pending RepositoryCommit failed")

// scopeOf derives the workspace scope from the Project's identity
// annotations. ok is false for legacy Projects created before the
// annotations existed — commit convergence silently skips those.
func scopeOf(p *aiv1alpha1.Project) (workspace.Scope, bool) {
	org := strings.TrimSpace(p.Annotations[orgUUIDAnnotation])
	ws := strings.TrimSpace(p.Annotations[workspaceUUIDAnnotation])
	if org == "" || ws == "" {
		return workspace.Scope{}, false
	}
	return workspace.Scope{
		OrgUUID:       org,
		WorkspaceUUID: ws,
		ProjectName:   p.Name,
		ProjectUID:    string(p.UID),
	}, true
}

// commitOutcome reports what commitWorkspace left behind.
type commitOutcome struct {
	// dirty means uncommitted work remains — waiting on something an event
	// announces (an idle turn, a Ready repository, a landing commit).
	dirty bool
	// retry means work remains that nothing will announce (files beyond one
	// commit's bounds, a failed call): the caller requeues with backoff.
	retry bool
	// retryAfter schedules a terminal failed RepositoryCommit after its
	// persisted cooldown, even if watch events enqueue this Project meanwhile.
	retryAfter time.Duration
}

// commitWorkspace pushes dirty workspace files to git when the project is
// idle. Two clients, as everywhere in this package: c is the manager's client
// over this provider's APIExport virtual workspace, which owns the Project,
// is where the pending-commit pointer is written, and — since RepositoryCommit
// became a claimed kind — is also where an in-flight commit is read. tc is the
// tenant-workspace client authenticated as the project identity, used for the
// one thing a claim cannot give: reading the workspace's APIBinding to address
// the Code provider, and carrying token on the action call itself.
//
// Asking for the commit is the one thing here that is not a CR write. The
// reconciler first stages an immutable bundle in the Code provider, then
// reserves its request ID, bundle, and source snapshot in the Project ledger,
// and only then invokes the keyed `repositories/{name}/commit/v1` action. If
// its response is lost, the same request ID and bundle are replayed; the Code
// provider returns the same RepositoryCommit. That CR is the pointer this
// loop follows over the watch, which is why the declared composition on
// repositorycommits still carries no create, and why the verb itself is
// claimed instead (repositories/commit in manifest.yaml spec.requires).
func (r *Reconciler) commitWorkspace(ctx context.Context, c client.Client, p *aiv1alpha1.Project, repo *unstructured.Unstructured) (commitOutcome, error) {
	if r.Workspace == nil || r.Callers == nil {
		return commitOutcome{}, nil // commit convergence not wired (REST-only dev)
	}
	b := p.Spec.Repository
	scope, ok := scopeOf(p)
	if !ok {
		return commitOutcome{}, nil // legacy project without identity annotations
	}

	// An explicitly attached repository gets the project's existing files as
	// its first commit. The annotation is the once-only record — it used to be
	// a receipt file on the workspace volume, which a replica without that
	// volume could not see — so it is cleared as soon as the paths are in the
	// ledger, and the union it performs is idempotent if this pass is retried
	// before the clear lands.
	if b != nil && strings.TrimSpace(b.RepositoryRef) != "" && p.Annotations[initializeRepositoryAnnotation] == b.RepositoryRef {
		if err := r.Workspace.InitializeRepositorySource(ctx, scope); err != nil {
			return commitOutcome{dirty: true}, fmt.Errorf("initialize repository source: %w", err)
		}
		if c != nil {
			if err := patchProjectAnnotation(ctx, c, p, initializeRepositoryAnnotation, ""); err != nil {
				return commitOutcome{dirty: true}, fmt.Errorf("clear repository initialization request: %w", err)
			}
		}
	}

	// Heal a settlement another writer recorded but could not reconcile
	// (e.g. the assistant crashed between commit and ledger settle).
	if _, err := r.Workspace.ReconcileCommitSettlement(ctx, scope); err != nil {
		return commitOutcome{dirty: true}, fmt.Errorf("reconcile commit settlement: %w", err)
	}

	if r.Owns != nil && !r.Owns(scope) {
		// Another replica owns this project's workspace; whatever this
		// replica's tree holds is a leftover from a previous ownership term
		// and must not be committed over the live owner's work.
		return commitOutcome{}, nil
	}

	// A reserved commit intent is completed from its immutable Code bundle,
	// even if the original action response was lost. Newer workspace edits wait
	// until that one intent settles.
	pending, hasPending, err := r.Workspace.PendingCommit(ctx, scope)
	if err != nil {
		return commitOutcome{dirty: true}, fmt.Errorf("read pending commit: %w", err)
	}
	if hasPending && pending.BlockedReason == repositoryCommitFailedReason {
		return r.failedPendingCommitCooldown(ctx, c, p, repo, scope, pending)
	}
	if hasPending {
		if pending.BlockedReason != "" {
			token := pendingCommitRetryToken(p, pending)
			if token != "" && token != pending.RetryToken {
				var retried bool
				if pending.Name == "" {
					retried, err = r.Workspace.RetryBlockedPendingCommit(ctx, scope, pending.RequestID, token)
				} else {
					retried, err = r.Workspace.RetryNamedBlockedPendingCommit(ctx, scope, pending.RequestID, pending.Name, token)
				}
				if err != nil {
					return commitOutcome{dirty: true, retry: true}, fmt.Errorf("acknowledge pending commit retry: %w", err)
				}
				if !retried {
					return commitOutcome{dirty: true}, nil // a concurrent settlement or newer request won
				}
				pending, hasPending, err = r.Workspace.PendingCommit(ctx, scope)
				if err != nil {
					return commitOutcome{dirty: true, retry: true}, fmt.Errorf("read retried pending commit: %w", err)
				}
				if !hasPending {
					return commitOutcome{dirty: true}, nil
				}
			} else if pending.Name == "" {
				return commitOutcome{dirty: true}, nil
			} else {
				// A named block still watches its RepositoryCommit. If the same CR
				// reappears, settle it even without an operator retry; if it remains
				// gone, do not replay the expired bundle automatically.
				resolved, err := r.resolvePendingCommit(ctx, c, p, repo, scope, pending)
				if errors.Is(err, errPendingRepositoryCommitFailed) {
					return r.failedPendingCommitCooldown(ctx, c, p, repo, scope, pending)
				}
				if err != nil {
					return commitOutcome{dirty: true}, err
				}
				if resolved {
					return commitOutcome{dirty: true, retry: true}, nil
				}
				return commitOutcome{dirty: true}, nil
			}
		}
		if pending.RequestID != "" && pending.Name == "" {
			if err := r.ensurePendingCommit(ctx, p, repo, scope, pending); err != nil {
				if blocked, blockErr := r.blockPendingCommitReplay(ctx, scope, pending, err); blocked {
					if blockErr != nil {
						return commitOutcome{dirty: true, retry: true}, blockErr
					}
					return commitOutcome{dirty: true}, nil
				}
				return commitOutcome{dirty: true, retry: true}, fmt.Errorf("resume reserved commit: %w", err)
			}
			return commitOutcome{dirty: true}, nil // wait for the RepositoryCommit watch
		}
		resolved, err := r.resolvePendingCommit(ctx, c, p, repo, scope, pending)
		if errors.Is(err, errPendingRepositoryCommitFailed) {
			return r.failedPendingCommitCooldown(ctx, c, p, repo, scope, pending)
		}
		if err != nil {
			if blocked, blockErr := r.blockPendingCommitReplay(ctx, scope, pending, err); blocked {
				if blockErr != nil {
					return commitOutcome{dirty: true, retry: true}, blockErr
				}
				return commitOutcome{dirty: true}, nil
			}
		}
		if err != nil || !resolved {
			return commitOutcome{dirty: true}, err
		}
	}
	if b == nil || strings.TrimSpace(b.RepositoryRef) == "" {
		return commitOutcome{}, nil
	}

	sourceRevision, err := r.Workspace.SourceRevision(ctx, scope)
	if err != nil {
		return commitOutcome{dirty: true}, fmt.Errorf("read workspace source revision: %w", err)
	}
	paths, err := r.Workspace.UncommittedPaths(ctx, scope)
	if err != nil {
		return commitOutcome{dirty: true}, fmt.Errorf("list uncommitted paths: %w", err)
	}
	if len(paths) == 0 {
		return commitOutcome{}, nil
	}
	if !repositoryReady(repo) {
		return commitOutcome{dirty: true}, nil // repository still provisioning; its watch wakes us
	}
	if r.Busy != nil && r.Busy(scope) {
		return commitOutcome{dirty: true}, nil // an assistant turn owns the workspace; its end is signalled
	}

	// This provider is what asks for the commit, through its own export
	// virtual workspace under the repositories/commit claim the tenant
	// accepted; the Code provider's gate admits it as a foreign provider whose
	// claim is the authorization. No provider credential means no commit path
	// — the files stay dirty and are committed once there is one.
	if r.Callers == nil {
		return commitOutcome{dirty: true, retry: true}, nil
	}

	// Build the payload: missing files are deletions, binaries travel base64
	// (the action's schema declares the encoding, so there is nothing to
	// probe for). A file that cannot be committed — over the per-file bound —
	// is skipped and stays dirty without forcing a requeue, so it neither
	// blocks text commits nor spins the reconciler. Files past the bundle
	// bound are committed on an immediate requeue.
	sort.Strings(paths)
	bundle, err := r.buildCommitBundle(ctx, scope, paths)
	if err != nil {
		return commitOutcome{dirty: true}, err
	}
	r.noteSkippedPaths(p.Name, scope, bundle.skipped)
	if len(bundle.files) == 0 && len(bundle.deletePaths) == 0 {
		return commitOutcome{dirty: bundle.deferred, retry: bundle.deferred}, nil
	}

	writtenPaths := make([]string, 0, len(bundle.files))
	for _, f := range bundle.files {
		writtenPaths = append(writtenPaths, f["path"])
	}
	digest, err := r.Workspace.WorkspaceDigest(ctx, scope, bundle.committed)
	if err != nil {
		return commitOutcome{dirty: true}, fmt.Errorf("workspace digest for pending commit: %w", err)
	}
	attributions, err := r.Workspace.SourceAttributions(ctx, scope, bundle.committed)
	if err != nil {
		return commitOutcome{dirty: true}, fmt.Errorf("capture pending commit attributions: %w", err)
	}
	if err := r.Workspace.VerifySourceRevision(ctx, scope, sourceRevision); err != nil {
		return commitOutcome{dirty: true, retry: true}, nil
	}
	message := commitMessage(writtenPaths, bundle.deletePaths)
	requestID := uuid.NewString()
	code := &codecommit.Client{Callers: r.Callers}
	staged, err := code.StageBundle(ctx, codecommit.Request{
		Cluster:       clusterOf(p),
		RepositoryRef: b.RepositoryRef,
		RepositoryUID: string(repo.GetUID()),
		Message:       message,
		Files:         bundle.wireFiles(),
	})
	if err != nil {
		return commitOutcome{dirty: true, retry: true}, fmt.Errorf("stage workspace commit: %w", err)
	}
	intent := workspace.PendingCommit{
		RequestID:       requestID,
		RepositoryRef:   b.RepositoryRef,
		RepositoryUID:   string(repo.GetUID()),
		BundleRef:       staged.BundleRef,
		BundleDigest:    staged.BundleDigest,
		Message:         message,
		WorkspaceDigest: digest,
		Paths:           bundle.committed,
		SourceRevision:  sourceRevision,
		Attributions:    attributions,
	}
	if err := r.Workspace.ReservePendingCommit(ctx, scope, intent); err != nil {
		if errors.Is(err, workspace.ErrPendingCommitExists) || errors.Is(err, workspace.ErrSourceRevisionConflict) {
			// A different writer reserved a commit or changed source while this
			// bundle was being staged. The bundle is an orphan, but no commit was
			// created; the next pass rebuilds against the current ledger.
			return commitOutcome{dirty: true, retry: true}, nil
		}
		return commitOutcome{dirty: true, retry: true}, fmt.Errorf("reserve workspace commit: %w", err)
	}
	created, err := code.Commit(ctx, codecommit.Request{
		Cluster:       clusterOf(p),
		RepositoryRef: intent.RepositoryRef,
		RepositoryUID: intent.RepositoryUID,
		RequestID:     intent.RequestID,
		BundleRef:     intent.BundleRef,
		BundleDigest:  intent.BundleDigest,
		Message:       intent.Message,
		Branch:        intent.Branch,
	})
	if err != nil {
		if blocked, blockErr := r.blockPendingCommitReplay(ctx, scope, intent, err); blocked {
			if blockErr != nil {
				return commitOutcome{dirty: true, retry: true}, blockErr
			}
			return commitOutcome{dirty: true}, nil
		}
		return commitOutcome{dirty: true, retry: true}, fmt.Errorf("create reserved workspace commit: %w", err)
	}
	if err := r.Workspace.BindPendingCommitName(ctx, scope, intent.RequestID, created.Name); err != nil {
		return commitOutcome{dirty: true, retry: true}, fmt.Errorf("bind reserved workspace commit: %w", err)
	}
	// The Code action returns immediately; the RepositoryCommit watch settles
	// the same durable request that was already persisted before the action.
	log.Printf("app-studio project %s: RepositoryCommit %s requested for %d file(s) (%d deletions); following it to settlement", p.Name, created.Name, len(bundle.files), len(bundle.deletePaths))
	// Deliberately not resolved here. The RepositoryCommit was created a
	// moment ago by another provider; reading it back immediately would race
	// its own creation, and a NotFound at that instant is indistinguishable
	// from "the commit is gone", which would clear the record and resend.
	// The watch delivers the object, and the pass it wakes settles it.
	return commitOutcome{dirty: true, retry: bundle.deferred}, nil
}

// commitBundle is one bounded commit payload.
type commitBundle struct {
	files       []map[string]string
	deletePaths []string
	// committed are the paths this commit settles, deletions included.
	committed []string
	// skipped maps a path that cannot be committed to the reason.
	skipped map[string]string
	// deferred reports files left for the next pass by the bundle bounds.
	deferred bool
}

// wireFiles is the bundle as the commit action's `files` member: writes with
// their content and encoding, deletions as {path, delete}. One list, because
// the action takes one — the MCP tool's separate deletePaths member was a
// second way to say the same thing.
func (b commitBundle) wireFiles() []codecommit.File {
	out := make([]codecommit.File, 0, len(b.files)+len(b.deletePaths))
	for _, f := range b.files {
		out = append(out, codecommit.File{Path: f["path"], Content: f["content"], Encoding: f["encoding"]})
	}
	for _, path := range b.deletePaths {
		out = append(out, codecommit.File{Path: path, Delete: true})
	}
	return out
}

// buildCommitBundle reads dirty paths into one payload bounded by the Code
// provider's limits (decoded bytes): 2 MiB per text file, 25 MiB per binary,
// 48 MiB and 500 files per commit.
func (r *Reconciler) buildCommitBundle(ctx context.Context, scope workspace.Scope, paths []string) (commitBundle, error) {
	bundle := commitBundle{skipped: map[string]string{}}
	var total int64
	for _, path := range paths {
		if len(bundle.committed) >= hubmcp.BundleMaxFiles {
			bundle.deferred = true
			break
		}
		data, err := r.Workspace.ReadFileBytes(ctx, scope, path, hubmcp.BinaryFileMaxBytes)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				bundle.deletePaths = append(bundle.deletePaths, path)
				bundle.committed = append(bundle.committed, path)
				continue
			}
			var tooLarge *workspace.FileTooLargeError
			if errors.As(err, &tooLarge) {
				bundle.skipped[path] = "larger than the 25 MiB per-file limit"
				continue
			}
			return commitBundle{}, fmt.Errorf("read %s: %w", path, err)
		}
		if hubmcp.IsText(data) {
			if len(data) > hubmcp.CommitTextMaxBytes {
				bundle.skipped[path] = "text larger than the 2 MiB per-file commit limit"
				continue
			}
		}
		if total+int64(len(data)) > hubmcp.BundleMaxBytes && len(bundle.committed) > 0 {
			bundle.deferred = true
			break
		}
		total += int64(len(data))
		bundle.files = append(bundle.files, hubmcp.WireFile(path, data))
		bundle.committed = append(bundle.committed, path)
	}
	return bundle, nil
}

// noteSkippedPaths logs files that stay uncommitted, once per distinct set.
func (r *Reconciler) noteSkippedPaths(project string, scope workspace.Scope, skipped map[string]string) {
	key := strings.Join([]string{scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID}, "/")
	lines := make([]string, 0, len(skipped))
	for path, reason := range skipped {
		lines = append(lines, path+" ("+reason+")")
	}
	sort.Strings(lines)
	notice := strings.Join(lines, ", ")
	r.noticeMu.Lock()
	defer r.noticeMu.Unlock()
	if r.skipNotices == nil {
		r.skipNotices = map[string]string{}
	}
	if r.skipNotices[key] == notice {
		return
	}
	r.skipNotices[key] = notice
	if notice != "" {
		log.Printf("app-studio project %s: not committing %d file(s); they stay uncommitted in the workspace: %s", project, len(lines), notice)
	}
}

// settleCommit records and applies the local cleanup for committed paths.
// The settlement only clears paths whose content still has digest, so edits
// made after the commit was sent stay dirty for the next commit.
func (r *Reconciler) settleCommit(ctx context.Context, scope workspace.Scope, pending workspace.PendingCommit) error {
	if err := r.Workspace.RecordPendingCommitSettlement(ctx, scope, pending.RequestID, pending.Name, pending.WorkspaceDigest, pending.Paths); err != nil {
		return fmt.Errorf("record commit settlement: %w", err)
	}
	if _, err := r.Workspace.ReconcileCommitSettlement(ctx, scope); err != nil {
		return fmt.Errorf("settle committed paths: %w", err)
	}
	return nil
}

func (r *Reconciler) notifyCommitted(ctx context.Context, scope workspace.Scope, commit CommitResult) {
	if r.OnCommitted != nil {
		r.OnCommitted(ctx, scope, commit)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Pending commits.
//
// Every commit intent is reserved in the Project ledger before the keyed
// action can create a RepositoryCommit. The reservation contains the request
// ID, immutable staged bundle, and source snapshot; a lost response therefore
// resumes the same action instead of building a new commit from mutable files.
// Once the response binds a RepositoryCommit name, the reconciler follows it
// through the watch until it settles. Nothing is polled: the watch drives each
// follow-up, and a commit queued behind a GitHub rate limit is just a commit
// that takes longer.
//
// The RepositoryCommit is created by the Code provider, not by this
// reconciler, because its spec references a provider-owned source bundle that
// only that provider can store; the object it names is still the one pointer
// everything converges on.

// repositoryCommitGVK is the Code provider's RepositoryCommit resource.
var repositoryCommitGVK = schema.GroupVersionKind{Group: "code.railgrid.ai", Version: "v1alpha1", Kind: "RepositoryCommit"}

// readRepositoryCommit reads one RepositoryCommit by name through the
// manager's client for the request's cluster.
//
// It used to be a Get with a fallback to the composition's unnamed list,
// because the project identity could hold no named grant on a commit that did
// not exist when the identity was last minted, and a Get on it came back 403.
// The claim has no such shape: it carries get, list and watch on
// repositorycommits for every workspace that accepted it, unconditioned on any
// name, so a plain Get is the whole read. A missing legacy commit can be
// cleared; a keyed request is replayed with the same immutable bundle.
func readRepositoryCommit(ctx context.Context, c client.Client, name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(repositoryCommitGVK)
	if err := c.Get(ctx, types.NamespacedName{Name: name}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// patchProjectAnnotation sets (or, with an empty value, removes) one
// annotation with a merge patch rather than an Update of the whole object.
//
// This is not a style preference. Since §9 Cut D.3 the working-copy ledger is
// written to the SAME Project's status, so `p` in hand goes stale the moment a
// dirty path or a pending commit is recorded — an Update carrying the
// resourceVersion this pass started with would lose that race every time. A
// merge patch names only the key it changes and takes no version with it, and
// the client writes the fresh object back into p.
func patchProjectAnnotation(ctx context.Context, c client.Client, p *aiv1alpha1.Project, key, value string) error {
	var annotation any = value
	if value == "" {
		annotation = nil
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{key: annotation}},
	})
	if err != nil {
		return err
	}
	if err := c.Patch(ctx, p, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return err
	}
	if p.Annotations == nil {
		p.Annotations = map[string]string{}
	}
	if value == "" {
		delete(p.Annotations, key)
	} else {
		p.Annotations[key] = value
	}
	return nil
}

// clearPendingCommit forgets a pending commit. One place now: the pointer and
// the record used to be an annotation and a file that could disagree, and
// since §9 Cut D.3 they are one member of one object.
func (r *Reconciler) clearPendingCommit(ctx context.Context, scope workspace.Scope, pending workspace.PendingCommit) (bool, error) {
	return r.Workspace.ClearPendingCommit(ctx, scope, pending.RequestID, pending.Name)
}

type pendingCommitBindingError struct {
	reason  string
	message string
}

func (e *pendingCommitBindingError) Error() string {
	return e.message
}

func permanentCommitReplayFailure(err error) (reason, message string, permanent bool) {
	var bindingErr *pendingCommitBindingError
	if errors.As(err, &bindingErr) {
		return bindingErr.reason, bindingErr.message, true
	}
	return codecommit.ClassifyPermanentCommitReplayFailure(err)
}

func pendingCommitRetryToken(p *aiv1alpha1.Project, pending workspace.PendingCommit) string {
	if p == nil || pending.RequestID == "" {
		return ""
	}
	value := strings.TrimSpace(p.Annotations[retryPendingCommitAnnotation])
	requestID, token, ok := strings.Cut(value, ":")
	if !ok || strings.TrimSpace(requestID) != pending.RequestID {
		return ""
	}
	token = strings.TrimSpace(token)
	if len(token) == 0 || len(token) > 160 {
		return ""
	}
	return token
}

// blockPendingCommitReplay keeps a permanent refusal attached to the same
// keyed request. Named requests are fenced by their exact RepositoryCommit so
// a late refusal cannot block another request's watch slot.
func (r *Reconciler) blockPendingCommitReplay(ctx context.Context, scope workspace.Scope, pending workspace.PendingCommit, replayErr error) (bool, error) {
	if pending.RequestID == "" {
		return false, nil
	}
	reason, message, permanent := permanentCommitReplayFailure(replayErr)
	if !permanent {
		return false, nil
	}
	var blocked bool
	var err error
	if pending.Name == "" {
		blocked, err = r.Workspace.BlockPendingCommit(ctx, scope, pending.RequestID, reason, message)
	} else {
		blocked, err = r.Workspace.BlockNamedPendingCommit(ctx, scope, pending.RequestID, pending.Name, reason, message)
	}
	if err != nil {
		return true, fmt.Errorf("record permanent pending commit failure: %w", err)
	}
	if blocked {
		log.Printf("app-studio project %s: pending workspace commit %s (%s) is blocked (%s): %s", scope.ProjectName, pending.RequestID, pending.Name, reason, message)
	}
	// A false result can mean a concurrent settlement or name bind won; let the
	// watch drive progress rather than hot-retrying against a stale snapshot.
	return true, nil
}

func (r *Reconciler) failedPendingCommitCooldown(ctx context.Context, c client.Client, p *aiv1alpha1.Project, repo *unstructured.Unstructured, scope workspace.Scope, expected workspace.PendingCommit) (commitOutcome, error) {
	pending, ok, err := r.Workspace.PendingCommit(ctx, scope)
	if err != nil {
		return commitOutcome{dirty: true, retry: true}, fmt.Errorf("read failed RepositoryCommit cooldown: %w", err)
	}
	if !ok || pending.RequestID != expected.RequestID || pending.Name != expected.Name {
		return commitOutcome{dirty: true}, nil
	}
	if pending.BlockedReason != repositoryCommitFailedReason || pending.Name == "" {
		return commitOutcome{dirty: true}, nil
	}
	if expected.BlockedReason == repositoryCommitFailedReason && expected.BlockedAt != nil &&
		(pending.BlockedAt == nil || !pending.BlockedAt.Equal(*expected.BlockedAt)) {
		return commitOutcome{dirty: true}, nil
	}
	if pending.BlockedAt == nil {
		if _, err := r.Workspace.BlockNamedPendingCommit(ctx, scope, pending.RequestID, pending.Name, repositoryCommitFailedReason, repositoryCommitFailedMessage); err != nil {
			return commitOutcome{dirty: true, retry: true}, fmt.Errorf("repair failed RepositoryCommit cooldown: %w", err)
		}
		pending, ok, err = r.Workspace.PendingCommit(ctx, scope)
		if err != nil {
			return commitOutcome{dirty: true, retry: true}, fmt.Errorf("read repaired RepositoryCommit cooldown: %w", err)
		}
		if !ok || pending.RequestID != expected.RequestID || pending.Name != expected.Name ||
			pending.BlockedReason != repositoryCommitFailedReason || pending.BlockedAt == nil {
			return commitOutcome{dirty: true}, nil
		}
	}
	if c != nil {
		obj, readErr := readRepositoryCommit(ctx, c, pending.Name)
		switch {
		case apierrors.IsNotFound(readErr):
			// The failed object can be deleted before its bundle is cleaned up.
			// Its durable BlockedAt still controls release below.
		case readErr != nil:
			return commitOutcome{dirty: true, retry: true}, fmt.Errorf("read failed RepositoryCommit during cooldown: %w", readErr)
		default:
			phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
			sha, _, _ := unstructured.NestedString(obj.Object, "status", "commitSHA")
			if phase == commitPhaseFailed {
				break
			}
			if phase == commitPhaseSucceeded && strings.TrimSpace(sha) != "" {
				resolved, err := r.resolvePendingCommit(ctx, c, p, repo, scope, pending)
				if err != nil {
					return commitOutcome{dirty: true, retry: true}, err
				}
				if resolved {
					return commitOutcome{dirty: true, retry: true}, nil
				}
				return commitOutcome{dirty: true}, nil
			}
			cleared, err := r.Workspace.ClearNamedPendingCommitBlock(ctx, scope, pending.RequestID, pending.Name, repositoryCommitFailedReason, pending.BlockedAt)
			if err != nil {
				return commitOutcome{dirty: true, retry: true}, fmt.Errorf("clear failed cooldown for reappeared RepositoryCommit: %w", err)
			}
			if cleared {
				// The named object is present but nonterminal. Return to its normal
				// watch-driven state; never release or replay it as a fresh request.
				return commitOutcome{dirty: true}, nil
			}
			return commitOutcome{dirty: true}, nil
		}
	}
	token := pendingCommitRetryToken(p, pending)
	if token != "" && token != pending.RetryToken {
		return r.releaseFailedPendingCommit(ctx, scope, pending)
	}
	remaining := time.Until(pending.BlockedAt.Add(repositoryCommitFailedRetryDelay))
	if remaining > 0 {
		return commitOutcome{dirty: true, retryAfter: remaining}, nil
	}
	return r.releaseFailedPendingCommit(ctx, scope, pending)
}

func (r *Reconciler) releaseFailedPendingCommit(ctx context.Context, scope workspace.Scope, pending workspace.PendingCommit) (commitOutcome, error) {
	released, err := r.Workspace.FailBlockedPendingCommit(ctx, scope, pending.RequestID, pending.Name, repositoryCommitFailedReason, pending.BlockedAt)
	if err != nil {
		return commitOutcome{dirty: true, retry: true}, fmt.Errorf("release failed pending RepositoryCommit: %w", err)
	}
	if !released {
		return commitOutcome{dirty: true}, nil
	}
	// Release now but leave staging for the next pass. This keeps a watch event
	// from turning the failed-status reconcile into an immediate second commit.
	return commitOutcome{dirty: true, retry: true}, nil
}

// blockPendingRepositoryBindingChange runs before ensureRepository so a
// permanent change to the Project's repository reference cannot fail outside
// the commit loop and leave the immutable request silently retrying forever.
func (r *Reconciler) blockPendingRepositoryBindingChange(ctx context.Context, p *aiv1alpha1.Project) error {
	if r.Workspace == nil || p == nil {
		return nil
	}
	scope, ok := scopeOf(p)
	if !ok || (r.Owns != nil && !r.Owns(scope)) {
		return nil
	}
	pending, hasPending, err := r.Workspace.PendingCommit(ctx, scope)
	if err != nil {
		return fmt.Errorf("read pending commit: %w", err)
	}
	if !hasPending || pending.RequestID == "" || pending.BlockedReason == repositoryCommitFailedReason {
		return nil
	}
	currentRef := ""
	if p.Spec.Repository != nil {
		currentRef = strings.TrimSpace(p.Spec.Repository.RepositoryRef)
	}
	if currentRef == pending.RepositoryRef {
		return nil
	}
	message := "The Project repository binding changed after this commit was saved. Restore the original binding or review the saved request before retrying."
	if pending.Name == "" {
		_, err = r.Workspace.BlockPendingCommit(ctx, scope, pending.RequestID, "RepositoryBindingChanged", message)
	} else {
		_, err = r.Workspace.BlockNamedPendingCommit(ctx, scope, pending.RequestID, pending.Name, "RepositoryBindingChanged", message)
	}
	if err != nil {
		return fmt.Errorf("block pending commit for repository binding change: %w", err)
	}
	return nil
}

func validatePendingCommitBinding(p *aiv1alpha1.Project, repo *unstructured.Unstructured, pending workspace.PendingCommit) error {
	if pending.RequestID == "" {
		return nil // pre-request-ID ledger entries are followed up by their recorded name
	}
	if p == nil || p.Spec.Repository == nil || strings.TrimSpace(p.Spec.Repository.RepositoryRef) != pending.RepositoryRef {
		return &pendingCommitBindingError{
			reason:  "RepositoryBindingChanged",
			message: "The Project repository binding changed after this commit was saved. Restore the original binding or review the saved request before retrying.",
		}
	}
	if repo == nil || strings.TrimSpace(string(repo.GetUID())) == "" || string(repo.GetUID()) != pending.RepositoryUID {
		return &pendingCommitBindingError{
			reason:  "RepositoryUIDMismatch",
			message: "The Repository identity changed after this commit was saved. Restore the original Repository or review the saved request before retrying.",
		}
	}
	return nil
}

// requestPendingCommit replays exactly the durable request stored on the
// Project. Its staged content and request ID stay constant even if the local
// workspace has changed since reservation.
func (r *Reconciler) ensurePendingCommit(ctx context.Context, p *aiv1alpha1.Project, repo *unstructured.Unstructured, scope workspace.Scope, pending workspace.PendingCommit) error {
	if pending.Name != "" {
		return nil
	}
	return r.requestPendingCommit(ctx, p, repo, scope, pending)
}

func (r *Reconciler) requestPendingCommit(ctx context.Context, p *aiv1alpha1.Project, repo *unstructured.Unstructured, scope workspace.Scope, pending workspace.PendingCommit) error {
	if err := validatePendingCommitBinding(p, repo, pending); err != nil {
		return err
	}
	if r.Callers == nil {
		return errors.New("code provider caller is not configured")
	}
	created, err := (&codecommit.Client{Callers: r.Callers}).Commit(ctx, codecommit.Request{
		Cluster:       clusterOf(p),
		RepositoryRef: pending.RepositoryRef,
		RepositoryUID: pending.RepositoryUID,
		RequestID:     pending.RequestID,
		BundleRef:     pending.BundleRef,
		BundleDigest:  pending.BundleDigest,
		Message:       pending.Message,
		Branch:        pending.Branch,
	})
	if err != nil {
		return err
	}
	return r.Workspace.BindPendingCommitName(ctx, scope, pending.RequestID, created.Name)
}

// resolvePendingCommit reads a pending RepositoryCommit and reports whether
// it is resolved: Succeeded (settled and announced) or Failed/gone (cleared,
// so a fresh commit may be sent). A still-running commit stays recorded and
// is re-read when its watch event arrives.
//
// One client now: c, the manager's client over this provider's APIExport
// virtual workspace. The read and the writes were split before because
// repositorycommits were reachable only inside the tenant workspace, as the
// project identity, on the composition's unnamed list/watch. The export claims
// the kind with get/list/watch and kcp resolves that claim per consumer
// workspace, so the commit arrives on the same client the Project does and a
// commit created after the identity was last minted is no longer a special
// case.
//
// A workspace that has not accepted the claim answers with a RESTMapper miss,
// not a 404, so it is NOT mistaken for "the commit is gone": the pending
// record survives, the failure is returned, and the controller retries.
func (r *Reconciler) resolvePendingCommit(ctx context.Context, c client.Client, p *aiv1alpha1.Project, repo *unstructured.Unstructured, scope workspace.Scope, pending workspace.PendingCommit) (bool, error) {
	if c == nil {
		return false, nil
	}
	obj, err := readRepositoryCommit(ctx, c, pending.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if pending.RequestID != "" {
				if pending.BlockedReason != "" {
					return false, nil // a named block watches the CR but does not replay its bundle
				}
				if err := validatePendingCommitBinding(p, repo, pending); err != nil {
					return false, err
				}
				if err := r.requestPendingCommit(ctx, p, repo, scope, pending); err != nil {
					return false, fmt.Errorf("replay missing RepositoryCommit %s: %w", pending.Name, err)
				}
				return false, nil
			}
			log.Printf("app-studio project %s: legacy pending RepositoryCommit %s is gone; a fresh commit will be sent", scope.ProjectName, pending.Name)
			return r.clearPendingCommit(ctx, scope, pending)
		}
		return false, fmt.Errorf("read pending RepositoryCommit %q: %w", pending.Name, err)
	}
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	sha, _, _ := unstructured.NestedString(obj.Object, "status", "commitSHA")
	var bindingErr error
	if pending.RequestID != "" {
		bindingErr = validatePendingCommitBinding(p, repo, pending)
	}
	switch {
	case phase == commitPhaseSucceeded && strings.TrimSpace(sha) != "":
		if bindingErr != nil {
			cleared, err := r.clearPendingCommit(ctx, scope, pending)
			if err != nil || !cleared {
				return false, err
			}
			log.Printf("app-studio project %s: completed RepositoryCommit %s targets a previous repository binding; source paths remain dirty for the current repository", scope.ProjectName, pending.Name)
			return true, nil
		}
		if err := r.settleCommit(ctx, scope, pending); err != nil {
			if errors.Is(err, workspace.ErrPendingCommitChanged) {
				return false, nil // a newer request owns the ledger now
			}
			return false, err
		}
		cleared, err := r.clearPendingCommit(ctx, scope, pending)
		if err != nil {
			return false, err
		}
		if !cleared {
			return false, nil // a newer request owns the ledger now
		}
		commitURL, _, _ := unstructured.NestedString(obj.Object, "status", "commitURL")
		branch, _, _ := unstructured.NestedString(obj.Object, "status", "branch")
		log.Printf("app-studio project %s: pending RepositoryCommit %s landed @ %s", scope.ProjectName, pending.Name, shortSHA(sha))
		r.notifyCommitted(ctx, scope, CommitResult{
			RepositoryRef: pending.RepositoryRef,
			CommitSHA:     sha,
			CommitURL:     commitURL,
			Branch:        branch,
			Files:         pending.Paths,
		})
		return true, nil
	case phase == commitPhaseFailed:
		log.Printf("app-studio project %s: pending RepositoryCommit %s failed; retaining it through the automatic retry cooldown", scope.ProjectName, pending.Name)
		if _, err := r.Workspace.BlockNamedPendingCommit(ctx, scope, pending.RequestID, pending.Name, repositoryCommitFailedReason, repositoryCommitFailedMessage); err != nil {
			return false, fmt.Errorf("record failed RepositoryCommit cooldown: %w", err)
		}
		return false, errPendingRepositoryCommitFailed
	default:
		if bindingErr != nil {
			return false, bindingErr
		}
		// Still running: the record is already on the project, so there is
		// nothing to mirror. Wait for the watch.
		return false, nil
	}
}

// CommitResult describes a settled reconciler commit, reported through
// Reconciler.OnCommitted.
type CommitResult struct {
	RepositoryRef string
	CommitSHA     string
	CommitURL     string
	Branch        string
	// Files are the workspace paths the commit settled, deletions included.
	Files []string
}

// RepositoryCommit phases the reconciler acts on.
const (
	commitPhaseSucceeded = "Succeeded"
	commitPhaseFailed    = "Failed"
)

const (
	// commitMessageMaxLength keeps generated messages under the
	// RepositoryCommit spec.message limit (512 characters) with headroom.
	commitMessageMaxLength = 480
	// commitSubjectMaxLength bounds the subject so the body always has room
	// for at least the "… and N more" line.
	commitSubjectMaxLength = 200
	commitMessageMaxListed = 20
)

// commitMessage builds a human-readable message describing what actually
// changed, so the git history reads like real work instead of an opaque
// "sync workspace (N files)". Subject names the file for a single change or
// summarizes the count + top-level areas for many; the body lists the paths
// until the message would exceed commitMessageMaxLength characters.
func commitMessage(writePaths, deletePaths []string) string {
	total := len(writePaths) + len(deletePaths)
	var subject string
	switch {
	case total == 1 && len(writePaths) == 1:
		subject = "Update " + writePaths[0]
	case total == 1 && len(deletePaths) == 1:
		subject = "Delete " + deletePaths[0]
	default:
		var parts []string
		if len(writePaths) > 0 {
			parts = append(parts, fmt.Sprintf("update %d %s", len(writePaths), pluralFiles(len(writePaths))))
		}
		if len(deletePaths) > 0 {
			parts = append(parts, fmt.Sprintf("delete %d %s", len(deletePaths), pluralFiles(len(deletePaths))))
		}
		subject = capitalizeFirst(strings.Join(parts, ", "))
		if areas := topLevelAreas(writePaths, deletePaths); areas != "" {
			subject += " in " + areas
		}
	}

	subject = truncateRunes(subject, commitSubjectMaxLength)

	lines := make([]string, 0, len(writePaths)+len(deletePaths))
	for _, p := range writePaths {
		lines = append(lines, "\n- "+p)
	}
	for _, p := range deletePaths {
		lines = append(lines, "\n- delete "+p)
	}
	more := func(n int) string {
		if n <= 0 {
			return ""
		}
		return fmt.Sprintf("\n- … and %d more", n)
	}
	message := subject + "\n"
	length := utf8.RuneCountInString(message)
	listed := 0
	for _, line := range lines {
		if listed >= commitMessageMaxListed {
			break
		}
		// Keep room for the trailer that would follow this line.
		next := length + utf8.RuneCountInString(line) + utf8.RuneCountInString(more(total-listed-1))
		if next > commitMessageMaxLength {
			break
		}
		message += line
		length += utf8.RuneCountInString(line)
		listed++
	}
	return message + more(total-listed)
}

// truncateRunes shortens s to at most n characters, marking the cut.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n-1]) + "…"
}

func pluralFiles(n int) string {
	if n == 1 {
		return "file"
	}
	return "files"
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// topLevelAreas lists the distinct top-level directories touched (e.g.
// "web, api"), so a multi-file commit says where the work happened. Returns
// "" when there are none, too many to be useful, or only root-level files.
func topLevelAreas(writePaths, deletePaths []string) string {
	seen := map[string]bool{}
	var areas []string
	for _, p := range append(append([]string{}, writePaths...), deletePaths...) {
		if i := strings.Index(p, "/"); i > 0 {
			dir := p[:i]
			if !seen[dir] {
				seen[dir] = true
				areas = append(areas, dir)
			}
		}
	}
	if len(areas) == 0 || len(areas) > 3 {
		return ""
	}
	return strings.Join(areas, ", ")
}

// clusterOf returns the workspace's logical-cluster id, which kcp stamps as
// an annotation on every object the VW serves.
func clusterOf(p *aiv1alpha1.Project) string {
	return p.Annotations["kcp.io/cluster"]
}
