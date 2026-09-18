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

// commitTestEnv drives commitWorkspace against a fake hub MCP endpoint, a
// fake tenant client holding RepositoryCommits, and a fake control-plane
// client holding the Project (the pending-commit pointer is written there).
type commitTestEnv struct {
	t        *testing.T
	ctx      context.Context
	r        *Reconciler
	project  *aiv1alpha1.Project
	scope    workspace.Scope
	files    *workspace.FileStore
	tenant   client.Client
	control  client.Client
	repo     *unstructured.Unstructured
	calls    int
	respond  func(call int) (text string, isError bool)
	notified []CommitResult
}

func newCommitTestEnv(t *testing.T, respond func(call int) (string, bool), objects ...runtime.Object) *commitTestEnv {
	t.Helper()
	env := &commitTestEnv{t: t, ctx: context.Background(), respond: respond}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode MCP request: %v", err)
			return
		}
		result := map[string]any{}
		if req.Method == "tools/call" {
			env.calls++
			text, isError := env.respond(env.calls)
			result = map[string]any{"isError": isError, "content": []any{map[string]any{"type": "text", "text": text}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
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
	env.write("app.txt", "hello\n")
	env.repo = &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}}
	env.tenant = fake.NewClientBuilder().WithRuntimeObjects(objects...).Build()
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	env.control = fake.NewClientBuilder().WithScheme(scheme).WithObjects(env.project).Build()
	if err := env.control.Get(env.ctx, types.NamespacedName{Name: env.project.Name}, env.project); err != nil {
		t.Fatal(err)
	}
	env.r = &Reconciler{
		Workspace: env.files,
		HubBase:   hub.URL,
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
	outcome, err := env.r.commitWorkspace(env.ctx, env.control, "token", env.tenant, env.project, env.repo)
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

// pendingCommit reports the durable pending-commit record and the pointer
// the Project carries for it; both must agree.
func (env *commitTestEnv) pendingCommit() (string, bool) {
	env.t.Helper()
	record, ok, err := env.files.PendingCommit(env.ctx, env.scope)
	if err != nil {
		env.t.Fatal(err)
	}
	stored := &aiv1alpha1.Project{}
	if err := env.control.Get(env.ctx, types.NamespacedName{Name: env.project.Name}, stored); err != nil {
		env.t.Fatal(err)
	}
	pointer := stored.Annotations[pendingCommitAnnotation]
	if ok && pointer != record.Name {
		env.t.Fatalf("Project pending-commit pointer = %q, want %q (the ledger record)", pointer, record.Name)
	}
	if !ok && pointer != "" {
		env.t.Fatalf("Project pending-commit pointer = %q without a ledger record", pointer)
	}
	return record.Name, ok
}

func (env *commitTestEnv) setRepositoryCommit(name, phase, sha string) {
	env.t.Helper()
	landed := repositoryCommitObject(name, phase, sha)
	current := repositoryCommitObject(name, "", "")
	if err := env.tenant.Get(env.ctx, types.NamespacedName{Name: name}, current); err != nil {
		env.t.Fatal(err)
	}
	landed.SetResourceVersion(current.GetResourceVersion())
	if err := env.tenant.Update(env.ctx, landed); err != nil {
		env.t.Fatal(err)
	}
}

func commitToolText(fields map[string]any) string {
	raw, _ := json.Marshal(fields)
	return string(raw)
}

func repositoryCommitObject(name, phase, sha string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name},
		"status":   map[string]any{"phase": phase, "commitSHA": sha, "commitURL": "https://github.example/commit/" + sha, "branch": "main"},
	}}
	obj.SetGroupVersionKind(repositoryCommitGVK)
	return obj
}

