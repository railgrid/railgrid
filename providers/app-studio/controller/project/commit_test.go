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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/internal/codecommit"
	"github.com/railgrid/provider-app-studio/internal/projectledger"
	"github.com/railgrid/provider-app-studio/internal/scopedidentity"
	"github.com/railgrid/provider-app-studio/tenant/tenanttest"
	"github.com/railgrid/provider-app-studio/workspace"
)

func TestCommitMessageDescribesChanges(t *testing.T) {
	// Single file → names it.
	if got := commitMessage([]string{"web/src/App.jsx"}, nil); !strings.HasPrefix(got, "Update web/src/App.jsx") {
		t.Fatalf("single-file subject = %q", got)
	}
	// Single deletion → names it.
	if got := commitMessage(nil, []string{"api/old.mjs"}); !strings.HasPrefix(got, "Delete api/old.mjs") {
		t.Fatalf("single-delete subject = %q", got)
	}
	// Many files → count + top-level areas + body lists paths.
	got := commitMessage([]string{"web/a.jsx", "web/b.jsx", "api/s.mjs"}, []string{"api/old.mjs"})
	subject := strings.SplitN(got, "\n", 2)[0]
	if !strings.Contains(subject, "3 files") || !strings.Contains(subject, "delete 1 file") {
		t.Fatalf("multi subject = %q", subject)
	}
	if !strings.Contains(subject, "web") || !strings.Contains(subject, "api") {
		t.Fatalf("multi subject missing areas: %q", subject)
	}
	if !strings.Contains(got, "- web/a.jsx") || !strings.Contains(got, "- delete api/old.mjs") {
		t.Fatalf("body missing paths: %q", got)
	}
	// Not the old useless message.
	if strings.Contains(got, "sync workspace") {
		t.Fatalf("still using the generic message: %q", got)
	}
}

func TestCommitMessageStaysUnderRepositoryCommitLimit(t *testing.T) {
	long := strings.Repeat("deeply-nested-directory/", 3)
	var writes, deletes []string
	for i := 0; i < 15; i++ {
		writes = append(writes, fmt.Sprintf("web/src/%scomponent-%02d.jsx", long, i))
	}
	for i := 0; i < 10; i++ {
		deletes = append(deletes, fmt.Sprintf("api/%sold-%02d.mjs", long, i))
	}
	got := commitMessage(writes, deletes)
	if n := utf8.RuneCountInString(got); n > commitMessageMaxLength {
		t.Fatalf("message is %d characters, want at most %d:\n%s", n, commitMessageMaxLength, got)
	}
	listed := strings.Count(got, "\n- ") - 1 // minus the trailer line
	if listed < 1 || listed >= len(writes)+len(deletes) {
		t.Fatalf("listed %d paths, want a truncated non-empty list:\n%s", listed, got)
	}
	if want := fmt.Sprintf("\n- … and %d more", len(writes)+len(deletes)-listed); !strings.HasSuffix(got, want) {
		t.Fatalf("message does not end with %q:\n%s", want, got)
	}

	// A single absurdly long path still yields a bounded message.
	single := commitMessage([]string{strings.Repeat("x", 1000)}, nil)
	if n := utf8.RuneCountInString(single); n > commitMessageMaxLength {
		t.Fatalf("single long path message is %d characters", n)
	}

	// Small change sets are listed in full, with no trailer.
	small := commitMessage([]string{"a.txt", "b.txt"}, nil)
	if strings.Contains(small, "more") || !strings.Contains(small, "- a.txt") || !strings.Contains(small, "- b.txt") {
		t.Fatalf("small message = %q", small)
	}
}

// commitTestEnv drives commitWorkspace against a fake Code provider verb
// endpoint and a fake manager client. The Project AND the RepositoryCommit
// ride this provider's APIExport virtual workspace (the commit kind is
// claimed); the commit itself is asked for as the provider on the verb the
// export claims (only the Code provider can store the source bundle a
// RepositoryCommit points at), addressed under the fake hub as if it were the
// export virtual workspace. The env also mints a project identity from a fake
// hub identity service, as the reconciler does for the project's workload.
type commitTestEnv struct {
	t        *testing.T
	ctx      context.Context
	r        *Reconciler
	project  *aiv1alpha1.Project
	scope    workspace.Scope
	files    *workspace.FileStore
	c        client.Client
	repo     *unstructured.Unstructured
	calls    []commitActionCall
	respond  func(call int) (name string, failCode string)
	notified []CommitResult
}

// commitActionCall is one invocation the fake provider saw.
type commitActionCall struct {
	verb  string
	input map[string]any
}

// commits counts the commit invocations, ignoring any staging round trip.
func (env *commitTestEnv) commits() int {
	n := 0
	for _, call := range env.calls {
		if call.verb == codecommit.Action {
			n++
		}
	}
	return n
}

func (env *commitTestEnv) stages() int {
	n := 0
	for _, call := range env.calls {
		if call.verb == codecommit.StageBundleAction {
			n++
		}
	}
	return n
}

