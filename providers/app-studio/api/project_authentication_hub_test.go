/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExistingAuthenticationUserPreservesHubStatusAndBoundsBodies(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantText   string
	}{
		{name: "forbidden", status: http.StatusForbidden, body: "project update permission required", wantStatus: http.StatusForbidden, wantText: "project update permission required"},
		{name: "missing account", status: http.StatusNotFound, body: `{"message":"account does not exist"}`, wantStatus: http.StatusNotFound, wantText: "account does not exist"},
		{name: "oversized failure", status: http.StatusForbidden, body: strings.Repeat("x", 64<<10), wantStatus: http.StatusForbidden, wantText: "exceeded"},
		{name: "upstream failure becomes gateway error", status: http.StatusInternalServerError, body: "internal details", wantStatus: http.StatusBadGateway, wantText: "could not complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/orgs/org/workspaces/workspace/app-auth/user" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Errorf("read request: %v", err)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()

			server := &Server{hubBase: upstream.URL, hubToken: "provider-token"}
			_, err := server.existingAuthenticationUser(context.Background(), identity{
				orgUUID: "org", workspaceUUID: "workspace", clusterID: "cluster-id", user: "alice", actionProof: "signed-proof",
			}, projectPublishingTargetView{
				APIVersion: "infrastructure.railgrid.ai/v1alpha1", Resource: "instances", Name: "demo-prod",
			}, "user-bob", "project-uid/production")
			if err == nil {
				t.Fatal("expected hub failure")
			}
			recorder := httptest.NewRecorder()
			writeHubError(recorder, err)
			if recorder.Code != tc.wantStatus || !strings.Contains(recorder.Body.String(), tc.wantText) {
				t.Fatalf("status=%d body=%q, want status %d containing %q", recorder.Code, recorder.Body.String(), tc.wantStatus, tc.wantText)
			}
			if tc.name == "oversized failure" && len(recorder.Body.Bytes()) > 2048 {
				t.Fatalf("oversized upstream body was reflected (%d bytes)", recorder.Body.Len())
			}
		})
	}
}

func TestExistingAuthenticationUserStrictlyBoundsAndDecodesSuccess(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		wantStatus int
		wantUser   string
	}{
		{name: "valid identity", body: `{"user":"user-bob","rbacIdentity":"railgrid:bob@example.test"}`, wantStatus: http.StatusOK, wantUser: "user-bob"},
		{name: "unknown field", body: `{"user":"user-bob","rbacIdentity":"railgrid:bob@example.test","extra":true}`, wantStatus: http.StatusBadGateway},
		{name: "trailing JSON", body: `{"user":"user-bob","rbacIdentity":"railgrid:bob@example.test"}{}`, wantStatus: http.StatusBadGateway},
		{name: "oversized response", body: `{"user":"user-bob","rbacIdentity":"railgrid:bob@example.test","padding":"` + strings.Repeat("x", 64<<10) + `"}`, wantStatus: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()

			server := &Server{hubBase: upstream.URL}
			member, err := server.existingAuthenticationUser(context.Background(), identity{
				orgUUID: "org", workspaceUUID: "workspace", clusterID: "cluster-id", user: "alice", actionProof: "signed-proof",
			}, projectPublishingTargetView{
				APIVersion: "infrastructure.railgrid.ai/v1alpha1", Resource: "instances", Name: "demo-prod",
			}, "user-bob", "project-uid/production")
			if tc.wantStatus == http.StatusOK {
				if err != nil || member.User != tc.wantUser || member.RBACIdentity == "" {
					t.Fatalf("member=%+v err=%v, want valid existing identity", member, err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected malformed/oversized hub response to fail")
			}
			recorder := httptest.NewRecorder()
			writeHubError(recorder, err)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%q, want %d", recorder.Code, recorder.Body.String(), tc.wantStatus)
			}
		})
	}
}
