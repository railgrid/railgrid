// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/tenant"
	"github.com/railgrid/provider-sdk/dataplane"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type testCallers struct{ dyn dynamic.Interface }

func (f testCallers) For(string, string) (dynamic.Interface, error) {
	panic("tests must never borrow a caller or local kubeconfig")
}
func (f testCallers) AsProvider(string) (dynamic.Interface, error) { return f.dyn, nil }

var testSigningKey = []byte("test-signature-key-with-at-least-32-bytes")

type fixture struct {
	s           *Server
	dyn         *dynamicfake.FakeDynamicClient
	event       Event
	pullRequest PullRequest
	role        string
	calls       int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{role: "admin"}
	conn := &api.Connection{TypeMeta: metav1.TypeMeta{APIVersion: api.SchemeGroupVersion.String(), Kind: "Connection"}, ObjectMeta: metav1.ObjectMeta{Name: "app", UID: "conn"}, Spec: api.ConnectionSpec{Provider: "github", Type: api.CredentialTypeGitHubApp, Owner: "railgrid", SecretRef: api.LocalSecretReference{Name: "app-key"}, GitHubWebhook: &api.GitHubWebhookBinding{RepositoryRef: "repo", RepositoryID: 99, Source: "app", BotLogin: "sample[bot]"}}, Status: api.ConnectionStatus{Login: "sample[bot]"}}
	repo := &api.Repository{TypeMeta: metav1.TypeMeta{APIVersion: api.SchemeGroupVersion.String(), Kind: "Repository"}, ObjectMeta: metav1.ObjectMeta{Name: "repo", UID: "repo-uid"}, Spec: api.RepositorySpec{Name: "example", ConnectionRef: "app"}, Status: api.RepositoryStatus{RepoID: "99"}}
	secret := &corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Name: "app-key", Namespace: "default", Labels: map[string]string{"railgrid.ai/owner": "code"}}, Data: map[string][]byte{"appID": []byte("123"), "installationID": []byte("456"), "webhookSecret": testSigningKey, "privateKey": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})}}
	var objs []runtime.Object
	for _, obj := range []any{conn, repo, secret} {
		u, err := asU(obj)
		if err != nil {
			t.Fatal(err)
		}
		objs = append(objs, u)
	}
	f.dyn = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{Events: "GitHubEventList"}, objs...)
	client := &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		f.calls++
		var value any
		status := 200
		switch {
		case r.URL.Path == "/app/installations/456/access_tokens":
			value = map[string]any{"token": "app-token", "expires_at": time.Now().Add(time.Hour)}
			status = 201
		case strings.Contains(r.URL.Path, "/collaborators/"):
			value = map[string]any{"permission": f.role}
		case strings.Contains(r.URL.Path, "/issues/comments/"):
			value = f.event.Comment
		case r.URL.Path == "/repos/railgrid/example/pulls/7":
			value = f.pullRequest
		default:
			t.Fatalf("unexpected GitHub request %s", r.URL)
		}
		data, _ := json.Marshal(value)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
	})}
	f.s = &Server{Callers: testCallers{f.dyn}, Credentials: tenant.CredentialResolver{Client: client}, HTTP: client}
	f.event.Action = "created"
	f.event.Installation.ID = 456
	f.event.Repository = Repo{ID: 99, FullName: "railgrid/example"}
	f.event.Sender = User{ID: 1, Login: "maintainer", Type: "User"}
	f.event.Comment = Comment{ID: 11, Body: "/railgrid review", User: f.event.Sender}
	f.event.Issue.Number = 7
	pr := json.RawMessage(`{}`)
	f.event.Issue.PullRequest = &pr
	f.pullRequest.State = "open"
	f.pullRequest.Number = 7
	f.pullRequest.Title = "Example"
	f.pullRequest.Body = "Fixture"
	f.pullRequest.Head.SHA = strings.Repeat("a", 40)
	f.pullRequest.Head.Ref = "feature"
	f.pullRequest.Head.Repo = Repo{ID: 99, FullName: "railgrid/example"}
	f.pullRequest.Base.SHA = strings.Repeat("b", 40)
	f.pullRequest.Base.Ref = "main"
	f.pullRequest.Base.Repo = Repo{ID: 99, FullName: "railgrid/example"}
	return f
}
func (f *fixture) deliver(t *testing.T, bad bool) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(f.event)
	r := httptest.NewRequest("POST", "/webhooks/github/cluster/app", strings.NewReader(string(raw)))
	r.SetPathValue("cluster", "cluster")
	r.SetPathValue("name", "app")
	r.Header.Set("X-GitHub-Event", "issue_comment")
	sig := "sha256=" + Signature(testSigningKey, raw)
	if bad {
		sig = "sha256=" + strings.Repeat("0", 64)
	}
	r.Header.Set("X-Hub-Signature-256", sig)
	w := httptest.NewRecorder()
	f.s.Webhook(w, r)
	return w
}
func TestReceiptSignatureIdentityAndDeduplication(t *testing.T) {
	f := newFixture(t)
	if w := f.deliver(t, true); w.Code != 404 || f.calls != 0 {
		t.Fatalf("unsigned work reached upstream: %d %d", w.Code, f.calls)
	}
	if w := f.deliver(t, false); w.Code != 202 {
		t.Fatalf("valid delivery: %d %s", w.Code, w.Body.String())
	}
	callsAfterFirstReceipt := f.calls
	originalLogin := f.event.Comment.User.Login
	f.event.Comment.User.Login = "renamed-maintainer"
	if w := f.deliver(t, false); w.Code != 200 {
		t.Fatalf("replay: %d %s", w.Code, w.Body.String())
	}
	f.event.Comment.User.Login = originalLogin
	if f.calls != callsAfterFirstReceipt {
		t.Fatalf("duplicate delivery made GitHub requests: before=%d after=%d", callsAfterFirstReceipt, f.calls)
	}
	list, err := f.dyn.Resource(Events).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("receipts=%v err=%v", list, err)
	}
	for _, change := range []func(*Event){func(e *Event) { e.Installation.ID = 999 }, func(e *Event) { e.Repository.ID = 100 }, func(e *Event) { e.Sender.ID = 2 }} {
		old := f.event
		change(&f.event)
		if w := f.deliver(t, false); w.Code != 403 {
			t.Fatalf("wrong event identity accepted: %d", w.Code)
		}
		f.event = old
	}
	f.event.Action = "edited"
	if w := f.deliver(t, false); w.Code != 200 {
		t.Fatal(w.Code)
	}
	f.event.Action = "created"
	f.event.Comment.User.Type = "Bot"
	if w := f.deliver(t, false); w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestWebhookIgnoresUnaddressedCommentsBeforeAppAuthentication(t *testing.T) {
	for _, body := range []string{"Looks good to me", "We should run /railgrid review", "> /railgrid review"} {
		t.Run(body, func(t *testing.T) {
			f := newFixture(t)
			f.event.Comment.Body = body
			w := f.deliver(t, false)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "unaddressed comment ignored") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if f.calls != 0 {
				t.Fatalf("unaddressed comment made %d GitHub requests", f.calls)
			}
			list, err := f.dyn.Resource(Events).List(t.Context(), metav1.ListOptions{})
			if err != nil || len(list.Items) != 0 {
				t.Fatalf("receipts=%v err=%v", list, err)
			}
		})
	}
}

