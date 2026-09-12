// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ktesting "k8s.io/client-go/testing"

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	"github.com/faroshq/provider-linear/internal/linearapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testClient(server *httptest.Server) *linearapi.Client {
	c := linearapi.New("api-key")
	target, _ := url.Parse(server.URL)
	c.HTTP.Transport = transport(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host
		return http.DefaultTransport.RoundTrip(r)
	})
	return c
}
func object(t *testing.T, v any) *unstructured.Unstructured {
	t.Helper()
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(v)
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: u}
}
func setup(t *testing.T) (Engine, *unstructured.Unstructured) {
	t.Helper()
	title := "approved"
	conn := api.Connection{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupName + "/v1alpha1", Kind: "Connection"}, ObjectMeta: metav1.ObjectMeta{Name: "linear", UID: types.UID("connection-one")}, Spec: api.ConnectionSpec{APIKeySecretRef: api.SecretReference{Name: "key", Key: "apiKey"}, Teams: []api.TeamReference{{ID: "allowed"}}, Subscription: &api.Subscription{ID: "hook", OrganizationID: "organization", SigningSecretRef: api.SecretReference{Name: "key", Key: "signing"}}}}
	op := api.Operation{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupName + "/v1alpha1", Kind: "Operation"}, ObjectMeta: metav1.ObjectMeta{Name: "op-1", UID: "op-one"}, Spec: api.OperationSpec{Connection: "linear", Action: "createIssue", TeamID: "allowed", Title: &title}}
	secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "key", "namespace": "default"}, "data": map[string]any{"apiKey": base64.StdEncoding.EncodeToString([]byte("api-key")), "signing": base64.StdEncoding.EncodeToString([]byte("a-long-signing-secret"))}}}
	u := object(t, &op)
	cl := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{Events: "EventList", Operations: "OperationList", Connections: "ConnectionList"}, object(t, &conn), u, secret)
	return Engine{Client: cl}, u
}
func TestAmbiguousMutationPersistsAndSurvivesRestartWithoutReplay(t *testing.T) {
	ctx := context.Background()
	e, u := setup(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "lost outcome", http.StatusBadGateway)
	}))
	defer srv.Close()
	e.NewClient = func(string) *linearapi.Client { return testClient(srv) }
	if err := e.Reconcile(ctx, u); err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	if phase != "Uncertain" || calls != 1 {
		t.Fatalf("phase=%s calls=%d", phase, calls)
	}
	restarted := e
	if err := restarted.Reconcile(ctx, u); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("replayed uncertain mutation")
	}
}
func TestIssueAndStatePolicyBeforeMutation(t *testing.T) {
	ctx := context.Background()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issue": map[string]any{"id": "issue", "team": map[string]any{"id": "forbidden"}}}})
	}))
	defer srv.Close()
	conn := api.Connection{Spec: api.ConnectionSpec{Teams: []api.TeamReference{{ID: "allowed"}}}}
	_, err := Execute(ctx, testClient(srv), conn, api.OperationSpec{Action: "addComment", IssueID: "issue", Body: "hi"})
	if err == nil || calls != 1 {
		t.Fatal("foreign team issue mutation was permitted")
	}
	_, err = Execute(ctx, testClient(srv), conn, api.OperationSpec{Action: "issues", TeamID: "forbidden"})
	if err == nil || calls != 1 {
		t.Fatal("foreign team list was sent")
	}
}
func TestCommentRepliesBindParentToAuthorizedIssue(t *testing.T) {
	newServer := func(t *testing.T, teamID, parentIssueID string) (*httptest.Server, *int) {
		t.Helper()
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			var request struct {
				Query string `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			switch {
			case strings.Contains(request.Query, "issue(id:$id)"):
				_, _ = fmt.Fprintf(w, `{"data":{"issue":{"id":"issue-uuid","identifier":"ENG-1","url":"https://linear.app/acme/issue/ENG-1","team":{"id":%q,"name":"Team","key":"ENG"},"state":{"id":"state","name":"Open"}}}}`, teamID)
			case strings.Contains(request.Query, "comment(id:$id){issueId}"):
				_, _ = fmt.Fprintf(w, `{"data":{"comment":{"issueId":%q}}}`, parentIssueID)
			case strings.Contains(request.Query, "children(first:$first"):
				_, _ = w.Write([]byte(`{"data":{"comment":{"children":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			default:
				t.Errorf("unexpected query %q", request.Query)
				http.Error(w, "unexpected query", http.StatusBadRequest)
			}
		}))
		return srv, &calls
	}
	connection := api.Connection{Spec: api.ConnectionSpec{Teams: []api.TeamReference{{ID: "allowed"}}}}
	t.Run("authorized parent", func(t *testing.T) {
		srv, calls := newServer(t, "allowed", "issue-uuid")
		defer srv.Close()
		page, err := Execute(context.Background(), testClient(srv), connection, api.OperationSpec{Action: "replies", IssueID: "ENG-1", CommentID: "comment-1", First: 2, After: "cursor-1"})
		if err != nil || calls == nil || *calls != 3 {
			t.Fatalf("Execute() page=%+v calls=%d error=%v", page, *calls, err)
		}
	})
	t.Run("parent mismatch", func(t *testing.T) {
		srv, calls := newServer(t, "allowed", "other-issue")
		defer srv.Close()
		_, err := Execute(context.Background(), testClient(srv), connection, api.OperationSpec{Action: "replies", IssueID: "ENG-1", CommentID: "comment-1"})
		if err == nil || !strings.Contains(err.Error(), "outside the requested issue") || *calls != 2 {
			t.Fatalf("error=%v calls=%d, want parent rejection before children", err, *calls)
		}
	})
	t.Run("foreign issue team", func(t *testing.T) {
		srv, calls := newServer(t, "forbidden", "issue-uuid")
		defer srv.Close()
		_, err := Execute(context.Background(), testClient(srv), connection, api.OperationSpec{Action: "replies", IssueID: "ENG-1", CommentID: "comment-1"})
		if err == nil || !strings.Contains(err.Error(), "outside connection policy") || *calls != 1 {
			t.Fatalf("error=%v calls=%d, want team policy rejection", err, *calls)
		}
	})
}
func signature(raw []byte) string {
	m := hmac.New(sha256.New, []byte("a-long-signing-secret"))
	_, _ = m.Write(raw)
	return hex.EncodeToString(m.Sum(nil))
}
func TestWebhookAuthenticityScopeDedupAndRetention(t *testing.T) {
	ctx := context.Background()
	e, _ := setup(t)
	now := time.Now().UTC()
	e.Now = func() time.Time { return now }
	raw := []byte(fmt.Sprintf(`{"action":"create","type":"Issue","organizationId":"organization","webhookId":"hook","webhookTimestamp":%d,"data":{"id":"issue","teamId":"allowed"}}`, now.UnixMilli()))
	if err := e.Webhook(ctx, "linear", signature(raw), "delivery", raw); err != nil {
		t.Fatal(err)
	}
	if err := e.Webhook(ctx, "linear", signature(raw), "different-header", raw); err != nil {
		t.Fatal(err)
	}
	list, err := e.Client.Resource(Events).List(ctx, metav1.ListOptions{})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("events=%v err=%v", list, err)
	}
	for _, bad := range [][]byte{[]byte(strings.Replace(string(raw), "allowed", "forbidden", 1)), []byte(strings.Replace(string(raw), "organization\"", "other\"", 1))} {
		if err := e.Webhook(ctx, "linear", signature(bad), "delivery", bad); err == nil {
			t.Fatal("accepted foreign event")
		}
	}
	if _, err := Verify(raw, "00", "a-long-signing-secret", now); err == nil {
		t.Fatal("invalid signature accepted")
	}
	if _, err := Verify(raw, signature(raw), "a-long-signing-secret", now.Add(2*time.Minute)); err == nil {
		t.Fatal("stale event accepted")
	}
	e.Now = func() time.Time { return now.Add(8 * 24 * time.Hour) }
	if err = e.Prune(ctx, &list.Items[0]); err != nil {
		t.Fatal(err)
	}
	list, err = e.Client.Resource(Events).List(ctx, metav1.ListOptions{})
	if err != nil || len(list.Items) != 0 {
		t.Fatal("expired event retained")
	}
	otherWorkspace := Engine{Client: fake.NewSimpleDynamicClient(runtime.NewScheme())}
	if _, _, err := otherWorkspace.Connection(ctx, "linear"); err == nil {
		t.Fatal("connection resolved outside its workspace")
	}
}

