// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
)

func TestCollaborationPinsRepositoryAndHeadBeforeCreatingPR(t *testing.T) {
	for _, kind := range []string{"success", "replaced repository", "changed head", "lost response"} {
		t.Run(kind, func(t *testing.T) {
			posts := 0
			commit := strings.Repeat("a", 40)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var output any
				switch r.URL.Path {
				case "/api/v3/repos/team/demo":
					id := 42
					if kind == "replaced repository" {
						id = 43
					}
					output = map[string]any{"id": id, "full_name": "team/demo"}
				case "/api/v3/repos/team/demo/git/ref/heads/feature":
					head := commit
					if kind == "changed head" {
						head = strings.Repeat("b", 40)
					}
					output = map[string]any{"ref": "refs/heads/feature", "object": map[string]any{"sha": head}}
				case "/api/v3/repos/team/demo/pulls":
					if r.Method != "POST" {
						t.Errorf("unexpected PR method %s", r.Method)
					}
					posts++
					var input map[string]any
					if json.NewDecoder(r.Body).Decode(&input) != nil || input["head"] != "feature" || input["base"] != "main" || input["title"] != "Public summary" {
						t.Error("PR request lost pinned public inputs")
					}
					if kind == "lost response" {
						w.WriteHeader(502)
						return
					}
					output = collaborationPR(commit)
				default:
					t.Errorf("unexpected API route %s", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(output)
			}))
			defer server.Close()
			conn := &api.Connection{Spec: api.ConnectionSpec{Owner: "team", BaseURL: server.URL}}
			repo := &api.Repository{Spec: api.RepositorySpec{Name: "demo"}, Status: api.RepositoryStatus{RepoID: "42"}}
			result, err := New().CreatePullRequest(context.Background(), conn, backend.Credential{Token: "test-token"}, repo, backend.PullRequestInput{Head: "feature", Base: "main", Commit: commit, Title: "Public summary", Body: "Ready for review"})
			switch kind {
			case "success":
				if err != nil || result == nil || result.Commit != commit || posts != 1 {
					t.Fatalf("PR=%#v posts=%d error=%v", result, posts, err)
				}
			case "lost response":
				if err == nil || posts != 1 {
					t.Fatalf("unconfirmed response retried or hidden: posts=%d error=%v", posts, err)
				}
			default:
				if !errors.Is(err, backend.ErrRepositoryIdentityConflict) || posts != 0 {
					t.Fatalf("identity conflict reached mutation: posts=%d error=%v", posts, err)
				}
			}
		})
	}
}
func collaborationPR(commit string) map[string]any {
	return map[string]any{"number": 1, "html_url": "https://github.com/team/demo/pull/1", "state": "closed", "merged": true, "merge_commit_sha": strings.Repeat("c", 40), "merged_at": "2026-09-13T12:00:00Z", "merged_by": map[string]any{"login": "human", "type": "User"}, "head": map[string]any{"ref": "feature", "sha": commit, "repo": map[string]any{"full_name": "team/demo"}}, "base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "team/demo"}}}
}
func TestFeedbackChecksExactCommitAndRechecksPRHead(t *testing.T) {
	for _, kind := range []string{"valid", "wrong check head", "PR changed"} {
		t.Run(kind, func(t *testing.T) {
			commit := strings.Repeat("a", 40)
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("feedback performed mutation")
				}
				var output any
				switch r.URL.Path {
				case "/api/v3/repos/team/demo":
					output = map[string]any{"id": 42, "full_name": "team/demo"}
				case "/api/v3/repos/team/demo/pulls/1":
					reads++
					head := commit
					if kind == "PR changed" && reads > 1 {
						head = strings.Repeat("b", 40)
					}
					output = collaborationPR(head)
					if kind == "PR closed" && reads > 1 {
						output.(map[string]any)["state"] = "closed"
					}
				case "/api/v3/repos/team/demo/commits/" + commit + "/check-runs":
					head := commit
					if kind == "wrong check head" {
						head = strings.Repeat("b", 40)
					}
					output = map[string]any{"total_count": 1, "check_runs": []any{map[string]any{"id": 11, "name": "test", "head_sha": head, "status": "completed", "conclusion": "success", "app": map[string]any{"id": 22}, "output": map[string]any{"summary": "passed", "annotations_count": 1}}}}
				case "/api/v3/repos/team/demo/check-runs/11/annotations":
					output = []any{map[string]any{"path": "main.go", "start_line": 1, "end_line": 1, "annotation_level": "failure", "message": "annotation context"}}
					if kind == "missing annotation" {
						output = []any{}
					}
				case "/api/v3/repos/team/demo/pulls/1/reviews/33/comments":
					comment := map[string]any{"id": 44, "pull_request_review_id": 33, "commit_id": commit, "path": "main.go", "line": 2, "body": "inline context"}
					if kind == "wrong review comment" {
						comment["pull_request_review_id"] = 34
					}
					if kind == "oversized diagnostic" {
						comment["body"] = strings.Repeat("x", 16385)
					}
					output = []any{comment}
				case "/api/v3/repos/team/demo/pulls/1/reviews":
					output = []any{map[string]any{"id": 33, "user": map[string]any{"login": "reviewer", "type": "User"}, "commit_id": commit, "state": "APPROVED", "body": "Reviewed", "submitted_at": "2026-09-13T11:00:00Z"}}
				default:
					t.Errorf("unexpected feedback route %s", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(output)
			}))
			defer server.Close()
			conn := &api.Connection{Spec: api.ConnectionSpec{Owner: "team", BaseURL: server.URL}}
			repo := &api.Repository{Spec: api.RepositorySpec{Name: "demo"}, Status: api.RepositoryStatus{RepoID: "42"}}
			result, err := New().PullRequestFeedback(context.Background(), conn, backend.Credential{Token: "test-token"}, repo, 1, commit)
			if kind == "valid" {
				if err != nil || len(result.Checks) != 1 || result.Checks[0].AppID != 22 || len(result.Reviews) != 1 || result.Reviews[0].Commit != commit || reads != 2 || !strings.Contains(result.Checks[0].Output, "annotation context") || !strings.Contains(result.Reviews[0].Body, "inline context") {
					t.Fatalf("feedback=%#v reads=%d error=%v", result, reads, err)
				}
			} else if err == nil {
				t.Fatal("incomplete or changed feedback accepted")
			}
		})
	}
}
func TestExistingOnlyRegistrationNeverCreatesOrDeletesRemote(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" {
			t.Error("existing-only registration mutated remote")
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	conn := &api.Connection{Spec: api.ConnectionSpec{Owner: "team", BaseURL: server.URL}}
	repo := &api.Repository{Spec: api.RepositorySpec{Name: "demo"}}
	repo.Annotations = map[string]string{"code.faros.sh/existing-only": "true"}
	implementation := New()
	cred := backend.Credential{Token: "test-token"}
	if _, err := implementation.EnsureRepository(context.Background(), conn, cred, repo); err == nil {
		t.Fatal("missing existing-only repository accepted")
	}
	if err := implementation.DeleteRepository(context.Background(), conn, cred, repo); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("existing-only registration made %d remote calls", calls)
	}
}