func newCommitTestEnv(t *testing.T, respond func(call int) (string, string), objects ...runtime.Object) *commitTestEnv {
	t.Helper()
	if respond == nil {
		respond = func(call int) (string, string) { return fmt.Sprintf("commit-%d", call), "" }
	}
	env := &commitTestEnv{t: t, ctx: context.Background(), respond: respond}
	staged := 0
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The route is the contract: the kube path of the claimed custom
		// subresource repositories/{verb} on the export virtual workspace,
		// /clusters/{id}/apis/code.railgrid.ai/v1alpha1/repositories/{name}/{verb},
		// rendered by dataplane.SubresourceURL. No contract version travels in
		// the path, and no cluster header: the path is the address.
		want := "/clusters/cluster-a/apis/code.railgrid.ai/v1alpha1/repositories/demo-repo/"
		if !strings.HasPrefix(r.URL.Path, want) {
			t.Errorf("verb route = %q, want %q{verb}", r.URL.Path, want)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+tenanttest.ProviderBearer {
			t.Errorf("verb called with %q, want the provider's own credential", got)
		}
		verb := strings.TrimPrefix(r.URL.Path, want)
		var envelope struct {
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Errorf("decode action input: %v", err)
			return
		}
		env.calls = append(env.calls, commitActionCall{verb: verb, input: envelope.Input})
		w.Header().Set("Content-Type", "application/json")
		if verb == codecommit.StageBundleAction {
			staged++
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
				"bundleRef":    fmt.Sprintf("bundle-%d", staged),
				"bundleDigest": "sha256:deadbeef",
				"fileCount":    len(envelope.Input["files"].([]any)),
				"size":         1,
			}})
			return
		}
		name, failCode := env.respond(env.commits())
		if failCode != "" {
			if failCode == "lost_response" {
				// Model Code accepting the request and persisting its object while
				// the response is lost between providers.
				commit := repositoryCommitObject(name, "Running", "")
				if err := env.c.Create(env.ctx, commit); err != nil {
					t.Errorf("persist accepted RepositoryCommit before losing response: %v", err)
				}
			}
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": failCode, "message": failCode}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
			"commit": map[string]any{"name": name, "uid": name + "-uid"},
		}})
	}))
	t.Cleanup(hub.Close)

	env.files = workspace.NewFileStore(t.TempDir())
	env.project = &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo",
			UID:  types.UID("uid-a"),
			Annotations: map[string]string{
				orgUUIDAnnotation:       "org-a",
				workspaceUUIDAnnotation: "ws-a",
				"kcp.io/cluster":        "cluster-a",
			},
		},
		Spec: aiv1alpha1.ProjectSpec{Repository: &aiv1alpha1.ProjectRepositoryBinding{RepositoryRef: "demo-repo", ConnectionRef: "github"}},
	}
	env.scope, _ = scopeOf(env.project)
	env.repo = &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "demo-repo", "uid": "repo-uid"},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}}
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	// The dependency kinds are served to the identity as any other: register
	// them so the fake client can hold RepositoryCommits and the workspace's
	// APIBinding alongside Projects.
	scheme.AddKnownTypeWithName(repositoryCommitGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(repositoryCommitGVK.GroupVersion().WithKind("RepositoryCommitList"), &unstructured.UnstructuredList{})
	env.c = fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(env.project).
		// The working-copy ledger is written through the status subresource,
		// so the fake has to serve it as one.
		WithStatusSubresource(&aiv1alpha1.Project{}).
		WithRuntimeObjects(objects...).Build()
	if err := env.c.Get(env.ctx, types.NamespacedName{Name: env.project.Name}, env.project); err != nil {
		t.Fatal(err)
	}
	// The working-copy ledger is the Project's own status (§9 Cut D.3), so the
	// convergence loop is exercised against it rather than against files the
	// reconciler's replica happens to hold. Reconcile attaches this same
	// ledger to its context; the tests' own reads go through the store's
	// default, which is pointed at the same client here.
	env.files.SetLedger(projectledger.FromControllerClient(env.c))
	env.ctx = workspace.ContextWithLedger(env.ctx, projectledger.FromControllerClient(env.c))
	env.write("app.txt", "hello\n")
	env.r = &Reconciler{
		Workspace:  env.files,
		HubBase:    hub.URL,
		Callers:    tenanttest.Callers(hub.URL),
		Identities: scopedidentity.New((&fakeIdentityHub{}).server(t)),
		OnCommitted: func(_ context.Context, got workspace.Scope, commit CommitResult) {
			if got != env.scope {
				t.Errorf("OnCommitted scope = %+v, want %+v", got, env.scope)
			}
			env.notified = append(env.notified, commit)
		},
	}
	return env
}

func (env *commitTestEnv) write(path, content string) {
	env.t.Helper()
	if _, err := env.files.WriteFile(env.ctx, env.scope, workspace.WriteOptions{Path: path, Content: content}); err != nil {
		env.t.Fatal(err)
	}
	if _, err := env.files.AddUncommittedPaths(env.ctx, env.scope, []string{path}); err != nil {
		env.t.Fatal(err)
	}
}

// commit runs one convergence pass and reports whether uncommitted work
// remains (commitOutcome.dirty).
func (env *commitTestEnv) commit() (bool, error) {
	outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	return outcome.dirty, err
}

func (env *commitTestEnv) pending() []string {
	env.t.Helper()
	paths, err := env.files.UncommittedPaths(env.ctx, env.scope)
	if err != nil {
		env.t.Fatal(err)
	}
	return paths
}

// pendingCommit reports the durable pending-commit record, and checks that the
// grant the project identity is built from names the same commit. They are one
// member of one object since §9 Cut D.3, so this asserts that the ledger the
// store reads and the status the identity reads have not drifted apart.
func (env *commitTestEnv) pendingCommit() (string, bool) {
	env.t.Helper()
	record, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil {
		env.t.Fatal(err)
	}
	stored := &aiv1alpha1.Project{}
	if err := env.c.Get(env.ctx, types.NamespacedName{Name: env.project.Name}, stored); err != nil {
		env.t.Fatal(err)
	}
	granted := projectPendingCommitRef(stored)
	if ok && granted != record.Name {
		env.t.Fatalf("project identity is granted %q, want the pending commit %q", granted, record.Name)
	}
	if !ok && granted != "" {
		env.t.Fatalf("project identity is granted %q with no pending commit", granted)
	}
	return record.Name, ok
}

func (env *commitTestEnv) setRepositoryCommit(name, phase, sha string) {
	env.t.Helper()
	landed := repositoryCommitObject(name, phase, sha)
	current := repositoryCommitObject(name, "", "")
	if err := env.c.Get(env.ctx, types.NamespacedName{Name: name}, current); err != nil {
		env.t.Fatal(err)
	}
	landed.SetResourceVersion(current.GetResourceVersion())
	if err := env.c.Update(env.ctx, landed); err != nil {
		env.t.Fatal(err)
	}
}

