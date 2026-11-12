// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/internal/connsecret"
	"github.com/railgrid/provider-agents/internal/githubapp"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tools"
)

func TestDelegationWaitDoesNotReturnPendingApprovalAsAResult(t *testing.T) {
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "worker"}
	now := time.Now()
	child := store.Run{ID: "child", AgentName: "worker", Phase: store.RunPhasePendingApproval, CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(t.Context(), scope, child); err != nil {
		t.Fatal(err)
	}
	done := make(chan store.Run, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	go func() {
		r, err := s.waitDelegation(ctx, scope, "child")
		if err == nil {
			done <- r
		}
	}()
	select {
	case <-done:
		t.Fatal("parked child was returned as completed")
	case <-time.After(30 * time.Millisecond):
	}
	child.Phase = store.RunPhaseSucceeded
	child.Output = "verified snapshot"
	if err := st.SaveRun(t.Context(), scope, child); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.Output != "verified snapshot" {
			t.Fatal("lost resumed result")
		}
	case <-ctx.Done():
		t.Fatal("did not join resumed child")
	}
}
func TestDelegationReplayReusesItsExistingChildBeforeBudgetGate(t *testing.T) {
	st := store.NewMemoryStore()
	s := &Server{store: st}
	parent := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "coordinator"}
	scope := parent
	scope.AgentName = "reviewer"
	now := time.Now()
	if err := st.SaveRun(t.Context(), parent, store.Run{ID: "parent", AgentName: parent.AgentName, Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	child := store.Run{ID: "child", AgentName: "reviewer", ParentRunID: "parent", Trigger: "delegation", IdempotencyKey: "delegate:parent:call", Phase: store.RunPhaseSucceeded, Output: "independent verdict", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRun(t.Context(), scope, child); err != nil {
		t.Fatal(err)
	}
	a := &agentsv1alpha1.Agent{}
	a.Name = "coordinator"
	got, err := s.delegateAndWait(backend.WithToolCallID(t.Context(), "call"), tools.Deps{Scope: parent, RunID: "parent", Agent: a}, "reviewer", "replayed tool input")
	if err != nil || got.ID != "child" || got.Output != "independent verdict" {
		t.Fatalf("replay: %+v %v", got, err)
	}
	rows, _ := st.ListRuns(t.Context(), scope, 10)
	if len(rows) != 1 {
		t.Fatal("replay created another worker")
	}
}

func TestNativeDelegationReturnsOnlyVerifiedStoredSnapshot(t *testing.T) {
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "worker"}
	data := []byte("verified runner bundle")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	artifact := store.RunArtifact{RunID: "child", Name: "git-result.bundle", Data: data, Digest: digest, Size: int64(len(data)), CreatedAt: time.Now()}
	if err := st.SaveRunArtifact(t.Context(), scope, artifact); err != nil {
		t.Fatal(err)
	}
	child := store.Run{ID: "child", Output: "tests passed", Repository: &store.RunRepository{BaseCommit: "base", CommitMessage: "Apply scoped PR fixes"}, Result: &store.RunResult{BaseCommit: "base", Commit: "commit", Tree: "tree", BundleDigest: digest}}
	raw, err := s.delegationOutput(t.Context(), scope, child)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		RunID      string `json:"runId"`
		Report     string `json:"report"`
		BaseCommit string `json:"baseCommit"`
		NoChanges  bool   `json:"noChanges"`
		Snapshot   struct {
			Bundle     []byte `json:"bundle"`
			BaseCommit string `json:"baseCommit"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || string(out.Snapshot.Bundle) != string(data) || out.Report != "tests passed" || out.RunID != "child" || out.BaseCommit != "base" || out.NoChanges {
		t.Fatalf("snapshot output: %s %v", raw, err)
	}
	child.Result.BundleDigest = "changed"
	if _, err := s.delegationOutput(t.Context(), scope, child); err == nil {
		t.Fatal("changed snapshot accepted")
	}
}

func TestNativeDelegationNoChangesStillReturnsChildEvidence(t *testing.T) {
	s := &Server{store: store.NewMemoryStore()}
	child := store.Run{
		ID: "review-run", Output: "No actionable findings.",
		Repository: &store.RunRepository{BaseCommit: "base"},
		Result:     &store.RunResult{BaseCommit: "base", NoChanges: true},
	}
	raw, err := s.delegationOutput(t.Context(), store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "reviewer"}, child)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got["runId"] != "review-run" || got["baseCommit"] != "base" || got["noChanges"] != true || got["report"] != "No actionable findings." {
		t.Fatalf("no-change delegated result = %s", raw)
	}
}

func TestDelegatedRepositoryChecksAdmittedCheckout(t *testing.T) {
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name, repository, repositoryID, taskRepoUID, wantError string
	}{
		{name: "matching connection", repository: "org/repo", repositoryID: "42", taskRepoUID: "repo-uid"},
		{name: "renamed repository", repository: "org/renamed", repositoryID: "42", taskRepoUID: "repo-uid", wantError: "does not match admitted repository"},
		{name: "swapped repository ID", repository: "org/repo", repositoryID: "43", taskRepoUID: "repo-uid", wantError: "does not match admitted repository"},
		{name: "missing task repository UID", repository: "org/repo", repositoryID: "42", wantError: "does not match admitted repository"},
		{name: "task repository UID changed in Code", repository: "org/repo", repositoryID: "42", taskRepoUID: "other-uid", wantError: "task repository UID mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Input map[string]any
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if request.Input["op"] != "observe" || request.Input["pullRequestUID"] != "pr-uid" {
					t.Error("observe request lost its PR binding")
				}
				// Code validates the live UIDs against the task's signed proof.
				if request.Input["scopeProof"] == "other-uid" {
					http.Error(w, "task repository UID mismatch", http.StatusForbidden)
					return
				}
				_, _ = fmt.Fprintf(w, `{"result":{"repository":"org/repo","repositoryID":42,"observation":{"head":%q}}}`, head)
			}))
			defer code.Close()
			conn := githubConnection("checkout", "")
			conn.Spec.Auth = "secret"
			conn.Spec.Config = map[string]string{"credentialType": "github-app", "githubRole": "checkout", "repository": tc.repository, "repositoryID": tc.repositoryID}
			cr := connectionsCR{connections: map[string]*agentsv1alpha1.Connection{"checkout": conn}}
			child := &agentsv1alpha1.Agent{}
			child.Spec.Backend = agentsv1alpha1.AgentBackendSpec{Type: agentsv1alpha1.AgentBackendHarness, Harness: &agentsv1alpha1.AgentHarnessBackend{GitHubConnectionRef: conn.Name}}
			server := &Server{githubAppTokenMint: func(_ context.Context, _ githubapp.Client, _ map[string][]byte) (string, time.Time, error) {
				return "minted-checkout-token", time.Time{}, nil
			}}
			coordinator := &agentsv1alpha1.Agent{}
			coordinator.Name, coordinator.UID = "coordinator", "coordinator-uid"
			repo, err := server.delegatedRepository(t.Context(), tools.Deps{
				Agent: coordinator, CR: cr,
				CodeScope: &tools.CodeScope{Mode: "task", PullRequest: "pr", PullRequestUID: "pr-uid", Repository: "org/repo",
					RepositoryUID: tc.taskRepoUID, ConnectionUID: "code-connection-uid", ScopeProof: tc.taskRepoUID},
				DataPlane:    tools.DataPlane{ClusterID: "tenant"},
				CodeOperator: httpCodeOperator{endpoint: code.URL},
			}, child)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || repo != nil {
					t.Fatalf("delegatedRepository = %+v, %v; want %q", repo, err, tc.wantError)
				}
				return
			}
			if err != nil || repo == nil || repo.RepositoryID != "42" || repo.BaseCommit != head || repo.Source.RemoteURL != "https://github.com/org/repo.git" {
				t.Fatalf("delegated repository did not pin Code's observation: %+v, %v", repo, err)
			}
			secretName := connsecret.Name(conn.Name)
			run := taskRun{Repository: repo, CR: cr, Creds: fakeCreds{secrets: map[string]*corev1.Secret{secretName: tokenSecret(secretName, "unused-personal-token")}}}
			env, err := server.harnessEnvironmentForRepository(t.Context(), run, child.Spec.Harness(), repo.RepositoryID)
			if err != nil || run.Repository.Source.Token != "minted-checkout-token" ||
				len(env) != 2 || env[0].Value != "minted-checkout-token" || env[1].Value != "minted-checkout-token" {
				t.Fatal("the delegated clone source and harness environment must receive the minted App token")
			}
		})
	}
}
