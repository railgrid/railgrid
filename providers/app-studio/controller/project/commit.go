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
	"unicode/utf8"

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
}

// commitWorkspace pushes dirty workspace files to git when the project is
// idle. Two clients, as everywhere in this package: c is the manager's client
// over this provider's APIExport virtual workspace, which owns the Project and
// is where the pending-commit pointer is written; tc is the tenant-workspace
// client authenticated as the project identity, which is where a
// RepositoryCommit is read. token is that same identity's bearer.
//
// Asking for the commit is the one thing here that is not a CR write, and
// deliberately: a RepositoryCommit is a POINTER at a source bundle held in the
// Code provider's own store (providers/code/commitbundle), and only that
// provider can put bytes there. The `repositories/{name}/commit/v1` action
// stores the bundle and creates the CR in one step and returns that CR's
// name, which this loop then follows over the RepositoryCommit watch — which
// is why the declared composition on repositorycommits still carries no
// create, and why the project identity carries the verb as a clause-C grant
// instead (commitaction.go, identity.go).
func (r *Reconciler) commitWorkspace(ctx context.Context, c, tc client.Client, token string, p *aiv1alpha1.Project, repo *unstructured.Unstructured) (commitOutcome, error) {
	if r.Workspace == nil || r.HubBase == "" {
		return commitOutcome{}, nil // commit convergence not wired (REST-only dev)
	}
	b := p.Spec.Repository
	if b == nil || strings.TrimSpace(b.RepositoryRef) == "" {
		return commitOutcome{}, nil
	}
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
	if p.Annotations[initializeRepositoryAnnotation] == b.RepositoryRef {
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

	// A commit the Code provider accepted but had not finished (rate limit,
	// wait timeout) is followed up by name; resending would only queue
	// another RepositoryCommit behind the same limit. Newer workspace edits
	// wait for it to settle. This runs before the idle gate so a commit that
	// lands mid-turn settles as soon as its watch event arrives.
	pending, hasPending, err := r.Workspace.PendingCommit(ctx, scope)
	if err != nil {
		return commitOutcome{dirty: true}, fmt.Errorf("read pending commit: %w", err)
	}
	if hasPending {
		resolved, err := r.resolvePendingCommit(ctx, c, tc, p, scope, pending)
		if err != nil || !resolved {
			return commitOutcome{dirty: true}, err
		}
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

	// The project's own hub-minted identity is what asks for the commit: the
	// Code provider's gates admit it on `get` of the Repository plus `create`
	// on repositories/commit, and the commit is therefore authorized as the
	// project rather than as this provider. No identity (no hub configured)
	// means no commit path — the files stay dirty and are committed once
	// there is one.
	if strings.TrimSpace(token) == "" || tc == nil || r.HubBase == "" {
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
	created, err := r.requestCommit(ctx, tc, token, clusterOf(p), b.RepositoryRef, string(repo.GetUID()), bundle, commitMessage(writtenPaths, bundle.deletePaths))
	if err != nil {
		return commitOutcome{dirty: true}, fmt.Errorf("commit workspace: %w", err)
	}

	// The action creates the RepositoryCommit and returns; it never waits for
	// the commit to land. So there is exactly one settlement path — record
	// the commit as pending, point the Project at it, and let the watch
	// converge it (resolvePendingCommit). The rate-limited case and the
	// ordinary case are the same code, which is the point of moving off the
	// tool: nothing parses a prose error for a name any more.
	digest, err := r.Workspace.WorkspaceDigest(ctx, scope, bundle.committed)
	if err != nil {
		return commitOutcome{dirty: true}, fmt.Errorf("workspace digest for pending commit: %w", err)
	}
	if err := r.Workspace.RecordPendingCommit(ctx, scope, workspace.PendingCommit{
		Name:            created.Name,
		RepositoryRef:   b.RepositoryRef,
		WorkspaceDigest: digest,
		Paths:           bundle.committed,
	}); err != nil {
		return commitOutcome{dirty: true}, fmt.Errorf("record pending commit: %w", err)
	}
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
func (r *Reconciler) settleCommit(ctx context.Context, scope workspace.Scope, digest string, paths []string) error {
	if err := r.Workspace.RecordCommitSettlement(ctx, scope, digest, paths); err != nil {
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
// Every commit is pending when it is made: the commit action creates the
// RepositoryCommit and returns its name, and the files are in git only once
// that CR reaches Succeeded. The reconciler records it durably — the name,
// and the workspace digest and paths it carried, in the project's workspace
// ledger next to the settlement receipt, with the name mirrored onto the
// Project as pendingCommitAnnotation — and re-reads it by name through the
// tenant client whenever the RepositoryCommit watch reports a change, until
// it settles. Nothing is polled: the watch drives every follow-up, and a
// commit queued behind a GitHub rate limit is not a special case, just a
// commit that takes longer.
//
// The RepositoryCommit is created by the Code provider, not by this
// reconciler, because its spec references a provider-owned source bundle that
// only that provider can store; the object it names is still the one pointer
// everything converges on.

// repositoryCommitGVK is the Code provider's RepositoryCommit resource.
var repositoryCommitGVK = schema.GroupVersionKind{Group: "code.railgrid.ai", Version: "v1alpha1", Kind: "RepositoryCommit"}

// readRepositoryCommit reads one RepositoryCommit by name as the project
// identity: a Get when the identity holds a named grant, otherwise the
// composition's unnamed list, reduced to the requested name. A commit absent
// from that list is reported as NotFound, exactly as the Get would.
func readRepositoryCommit(ctx context.Context, tc client.Client, name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(repositoryCommitGVK)
	err := tc.Get(ctx, types.NamespacedName{Name: name}, obj)
	if err == nil || !apierrors.IsForbidden(err) {
		return obj, err
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(repositoryCommitGVK.GroupVersion().WithKind(repositoryCommitGVK.Kind + "List"))
	if listErr := tc.List(ctx, list); listErr != nil {
		return nil, fmt.Errorf("%w (and listing commits instead: %v)", err, listErr)
	}
	for i := range list.Items {
		if list.Items[i].GetName() == name {
			return &list.Items[i], nil
		}
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: repositoryCommitGVK.Group, Resource: "repositorycommits"}, name)
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
func (r *Reconciler) clearPendingCommit(ctx context.Context, scope workspace.Scope) error {
	return r.Workspace.ClearPendingCommit(ctx, scope)
}

// resolvePendingCommit reads a pending RepositoryCommit and reports whether
// it is resolved: Succeeded (settled and announced) or Failed/gone (cleared,
// so a fresh commit may be sent). A still-running commit stays recorded and
// is re-read when its watch event arrives.
//
// The read is tc — the tenant workspace, as the project identity — while every
// write here is c, the Project's own virtual workspace. The identity's grant on
// repositorycommits is the composition's unnamed `list`/`watch`: a named `get`
// on THIS commit cannot be minted before the commit exists, and the identity
// is not re-minted per commit. So a Get that the workspace refuses falls back
// to the list the identity does hold, filtered to the one name. An identity
// that has since been refreshed with the name keeps using the cheaper Get.
func (r *Reconciler) resolvePendingCommit(ctx context.Context, c, tc client.Client, p *aiv1alpha1.Project, scope workspace.Scope, pending workspace.PendingCommit) (bool, error) {
	if c == nil || tc == nil {
		return false, nil
	}
	obj, err := readRepositoryCommit(ctx, tc, pending.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Printf("app-studio project %s: pending RepositoryCommit %s is gone; a fresh commit will be sent", scope.ProjectName, pending.Name)
			return true, r.clearPendingCommit(ctx, scope)
		}
		return false, fmt.Errorf("read pending RepositoryCommit %q: %w", pending.Name, err)
	}
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	sha, _, _ := unstructured.NestedString(obj.Object, "status", "commitSHA")
	switch {
	case phase == commitPhaseSucceeded && strings.TrimSpace(sha) != "":
		if err := r.settleCommit(ctx, scope, pending.WorkspaceDigest, pending.Paths); err != nil {
			return false, err
		}
		if err := r.clearPendingCommit(ctx, scope); err != nil {
			return false, err
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
		log.Printf("app-studio project %s: pending RepositoryCommit %s failed; a fresh commit will be sent", scope.ProjectName, pending.Name)
		return true, r.clearPendingCommit(ctx, scope)
	default:
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