func repositoryCommitObject(name, phase, sha string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name},
		"status":   map[string]any{"phase": phase, "commitSHA": sha, "commitURL": "https://github.example/commit/" + sha, "branch": "main"},
	}}
	obj.SetGroupVersionKind(repositoryCommitGVK)
	return obj
}

// The whole protocol in one test: App Studio stages immutable bytes, records
// a keyed intent before invoking the action, and follows the returned
// RepositoryCommit. Nothing is settled until the CR itself says Succeeded.
func TestCommitWorkspaceRequestsTheActionAndFollowsTheRepositoryCommit(t *testing.T) {
	env := newCommitTestEnv(t, nil, repositoryCommitObject("commit-1", "Running", ""))

	dirty, err := env.commit()
	if err != nil || !dirty {
		t.Fatalf("commit = dirty %t, err %v; want the commit pending", dirty, err)
	}
	if env.stages() != 1 || env.commits() != 1 || len(env.calls) != 2 ||
		env.calls[0].verb != codecommit.StageBundleAction || env.calls[1].verb != codecommit.Action {
		t.Fatalf("action calls = %+v, want one stage then one commit", env.calls)
	}
	input := env.calls[1].input
	if input["repositoryUID"] != "repo-uid" {
		t.Fatalf("action input does not pin the Repository: %+v", input)
	}
	if input["requestID"] == "" || input["bundleRef"] != "bundle-1" || input["bundleDigest"] != "sha256:deadbeef" {
		t.Fatalf("action input does not carry its keyed immutable bundle: %+v", input)
	}
	if _, hasFiles := input["files"]; hasFiles {
		t.Fatalf("keyed commit carried mutable inline files: %+v", input)
	}
	files, _ := env.calls[0].input["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["path"] != "app.txt" {
		t.Fatalf("staged input files = %+v", files)
	}
	if name, ok := env.pendingCommit(); !ok || name != "commit-1" {
		t.Fatalf("pending commit = %q, %t; want commit-1 recorded and pointed at", name, ok)
	}
	if len(env.notified) != 0 || len(env.pending()) != 1 {
		t.Fatalf("an unsettled commit was announced: %+v / %v", env.notified, env.pending())
	}

	// The watch reports it landed: the paths settle with its SHA and both
	// records clear. No second action call: the CR is the authority.
	env.setRepositoryCommit("commit-1", "Succeeded", "feedface00")
	if dirty, err := env.commit(); err != nil || dirty {
		t.Fatalf("settling pass = dirty %t, err %v; want clean", dirty, err)
	}
	if got := env.pending(); len(got) != 0 {
		t.Fatalf("uncommitted paths = %v, want none", got)
	}
	if _, ok := env.pendingCommit(); ok {
		t.Fatal("pending commit not cleared after it landed")
	}
	want := CommitResult{RepositoryRef: "demo-repo", CommitSHA: "feedface00", CommitURL: "https://github.example/commit/feedface00", Branch: "main", Files: []string{"app.txt"}}
	if env.commits() != 1 || len(env.notified) != 1 || fmt.Sprint(env.notified[0]) != fmt.Sprint(want) {
		t.Fatalf("calls = %d, notified = %+v, want %+v", env.commits(), env.notified, want)
	}
}

// A commit the provider accepted but has not landed — rate-limited, queued,
// simply slow — is followed by name. It is no longer a special case: there is
// one path, and resending would queue a second RepositoryCommit behind the
// same limit.
func TestCommitWorkspaceDoesNotResendWhileACommitIsPending(t *testing.T) {
	env := newCommitTestEnv(t, nil, repositoryCommitObject("commit-1", "Running", ""))
	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("first commit = dirty %t, err %v; want pending", dirty, err)
	}
	env.write("later.txt", "later\n")
	for pass := 0; pass < 2; pass++ {
		if dirty, err := env.commit(); err != nil || !dirty {
			t.Fatalf("running pass %d = dirty %t, err %v", pass, dirty, err)
		}
	}
	if env.commits() != 1 {
		t.Fatalf("commit calls = %d, want 1 while the RepositoryCommit is pending", env.commits())
	}

	env.setRepositoryCommit("commit-1", "Succeeded", "feedface00")
	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("settling pass = dirty %t, err %v; want the later edit still pending", dirty, err)
	}
	if env.commits() != 2 || len(env.notified) != 1 || env.notified[0].CommitSHA != "feedface00" ||
		fmt.Sprint(env.notified[0].Files) != "[app.txt]" {
		t.Fatalf("calls = %d, notified = %+v; want the pending commit announced and one fresh commit sent", env.commits(), env.notified)
	}
	if name, ok := env.pendingCommit(); !ok || name != "commit-2" {
		t.Fatalf("pending commit = %q, %t; want the fresh commit-2 recorded", name, ok)
	}
}

func TestCommitWorkspaceResendsAfterPendingCommitFails(t *testing.T) {
	env := newCommitTestEnv(t, nil, repositoryCommitObject("commit-1", "Failed", ""))
	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("first commit = dirty %t, err %v; want pending", dirty, err)
	}
	outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || !outcome.dirty || outcome.retry || outcome.retryAfter <= 0 {
		t.Fatalf("after failed pending commit = %#v, %v; want persisted cooldown", outcome, err)
	}
	if env.commits() != 1 || env.stages() != 1 {
		t.Fatalf("stages=%d commits=%d; failed commit must not hot-retry or restage", env.stages(), env.commits())
	}
	pending, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || pending.Name != "commit-1" || pending.BlockedReason != repositoryCommitFailedReason || pending.BlockedAt == nil {
		t.Fatalf("pending failed commit = %#v, %t, %v; want retained failed object and cooldown", pending, ok, err)
	}
	if len(env.notified) != 0 {
		t.Fatalf("a failed commit was announced: %+v", env.notified)
	}
}