func TestWebhookForwardsAddressedCommandsToFactory(t *testing.T) {
	f := newFixture(t)
	f.event.Comment.Body = "/railgrid future-command"
	w := f.deliver(t, false)
	if w.Code != 202 {
		t.Fatalf("addressed command was not recorded for Factory: %d %s", w.Code, w.Body.String())
	}
	list, err := f.dyn.Resource(Events).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("receipts=%v err=%v", list, err)
	}
}

func TestContextRechecksPermissionsAndRejectsForgery(t *testing.T) {
	f := newFixture(t)
	f.deliver(t, false)
	for _, role := range []string{"admin", "maintain", "write", "read"} {
		f.role = role
		_, _, allowed, err := f.s.Context(context.Background(), f.dyn, "cluster", "github-99-11")
		if err != nil || allowed != (role == "admin" || role == "maintain") {
			t.Fatalf("role=%s allowed=%v err=%v", role, allowed, err)
		}
	}
	if _, _, _, err := f.s.Context(context.Background(), f.dyn, "another-cluster", "github-99-11"); err == nil {
		t.Fatal("receipt accepted in another workspace")
	}
	u, err := f.dyn.Resource(Events).Get(context.Background(), "github-99-11", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedField(u.Object, "forged scope", "spec", "comment"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.dyn.Resource(Events).Update(context.Background(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	callsBeforeForgery := f.calls
	if _, _, _, err = f.s.Context(context.Background(), f.dyn, "cluster", "github-99-11"); err == nil {
		t.Fatal("forged command admitted")
	}
	if f.calls != callsBeforeForgery {
		t.Fatalf("forged receipt minted an App token or reached GitHub: calls before=%d after=%d", callsBeforeForgery, f.calls)
	}
}
func TestPublicationUsesFrozenScopeAndExpectedParent(t *testing.T) {
	e := &api.GitHubEvent{Spec: api.GitHubEventSpec{Comment: "/railgrid review-fix", RepositoryID: 99, Branch: "feature", Base: "main"}}
	pr := PullRequest{State: "open"}
	pr.Head.Repo.ID = 99
	pr.Base.Repo.ID = 99
	pr.Head.Ref = "feature"
	pr.Base.Ref = "main"
	pr.Head.SHA = strings.Repeat("a", 40)
	input := Operation{EventUID: "event-uid", ExpectedHead: pr.Head.SHA, Snapshot: &backend.Snapshot{BaseCommit: pr.Head.SHA, Commit: strings.Repeat("b", 40)}}
	for _, scenario := range []string{"allowed", "review-only", "main", "changed-head", "wrong-parent", "base-branch"} {
		t.Run(scenario, func(t *testing.T) {
			ec := *e
			pc := pr
			in := input
			snapshot := *input.Snapshot
			in.Snapshot = &snapshot
			switch scenario {
			case "review-only":
				ec.Spec.Comment = "/railgrid review"
			case "main":
				pc.Head.Ref = "main"
				ec.Spec.Branch = "main"
			case "changed-head":
				pc.Head.SHA = strings.Repeat("c", 40)
			case "wrong-parent":
				in.Snapshot.BaseCommit = strings.Repeat("d", 40)
			case "base-branch":
				pc.Head.Ref = pc.Base.Ref
				ec.Spec.Branch = pc.Base.Ref
			}
			err := validPublication(&api.PullRequest{}, &ec, pc, in)
			if (err == nil) != (scenario == "allowed") {
				t.Fatal(fmt.Sprint(err))
			}
		})
	}
}

func TestCIObservesAllPagesAndExactHead(t *testing.T) {
	for _, scenario := range []string{"second-page-failure", "pending", "none", "wrong-head", "passed"} {
		t.Run(scenario, func(t *testing.T) {
			head := strings.Repeat("a", 40)
			pages := 0
			api := Client{HTTP: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				var out any
				if strings.HasSuffix(r.URL.Path, "/actions/runs") {
					out = map[string]any{"total_count": 0, "workflow_runs": []any{}}
				} else if strings.HasSuffix(r.URL.Path, "/status") {
					out = map[string]any{"sha": head, "total_count": 0, "state": "pending"}
				} else {
					pages++
					checks := []any{}
					total := 0
					if scenario != "none" {
						count := 100
						total = 101
						if pages == 2 {
							count = 1
						}
						for i := 0; i < count; i++ {
							conclusion, status, sha := "success", "completed", head
							if pages == 2 && scenario == "second-page-failure" {
								conclusion = "failure"
							}
							if scenario == "pending" {
								status = "in_progress"
							}
							if scenario == "wrong-head" {
								sha = strings.Repeat("b", 40)
							}
							checks = append(checks, map[string]any{"head_sha": sha, "status": status, "conclusion": conclusion})
						}
					}
					out = map[string]any{"total_count": total, "check_runs": checks}
				}
				data, _ := json.Marshal(out)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
			})}}
			_, state, err := observeCI(context.Background(), api, "railgrid/example", head)
			want := map[string]string{"second-page-failure": "failed", "pending": "pending", "none": "unknown", "passed": "passed"}[scenario]
			if scenario == "wrong-head" {
				if err == nil {
					t.Fatal("wrong head accepted")
				}
				return
			}
			if err != nil || state != want {
				t.Fatalf("state=%s err=%v", state, err)
			}
			if scenario != "none" && pages != 2 {
				t.Fatal("checks were not paginated")
			}
		})
	}
}

func TestUnconfirmedReviewPublicationRecoversByAppIdentity(t *testing.T) {
	var comments []Comment
	posts := 0
	client := Client{BotLogin: "railgrid-app[bot]", HTTP: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			posts++
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input["event"] != "COMMENT" || input["commit_id"] != strings.Repeat("a", 40) {
				t.Fatal("review gained approval or lost its expected commit")
			}
			comments = append(comments, Comment{ID: 12, Body: input["body"].(string), User: User{Type: "Bot", Login: "railgrid-app[bot]"}})
			return nil, fmt.Errorf("response lost after GitHub accepted review")
		}
		raw, _ := json.Marshal(comments)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	})}}
	event := &api.GitHubEvent{Spec: api.GitHubEventSpec{Repository: "railgrid/example", PullRequest: 7}}
	if _, err := publishText(context.Background(), client, event, strings.Repeat("a", 40), "Review findings", "<!-- exact-operation -->"); err == nil {
		t.Fatal("first response was not lost")
	}
	if _, err := publishText(context.Background(), client, event, strings.Repeat("a", 40), "Review findings", "<!-- exact-operation -->"); err != nil {
		t.Fatal(err)
	}
	if posts != 1 {
		t.Fatalf("recovery published %d reviews", posts)
	}
}