func TestCommitWorkspaceSettlesOnlySucceededCommits(t *testing.T) {
	succeeded := commitToolText(map[string]any{"repositoryRef": "demo-repo", "name": "commit-1", "phase": "Succeeded", "commitSHA": "0123456789abcdef", "commitURL": "https://github.example/commit/0123456", "branch": "main"})
	for _, tt := range []struct {
		name        string
		text        string
		isError     bool
		wantErr     bool
		wantPending bool
		wantNotify  bool
		wantDirty   int
	}{
		{name: "failed commit is retried", text: `RepositoryCommit "commit-1" failed: branch protected`, isError: true, wantErr: true, wantDirty: 1},
		{name: "succeeded without sha", text: commitToolText(map[string]any{"name": "commit-1", "phase": "Succeeded"}), wantErr: true, wantDirty: 1},
		{name: "still running result is followed up", text: commitToolText(map[string]any{"name": "commit-1", "phase": "Running"}), wantPending: true, wantDirty: 1},
		{name: "succeeded", text: succeeded, wantNotify: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := newCommitTestEnv(t, func(int) (string, bool) { return tt.text, tt.isError })
			dirty, err := env.commit()
			if (err != nil) != tt.wantErr || dirty != (tt.wantDirty > 0) {
				t.Fatalf("commitWorkspace = dirty %t, err %v; want dirty %t, err %t", dirty, err, tt.wantDirty > 0, tt.wantErr)
			}
			if got := env.pending(); len(got) != tt.wantDirty {
				t.Fatalf("uncommitted paths = %v, want %d", got, tt.wantDirty)
			}
			if name, ok := env.pendingCommit(); ok != tt.wantPending || (ok && name != "commit-1") {
				t.Fatalf("pending commit recorded = %t (%q), want %t", ok, name, tt.wantPending)
			}
			if !tt.wantNotify {
				if len(env.notified) != 0 {
					t.Fatalf("OnCommitted called for an unsettled commit: %+v", env.notified)
				}
				return
			}
			want := CommitResult{RepositoryRef: "demo-repo", CommitSHA: "0123456789abcdef", CommitURL: "https://github.example/commit/0123456", Branch: "main", Files: []string{"app.txt"}}
			if len(env.notified) != 1 || fmt.Sprint(env.notified[0]) != fmt.Sprint(want) {
				t.Fatalf("OnCommitted = %+v, want %+v", env.notified, want)
			}
		})
	}
}

func TestCommitWorkspaceFollowsUpRateLimitedCommitInsteadOfResending(t *testing.T) {
	rateLimited := `RepositoryCommit "commit-1" is queued behind a GitHub rate limit (secondary rate limit); the provider retries it until 2026-09-10T13:00:00Z, then marks it Failed. The files are not committed yet: watch RepositoryCommit "commit-1" for phase Succeeded before relying on them`
	env := newCommitTestEnv(t, func(int) (string, bool) { return rateLimited, true }, repositoryCommitObject("commit-1", "Running", ""))

	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("first commit = dirty %t, err %v; want pending", dirty, err)
	}
	if name, ok := env.pendingCommit(); !ok || name != "commit-1" {
		t.Fatalf("pending commit = %q, %t; want commit-1 recorded and pointed at", name, ok)
	}
	// Every later pass (a watch event, a signal, the resync) re-reads the
	// RepositoryCommit by name and never resends while it runs. Newer edits
	// wait for it.
	env.write("later.txt", "later\n")
	for pass := 0; pass < 2; pass++ {
		if dirty, err := env.commit(); err != nil || !dirty {
			t.Fatalf("running pass %d = dirty %t, err %v", pass, dirty, err)
		}
	}
	if env.calls != 1 {
		t.Fatalf("commit_files calls = %d, want 1 while the RepositoryCommit is pending", env.calls)
	}

	// It lands: the pending paths settle with its SHA, the later edit is
	// committed by a fresh call on the same pass, and both records clear.
	env.setRepositoryCommit("commit-1", "Succeeded", "feedface00")
	env.respond = func(int) (string, bool) {
		return commitToolText(map[string]any{"repositoryRef": "demo-repo", "name": "commit-2", "phase": "Succeeded", "commitSHA": "abcdef1234"}), false
	}
	if dirty, err := env.commit(); err != nil || dirty {
		t.Fatalf("settling pass = dirty %t, err %v; want clean", dirty, err)
	}
	if got := env.pending(); len(got) != 0 {
		t.Fatalf("uncommitted paths = %v, want none", got)
	}
	if _, ok := env.pendingCommit(); ok {
		t.Fatal("pending commit not cleared after it landed")
	}
	if env.calls != 2 || len(env.notified) != 2 || env.notified[0].CommitSHA != "feedface00" ||
		fmt.Sprint(env.notified[0].Files) != "[app.txt]" || env.notified[1].CommitSHA != "abcdef1234" {
		t.Fatalf("calls = %d, notified = %+v; want the pending commit announced, then one fresh commit for later.txt", env.calls, env.notified)
	}
}