func TestCommitWorkspaceReplaysWhenPendingCommitIsGone(t *testing.T) {
	// No RepositoryCommit object at all: this fake models an action whose
	// keyed object was not present when read. Replay still uses the same key,
	// and Code must return that request's stable object name.
	env := newCommitTestEnv(t, func(int) (string, string) { return "commit-1", "" })
	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("first commit = dirty %t, err %v; want pending", dirty, err)
	}
	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("after the pending commit vanished = dirty %t, err %v", dirty, err)
	}
	if env.commits() != 2 || env.stages() != 1 || len(env.notified) != 0 {
		t.Fatalf("stages=%d calls=%d notified=%+v; want same keyed replay and no announcement", env.stages(), env.commits(), env.notified)
	}
}

// The pending commit is ONE record now. It used to be two — an annotation
// pointing at the RepositoryCommit and a file beside the tree holding what it
// carried — which could disagree the moment a replica lost its volume, and the
// reconciler had a branch for exactly that. Both halves are members of
// `status.workspace` since §9 Cut D.3, so this asserts what replaced that
// branch: the record a commit writes is the one the identity grant reads, and
// there is no second place for it to go missing from.
func TestPendingCommitIsOneRecordOnTheProject(t *testing.T) {
	env := newCommitTestEnv(t, nil, repositoryCommitObject("commit-1", "Running", ""))

	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("commit = dirty %t, err %v; want a fresh commit", dirty, err)
	}
	if name, ok := env.pendingCommit(); !ok || name != "commit-1" {
		t.Fatalf("pending commit = %q, %t; want commit-1", name, ok)
	}
	stored := &aiv1alpha1.Project{}
	if err := env.c.Get(env.ctx, types.NamespacedName{Name: env.project.Name}, stored); err != nil {
		t.Fatal(err)
	}
	pending := stored.Status.Workspace.PendingCommit
	if pending == nil || pending.Name != "commit-1" ||
		pending.RepositoryRef != "demo-repo" || pending.RepositoryUID != "repo-uid" ||
		pending.BundleRef != "bundle-1" || pending.BundleDigest != "sha256:deadbeef" ||
		pending.RequestID == "" || pending.WorkspaceDigest == "" ||
		len(pending.Paths) != 1 || pending.Paths[0] != "app.txt" {
		t.Fatalf("Project.status.workspace.pendingCommit = %#v", pending)
	}
	if pending.RequestedAt == nil {
		t.Fatal("the pending commit carries no requestedAt")
	}
	// Nothing about it is a metadata pointer any more.
	if _, ok := stored.Annotations["ai.railgrid.ai/pending-commit"]; ok {
		t.Fatal("the retired pending-commit annotation is still written")
	}
	if env.commits() != 1 {
		t.Fatalf("calls = %d", env.commits())
	}
}

// If Code accepts a keyed commit but the response is lost, the Project keeps
// the reservation without a name. Reconciliation repeats the exact request
// and staged bundle; it never restages the now-mutable workspace contents.
func TestCommitWorkspaceRecoversLostResponseWithSameImmutableIntent(t *testing.T) {
	env := newCommitTestEnv(t, func(call int) (string, string) {
		if call == 1 {
			return "commit-1", "lost_response"
		}
		return "commit-1", ""
	})

	if dirty, err := env.commit(); err == nil || !dirty {
		t.Fatalf("first pass = dirty %t, err %v; want the unknown outcome preserved", dirty, err)
	}
	reserved, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok {
		t.Fatalf("reserved intent = %#v, %t, %v", reserved, ok, err)
	}
	if reserved.Name != "" || reserved.RequestID == "" || reserved.BundleRef != "bundle-1" || reserved.BundleDigest != "sha256:deadbeef" {
		t.Fatalf("lost response did not leave the immutable unnamed intent: %#v", reserved)
	}

	// A later local mutation must not change what the existing request commits.
	env.write("later.txt", "later\n")
	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("recovery pass = dirty %t, err %v; want the same request pending", dirty, err)
	}
	if env.stages() != 1 || env.commits() != 2 {
		t.Fatalf("stages=%d commits=%d; want one stage and an idempotent replay", env.stages(), env.commits())
	}
	first, second := env.calls[1].input, env.calls[2].input
	for _, key := range []string{"requestID", "repositoryUID", "bundleRef", "bundleDigest", "message", "branch"} {
		if first[key] != second[key] {
			t.Fatalf("replayed input %s changed: first=%v second=%v", key, first[key], second[key])
		}
	}
	if _, hasFiles := second["files"]; hasFiles {
		t.Fatalf("recovery reread mutable files instead of using the reservation: %+v", second)
	}
	if name, ok := env.pendingCommit(); !ok || name != "commit-1" {
		t.Fatalf("recovered pending name = %q, %t", name, ok)
	}
}

type rejectCommitReservationLedger struct {
	workspace.Ledger
}

func (l rejectCommitReservationLedger) Update(ctx context.Context, scope workspace.Scope, mutate func(*workspace.LedgerRecord) (bool, error)) (workspace.LedgerRecord, error) {
	return l.Ledger.Update(ctx, scope, func(record *workspace.LedgerRecord) (bool, error) {
		changed, err := mutate(record)
		if err != nil || !changed || record.PendingCommit == nil || record.PendingCommit.RequestID == "" {
			return changed, err
		}
		return false, errors.New("injected Project reservation write failure")
	})
}

// Staging may leave an orphan bundle, but no RepositoryCommit action may run
// until the immutable intent has been durably reserved on the Project.
func TestCommitWorkspaceDoesNotCallCommitWhenReservationFails(t *testing.T) {
	env := newCommitTestEnv(t, nil)
	ledger := rejectCommitReservationLedger{Ledger: projectledger.FromControllerClient(env.c)}
	env.files.SetLedger(ledger)
	env.ctx = workspace.ContextWithLedger(env.ctx, ledger)

	dirty, err := env.commit()
	if err == nil || !dirty {
		t.Fatalf("commit = dirty %t, err %v; want reservation failure", dirty, err)
	}
	if env.stages() != 1 || env.commits() != 0 {
		t.Fatalf("stages=%d commits=%d; want an orphan stage and no commit action", env.stages(), env.commits())
	}
	if _, ok, err := env.files.PendingCommit(env.ctx, env.scope); err != nil || ok {
		t.Fatalf("pending after failed reservation = %t, %v; want none", ok, err)
	}
}