func TestObservationReportsExactBaseAncestry(t *testing.T) {
	for _, scenario := range []struct {
		state  string
		behind int
		want   bool
	}{{"ahead", 0, true}, {"identical", 0, true}, {"diverged", 2, false}, {"behind", 1, false}} {
		t.Run(scenario.state, func(t *testing.T) {
			head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
			client := Client{HTTP: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				var out any
				switch {
				case strings.Contains(r.URL.Path, "/compare/"):
					if !strings.HasSuffix(r.URL.Path, base+"..."+head) {
						t.Fatal("base/head not pinned")
					}
					out = map[string]any{"status": scenario.state, "behind_by": scenario.behind}
				case strings.HasSuffix(r.URL.Path, "/check-runs"):
					out = map[string]any{"total_count": 1, "check_runs": []any{map[string]any{"head_sha": head, "status": "completed", "conclusion": "success"}}}
				case strings.HasSuffix(r.URL.Path, "/actions/runs"):
					out = map[string]any{"total_count": 0, "workflow_runs": []any{}}
				case strings.HasSuffix(r.URL.Path, "/status"):
					out = map[string]any{"sha": head, "total_count": 0, "state": "pending"}
				default:
					t.Fatal(r.URL)
				}
				raw, _ := json.Marshal(out)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
			})}}
			pr := PullRequest{State: "open"}
			pr.Head.SHA = head
			pr.Base.SHA = base
			result, err := observe(context.Background(), client, &api.GitHubEvent{Spec: api.GitHubEventSpec{Repository: "railgrid/example"}}, pr)
			if err != nil || result.BaseCurrent == nil || *result.BaseCurrent != scenario.want || result.CIState != "passed" {
				t.Fatalf("observation=%+v err=%v", result, err)
			}
		})
	}
}

