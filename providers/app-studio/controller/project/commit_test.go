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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

// commitTestEnv drives commitWorkspace against a fake Code provider action
// endpoint and a fake client. Production splits that client in two — the
// Project rides this provider's APIExport virtual workspace, the
// RepositoryCommit and the APIBinding ride the tenant workspace as the
// project identity — but a single fake holding all three stands in for the
// pair here; what the tests are about is the commit protocol, not which
// socket each read goes down. The commit itself is asked for on the action
// grammar (only the Code provider can store the source bundle a
// RepositoryCommit points at), so the env also mints a project identity from
// a fake hub identity service.
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

func newCommitTestEnv(t *testing.T, respond func(call int) (string, string), objects ...runtime.Object) *commitTestEnv {
	t.Helper()
	if respond == nil {
		respond = func(call int) (string, string) { return fmt.Sprintf("commit-%d", call), "" }
	}
	env := &commitTestEnv{t: t, ctx: context.Background(), respond: respond}
	staged := 0
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The route is the contract: /services/providers/{provider}/actions/
		// clusters/{id}/repositories/{name}/{verb}/v1, addressed through
		// dataplane.ProviderPath at the provider this workspace's APIBinding
		// names.
		want := "/services/providers/code/actions/clusters/cluster-a/repositories/demo-repo/"
		if !strings.HasPrefix(r.URL.Path, want) || !strings.HasSuffix(r.URL.Path, "/v1") {
			t.Errorf("action route = %q, want %q{verb}/v1", r.URL.Path, want)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("action called without the project identity: %q", got)
		}
		if got := r.Header.Get("X-Railgrid-Cluster"); got != "cluster-a" {
			t.Errorf("action cluster header = %q", got)
		}
		verb := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, want), "/v1")
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
	scheme.AddKnownTypeWithName(apiBindingGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(apiBindingGVK.GroupVersion().WithKind("APIBindingList"), &unstructured.UnstructuredList{})
	env.c = fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(env.project, codeAPIBindingObject()).
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

// codeAPIBindingObject is the workspace's own binding for the Code APIExport:
// the object that says which provider segment addresses it here.
func codeAPIBindingObject() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "code"},
		"spec": map[string]any{
			"reference": map[string]any{"export": map[string]any{"name": "code.providers.railgrid.ai"}},
		},
	}}
	obj.SetGroupVersionKind(apiBindingGVK)
	return obj
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
	token, err := env.r.identityToken(env.ctx, clusterOf(env.project), env.project)
	if err != nil {
		env.t.Fatalf("project identity: %v", err)
	}
	outcome, err := env.r.commitWorkspace(env.ctx, env.c, env.c, token, env.project, env.repo)
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

// The whole protocol in one test: the action is invoked on the grammar with
// the Repository's UID pinned, the RepositoryCommit it names is recorded as
// pending on both the ledger and the Project, and nothing is settled until
// the CR itself says Succeeded.
func TestCommitWorkspaceRequestsTheActionAndFollowsTheRepositoryCommit(t *testing.T) {
	env := newCommitTestEnv(t, nil, repositoryCommitObject("commit-1", "Running", ""))

	dirty, err := env.commit()
	if err != nil || !dirty {
		t.Fatalf("commit = dirty %t, err %v; want the commit pending", dirty, err)
	}
	if env.commits() != 1 || env.calls[0].verb != codecommit.Action {
		t.Fatalf("action calls = %+v, want one commit", env.calls)
	}
	input := env.calls[0].input
	if input["repositoryUID"] != "repo-uid" {
		t.Fatalf("action input does not pin the Repository: %+v", input)
	}
	files, _ := input["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["path"] != "app.txt" {
		t.Fatalf("action input files = %+v", files)
	}
	if _, hasBundle := input["bundleRef"]; hasBundle {
		t.Fatalf("a one-file commit was staged: %+v", input)
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
	// The failure clears the record; the same pass then sends a fresh commit.
	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("after failed pending commit = dirty %t, err %v", dirty, err)
	}
	if env.commits() != 2 {
		t.Fatalf("commit calls = %d, want a resend after the failure", env.commits())
	}
	if name, ok := env.pendingCommit(); !ok || name != "commit-2" {
		t.Fatalf("pending commit = %q, %t; want commit-2", name, ok)
	}
	if len(env.notified) != 0 {
		t.Fatalf("a failed commit was announced: %+v", env.notified)
	}
}

func TestCommitWorkspaceResendsWhenPendingCommitIsGone(t *testing.T) {
	// No RepositoryCommit object at all: the provider never persisted it.
	env := newCommitTestEnv(t, nil)
	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("first commit = dirty %t, err %v; want pending", dirty, err)
	}
	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("after the pending commit vanished = dirty %t, err %v", dirty, err)
	}
	if env.commits() != 2 || len(env.notified) != 0 {
		t.Fatalf("calls = %d, notified = %+v; want one fresh commit and no announcement", env.commits(), env.notified)
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
		pending.RepositoryRef != "demo-repo" || pending.WorkspaceDigest == "" ||
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

// A refused action leaves the files dirty and records nothing: there is no
// RepositoryCommit to follow, so the next idle pass simply tries again.
func TestCommitWorkspaceRefusalLeavesFilesDirty(t *testing.T) {
	env := newCommitTestEnv(t, func(int) (string, string) { return "", "action_forbidden" })
	dirty, err := env.commit()
	if err == nil || !dirty {
		t.Fatalf("commit = dirty %t, err %v; want a reported failure", dirty, err)
	}
	if !strings.Contains(err.Error(), "action_forbidden") {
		t.Fatalf("error does not carry the provider's typed code: %v", err)
	}
	if _, ok := env.pendingCommit(); ok {
		t.Fatal("a refused commit was recorded as pending")
	}
	if got := env.pending(); len(got) != 1 {
		t.Fatalf("uncommitted paths = %v, want the file still dirty", got)
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