// Even a refused action leaves its immutable intent reserved: a timeout or
// response lost after an earlier attempt must never allow a different request
// to overwrite the possibly accepted commit.
func TestCommitWorkspaceRefusalLeavesFilesDirty(t *testing.T) {
	env := newCommitTestEnv(t, func(int) (string, string) { return "", "action_forbidden" })
	dirty, err := env.commit()
	if err != nil || !dirty {
		t.Fatalf("commit = dirty %t, err %v; want durable block without a hot retry", dirty, err)
	}
	if name, ok := env.pendingCommit(); !ok || name != "" {
		t.Fatalf("reserved commit = %q, %t; want the unnamed intent retained", name, ok)
	}
	pending, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || pending.RequestID == "" || pending.BundleRef == "" || pending.BlockedReason != "ActionForbidden" {
		t.Fatalf("pending recovery intent = %#v, %t, %v", pending, ok, err)
	}
	if got := env.pending(); len(got) != 1 {
		t.Fatalf("uncommitted paths = %v, want the file still dirty", got)
	}
}

func TestCommitWorkspaceBlocksPermanentReplayFailuresAndLeavesTransientFailuresRetryable(t *testing.T) {
	tests := []struct {
		name       string
		code       string
		wantReason string
		wantRetry  bool
		wantError  bool
	}{
		{name: "request conflict", code: "commit_request_conflict", wantReason: "CommitRequestConflict"},
		{name: "missing staged bundle", code: "bundle_unavailable", wantReason: "StagedBundleMissing"},
		{name: "forbidden", code: "action_forbidden", wantReason: "ActionForbidden"},
		{name: "temporary backend outage", code: "commit_not_created", wantRetry: true, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newCommitTestEnv(t, func(int) (string, string) { return "", tt.code })
			env.write("app.txt", "source\n")
			outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
			if (err != nil) != tt.wantError || outcome.retry != tt.wantRetry || !outcome.dirty {
				t.Fatalf("first outcome = %#v, %v; want dirty with retry=%t, error=%t", outcome, err, tt.wantRetry, tt.wantError)
			}
			pending, ok, pendingErr := env.files.PendingCommit(env.ctx, env.scope)
			if pendingErr != nil || !ok {
				t.Fatalf("pending intent = %#v, %t, %v", pending, ok, pendingErr)
			}
			if tt.wantReason == "" {
				if pending.BlockedReason != "" {
					t.Fatalf("transient failure was blocked: %+v", pending)
				}
				return
			}
			if pending.BlockedReason != tt.wantReason || pending.BlockedMessage == "" || pending.BlockedAt == nil || pending.Name != "" {
				t.Fatalf("pending block = %+v; want unnamed %s block", pending, tt.wantReason)
			}
			beforeCalls := env.commits()
			retryOutcome, retryErr := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
			if retryErr != nil || retryOutcome.retry || !retryOutcome.dirty || env.commits() != beforeCalls {
				t.Fatalf("blocked follow-up = %#v, %v; commits=%d, want no retry or action call", retryOutcome, retryErr, env.commits())
			}
		})
	}
}

func TestCommitWorkspaceRecoversBlockedReplayWithExplicitFreshToken(t *testing.T) {
	env := newCommitTestEnv(t, func(call int) (string, string) {
		if call == 1 {
			return "commit-1", "lost_response"
		}
		if call == 2 {
			return "", "commit_request_conflict"
		}
		return "commit-1", ""
	})
	env.write("app.txt", "saved source\n")
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err == nil || !outcome.retry {
		t.Fatalf("lost response = %#v, %v; want a retryable ambiguous result", outcome, err)
	}
	reserved, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || reserved.Name != "" {
		t.Fatalf("reserved request = %#v, %t, %v; want unnamed", reserved, ok, err)
	}
	env.write("later.txt", "later source\n")
	outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || outcome.retry {
		t.Fatalf("permanent replay = %#v, %v; want durable block without a hot retry", outcome, err)
	}
	blocked, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || blocked.BlockedReason != "CommitRequestConflict" || blocked.Name != "" {
		t.Fatalf("blocked request = %#v, %t, %v", blocked, ok, err)
	}
	if blocked.RequestID != reserved.RequestID || blocked.BundleRef != reserved.BundleRef || blocked.BundleDigest != reserved.BundleDigest ||
		blocked.WorkspaceDigest != reserved.WorkspaceDigest || strings.Join(blocked.Paths, ",") != strings.Join(reserved.Paths, ",") {
		t.Fatalf("blocking changed the immutable request: before=%+v after=%+v", reserved, blocked)
	}
	if env.stages() != 1 || env.commits() != 2 {
		t.Fatalf("stages=%d commits=%d; want one stage and the original request plus one replay", env.stages(), env.commits())
	}

	// A fresh token explicitly asks App Studio to replay this request. The
	// controller consumes the token before calling Code and keeps the same
	// request ID, bundle, and captured source snapshot.
	env.project.Annotations[retryPendingCommitAnnotation] = blocked.RequestID + ":operator-retry-1"
	outcome, err = env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || outcome.retry || !outcome.dirty {
		t.Fatalf("explicit retry = %#v, %v; want the original request to be pending", outcome, err)
	}
	recovered, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || recovered.Name != "commit-1" || recovered.BlockedReason != "" || recovered.RetryToken != "operator-retry-1" {
		t.Fatalf("recovered request = %#v, %t, %v", recovered, ok, err)
	}
	if env.stages() != 1 || env.commits() != 3 {
		t.Fatalf("stages=%d commits=%d; explicit recovery must replay without restaging", env.stages(), env.commits())
	}
}