func TestOperateRejectsForeignOrRevokedCommandBeforeGitHub(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		event   string
		revoked bool
	}{{"other", false}, {"current", true}} {
		pr := &api.PullRequest{ObjectMeta: metav1.ObjectMeta{Name: "pr", UID: "pr-uid"}, Spec: api.PullRequestSpec{Command: &api.PullRequestCommand{EventUID: "current", Deadline: metav1.NewTime(time.Now().Add(time.Hour))}}}
		if tc.revoked {
			pr.Spec.Command.Deadline = metav1.NewTime(time.Now().Add(-time.Minute))
		}
		u, _ := asU(pr)
		raw, _ := json.Marshal(Operation{Op: "comment", PullRequestUID: "pr-uid", EventUID: tc.event})
		if _, err := f.s.Operate(context.Background(), f.dyn, "cluster", u, raw, dataplane.ProxiedIdentity{}); err == nil {
			t.Fatal("foreign/revoked command accepted")
		}
	}
	if f.calls != 0 {
		t.Fatal("rejected request reached GitHub")
	}
}

func TestQueuedPublicationRereadsRetiredAuthority(t *testing.T) {
	f := newFixture(t)
	cached := &api.PullRequest{ObjectMeta: metav1.ObjectMeta{Name: "pr", UID: "pr-uid"}, Spec: api.PullRequestSpec{Command: &api.PullRequestCommand{EventRef: "event", EventUID: "old-command", Deadline: metav1.NewTime(time.Now().Add(time.Hour))}, DesiredHead: &api.PullRequestHead{Commit: strings.Repeat("a", 40)}}}
	current := cached.DeepCopy()
	current.Spec.Command.Deadline = metav1.NewTime(time.Now().Add(-time.Second))
	current.Spec.DesiredHead = nil
	u, _ := asU(current)
	if _, err := f.dyn.Resource(PullRequests).Create(context.Background(), u, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.CheckPublication(context.Background(), "cluster", cached); err == nil {
		t.Fatal("cached publication survived cancellation")
	}
	if f.calls != 0 {
		t.Fatal("retired authority reached GitHub")
	}
}

func publicationFixture(t *testing.T) (*fixture, *api.PullRequest) {
	t.Helper()
	f := newFixture(t)
	f.event.Comment.Body = "/railgrid review-fix"
	head, commit := strings.Repeat("a", 40), strings.Repeat("b", 40)
	event := &api.GitHubEvent{
		TypeMeta:   metav1.TypeMeta{APIVersion: api.SchemeGroupVersion.String(), Kind: "GitHubEvent"},
		ObjectMeta: metav1.ObjectMeta{Name: "event", UID: "event-uid"},
		Spec: api.GitHubEventSpec{
			ConnectionRef: "app", ConnectionUID: "conn", RepositoryRef: "repo", RepositoryUID: "repo-uid",
			Repository: "railgrid/example", RepositoryID: 99, PullRequest: 7, CommentID: 11,
			Actor: "maintainer", ActorID: 1, Comment: f.event.Comment.Body,
			Head: head, Branch: "feature", Base: "main", Description: "Fixture", Title: "Example",
		},
	}
	event.Spec.Seal = seal("cluster", event.Spec, testSigningKey)
	eventU, err := asU(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.dyn.Resource(Events).Create(t.Context(), eventU, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	desired := &api.PullRequestHead{Commit: commit, BaseCommit: head, Tree: strings.Repeat("c", 40), BundleRef: strings.Repeat("d", 64)}
	pr := &api.PullRequest{
		TypeMeta:   metav1.TypeMeta{APIVersion: api.SchemeGroupVersion.String(), Kind: "PullRequest"},
		ObjectMeta: metav1.ObjectMeta{Name: "pr", UID: "pr-uid"},
		Spec: api.PullRequestSpec{
			Number: 7, Command: &api.PullRequestCommand{EventRef: event.Name, EventUID: string(event.UID), Deadline: metav1.NewTime(time.Now().Add(time.Hour).Truncate(time.Second))},
			RepositoryRef: "repo", Branch: "feature", Base: "main", Title: "Example", DesiredHead: desired,
		},
		Status: api.PullRequestStatus{Number: 7, Head: head},
	}
	prU, err := asU(pr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.dyn.Resource(PullRequests).Create(t.Context(), prU, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return f, pr.DeepCopy()
}

func updatePublicationResource(t *testing.T, f *fixture, mutate func(*api.PullRequest)) {
	t.Helper()
	u, err := f.dyn.Resource(PullRequests).Get(t.Context(), "pr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := decode[api.PullRequest](u)
	if err != nil {
		t.Fatal(err)
	}
	mutate(pr)
	u, err = asU(pr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.dyn.Resource(PullRequests).Update(t.Context(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPublicationRejectsDelayedTargetChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *fixture)
	}{
		{
			name: "command binding changed",
			mutate: func(t *testing.T, f *fixture) {
				updatePublicationResource(t, f, func(pr *api.PullRequest) { pr.Spec.Command.EventRef = "replacement-event" })
			},
		},
		{
			name: "repository binding changed",
			mutate: func(t *testing.T, f *fixture) {
				u, err := f.dyn.Resource(repositories).Get(t.Context(), "repo", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				repo, err := decode[api.Repository](u)
				if err != nil {
					t.Fatal(err)
				}
				repo.Spec.ConnectionRef = "replacement-app"
				u, err = asU(repo)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.dyn.Resource(repositories).Update(t.Context(), u, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "snapshot parent changed",
			mutate: func(t *testing.T, f *fixture) {
				updatePublicationResource(t, f, func(pr *api.PullRequest) { pr.Spec.DesiredHead.BaseCommit = strings.Repeat("e", 40) })
			},
		},
		{
			name:   "head advanced elsewhere",
			mutate: func(_ *testing.T, f *fixture) { f.pullRequest.Head.SHA = strings.Repeat("e", 40) },
		},
		{
			name:   "PR closed",
			mutate: func(_ *testing.T, f *fixture) { f.pullRequest.State = "closed" },
		},
		{
			name:   "head branch retargeted",
			mutate: func(_ *testing.T, f *fixture) { f.pullRequest.Head.Ref = "other-feature" },
		},
		{
			name:   "base branch retargeted",
			mutate: func(_ *testing.T, f *fixture) { f.pullRequest.Base.Ref = "release" },
		},
		{
			name:   "repository changed",
			mutate: func(_ *testing.T, f *fixture) { f.pullRequest.Head.Repo.ID = 100 },
		},
		{
			name:   "base repository changed",
			mutate: func(_ *testing.T, f *fixture) { f.pullRequest.Base.Repo.ID = 100 },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cached := publicationFixture(t)
			tc.mutate(t, f)
			if err := f.s.CheckPublication(t.Context(), "cluster", cached); err == nil {
				t.Fatal("delayed publication accepted a changed target")
			}
		})
	}
}

func TestCheckPublicationAllowsExpectedHeadAndPublishedCommitRecovery(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "expected head", true: "already published commit"}[replay], func(t *testing.T) {
			f, cached := publicationFixture(t)
			if replay {
				f.pullRequest.Head.SHA = cached.Spec.DesiredHead.Commit
			}
			if err := f.s.CheckPublication(t.Context(), "cluster", cached); err != nil {
				t.Fatalf("valid publication retry rejected: %v", err)
			}
		})
	}
}

func TestPublicationProofBindsInvocationAndLease(t *testing.T) {
	pr := &api.PullRequest{ObjectMeta: metav1.ObjectMeta{Name: "pr", UID: "pr-uid"}, Spec: api.PullRequestSpec{Command: &api.PullRequestCommand{EventUID: "event-uid", Deadline: metav1.NewTime(time.Now().Add(time.Hour))}}}
	key := []byte("test-signature-key-with-at-least-32-bytes")
	proof := publicationProof("cluster", pr, "factory:run:1", "coordinator", "agent-uid", key)
	for _, change := range []string{"cluster", "pr", "event", "deadline", "invocation", "agent", "agent-uid"} {
		copy := pr.DeepCopy()
		cluster, invocation, agent, uid := "cluster", "factory:run:1", "coordinator", "agent-uid"
		switch change {
		case "cluster":
			cluster = "other"
		case "pr":
			copy.UID = "other"
		case "event":
			copy.Spec.Command.EventUID = "other"
		case "deadline":
			copy.Spec.Command.Deadline = metav1.NewTime(time.Now().Add(2 * time.Hour))
		case "invocation":
			invocation = "factory:run:2"
		case "agent":
			agent = "other"
		case "agent-uid":
			uid = "other"
		}
		if publicationProof(cluster, copy, invocation, agent, uid, key) == proof {
			t.Fatalf("proof replay survived changed %s", change)
		}
	}
	if publicationProof("cluster", pr, "factory:run:1", "coordinator", "agent-uid", key) != proof {
		t.Fatal("recovery changed scope proof")
	}
}

func TestSameCommandChecksExactDeadline(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 100, time.UTC)
	a := &api.PullRequestCommand{EventRef: "event", EventUID: "uid", Deadline: metav1.NewTime(base)}
	b := a.DeepCopy()
	b.Deadline = metav1.NewTime(base.Add(500 * time.Millisecond))
	if a.Deadline.Unix() != b.Deadline.Unix() {
		t.Fatal("test deadlines must share a Unix second")
	}
	if sameCommand(a, b) {
		t.Fatal("changed publication deadline was treated as the same command")
	}
}

func TestStatusPublicationReusesCommentAcrossCommandsAndLostResponses(t *testing.T) {
	const marker = "<!-- railgrid-pr-status:pr-uid -->"
	var comments []Comment
	posts, patches := 0, 0
	client := Client{BotLogin: "railgrid-app[bot]", HTTP: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		var input struct {
			Body string `json:"body"`
		}
		var out any
		if r.Method != http.MethodGet {
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
		}
		switch r.Method {
		case http.MethodGet:
			out = comments
		case http.MethodPost:
			posts++
			comments = append(comments, Comment{ID: 42, Body: input.Body, User: User{Type: "Bot", Login: clientLoginForTest}})
			return nil, fmt.Errorf("create response lost")
		case http.MethodPatch:
			if r.URL.Path != "/repos/railgrid/example/issues/comments/42" {
				t.Fatalf("wrong patch target %s", r.URL.Path)
			}
			patches++
			comments[len(comments)-1].Body = input.Body
			return nil, fmt.Errorf("update response lost")
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
		raw, _ := json.Marshal(out)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	})}}
	// Neither another account nor a forged inline marker is a status comment.
	comments = []Comment{
		{ID: 1, Body: "spoof\n\n" + marker, User: User{Type: "User", Login: "maintainer"}},
		{ID: 2, Body: "spoof\n\n" + marker, User: User{Type: "Bot", Login: "other[bot]"}},
		{ID: 3, Body: marker + "\nquoted data", User: User{Type: "Bot", Login: clientLoginForTest}},
	}
	event := &api.GitHubEvent{Spec: api.GitHubEventSpec{Repository: "railgrid/example", PullRequest: 7, CommentID: 11}}
	for _, body := range []string{"Reviewing", "Checking CI", "Complete", "Reviewing another command"} {
		// New commands and continuations update the same PR comment.
		event.Spec.CommentID++
		if _, err := publishTextUpdating(t.Context(), client, event, 0, body, marker, true); err == nil {
			t.Fatal("expected lost response")
		}
		if _, err := publishTextUpdating(t.Context(), client, event, 0, body, marker, true); err != nil {
			t.Fatal(err)
		}
	}
	if posts != 1 || patches != 3 || len(comments) != 4 {
		t.Fatalf("posts=%d patches=%d comments=%d", posts, patches, len(comments))
	}
}

const clientLoginForTest = "railgrid-app[bot]"

func TestCleanReviewSuppressedAtCodeBoundary(t *testing.T) {
	f := newFixture(t)
	if w := f.deliver(t, false); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	event, _, _, err := f.s.Context(t.Context(), f.dyn, "cluster", "github-99-11")
	if err != nil {
		t.Fatal(err)
	}
	pr := &api.PullRequest{ObjectMeta: metav1.ObjectMeta{Name: "pr", UID: "pr-uid"}, Spec: api.PullRequestSpec{Command: &api.PullRequestCommand{EventRef: event.Name, EventUID: string(event.UID), Deadline: metav1.NewTime(time.Now().Add(time.Hour).Truncate(time.Second))}}}
	u, _ := asU(pr)
	zero, negative := 0, -1
	for _, count := range []*int{nil, &negative, &zero} {
		in := Operation{Op: "review", PullRequestUID: "pr-uid", EventUID: string(event.UID), InvocationKey: "factory:run:1", InvocationAgent: "coordinator", InvocationAgentUID: "agent-uid", Key: "review", ExpectedHead: strings.Repeat("a", 40), Body: "No actionable findings", FindingsCount: count}
		in.ScopeProof = publicationProof("cluster", pr, in.InvocationKey, in.InvocationAgent, in.InvocationAgentUID, testSigningKey)
		raw, _ := json.Marshal(in)
		result, err := f.s.Operate(t.Context(), f.dyn, "cluster", u, raw, dataplane.ProxiedIdentity{})
		if count != &zero {
			if err == nil {
				t.Fatal("review missing valid count was accepted")
			}
			continue
		}
		if err != nil || result.(map[string]any)["suppressed"] != true {
			t.Fatalf("result=%v err=%v", result, err)
		}
	}
	// The fixture rejects any publication HTTP request, so success proves no post.
}

type failingCallers struct{}

func (failingCallers) For(string, string) (dynamic.Interface, error) {
	return nil, errors.New("unavailable")
}
func (failingCallers) AsProvider(string) (dynamic.Interface, error) {
	return nil, errors.New("unavailable")
}

func TestWebhookPreAuthenticationFailuresMatch(t *testing.T) {
	f := newFixture(t)
	badSignature := f.deliver(t, true)
	missing := newFixture(t)
	if err := missing.dyn.Resource(connections).Delete(context.Background(), "app", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	missingBinding := missing.deliver(t, false)
	unavailable := newFixture(t)
	unavailable.s.Callers = failingCallers{}
	noWorkspace := unavailable.deliver(t, false)
	for _, w := range []*httptest.ResponseRecorder{badSignature, missingBinding, noWorkspace} {
		if w.Code != http.StatusNotFound || w.Body.String() != "{\"error\":\"not found\"}\n" {
			t.Fatalf("unexpected response: %d %q", w.Code, w.Body.String())
		}
	}
}
