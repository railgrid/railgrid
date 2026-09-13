// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	"github.com/faroshq/provider-linear/internal/actionapi"
	"github.com/faroshq/provider-linear/internal/engine"
	"github.com/faroshq/provider-linear/internal/linearapi"
	"github.com/faroshq/provider-sdk/actionwire"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

type receiptTransport func(*http.Request) (*http.Response, error)

func (f receiptTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPrivateWriteReceiptsFenceReplayAndBindingChanges(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "lost upstream response"}[uncertain], func(t *testing.T) {
			var writes atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writes.Add(1)
				if uncertain {
					http.Error(w, "lost response", http.StatusBadGateway)
					return
				}
				_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"created"}}}}`))
			}))
			defer upstream.Close()
			s, e, caller, team, conn := receiptFixture(t, upstream)
			input := actionapi.Input{Connection: conn.Name, TeamID: team.Spec.TeamID, Action: "createIssue"}
			title := "Create once"
			input.Title = &title
			request := ActionRequest{RequestID: time.Now().UTC().Format("20060102T150405Z") + ".stable-intent"}
			r := httptest.NewRequest("POST", "/", nil)
			r.Header.Set("X-Faros-Cluster", "tenant-one")
			out, err := s.write(context.Background(), r, caller, team, conn, e, "create_issue", input, request, false)
			want := "Succeeded"
			if uncertain {
				want = "Uncertain"
			}
			if err != nil || out.Phase != want {
				t.Fatalf("outcome=%+v err=%v", out, err)
			}
			for i := 0; i < 3; i++ {
				out, err = s.write(context.Background(), r, caller, team, conn, e, "create_issue", input, request, i%2 == 0)
				if err != nil || out.Phase != want {
					t.Fatalf("recovery=%+v err=%v", out, err)
				}
			}
			changed := input
			other := "Changed"
			changed.Title = &other
			if _, err = s.write(context.Background(), r, caller, team, conn, e, "create_issue", changed, request, false); err == nil {
				t.Fatal("same key accepted different input")
			}
			replacement := team
			replacement.UID = "replacement"
			replacement.Name = "renamed-registration"
			if _, err = s.write(context.Background(), r, caller, replacement, conn, e, "create_issue", input, request, false); err == nil {
				t.Fatal("same key adopted replacement binding")
			}
			for _, invalidKey := range []string{"", time.Now().UTC().Add(-31*24*time.Hour).Format("20060102T150405Z") + ".expired", time.Now().UTC().Add(time.Hour).Format("20060102T150405Z") + ".future"} {
				invalid := ActionRequest{RequestID: invalidKey}
				if _, err := s.write(context.Background(), r, caller, team, conn, e, "create_issue", input, invalid, false); err == nil {
					t.Fatalf("invalid or expired key accepted: %q", invalidKey)
				}
			}
			if writes.Load() != 1 {
				t.Fatalf("upstream writes=%d", writes.Load())
			}
			records, err := s.PrivateReceipts.Resource(engine.Receipts).List(context.Background(), metav1.ListOptions{})
			if err != nil || len(records.Items) != 1 {
				t.Fatalf("private records=%v err=%v", records, err)
			}
			if _, found, _ := unstructured.NestedString(records.Items[0].Object, "spec", "title"); found {
				t.Fatal("terminal record retains dispatch body")
			}
			for _, call := range e.Client.(*fake.FakeDynamicClient).Actions() {
				if call.GetResource().Resource == "actionreceipts" {
					t.Fatal("receipt reached tenant client")
				}
			}
		})
	}
}

func TestConcurrentPrivateReceiptCreatesDispatchOnce(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var writes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writes.Add(1) == 1 {
			close(entered)
		}
		<-release
		_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"created"}}}}`))
	}))
	defer upstream.Close()
	s, e, caller, team, conn := receiptFixture(t, upstream)
	title := "Once"
	input := actionapi.Input{Connection: conn.Name, TeamID: team.Spec.TeamID, Action: "createIssue", Title: &title}
	req := ActionRequest{RequestID: time.Now().UTC().Format("20060102T150405Z") + ".concurrent-intent"}
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-Faros-Cluster", "tenant-one")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s.write(context.Background(), r, caller, team, conn, e, "create_issue", input, req, false)
		if err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("write was not dispatched")
	}
	for i := 0; i < 5; i++ {
		out, err := s.write(context.Background(), r, caller, team, conn, e, "create_issue", input, req, false)
		if err != nil || out.Phase != "Running" {
			t.Errorf("concurrent outcome=%+v err=%v", out, err)
		}
	}
	close(release)
	wg.Wait()
	if writes.Load() != 1 {
		t.Fatalf("writes=%d", writes.Load())
	}
}