func TestCommitWorkspaceResendsAfterPendingCommitFails(t *testing.T) {
	unfinished := `RepositoryCommit "commit-1" did not finish within the 1m15s wait (phase Running); the files may not be committed yet: watch RepositoryCommit "commit-1" for phase Succeeded or Failed`
	env := newCommitTestEnv(t, func(call int) (string, bool) {
		if call == 1 {
			return unfinished, true
		}
		return commitToolText(map[string]any{"repositoryRef": "demo-repo", "name": "commit-2", "phase": "Succeeded", "commitSHA": "abcdef1234"}), false
	}, repositoryCommitObject("commit-1", "Failed", ""))

	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("first commit = dirty %t, err %v; want pending", dirty, err)
	}
	if dirty, err := env.commit(); err != nil || dirty {
		t.Fatalf("after failed pending commit = dirty %t, err %v; want a fresh successful commit", dirty, err)
	}
	if env.calls != 2 || len(env.notified) != 1 || env.notified[0].CommitSHA != "abcdef1234" {
		t.Fatalf("calls = %d, notified = %+v; want one fresh commit after the failure", env.calls, env.notified)
	}
	if got := env.pending(); len(got) != 0 {
		t.Fatalf("uncommitted paths = %v, want none", got)
	}
	if _, ok := env.pendingCommit(); ok {
		t.Fatal("failed pending commit was not cleared")
	}
}

func TestCommitWorkspaceResendsWhenPendingCommitIsGone(t *testing.T) {
	unfinished := `RepositoryCommit "commit-1" did not finish within the 1m15s wait (phase Running); the files may not be committed yet: watch RepositoryCommit "commit-1" for phase Succeeded or Failed`
	env := newCommitTestEnv(t, func(call int) (string, bool) {
		if call == 1 {
			return unfinished, true
		}
		return commitToolText(map[string]any{"repositoryRef": "demo-repo", "name": "commit-2", "phase": "Succeeded", "commitSHA": "abcdef1234"}), false
	}) // no RepositoryCommit object at all: the provider never persisted it

	if dirty, err := env.commit(); err != nil || !dirty {
		t.Fatalf("first commit = dirty %t, err %v; want pending", dirty, err)
	}
	if dirty, err := env.commit(); err != nil || dirty {
		t.Fatalf("after the pending commit vanished = dirty %t, err %v; want a fresh successful commit", dirty, err)
	}
	if env.calls != 2 || len(env.notified) != 1 {
		t.Fatalf("calls = %d, notified = %+v; want one fresh commit", env.calls, env.notified)
	}
}

func TestCommitWorkspaceClearsPointerWithoutLedgerRecord(t *testing.T) {
	env := newCommitTestEnv(t, func(int) (string, bool) {
		return commitToolText(map[string]any{"repositoryRef": "demo-repo", "name": "commit-2", "phase": "Succeeded", "commitSHA": "abcdef1234"}), false
	})
	// The Project points at a commit the (replaced) workspace volume never
	// heard of: the pointer is dropped and the commit is simply resent.
	env.project.Annotations[pendingCommitAnnotation] = "commit-lost"
	if err := env.control.Update(env.ctx, env.project); err != nil {
		t.Fatal(err)
	}
	if dirty, err := env.commit(); err != nil || dirty {
		t.Fatalf("commit = dirty %t, err %v; want a fresh commit", dirty, err)
	}
	if _, ok := env.pendingCommit(); ok {
		t.Fatal("stale pointer survived")
	}
	if env.calls != 1 || len(env.notified) != 1 {
		t.Fatalf("calls = %d, notified = %+v", env.calls, env.notified)
	}
}
