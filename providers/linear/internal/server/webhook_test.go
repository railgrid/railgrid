// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroshq/provider-linear/internal/engine"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func webhookFixture(name string) *fake.FakeDynamicClient {
	conn := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "linear.providers.faros.sh/v1alpha1", "kind": "Connection", "metadata": map[string]any{"name": name, "uid": "connection"}, "spec": map[string]any{"apiKeySecretRef": map[string]any{"name": "api"}, "subscription": map[string]any{"id": "hook", "organizationID": "org", "signingSecretRef": map[string]any{"name": "signing"}}}}}
	secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "signing", "namespace": "default"}, "data": map[string]any{"apiKey": base64.StdEncoding.EncodeToString([]byte("long-signing-key-for-tests"))}}}
	team := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "linear.providers.faros.sh/v1alpha1", "kind": "Team", "metadata": map[string]any{"name": "team"}, "spec": map[string]any{"connection": name, "connectionUID": "connection", "teamID": "team"}}}
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{engine.Events: "EventList", engine.Teams: "TeamList"}, conn, secret, team)
}
func webhookRequest(name string) *http.Request {
	raw := fmt.Sprintf(`{"action":"create","type":"Issue","organizationId":"org","webhookId":"hook","webhookTimestamp":%d,"data":{"id":"issue","teamId":"team"}}`, time.Now().UnixMilli())
	mac := hmac.New(sha256.New, []byte("long-signing-key-for-tests"))
	_, _ = mac.Write([]byte(raw))
	r := httptest.NewRequest("POST", "/webhooks/workspace/"+name, strings.NewReader(raw))
	r.Header.Set("Linear-Signature", hex.EncodeToString(mac.Sum(nil)))
	return r
}
func TestWebhookHTTPAcknowledgmentNamesAndAuthentication(t *testing.T) {
	for _, name := range []string{"engineering.linear", strings.Repeat("a", 70)} {
		t.Run(name, func(t *testing.T) {
			cl := webhookFixture(name)
			mux := http.NewServeMux()
			mux.Handle("POST /webhooks/{cluster}/{connection}", newWebhookHandler(func(ctx context.Context, cluster, connection string) (dynamic.Interface, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 4*time.Second {
					t.Error("missing webhook processing deadline")
				}
				if cluster != "workspace" || connection != name {
					t.Errorf("scope %s/%s", cluster, connection)
				}
				return cl, nil
			}))
			for range 2 {
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, webhookRequest(name))
				if w.Code != 200 {
					t.Fatalf("ack=%d body=%s", w.Code, w.Body.String())
				}
			}
			events, err := cl.Resource(engine.Events).List(context.Background(), metav1.ListOptions{})
			if err != nil || len(events.Items) != 1 {
				t.Fatalf("events=%v error=%v", events, err)
			}
			cl.ClearActions()
			r := webhookRequest(name)
			r.Header.Set("Linear-Signature", "00")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code == 200 {
				t.Fatal("invalid signature accepted")
			}
			for _, action := range cl.Actions() {
				if action.GetResource() == engine.Events {
					t.Fatal("invalid signature reached event admission")
				}
			}
		})
	}
}
func TestWebhookSlowWorkspaceIsolationAndAdmissionBound(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	cl := webhookFixture("linear")
	mux := http.NewServeMux()
	mux.Handle("POST /webhooks/{cluster}/{connection}", newWebhookHandler(func(ctx context.Context, cluster, name string) (dynamic.Interface, error) {
		if cluster == "slow" {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return cl, nil
	}))
	done := make(chan struct{}, 2)
	for range 2 {
		go func() {
			defer func() { done <- struct{}{} }()
			r := webhookRequest("linear")
			r.URL.Path = "/webhooks/slow/linear"
			mux.ServeHTTP(httptest.NewRecorder(), r)
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("request did not start")
		}
	}
	w := httptest.NewRecorder()
	r := webhookRequest("linear")
	r.URL.Path = "/webhooks/slow/linear"
	mux.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("unbounded admission: %d", w.Code)
	}
	fast := make(chan int, 1)
	go func() { w := httptest.NewRecorder(); mux.ServeHTTP(w, webhookRequest("linear")); fast <- w.Code }()
	select {
	case code := <-fast:
		if code != 200 {
			t.Fatalf("fast workspace ack=%d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("slow workspace blocked another workspace")
	}
	// Cancellation and cleanup finish before the test's fake client goes out of scope.
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		<-done
	}
}

func TestWebhookCapacityCheckRemainsAtomicPerWorkspace(t *testing.T) {
	cl := webhookFixture("linear")
	for i := 0; i < 999; i++ {
		event := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "linear.providers.faros.sh/v1alpha1", "kind": "Event", "metadata": map[string]any{"name": fmt.Sprintf("retained-%d", i)}}}
		if err := cl.Tracker().Add(event); err != nil {
			t.Fatal(err)
		}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var once sync.Once
	cl.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
		once.Do(func() { close(entered); <-release })
		return false, nil, nil
	})
	// Distinct fake transports share storage. A reactor must not hold one fake's
	// action mutex while another request is trying to authenticate through it.
	other := webhookFixture("linear")
	other.PrependReactor("*", "*", ktesting.ObjectReaction(cl.Tracker()))
	var resolved atomic.Int32
	mux := http.NewServeMux()
	mux.Handle("POST /webhooks/{cluster}/{connection}", newWebhookHandler(func(context.Context, string, string) (dynamic.Interface, error) {
		if resolved.Add(1) == 1 {
			return cl, nil
		}
		return other, nil
	}))
	first := make(chan int, 1)
	go func() { w := httptest.NewRecorder(); mux.ServeHTTP(w, webhookRequest("linear")); first <- w.Code }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not reach admission")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, webhookRequest("linear"))
	if w.Code != 503 {
		t.Fatalf("concurrent admission=%d", w.Code)
	}
	release <- struct{}{}
	if code := <-first; code != 200 {
		t.Fatalf("persisted delivery=%d", code)
	}
	items, err := cl.Resource(engine.Events).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(items.Items) != 1000 {
		t.Fatalf("cap=%d err=%v", len(items.Items), err)
	}
}
