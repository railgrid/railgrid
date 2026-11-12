// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-sdk/dataplane"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInlineDiffLocations(t *testing.T) {
	patch := "@@ -10,3 +10,3 @@ function\n context\n-old\n+new\n tail\n@@ -30 +30,2 @@\n-old2\n+new2\n+added\n\\ No newline at end of file"
	locations, err := patchLocations(patch)
	if err != nil {
		t.Fatal(err)
	}
	for _, location := range []diffLocation{{"RIGHT", 10}, {"LEFT", 11}, {"RIGHT", 11}, {"RIGHT", 31}} {
		if !locations[location] {
			t.Fatalf("missing %+v", location)
		}
	}
	if locations[diffLocation{"LEFT", 10}] || locations[diffLocation{"LEFT", 31}] || locations[diffLocation{"RIGHT", 20}] {
		t.Fatal("accepted outside diff")
	}
	for _, patch := range []string{"", "@@ -999999999999999999999 +1 @@\n-a\n+b", "@@ -1,2 +1,2 @@\n a", "@@ invalid\n+a", "@@ -1 +1 @@\n a\n+extra"} {
		if _, err := patchLocations(patch); err == nil {
			t.Fatalf("accepted malformed/truncated patch %q", patch)
		}
	}
}

func TestInlineReviewValidation(t *testing.T) {
	valid := ReviewComment{Path: "src/a.go", Line: 11, Side: "RIGHT", Body: "Return upper."}
	if err := validateReviewComments([]ReviewComment{valid}, 1); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ReviewComment){func(c *ReviewComment) { c.Path = "../secret" }, func(c *ReviewComment) { c.Path = "/home/secret" }, func(c *ReviewComment) { c.Line = 0 }, func(c *ReviewComment) { c.Side = "OTHER" }, func(c *ReviewComment) { c.Body = " " }, func(c *ReviewComment) { c.Body = strings.Repeat("x", 8193) }} {
		c := valid
		mutate(&c)
		if validateReviewComments([]ReviewComment{c}, 1) == nil {
			t.Fatal("invalid comment accepted")
		}
	}
	if validateReviewComments([]ReviewComment{valid}, 0) == nil || validateReviewComments([]ReviewComment{valid}, 2) == nil || validateReviewComments(make([]ReviewComment, 51), 51) == nil {
		t.Fatal("finding count/bound not enforced")
	}
}