func TestCommitWorkspaceBlocksMissingNamedCommitAndRequiresExplicitRetry(t *testing.T) {
	env := newCommitTestEnv(t, nil)
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err != nil || !outcome.dirty {
		t.Fatalf("initial commit = %#v, %v; want a named pending commit", outcome, err)
	}
	pending, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || pending.Name != "commit-1" || pending.RequestID == "" {
		t.Fatalf("initial pending commit = %#v, %t, %v", pending, ok, err)
	}

	// Simulate an expired bundle after the named RepositoryCommit was deleted.
	env.respond = func(int) (string, string) { return "", "bundle_unavailable" }
	outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || outcome.retry || !outcome.dirty {
		t.Fatalf("missing named commit replay = %#v, %v; want durable block without hot retry", outcome, err)
	}
	blocked, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || blocked.Name != "commit-1" || blocked.BlockedReason != "StagedBundleMissing" {
		t.Fatalf("named blocked intent = %#v, %t, %v", blocked, ok, err)
	}
	if env.stages() != 1 || env.commits() != 2 {
		t.Fatalf("stages=%d commits=%d; want one stage and one missing-bundle replay", env.stages(), env.commits())
	}

	// The unchanged annotation does not wake the named request. A failed
	// explicit attempt is also blocked again and is not retried on the next pass.
	env.project.Annotations[retryPendingCommitAnnotation] = blocked.RequestID + ":operator-retry-1"
	outcome, err = env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || outcome.retry || !outcome.dirty {
		t.Fatalf("explicit missing-bundle retry = %#v, %v; want a fresh durable block", outcome, err)
	}
	if env.commits() != 3 || env.stages() != 1 {
		t.Fatalf("stages=%d commits=%d; explicit retry should replay without staging", env.stages(), env.commits())
	}
	outcome, err = env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || outcome.retry || env.commits() != 3 {
		t.Fatalf("same-token follow-up = %#v, %v; commits=%d, want no hot retry", outcome, err, env.commits())
	}

	// A new token is the only way to replay the exact named intent again.
	env.project.Annotations[retryPendingCommitAnnotation] = blocked.RequestID + ":operator-retry-2"
	env.respond = func(int) (string, string) { return "commit-1", "" }
	outcome, err = env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || outcome.retry || !outcome.dirty {
		t.Fatalf("second explicit retry = %#v, %v; want the same named request watched", outcome, err)
	}
	recovered, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || recovered.Name != "commit-1" || recovered.BlockedReason != "" || recovered.RetryToken != "operator-retry-2" {
		t.Fatalf("recovered named intent = %#v, %t, %v", recovered, ok, err)
	}
	if env.stages() != 1 || env.commits() != 4 {
		t.Fatalf("stages=%d commits=%d; explicit named retries must not restage", env.stages(), env.commits())
	}
}

func TestNamedBlockedCommitStillSettlesWhenItsRepositoryCommitReappears(t *testing.T) {
	env := newCommitTestEnv(t, nil)
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err != nil || !outcome.dirty {
		t.Fatalf("initial commit = %#v, %v", outcome, err)
	}
	pending, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || pending.Name != "commit-1" {
		t.Fatalf("initial named pending = %#v, %t, %v", pending, ok, err)
	}
	if err := env.c.Create(env.ctx, repositoryCommitObject("commit-1", "Running", "")); err != nil {
		t.Fatal(err)
	}
	if blocked, err := env.files.BlockNamedPendingCommit(env.ctx, env.scope, pending.RequestID, pending.Name, "StagedBundleMissing", "bundle unavailable"); err != nil || !blocked {
		t.Fatalf("block named pending = %t, %v", blocked, err)
	}

	outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || outcome.retry || env.commits() != 1 {
		t.Fatalf("running reappeared commit = %#v, %v; commits=%d, want watch wait", outcome, err, env.commits())
	}
	env.setRepositoryCommit("commit-1", "Succeeded", "feedface00")
	outcome, err = env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || !outcome.retry || env.commits() != 1 {
		t.Fatalf("settled reappeared commit = %#v, %v; commits=%d, want settle then schedule next pass", outcome, err, env.commits())
	}
	if _, ok, err := env.files.PendingCommit(env.ctx, env.scope); err != nil || ok {
		t.Fatalf("pending after reappeared commit settlement = %t, %v; want cleared", ok, err)
	}
}

