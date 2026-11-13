// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-sdk/dataplane"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
)

func TestSessionNotificationIsAuthorizedAndObservedOutsideTranscript(t *testing.T) {
	ctx := t.Context()
	mem := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"}
	now := time.Now().UTC()
	if err := mem.ClaimSessionOwner(ctx, scope, "agent-uid", "chat", "owner", now); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantSessionAccess(ctx, scope, "agent-uid", "chat", "owner", "writer", now); err != nil {
		t.Fatal(err)
	}

	parent := &unstructured.Unstructured{}
	parent.SetUID(types.UID("agent-uid"))
	provider := fake.NewSimpleDynamicClient(runtime.NewScheme())
	request := dataplane.Request{ClusterID: "cluster", Resource: "agents", Name: "agent", Verb: "deliver"}
	makeRequest := func(method, subject, body string) *http.Request {
		r := httptest.NewRequest(method, "/clusters/cluster/apis/agents.railgrid.ai/v1alpha1/agents/agent", strings.NewReader(body))
		id := identity{clusterID: "cluster", orgUUID: scope.OrgUUID, workspaceUUID: scope.WorkspaceUUID, user: subject}
		r = r.WithContext(withGate(context.Background(), &gateInfo{request: request, provider: provider, object: parent, identity: id}))
		r.SetPathValue("name", "agent")
		r.SetPathValue("tail", "chat")
		return r
	}
	s := &Server{store: mem}
	deliver := httptest.NewRecorder()
	s.deliverSessionNotification(deliver, makeRequest(http.MethodPost, "writer", `{"key":"opaque-1","payload":"run completed"}`))
	if deliver.Code != http.StatusOK {
		t.Fatalf("deliver status = %d: %s", deliver.Code, deliver.Body.String())
	}

	// A producer grant does not authorize delivery into a different owner's
	// session when the caller supplied an exact owner binding.
	confusedDeputy := httptest.NewRecorder()
	s.deliverSessionNotification(confusedDeputy, makeRequest(http.MethodPost, "writer", `{"key":"opaque-wrong-owner","payload":"misrouted","expectedOwner":"another-owner"}`))
	if confusedDeputy.Code != http.StatusForbidden {
		t.Fatalf("cross-owner delivery status = %d, want 403: %s", confusedDeputy.Code, confusedDeputy.Body.String())
	}

	bound := httptest.NewRecorder()
	s.deliverSessionNotification(bound, makeRequest(http.MethodPost, "writer", `{"key":"opaque-bound","payload":"bound to owner","expectedOwner":"owner"}`))
	if bound.Code != http.StatusOK {
		t.Fatalf("correctly owner-bound delivery status = %d: %s", bound.Code, bound.Body.String())
	}

	feed := httptest.NewRecorder()
	s.listSessionNotifications(feed, makeRequest(http.MethodGet, "writer", ""))
	if feed.Code != http.StatusOK {
		t.Fatalf("feed status = %d: %s", feed.Code, feed.Body.String())
	}
	var response struct {
		Items []store.SessionNotification `json:"items"`
	}
	if err := json.Unmarshal(feed.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 2 || response.Items[0].Payload != "bound to owner" || response.Items[1].Payload != "run completed" {
		t.Fatalf("observed notification feed = %+v", response.Items)
	}
	if page, err := mem.ListMessages(ctx, scope, "chat", 20, ""); err != nil || len(page.Items) != 0 {
		t.Fatalf("notification entered transcript: %+v, %v", page.Items, err)
	}

	denied := httptest.NewRecorder()
	s.deliverSessionNotification(denied, makeRequest(http.MethodPost, "stranger", `{"key":"opaque-2","payload":"hidden"}`))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("unauthorized delivery status = %d, want 403: %s", denied.Code, denied.Body.String())
	}
}