func TestCompetingDispatchClaimsOnlyPermitOneWrite(t *testing.T) {
	e, u := setup(t)
	var claims atomic.Int32
	var writes atomic.Int32
	e.Client.(*fake.FakeDynamicClient).PrependReactor("update", "operations", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "status" {
			obj := action.(ktesting.UpdateAction).GetObject().(*unstructured.Unstructured)
			phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
			if phase == "Running" && claims.Add(1) > 1 {
				return true, nil, apierrors.NewConflict(Operations.GroupResource(), u.GetName(), fmt.Errorf("stale resource version"))
			}
		}
		return false, nil, nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"new-issue"}}}}`))
	}))
	defer srv.Close()
	e.NewClient = func(string) *linearapi.Client { return testClient(srv) }
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = e.Reconcile(context.Background(), u.DeepCopy()) }()
	}
	wg.Wait()
	if writes.Load() != 1 {
		t.Fatalf("writes=%d", writes.Load())
	}
}

func TestCredentialNamespacesRemainExplicitWithinWorkspace(t *testing.T) {
	ctx := context.Background()
	e, _ := setup(t)
	for _, namespace := range []string{"", "default"} {
		key, err := e.Secret(ctx, api.SecretReference{Name: "key", Namespace: namespace})
		if err != nil || key != "api-key" {
			t.Fatalf("default credential: key present=%t err=%v", key != "", err)
		}
	}
	secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "key", "namespace": "credentials"}, "data": map[string]any{"apiKey": base64.StdEncoding.EncodeToString([]byte("scoped-key"))}}}
	if _, err := e.Client.Resource(secrets).Namespace("credentials").Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	key, err := e.Secret(ctx, api.SecretReference{Name: "key", Namespace: "credentials"})
	if err != nil || key != "scoped-key" {
		t.Fatalf("explicit credential namespace not honored: %v", err)
	}
	if _, err := e.Secret(ctx, api.SecretReference{Name: "key", Namespace: "missing"}); err == nil {
		t.Fatal("missing namespace fell back to another credential")
	}
	conn, err := e.Client.Resource(Connections).Get(ctx, "linear", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedField(conn.Object, "credentials", "spec", "apiKeySecretRef", "namespace"); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Client.Resource(Connections).Update(ctx, conn, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, key, err = e.Connection(ctx, "linear")
	if err != nil || key != "scoped-key" {
		t.Fatalf("connection did not use referenced namespace: %v", err)
	}
	for _, action := range e.Client.(*fake.FakeDynamicClient).Actions() {
		if action.GetResource().Group == api.GroupName && action.GetNamespace() != "" {
			t.Fatalf("provider resource used namespace: %s", action.GetNamespace())
		}
	}
}