func TestFailedNamedCommitRequeuesBeforeRestaging(t *testing.T) {
	env := newCommitTestEnv(t, nil)
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err != nil || !outcome.dirty {
		t.Fatalf("initial commit = %#v, %v", outcome, err)
	}
	if err := env.c.Create(env.ctx, repositoryCommitObject("commit-1", "Failed", "")); err != nil {
		t.Fatal(err)
	}
	outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || !outcome.dirty || outcome.retry || outcome.retryAfter <= 0 {
		t.Fatalf("failed commit = %#v, %v; want dirty work held through a durable cooldown", outcome, err)
	}
	if env.stages() != 1 || env.commits() != 1 {
		t.Fatalf("stages=%d commits=%d; failed commit must not retry during cooldown", env.stages(), env.commits())
	}
	pending, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || pending.Name != "commit-1" || pending.BlockedReason != repositoryCommitFailedReason || pending.BlockedAt == nil {
		t.Fatalf("pending after failure = %#v, %t, %v; want exact failed request retained with cooldown", pending, ok, err)
	}
	if got := env.pending(); len(got) != 1 {
		t.Fatalf("dirty paths after failed commit = %v; want source retained", got)
	}

	// A status watch event or controller restart before the deadline must read
	// the same persisted timestamp and avoid another Code action.
	blockedAt := *pending.BlockedAt
	outcome, err = env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || !outcome.dirty || outcome.retry || outcome.retryAfter <= 0 || env.commits() != 1 || env.stages() != 1 {
		t.Fatalf("immediate cooldown replay = %#v, %v; stages=%d commits=%d, want same persisted wait", outcome, err, env.stages(), env.commits())
	}
	// The failed object may be deleted while the Project is waiting. Its
	// persisted BlockedAt still drives the release deadline.
	failed := repositoryCommitObject("commit-1", "", "")
	if err := env.c.Get(env.ctx, types.NamespacedName{Name: "commit-1"}, failed); err != nil {
		t.Fatal(err)
	}
	if err := env.c.Delete(env.ctx, failed); err != nil {
		t.Fatal(err)
	}
	if env.project.Spec.Repository != nil {
		env.project.Spec.Repository.RepositoryRef = "replacement-repository"
	}
	if err := env.r.blockPendingRepositoryBindingChange(env.ctx, env.project); err != nil {
		t.Fatalf("preflight must preserve failed-commit cooldown: %v", err)
	}
	stillBlocked, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || stillBlocked.BlockedReason != repositoryCommitFailedReason || stillBlocked.BlockedAt == nil || !stillBlocked.BlockedAt.Equal(blockedAt) {
		t.Fatalf("preflight changed failed cooldown = %#v, %t, %v; want stable reason and timestamp", stillBlocked, ok, err)
	}
	env.project.Spec.Repository.RepositoryRef = "demo-repo"

	stored := &aiv1alpha1.Project{}
	if err := env.c.Get(env.ctx, types.NamespacedName{Name: env.project.Name}, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Workspace == nil || stored.Status.Workspace.PendingCommit == nil {
		t.Fatal("failed cooldown disappeared before its deadline")
	}
	expired := metav1.NewTime(time.Now().Add(-repositoryCommitFailedRetryDelay - time.Second))
	stored.Status.Workspace.PendingCommit.BlockedAt = &expired
	if err := env.c.Status().Update(env.ctx, stored); err != nil {
		t.Fatalf("age failed cooldown timestamp: %v", err)
	}
	outcome, err = env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || !outcome.dirty || !outcome.retry || env.commits() != 1 || env.stages() != 1 {
		t.Fatalf("expired cooldown release = %#v, %v; stages=%d commits=%d, want release before fresh stage", outcome, err, env.stages(), env.commits())
	}
	if _, ok, err := env.files.PendingCommit(env.ctx, env.scope); err != nil || ok {
		t.Fatalf("pending after cooldown release = %t, %v; want cleared", ok, err)
	}
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err != nil || !outcome.dirty {
		t.Fatalf("next pass after cooldown = %#v, %v; want new request after clean release", outcome, err)
	}
	if env.stages() != 2 || env.commits() != 2 {
		t.Fatalf("stages=%d commits=%d; fresh immutable intent should be created only after deadline", env.stages(), env.commits())
	}
}

func TestLegacyNamedFailedCommitEntersCooldown(t *testing.T) {
	env := newCommitTestEnv(t, nil)
	if err := env.files.RecordPendingCommit(env.ctx, env.scope, workspace.PendingCommit{
		Name: "legacy-commit", WorkspaceDigest: "sha256:legacy", Paths: []string{"app.txt"},
	}); err != nil {
		t.Fatalf("record legacy named pending commit: %v", err)
	}
	if err := env.c.Create(env.ctx, repositoryCommitObject("legacy-commit", "Failed", "")); err != nil {
		t.Fatal(err)
	}
	outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || !outcome.dirty || outcome.retry || outcome.retryAfter <= 0 {
		t.Fatalf("legacy failed commit = %#v, %v; want named cooldown", outcome, err)
	}
	pending, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || pending.RequestID != "" || pending.Name != "legacy-commit" || pending.BlockedReason != repositoryCommitFailedReason || pending.BlockedAt == nil {
		t.Fatalf("legacy pending after failed phase = %#v, %t, %v; want retained named cooldown", pending, ok, err)
	}
	if env.stages() != 0 || env.commits() != 0 {
		t.Fatalf("legacy cooldown unexpectedly called Code: stages=%d commits=%d", env.stages(), env.commits())
	}
}

func TestFreshRetryTokenBypassesFailedCommitCooldown(t *testing.T) {
	env := newCommitTestEnv(t, nil)
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err != nil || !outcome.dirty {
		t.Fatalf("initial commit = %#v, %v", outcome, err)
	}
	if err := env.c.Create(env.ctx, repositoryCommitObject("commit-1", "Failed", "")); err != nil {
		t.Fatal(err)
	}
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err != nil || outcome.retryAfter <= 0 {
		t.Fatalf("failed commit = %#v, %v; want cooldown", outcome, err)
	}
	pending, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || pending.BlockedReason != repositoryCommitFailedReason {
		t.Fatalf("pending failed commit = %#v, %t, %v", pending, ok, err)
	}
	env.project.Annotations[retryPendingCommitAnnotation] = pending.RequestID + ":operator-bypass-1"
	outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || !outcome.dirty || !outcome.retry {
		t.Fatalf("explicit cooldown bypass = %#v, %v; want release and next-pass retry", outcome, err)
	}
	if _, ok, err := env.files.PendingCommit(env.ctx, env.scope); err != nil || ok {
		t.Fatalf("pending after explicit cooldown bypass = %t, %v; want released", ok, err)
	}
	if env.stages() != 1 || env.commits() != 1 {
		t.Fatalf("explicit cooldown bypass retried in same pass: stages=%d commits=%d", env.stages(), env.commits())
	}
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err != nil || !outcome.dirty {
		t.Fatalf("pass after explicit cooldown bypass = %#v, %v; want fresh request", outcome, err)
	}
	if env.stages() != 2 || env.commits() != 2 {
		t.Fatalf("fresh request counts after bypass: stages=%d commits=%d", env.stages(), env.commits())
	}
}

func TestFailedNamedCommitThatReappearsRunningReturnsToWatchSettlement(t *testing.T) {
	env := newCommitTestEnv(t, nil)
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err != nil || !outcome.dirty {
		t.Fatalf("initial commit = %#v, %v", outcome, err)
	}
	if err := env.c.Create(env.ctx, repositoryCommitObject("commit-1", "Failed", "")); err != nil {
		t.Fatal(err)
	}
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err != nil || outcome.retryAfter <= 0 {
		t.Fatalf("failed commit = %#v, %v; want cooldown", outcome, err)
	}
	pending, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || pending.BlockedReason != repositoryCommitFailedReason {
		t.Fatalf("pending failed commit = %#v, %t, %v", pending, ok, err)
	}
	env.setRepositoryCommit("commit-1", "Running", "")
	outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || !outcome.dirty || outcome.retry || outcome.retryAfter != 0 {
		t.Fatalf("reappeared running commit = %#v, %v; want watch-driven wait", outcome, err)
	}
	pending, ok, err = env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || pending.Name != "commit-1" || pending.BlockedReason != "" {
		t.Fatalf("pending running commit = %#v, %t, %v; want normal named watch state", pending, ok, err)
	}
	env.setRepositoryCommit("commit-1", "Succeeded", "feedface00")
	outcome, err = env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || outcome.retry || env.commits() != 1 {
		t.Fatalf("reappeared succeeded commit = %#v, %v; commits=%d, want settlement without replay", outcome, err, env.commits())
	}
	if _, ok, err := env.files.PendingCommit(env.ctx, env.scope); err != nil || ok {
		t.Fatalf("pending after reappeared success = %t, %v; want cleared", ok, err)
	}
}

func TestCommitWorkspaceBlocksRepositoryBindingChangeForReservedIntent(t *testing.T) {
	env := newCommitTestEnv(t, func(int) (string, string) { return "commit-1", "lost_response" })
	env.write("app.txt", "source\n")
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err == nil || !outcome.retry {
		t.Fatalf("lost response = %#v, %v; want an unnamed durable intent", outcome, err)
	}
	env.project.Spec.Repository = nil
	outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || outcome.retry {
		t.Fatalf("repository removal = %#v, %v; want a visible block without retry", outcome, err)
	}
	pending, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || pending.BlockedReason != "RepositoryBindingChanged" || pending.Name != "" {
		t.Fatalf("pending after repository removal = %#v, %t, %v", pending, ok, err)
	}
	if env.commits() != 1 {
		t.Fatalf("commits=%d; binding removal must not replay the saved request", env.commits())
	}
}

func TestPreflightBlocksChangedRepositoryRefBeforeRepositoryConvergence(t *testing.T) {
	env := newCommitTestEnv(t, func(int) (string, string) { return "commit-1", "lost_response" })
	env.write("app.txt", "source\n")
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err == nil || !outcome.retry {
		t.Fatalf("lost response = %#v, %v; want an unnamed durable intent", outcome, err)
	}
	env.project.Spec.Repository.RepositoryRef = "replacement-repository"
	if err := env.r.blockPendingRepositoryBindingChange(env.ctx, env.project); err != nil {
		t.Fatalf("preflight binding check: %v", err)
	}
	pending, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || pending.BlockedReason != "RepositoryBindingChanged" || pending.Name != "" {
		t.Fatalf("preflight pending block = %#v, %t, %v; want repository binding block", pending, ok, err)
	}
	if env.commits() != 1 {
		t.Fatalf("commit calls=%d; preflight must not replay the saved request", env.commits())
	}
}

func TestCommitWorkspaceBlocksRepositoryUIDMismatchForReservedIntent(t *testing.T) {
	env := newCommitTestEnv(t, func(int) (string, string) { return "commit-1", "lost_response" })
	env.write("app.txt", "source\n")
	if outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo); err == nil || !outcome.retry {
		t.Fatalf("lost response = %#v, %v; want an unnamed durable intent", outcome, err)
	}
	env.repo.SetUID(types.UID("replacement-repo-uid"))
	outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.project, env.repo)
	if err != nil || outcome.retry {
		t.Fatalf("repository UID change = %#v, %v; want a visible block without retry", outcome, err)
	}
	pending, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil || !ok || pending.BlockedReason != "RepositoryUIDMismatch" || pending.Name != "" {
		t.Fatalf("pending after repository UID change = %#v, %t, %v", pending, ok, err)
	}
	if env.commits() != 1 {
		t.Fatalf("commits=%d; UID mismatch must not replay the saved request", env.commits())
	}
}

