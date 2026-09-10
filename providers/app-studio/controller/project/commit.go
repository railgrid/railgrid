/*
Copyright 2026 The Faros Authors.

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
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/hubmcp"
	"github.com/faroshq/provider-app-studio/workspace"
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
	orgUUIDAnnotation       = "ai.faros.sh/org-uuid"
	workspaceUUIDAnnotation = "ai.faros.sh/workspace-uuid"
)

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

// commitWorkspace pushes dirty workspace files to git when the project is
// idle. Returns (dirty, err): dirty=true means uncommitted work remains (not
// committed this pass, or partially skipped) so the caller keeps polling.
func (r *Reconciler) commitWorkspace(ctx context.Context, token string, tc client.Client, p *aiv1alpha1.Project, repo *unstructured.Unstructured) (bool, error) {
	if r.Workspace == nil || r.HubBase == "" {
		return false, nil // commit convergence not wired (REST-only dev)
	}
	b := p.Spec.Repository
	if b == nil || strings.TrimSpace(b.RepositoryRef) == "" {
		return false, nil
	}
	scope, ok := scopeOf(p)
	if !ok {
		return false, nil // legacy project without identity annotations
	}

	if p.Annotations["ai.faros.sh/initialize-repository"] == b.RepositoryRef {
		if err := r.Workspace.InitializeRepositorySource(ctx, scope, b.RepositoryRef); err != nil {
			return true, fmt.Errorf("initialize repository source: %w", err)
		}
	}

	// Heal a settlement another writer recorded but could not reconcile
	// (e.g. the assistant crashed between commit and ledger settle).
	if _, err := r.Workspace.ReconcileCommitSettlement(ctx, scope); err != nil {
		return true, fmt.Errorf("reconcile commit settlement: %w", err)
	}

	paths, err := r.Workspace.UncommittedPaths(ctx, scope)
	if err != nil {
		return true, fmt.Errorf("list uncommitted paths: %w", err)
	}
	if len(paths) == 0 {
		return false, nil
	}
	if !repositoryReady(repo) {
		return true, nil // repository still provisioning; poll
	}
	if r.Owns != nil && !r.Owns(scope) {
		// Another replica owns this project's workspace; whatever this
		// replica's tree holds is a leftover from a previous ownership term
		// and must not be committed over the live owner's work.
		return false, nil
	}
	if r.Busy != nil && r.Busy(scope) {
		return true, nil // an assistant turn owns the workspace; poll
	}
	// A commit the Code provider accepted but had not finished (rate limit,
	// wait timeout) is followed up by name; resending would only queue
	// another RepositoryCommit behind the same limit. Newer workspace edits
	// wait for it to settle.
	if pending, ok := r.pendingCommitFor(scope); ok {
		resolved, err := r.resolvePendingCommit(ctx, tc, scope, pending)
		if err != nil || !resolved {
			return true, err
		}
		if paths, err = r.Workspace.UncommittedPaths(ctx, scope); err != nil {
			return true, fmt.Errorf("list uncommitted paths: %w", err)
		}
		if len(paths) == 0 {
			return false, nil
		}
	}

	mcp := hubmcp.NewClient(r.HubBase, clusterOf(p), token, r.HubInsecure)
	if !mcp.Ready() {
		return true, nil
	}

	// Build the payload the same way the assistant's commit tool does:
	// missing files are deletions; binaries travel base64 when the Code
	// provider supports it. A file that cannot be committed (binary on an
	// older provider, or over the per-file bound) is skipped and stays dirty
	// without forcing a requeue, so it neither blocks text commits nor spins
	// the reconciler; the next commit pass after a provider upgrade picks it
	// up. Files past the bundle bound wait for the next pass.
	sort.Strings(paths)
	bundle, err := r.buildCommitBundle(ctx, mcp, clusterOf(p), scope, paths)
	if err != nil {
		return true, err
	}
	files, deletePaths, committed := bundle.files, bundle.deletePaths, bundle.committed
	r.noteSkippedPaths(p.Name, scope, bundle.skipped)
	if len(files) == 0 && len(deletePaths) == 0 {
		return bundle.deferred, nil
	}

	writtenPaths := make([]string, 0, len(files))
	for _, f := range files {
		writtenPaths = append(writtenPaths, f["path"])
	}
	commitArgs := map[string]any{
		"repositoryRef": b.RepositoryRef,
		"message":       commitMessage(writtenPaths, deletePaths),
		"files":         files,
	}
	if len(deletePaths) > 0 {
		commitArgs["deletePaths"] = deletePaths
	}
	result, callErr := mcp.CallCodeTool(ctx, "code__commit_files", commitArgs)
	var commit commitToolResult
	if callErr == nil {
		if err := json.Unmarshal(result, &commit); err != nil {
			return true, fmt.Errorf("commit workspace: decode commit_files result: %w", err)
		}
	}
	// Only a Succeeded commit with a SHA has landed. An accepted but
	// unfinished one is remembered and followed up by name; anything else
	// leaves the paths dirty so the next idle reconcile retries.
	if callErr != nil || !commit.settled() {
		name := unfinishedCommitName(callErr, commit)
		if name == "" {
			if callErr != nil {
				return true, fmt.Errorf("commit workspace: %w", callErr)
			}
			return true, fmt.Errorf("commit workspace: RepositoryCommit %q is not settled (phase %q); retrying on the next idle reconcile", commit.Name, commit.Phase)
		}
		digest, err := r.Workspace.WorkspaceDigest(ctx, scope, committed)
		if err != nil {
			return true, fmt.Errorf("workspace digest for pending commit: %w", err)
		}
		r.setPendingCommit(scope, pendingCommit{
			Name:          name,
			RepositoryRef: b.RepositoryRef,
			Digest:        digest,
			Paths:         committed,
			NextCheck:     r.clock().Add(pendingCommitRecheckInterval),
		})
		log.Printf("app-studio project %s: RepositoryCommit %s accepted but not finished; following it up instead of resending", p.Name, name)
		return true, nil
	}

	digest, err := r.Workspace.WorkspaceDigest(ctx, scope, committed)
	if err != nil {
		return true, fmt.Errorf("workspace digest after commit: %w", err)
	}
	if err := r.settleCommit(ctx, scope, digest, committed); err != nil {
		return true, err
	}
	log.Printf("app-studio project %s: committed %d files (%d deletions) @ %s", p.Name, len(files), len(deletePaths), shortSHA(commit.CommitSHA))
	if bundle.deferred {
		log.Printf("app-studio project %s: more uncommitted files remain beyond one commit's bounds; committing them next pass", p.Name)
	}
	repositoryRef := commit.RepositoryRef
	if repositoryRef == "" {
		repositoryRef = b.RepositoryRef
	}
	r.notifyCommitted(ctx, scope, CommitResult{
		RepositoryRef: repositoryRef,
		CommitSHA:     commit.CommitSHA,
		CommitURL:     commit.CommitURL,
		Branch:        commit.Branch,
		Files:         committed,
	})
	return bundle.deferred, nil
}

// commitBundle is one bounded commit_files payload.
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

// buildCommitBundle reads dirty paths into one payload bounded by the Code
// provider's limits (decoded bytes): 2 MiB per text file, 25 MiB per binary,
// 48 MiB and 500 files per commit.
func (r *Reconciler) buildCommitBundle(ctx context.Context, mcp *hubmcp.Client, cluster string, scope workspace.Scope, paths []string) (commitBundle, error) {
	bundle := commitBundle{skipped: map[string]string{}}
	var total int64
	binarySupported := -1 // unknown until the first binary needs it
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
		} else {
			if binarySupported < 0 {
				binarySupported = 0
				if r.commitFilesSupportsBinary(ctx, mcp, cluster) {
					binarySupported = 1
				}
			}
			if binarySupported == 0 {
				bundle.skipped[path] = "binary; the Code provider does not accept binary commits yet"
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

// commitFilesSupportsBinary reads (and caches per cluster) whether
// code__commit_files advertises base64 file items. A failed probe is treated
// as unsupported for this pass only.
func (r *Reconciler) commitFilesSupportsBinary(ctx context.Context, mcp *hubmcp.Client, cluster string) bool {
	if supported, ok := r.binaryCommits.Get(cluster); ok {
		return supported
	}
	tools, err := mcp.ListTools(ctx)
	if err != nil {
		log.Printf("app-studio: read Code provider tool catalog for cluster %s: %v", cluster, err)
		return false
	}
	supported := hubmcp.CommitFilesSupportsEncoding(tools)
	r.binaryCommits.Set(cluster, supported)
	return supported
}

// noteSkippedPaths logs files that stay uncommitted, once per distinct set.
func (r *Reconciler) noteSkippedPaths(project string, scope workspace.Scope, skipped map[string]string) {
	key := pendingCommitKey(scope)
	lines := make([]string, 0, len(skipped))
	for path, reason := range skipped {
		lines = append(lines, path+" ("+reason+")")
	}
	sort.Strings(lines)
	notice := strings.Join(lines, ", ")
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
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
// commit_files waits a bounded time for the RepositoryCommit it creates. When
// GitHub rate-limits the provider (or the wait simply ends first) the tool
// fails with the RepositoryCommit's name while the provider keeps retrying it.
// Resending on every poll would queue yet another RepositoryCommit behind the
// same limit, so the reconciler remembers the pending one per project and
// reads it by name through the tenant client (the project identity's own
// binding to the Code provider) until it settles. The map is in memory: the
// provider is single-replica (the chart rejects replicaCount > 1), and after a
// restart the worst case is one resend.

// pendingCommitRecheckInterval spaces follow-up reads of a pending
// RepositoryCommit; the provider's own rate-limit retries are far slower.
const pendingCommitRecheckInterval = 60 * time.Second

// repositoryCommitGVK is the Code provider's RepositoryCommit resource.
var repositoryCommitGVK = schema.GroupVersionKind{Group: "code.faros.sh", Version: "v1alpha1", Kind: "RepositoryCommit"}

type pendingCommit struct {
	Name          string
	RepositoryRef string
	// Digest and Paths are the workspace content the commit carried.
	Digest    string
	Paths     []string
	NextCheck time.Time
}

// unfinishedCommitPattern extracts the RepositoryCommit name from the Code
// provider's "queued behind a GitHub rate limit" / "did not finish within the
// wait" commit_files errors.
var unfinishedCommitPattern = regexp.MustCompile(`RepositoryCommit "([^"]+)" (?:is queued behind a GitHub rate limit|did not finish within)`)

// unfinishedCommitName names the RepositoryCommit a commit_files call left
// running, or "" when the call did not leave one (a failed or rejected
// commit is simply retried).
func unfinishedCommitName(callErr error, result commitToolResult) string {
	if callErr != nil {
		if m := unfinishedCommitPattern.FindStringSubmatch(callErr.Error()); m != nil {
			return m[1]
		}
		return ""
	}
	switch result.Phase {
	case commitPhaseSucceeded, commitPhaseFailed:
		return ""
	}
	return strings.TrimSpace(result.Name)
}

func (r *Reconciler) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *Reconciler) pendingCommitFor(scope workspace.Scope) (pendingCommit, bool) {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	pending, ok := r.pendingCommits[pendingCommitKey(scope)]
	return pending, ok
}

func (r *Reconciler) setPendingCommit(scope workspace.Scope, pending pendingCommit) {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	if r.pendingCommits == nil {
		r.pendingCommits = map[string]pendingCommit{}
	}
	r.pendingCommits[pendingCommitKey(scope)] = pending
}

func (r *Reconciler) clearPendingCommit(scope workspace.Scope) {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	delete(r.pendingCommits, pendingCommitKey(scope))
}

func pendingCommitKey(scope workspace.Scope) string {
	return strings.Join([]string{scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID}, "/")
}

// resolvePendingCommit reads a pending RepositoryCommit and reports whether
// it is resolved: Succeeded (settled and announced) or Failed/gone (cleared,
// so a fresh commit may be sent). A still-running commit is re-read no sooner
// than pendingCommitRecheckInterval.
func (r *Reconciler) resolvePendingCommit(ctx context.Context, tc client.Client, scope workspace.Scope, pending pendingCommit) (bool, error) {
	now := r.clock()
	if tc == nil || now.Before(pending.NextCheck) {
		return false, nil
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(repositoryCommitGVK)
	if err := tc.Get(ctx, types.NamespacedName{Name: pending.Name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			r.clearPendingCommit(scope)
			return true, nil
		}
		pending.NextCheck = now.Add(pendingCommitRecheckInterval)
		r.setPendingCommit(scope, pending)
		return false, fmt.Errorf("read pending RepositoryCommit %q: %w", pending.Name, err)
	}
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	sha, _, _ := unstructured.NestedString(obj.Object, "status", "commitSHA")
	switch {
	case phase == commitPhaseSucceeded && strings.TrimSpace(sha) != "":
		if err := r.settleCommit(ctx, scope, pending.Digest, pending.Paths); err != nil {
			return false, err
		}
		r.clearPendingCommit(scope)
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
		r.clearPendingCommit(scope)
		return true, nil
	default:
		pending.NextCheck = now.Add(pendingCommitRecheckInterval)
		r.setPendingCommit(scope, pending)
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

// commitToolResult is the subset of the Code provider's commit_files result
// the reconciler acts on.
type commitToolResult struct {
	RepositoryRef string `json:"repositoryRef"`
	Name          string `json:"name"`
	Phase         string `json:"phase"`
	CommitSHA     string `json:"commitSHA"`
	CommitURL     string `json:"commitURL"`
	Branch        string `json:"branch"`
}

// RepositoryCommit phases the reconciler acts on.
const (
	commitPhaseSucceeded = "Succeeded"
	commitPhaseFailed    = "Failed"
)

func (c commitToolResult) settled() bool {
	return c.Phase == commitPhaseSucceeded && strings.TrimSpace(c.CommitSHA) != ""
}

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