func TestInlineReviewPublicationRecoveryAndHeadGuard(t *testing.T) {
	for _, scenario := range []string{"recover", "changed-head", "outside-diff", "missing-file", "missing-patch", "api-failure"} {
		t.Run(scenario, func(t *testing.T) {
			head := strings.Repeat("a", 40)
			posts := 0
			var saved []Comment
			client := Client{BotLogin: "railgrid-app[bot]", HTTP: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				var out any
				switch {
				case strings.HasSuffix(r.URL.Path, "/files"):
					if scenario == "api-failure" {
						return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("{}"))}, nil
					}
					patch := "@@ -1 +1 @@\n-old\n+new"
					if scenario == "missing-patch" {
						patch = ""
					}
					out = []map[string]any{{"filename": "a.go", "patch": patch}}
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/reviews"):
					out = saved
				case r.Method == http.MethodGet:
					h := head
					if scenario == "changed-head" {
						h = strings.Repeat("b", 40)
					}
					out = map[string]any{"state": "open", "head": map[string]any{"sha": h}}
				case r.Method == http.MethodPost:
					posts++
					var input struct {
						Event    string          `json:"event"`
						Commit   string          `json:"commit_id"`
						Body     string          `json:"body"`
						Comments []ReviewComment `json:"comments"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					if input.Event != "COMMENT" || input.Commit != head || len(input.Comments) != 1 || input.Comments[0].Line != 1 || input.Comments[0].Side != "RIGHT" {
						t.Fatalf("wrong publication: %+v", input)
					}
					saved = append(saved, Comment{ID: 12, Body: input.Body, User: User{Login: "railgrid-app[bot]", Type: "Bot"}})
					return nil, errors.New("lost response after accepted review")
				default:
					t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
				}
				raw, _ := json.Marshal(out)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}}
			event := &api.GitHubEvent{Spec: api.GitHubEventSpec{Repository: "railgrid/example", PullRequest: 7}}
			comments := []ReviewComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "Fix this."}}
			if scenario == "outside-diff" {
				comments[0].Line = 50
			}
			if scenario == "missing-file" {
				comments[0].Path = "other.go"
			}
			if _, err := publishReview(t.Context(), client, event, head, "One finding", "<!-- exact-operation -->", comments); err == nil {
				t.Fatal("expected refusal or lost response")
			}
			if scenario != "recover" {
				if posts != 0 {
					t.Fatal("invalid review posted")
				}
				return
			}
			out, err := publishReview(t.Context(), client, event, head, "One finding", "<!-- exact-operation -->", comments)
			if err != nil || out.(map[string]any)["reused"] != true || posts != 1 {
				t.Fatalf("recovery duplicated review: %v %v %d", out, err, posts)
			}
			comments[0].Body = "Changed content"
			if _, err := publishReview(t.Context(), client, event, head, "One finding", "<!-- exact-operation -->", comments); err == nil || posts != 1 {
				t.Fatal("changed payload reused key")
			}
		})
	}
}

func TestInlineReviewFilesPagination(t *testing.T) {
	pages := 0
	c := Client{HTTP: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		pages++
		files := []map[string]any{}
		if pages == 1 {
			for range 100 {
				files = append(files, map[string]any{"filename": "unrelated"})
			}
		} else {
			files = append(files, map[string]any{"filename": "renamed.go", "patch": "@@ -1 +1 @@\n-old\n+new"})
		}
		raw, _ := json.Marshal(files)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}}
	e := &api.GitHubEvent{Spec: api.GitHubEventSpec{Repository: "railgrid/example", PullRequest: 7}}
	if err := validateDiffLocations(t.Context(), c, e, []ReviewComment{{Path: "renamed.go", Line: 1, Side: "LEFT", Body: "Deleted line finding"}}); err != nil || pages != 2 {
		t.Fatalf("pagination: %v %d", err, pages)
	}
}

// Exercise both public command and coordinator Task grants, rather than only
// testing the publisher helper. Existing identity checks remain on these paths.
func captureInlineRequests(t *testing.T, f *fixture) *int {
	t.Helper()
	posts := 0
	original := f.s.HTTP.Transport
	f.s.HTTP.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		var value any
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			value = []map[string]any{{"filename": "a.go", "patch": "@@ -1 +1 @@\n-old\n+new"}}
		case strings.HasSuffix(r.URL.Path, "/reviews") && r.Method == http.MethodGet:
			value = []Comment{}
		case strings.HasSuffix(r.URL.Path, "/reviews") && r.Method == http.MethodPost:
			posts++
			var in map[string]any
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if in["event"] != "COMMENT" || len(in["comments"].([]any)) != 1 {
				t.Fatal("formal inline review not forwarded")
			}
			value = Comment{ID: 15}
		default:
			return original.RoundTrip(r)
		}
		raw, _ := json.Marshal(value)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	return &posts
}

func TestEventScopePublishesInlineReview(t *testing.T) {
	f, pr := publicationFixture(t)
	posts := captureInlineRequests(t, f)
	u, _ := asU(pr)
	count := 1
	in := Operation{Op: "review", PullRequestUID: string(pr.UID), EventUID: pr.Spec.Command.EventUID, InvocationKey: "factory:run:1", InvocationAgent: "coordinator", InvocationAgentUID: "agent-uid", ExpectedHead: strings.Repeat("a", 40), Key: "inline", Body: "One finding", FindingsCount: &count, Comments: []ReviewComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "Return upper."}}}
	in.ScopeProof = publicationProof("cluster", pr, in.InvocationKey, in.InvocationAgent, in.InvocationAgentUID, testSigningKey)
	raw, _ := json.Marshal(in)
	if _, err := f.s.Operate(t.Context(), f.dyn, "cluster", u, raw, dataplane.ProxiedIdentity{}); err != nil {
		t.Fatal(err)
	}
	if *posts != 1 {
		t.Fatal("inline review not posted")
	}
	count = 0
	raw, _ = json.Marshal(in)
	if _, err := f.s.Operate(t.Context(), f.dyn, "cluster", u, raw, dataplane.ProxiedIdentity{}); err == nil {
		t.Fatal("zero findings accepted inline comments")
	}
}

func TestTaskScopePublishesInlineReview(t *testing.T) {
	f, repo, input, issuer := taskTestFixture(t)
	allowTaskCallerSAR(f, true, nil)
	result, err := authorizeTaskTest(t, f, repo, input, issuer)
	if err != nil {
		t.Fatal(err)
	}
	grant := result.(TaskAuthorizationResult)
	scope := input
	scope.Op = "scope"
	scope.GrantRef = grant.GrantRef
	scope.Proof = grant.Proof
	scope.InvocationKey = "task:run:1"
	scope.InvocationAgent = "coordinator"
	scope.InvocationAgentUID = "agent-uid"
	result, err = authorizeTaskTest(t, f, repo, scope, issuer)
	if err != nil {
		t.Fatal(err)
	}
	u, err := f.dyn.Resource(PullRequests).Get(t.Context(), "task-pr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := decode[api.PullRequest](u)
	if err != nil {
		t.Fatal(err)
	}
	pr.Spec.Number = 7
	updated, err := asU(pr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.dyn.Resource(PullRequests).Update(t.Context(), updated, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	f.pullRequest.Head.Ref = "task-branch"
	count := 1
	posts := captureInlineRequests(t, f)
	in := Operation{Op: "review", PullRequestUID: string(pr.UID), SourceUID: input.SourceUID, SourceDigest: input.SourceDigest, GrantRef: grant.GrantRef, SourceProof: grant.Proof, InvocationKey: scope.InvocationKey, InvocationAgent: scope.InvocationAgent, InvocationAgentUID: scope.InvocationAgentUID, ScopeProof: result.(TaskAuthorizationResult).ScopeProof, ExpectedHead: f.pullRequest.Head.SHA, Key: "inline", Body: "One finding", FindingsCount: &count, Comments: []ReviewComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "Return upper."}}}
	raw, _ := json.Marshal(in)
	if _, err := f.s.Operate(t.Context(), f.dyn, "tenant", u, raw, issuer); err != nil {
		t.Fatal(err)
	}
	if *posts != 1 {
		t.Fatal("task review not posted")
	}
}

func TestInlineBatchIsOneReviewAndInvalidFindingPreventsAllPublication(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(strconv.FormatBool(invalid), func(t *testing.T) {
			posts := 0
			head := strings.Repeat("a", 40)
			c := Client{HTTP: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				var value any
				switch {
				case strings.HasSuffix(r.URL.Path, "/files"):
					value = []map[string]any{{"filename": "a.go", "patch": "@@ -1 +1,2 @@\n-old\n+first\n+second"}}
				case strings.HasSuffix(r.URL.Path, "/reviews") && r.Method == http.MethodGet:
					value = []Comment{}
				case r.Method == http.MethodGet:
					value = map[string]any{"state": "open", "head": map[string]any{"sha": head}}
				case r.Method == http.MethodPost:
					posts++
					var in struct {
						Comments []ReviewComment `json:"comments"`
					}
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
						t.Fatal(err)
					}
					if len(in.Comments) != 2 {
						t.Fatal("batch split or truncated")
					}
					value = Comment{ID: 10}
				default:
					t.Fatalf("unexpected request %s", r.URL)
				}
				raw, _ := json.Marshal(value)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}}
			findings := []ReviewComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "First finding"}, {Path: "a.go", Line: 2, Side: "RIGHT", Body: "Second finding"}}
			if invalid {
				findings[1].Line = 100
			}
			e := &api.GitHubEvent{Spec: api.GitHubEventSpec{Repository: "railgrid/example", PullRequest: 7}}
			_, err := publishReview(t.Context(), c, e, head, "Findings", "<!-- batch -->", findings)
			if invalid {
				if err == nil || posts != 0 {
					t.Fatal("partially posted invalid batch")
				}
			} else if err != nil || posts != 1 {
				t.Fatalf("batch publication: %v posts=%d", err, posts)
			}
		})
	}
}