// Past the catalogue's 1 MiB input ceiling the payload is staged first and
// the commit names the handle instead of inline files.
func TestCommitWorkspaceStagesPayloadsOverTheCatalogueCeiling(t *testing.T) {
	env := newCommitTestEnv(t, nil)
	// The workspace store bounds one file well under the action ceiling, so
	// the payload gets there the way a real project does: many files.
	for i := 0; i < 6; i++ {
		env.write(fmt.Sprintf("src/page-%d.txt", i), strings.Repeat("x", 200<<10))
	}

	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("commit = dirty %t, err %v", dirty, err)
	}
	if len(env.calls) != 2 || env.calls[0].verb != codecommit.StageBundleAction || env.calls[1].verb != codecommit.Action {
		t.Fatalf("calls = %+v, want stage then commit", env.calls)
	}
	staged := env.calls[0].input
	if staged["repositoryUID"] != "repo-uid" || len(staged["files"].([]any)) != 7 {
		t.Fatalf("staging input = %+v", staged)
	}
	commit := env.calls[1].input
	if commit["bundleRef"] != "bundle-1" || commit["bundleDigest"] != "sha256:deadbeef" {
		t.Fatalf("commit input does not name the staged bundle: %+v", commit)
	}
	if _, hasFiles := commit["files"]; hasFiles {
		t.Fatalf("a staged commit still carried inline files: %+v", commit)
	}
}