func receiptFixture(t *testing.T, upstream *httptest.Server) (Server, engine.Engine, *fake.FakeDynamicClient, api.Team, api.Connection) {
	t.Helper()
	conn := api.Connection{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupName + "/v1alpha1", Kind: "Connection"}, ObjectMeta: metav1.ObjectMeta{Name: "linear", UID: "connection-uid"}, Spec: api.ConnectionSpec{APIKeySecretRef: api.SecretReference{Name: "key"}}}
	team := api.Team{ObjectMeta: metav1.ObjectMeta{Name: "team", UID: "team-uid"}, Spec: api.TeamSpec{Connection: conn.Name, ConnectionUID: string(conn.UID), TeamID: "upstream-team"}}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&conn)
	if err != nil {
		t.Fatal(err)
	}
	secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "key", "namespace": "default"}, "data": map[string]any{"apiKey": base64.StdEncoding.EncodeToString([]byte("test-key"))}}}
	tenant := fake.NewSimpleDynamicClient(runtime.NewScheme(), &unstructured.Unstructured{Object: object}, secret)
	caller := fake.NewSimpleDynamicClient(runtime.NewScheme())
	caller.PrependReactor("create", "selfsubjectreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"userInfo": map[string]any{"username": "caller", "uid": "caller-uid"}}}}, nil
	})
	store := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{engine.Receipts: "ActionReceiptList"})
	// Match CRD status-subresource semantics rather than preserving create status.
	store.PrependReactor("create", "actionreceipts", func(action ktesting.Action) (bool, runtime.Object, error) {
		delete(action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).Object, "status")
		return false, nil, nil
	})
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.Engine{Client: tenant, Access: engine.TeamAccess{team.Spec.TeamID: true}, NewClient: func(string) *linearapi.Client {
		client := linearapi.New("test-key")
		client.HTTP.Transport = receiptTransport(func(r *http.Request) (*http.Response, error) {
			r = r.Clone(r.Context())
			r.URL.Scheme = target.Scheme
			r.URL.Host = target.Host
			return http.DefaultTransport.RoundTrip(r)
		})
		return client
	}}
	return Server{PrivateReceipts: store, InstanceID: "test"}, e, caller, team, conn
}

func TestReceiptRetentionNeverEvictsUncertainEvidence(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	objects := []runtime.Object{}
	for _, phase := range []string{"Succeeded", "Failed", "Running", "Uncertain"} {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "linear.internal.faros.sh/v1alpha1", "kind": "ActionReceipt", "metadata": map[string]any{"name": strings.ToLower(phase), "labels": map[string]any{receiptTenantLabel: "tenant"}, "annotations": map[string]any{receiptIssuedAnnotation: old}, "finalizers": []any{"linear.internal.faros.sh/receipt-history"}}, "status": map[string]any{"phase": phase, "completedAt": old}}})
	}
	objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "linear.internal.faros.sh/v1alpha1", "kind": "ActionReceipt", "metadata": map[string]any{"name": "opaque-key", "labels": map[string]any{receiptTenantLabel: "tenant"}}, "status": map[string]any{"phase": "Succeeded", "completedAt": old}}})
	store := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{engine.Receipts: "ActionReceiptList"}, objects...)
	if err := admitReceipt(context.Background(), store.Resource(engine.Receipts), "tenant", now); err != nil {
		t.Fatal(err)
	}
	retained, err := store.Resource(engine.Receipts).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(retained.Items) != 3 {
		t.Fatalf("retained=%v", retained.Items)
	}
	for _, item := range retained.Items {
		if item.GetName() != "running" && item.GetName() != "uncertain" && item.GetName() != "opaque-key" {
			t.Fatalf("unexpected retained record %s", item.GetName())
		}
	}
}

func TestActionHTTPKeyAndReceiptRecoveryWithoutCredentials(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"created"}}}}`))
	}))
	defer upstream.Close()
	s, e, caller, team, conn := receiptFixture(t, upstream)
	title := "Create once"
	input := actionapi.Input{Connection: conn.Name, TeamID: team.Spec.TeamID, Action: "createIssue", Title: &title}
	key := "sdk-opaque-key"
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-Faros-Cluster", "tenant-one")
	if out, err := s.write(context.Background(), r, caller, team, conn, e, "create_issue", input, ActionRequest{RequestID: key}, false); err != nil || out.Phase != "Succeeded" {
		t.Fatalf("seed: %+v %v", out, err)
	}
	team.TypeMeta = metav1.TypeMeta{APIVersion: api.GroupName + "/v1alpha1", Kind: "Team"}
	var endpoint string
	secrets := 0
	deny := false
	tenant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch {
		case strings.Contains(r.URL.Path, "/secrets/"):
			secrets++
			http.Error(w, "credential unavailable", 404)
			return
		case strings.HasSuffix(r.URL.Path, "/apiexportendpointslices/"+api.GroupName):
			result = map[string]any{"apiVersion": "apis.kcp.io/v1alpha1", "kind": "APIExportEndpointSlice", "status": map[string]any{"endpoints": []any{map[string]any{"url": endpoint}}}}
		case strings.HasSuffix(r.URL.Path, "/teams/"+team.Name):
			result = team
		case strings.HasSuffix(r.URL.Path, "/connections/"+conn.Name):
			result = conn
		case strings.HasSuffix(r.URL.Path, "/selfsubjectaccessreviews"):
			result = map[string]any{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview", "status": map[string]any{"allowed": !deny}}
		case strings.HasSuffix(r.URL.Path, "/selfsubjectreviews"):
			result = map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "SelfSubjectReview", "status": map[string]any{"userInfo": map[string]any{"username": "caller", "uid": "caller-uid"}}}
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer tenant.Close()
	endpoint = tenant.URL + "/export"
	s.HubURL = tenant.URL
	s.Authority.Config = &rest.Config{Host: tenant.URL + "/provider"}
	mux := http.NewServeMux()
	s.Routes(mux)
	for _, scenario := range []string{"header replay", "inspect", "conflicting keys", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			path := "/actions/clusters/tenant-one/teams/" + team.Name + "/create_issue/v1"
			method, body := "POST", `{"input":{"title":"Create once"}}`
			if scenario == "inspect" {
				method, body = "GET", ""
				path += "?requestId=" + key
			}
			if scenario == "conflicting keys" {
				body = `{"requestId":"other-key","input":{"title":"Create once"}}`
			}
			deny = scenario == "denied"
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer caller-token")
			req.Header.Set("X-Faros-Cluster", "tenant-one")
			req.Header.Set("Idempotency-Key", key)
			req.Header.Set("X-Request-ID", "correlation-only")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			var envelope actionwire.Envelope
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.RequestID != "correlation-only" || envelope.Provider != "linear" || envelope.Action != "create_issue" || envelope.ActionVersion != "v1" || envelope.ResourceRef.Name != team.Name || envelope.ResourceRef.Resource != "teams" || envelope.ResourceRef.Kind != "Team" || envelope.ResourceRef.APIVersion != api.GroupName+"/v1alpha1" {
				t.Fatalf("invalid envelope: %s", w.Body.String())
			}
			if scenario == "conflicting keys" || scenario == "denied" {
				want := 400
				if deny {
					want = 403
				}
				if w.Code != want || envelope.Error == nil {
					t.Fatalf("denial: %d %s", w.Code, w.Body.String())
				}
				return
			}
			var out actionapi.Outcome
			if err := json.Unmarshal(envelope.Result, &out); err != nil {
				t.Fatalf("decode %d %s: %v", w.Code, w.Body.String(), err)
			}
			if w.Code != 200 || out.Phase != "Succeeded" || envelope.Error != nil {
				t.Fatalf("recovery failed: %d %s", w.Code, w.Body.String())
			}
		})
	}
	if secrets != 0 {
		t.Fatalf("recovery accessed unavailable credentials %d times", secrets)
	}
}

func TestReceiptDispatchRejectsReplacementBeforeReadingCredentials(t *testing.T) {
	for _, scenario := range []string{"replaced", "missing binding"} {
		t.Run(scenario, func(t *testing.T) {
			writes := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writes++
				_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"created"}}}}`))
			}))
			defer upstream.Close()
			s, e, caller, team, conn := receiptFixture(t, upstream)
			tenant := e.Client.(*fake.FakeDynamicClient)
			s.PrivateReceipts.(*fake.FakeDynamicClient).PrependReactor("create", "actionreceipts", func(action ktesting.Action) (bool, runtime.Object, error) {
				u := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
				if scenario == "missing binding" {
					annotations := u.GetAnnotations()
					delete(annotations, "linear.internal.faros.sh/connection-uid")
					u.SetAnnotations(annotations)
				} else {
					replacement, err := tenant.Resource(engine.Connections).Get(context.Background(), conn.Name, metav1.GetOptions{})
					if err != nil {
						t.Fatal(err)
					}
					replacement.SetUID("replacement-uid")
					if err := tenant.Resource(engine.Connections).Delete(context.Background(), conn.Name, metav1.DeleteOptions{}); err != nil {
						t.Fatal(err)
					}
					if _, err := tenant.Resource(engine.Connections).Create(context.Background(), replacement, metav1.CreateOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				return false, nil, nil
			})
			title := "Original intent"
			input := actionapi.Input{Connection: conn.Name, TeamID: team.Spec.TeamID, Action: "createIssue", Title: &title}
			r := httptest.NewRequest("POST", "/", nil)
			r.Header.Set("X-Faros-Cluster", "tenant-one")
			out, err := s.write(context.Background(), r, caller, team, conn, e, "create_issue", input, ActionRequest{RequestID: "replacement-test"}, false)
			if writes != 0 {
				t.Fatalf("replacement received %d upstream writes", writes)
			}
			for _, action := range tenant.Actions() {
				if action.GetResource().Resource == "secrets" {
					t.Fatal("invalid binding reached credential lookup")
				}
			}
			if scenario == "replaced" && (err != nil || out.Phase != "Failed") {
				t.Fatalf("outcome=%+v err=%v", out, err)
			}
			if scenario == "missing binding" && err == nil {
				t.Fatal("missing binding accepted")
			}
			records, err := s.PrivateReceipts.Resource(engine.Receipts).List(context.Background(), metav1.ListOptions{})
			if err != nil || len(records.Items) != 1 {
				t.Fatalf("receipts=%v err=%v", records, err)
			}
			phase, _, _ := unstructured.NestedString(records.Items[0].Object, "status", "phase")
			if phase != "Failed" {
				t.Fatalf("rejection was not recorded: %s", phase)
			}
		})
	}
}
