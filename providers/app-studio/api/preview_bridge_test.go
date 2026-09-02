/*
Copyright 2026 The Faros Authors.

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
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/tenant"
)

func TestPreviewBridgeCapabilityRoundTripAndTamperResistance(t *testing.T) {
	signer, err := newEphemeralPreviewBridgeCapabilitySigner()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	signer.now = func() time.Time { return now }
	claims := previewBridgeCapabilityClaims{
		Issuer:        "app-studio",
		Audience:      "preview-bridge",
		JTI:           "session-1",
		SessionID:     "session-1",
		Version:       previewBridgeProtocolVersion,
		PreviewOrigin: "https://demo.preview.example",
		PortalOrigin:  "https://console.example",
		Generation:    "826e6fa5-c38b-4bdb-8f8f-098198b74f65",
		IssuedAt:      now.Unix(),
		ExpiresAt:     now.Add(time.Minute).Unix(),
	}
	token, err := signer.sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	got, err := signer.verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if got != claims {
		t.Fatalf("claims = %#v, want %#v", got, claims)
	}

	parts := strings.Split(token, ".")
	parts[1] = parts[1] + "A"
	if _, err := signer.verify(strings.Join(parts, ".")); err == nil {
		t.Fatal("tampered capability unexpectedly verified")
	}

	signer.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := signer.verify(token); err == nil {
		t.Fatal("expired capability unexpectedly verified")
	}
}

func TestPreviewBridgeStoreScopesAndReplacesSessions(t *testing.T) {
	store := newPreviewBridgeStore()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	scope := previewBridgeScope{
		ClusterID:     "cluster-1",
		OrgUUID:       "org-1",
		WorkspaceUUID: "workspace-1",
		ProjectUID:    "project-uid-1",
		ProjectName:   "demo",
		Actor:         "alice@example.com",
	}
	generation := "826e6fa5-c38b-4bdb-8f8f-098198b74f65"
	const portalInstanceID = "77915ea4-f533-433a-a7fd-30a1f0fcc47d"
	first, err := store.create(scope, portalInstanceID, "https://demo.preview.example", "https://console.example", generation, 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.Scope != scope || first.Generation != generation || first.PreviewOrigin != "https://demo.preview.example" || first.PortalOrigin != "https://console.example" {
		t.Fatalf("session metadata = %#v", first)
	}

	wrongScope := scope
	wrongScope.ClusterID = "cluster-2"
	if store.delete(first.ID, wrongScope) {
		t.Fatal("cross-cluster session deletion unexpectedly succeeded")
	}

	second, err := store.create(scope, "0b106f8d-6760-49db-8fc5-faa377e5db3e", "https://demo.preview.example", "https://console.example", generation, 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("sibling tab unexpectedly reused session ID")
	}
	replacement, err := store.create(scope, portalInstanceID, "https://demo.preview.example", "https://console.example", generation, 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == first.ID {
		t.Fatal("replacement reused session ID")
	}
	if store.delete(first.ID, scope) {
		t.Fatal("superseded session remained addressable")
	}
	if !store.delete(second.ID, scope) {
		t.Fatal("same-project sibling session was not deletable")
	}
	if !store.delete(replacement.ID, scope) {
		t.Fatal("replacement session was not deletable")
	}
}

func TestPreviewBridgeStoreExpiresSessions(t *testing.T) {
	store := newPreviewBridgeStore()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	scope := previewBridgeScope{
		ClusterID:     "cluster-1",
		OrgUUID:       "org-1",
		WorkspaceUUID: "workspace-1",
		ProjectUID:    "project-uid-1",
		ProjectName:   "demo",
		Actor:         "alice@example.com",
	}
	generation := "826e6fa5-c38b-4bdb-8f8f-098198b74f65"
	session, err := store.create(scope, "77915ea4-f533-433a-a7fd-30a1f0fcc47d", "https://demo.preview.example", "https://console.example", generation, 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.sessions[session.ID]; !ok {
		t.Fatal("created session was not retained")
	}

	now = now.Add(2 * time.Minute)
	if _, err := store.create(scope, "0b106f8d-6760-49db-8fc5-faa377e5db3e", "https://demo.preview.example", "https://console.example", generation, 1, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.sessions[session.ID]; ok {
		t.Fatal("expired session was not cleaned up on create")
	}
}

func TestPreviewBridgeToolIsRemoved(t *testing.T) {
	if projectAssistantLocalToolRegistry(&Server{}).Has("get_preview_bridge_logs") {
		t.Fatal("legacy preview bridge tool is still registered")
	}
}

func TestProjectAssistantMetricsRouteExposesSkillMetrics(t *testing.T) {
	router := mux.NewRouter()
	(&Server{}).Register(router)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("metrics response = %d %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/plain; version=0.0.4" {
		t.Fatalf("metrics content type = %q", got)
	}
	if !strings.Contains(response.Body.String(), "app_studio_assistant_skill_catalog_total") {
		t.Fatalf("metrics body omitted assistant skill metrics: %s", response.Body.String())
	}
}

func TestPreviewBridgeDisabledRouteReturnsControlledNotFound(t *testing.T) {
	server := &Server{}
	router := mux.NewRouter()
	server.Register(router)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/preview-bridge/sessions", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "preview bridge sharing is not enabled") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestPreviewBridgeProjectSupportIsLimitedToBuiltInViteTemplates(t *testing.T) {
	for _, tc := range []struct {
		name      string
		template  string
		supported bool
	}{
		{name: "simple webapp", template: "simple-webapp", supported: true},
		{name: "application", template: "application", supported: true},
		{name: "custom", template: "custom-frontend", supported: false},
		{name: "empty", template: "", supported: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := &aiv1alpha1.Project{
				Spec: aiv1alpha1.ProjectSpec{
					Template: &aiv1alpha1.ProjectTemplateSpec{Name: tc.template},
				},
			}
			if got := previewBridgeProjectSupported(project); got != tc.supported {
				t.Fatalf("previewBridgeProjectSupported() = %v, want %v", got, tc.supported)
			}
		})
	}
}

func TestPreviewBridgeSessionHTTPFlowUsesCurrentPreviewAndCallerScope(t *testing.T) {
	templateJSON, err := json.Marshal(applicationTemplateObject().Object)
	if err != nil {
		t.Fatal(err)
	}
	projectYAML := `apiVersion: ai.faros.sh/v1alpha1
kind: Project
metadata:
  name: demo
  uid: project-uid-1
spec:
  displayName: Demo
  template:
    name: application
`
	graphQL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(request.Query, "ProjectYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"ai_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": projectYAML}},
			}})
		case strings.Contains(request.Query, "TemplateYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"infrastructure_faros_sh": map[string]any{"v1alpha1": map[string]any{"TemplateYaml": string(templateJSON)}},
			}})
		case strings.Contains(request.Query, "InstanceYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"infrastructure_faros_sh": map[string]any{"v1alpha1": map[string]any{
					"InstanceYaml": `{"apiVersion":"infrastructure.faros.sh/v1alpha1","kind":"Instance","metadata":{"name":"demo-dev"},"spec":{"template":"application"},"status":{"url":"https://demo.preview.example/app?token=server-only"}}`,
				}},
			}})
		default:
			t.Fatalf("unexpected GraphQL query: %s", request.Query)
		}
	}))
	t.Cleanup(graphQL.Close)

	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), nil, nil, "", false)
	signer, err := newEphemeralPreviewBridgeCapabilitySigner()
	if err != nil {
		t.Fatal(err)
	}
	server.previewBridgeEnabled = true
	server.previewBridgeStore = newPreviewBridgeStore()
	server.previewBridgeSigner = signer
	server.SetPreviewEdgeProbe(func(_ context.Context, _ string) error { return nil })
	router := mux.NewRouter()
	server.Register(router)

	generation := "826e6fa5-c38b-4bdb-8f8f-098198b74f65"
	create := httptest.NewRequest(http.MethodPost, "/api/projects/demo/preview-bridge/sessions", strings.NewReader(
		`{"generation":"`+generation+`","protocolVersion":1,"portalInstanceID":"77915ea4-f533-433a-a7fd-30a1f0fcc47d"}`,
	))
	setPreviewBridgeTestHeaders(create, "alice@example.com", "cluster-1")
	create.Header.Set("Origin", "https://console.example")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", createResponse.Code, createResponse.Body.String())
	}
	var session previewBridgeSessionCreateResponse
	if err := json.NewDecoder(createResponse.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.Status != "available" || session.Generation != generation ||
		session.PreviewOrigin != "https://demo.preview.example" ||
		session.PortalOrigin != "https://console.example" {
		t.Fatalf("session = %#v", session)
	}
	claims, err := signer.verify(session.Capability)
	if err != nil {
		t.Fatal(err)
	}
	if claims.SessionID != session.SessionID || claims.Generation != generation ||
		claims.PreviewOrigin != session.PreviewOrigin || claims.PortalOrigin != session.PortalOrigin {
		t.Fatalf("capability claims = %#v", claims)
	}
	parts := strings.Split(session.Capability, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, forbiddenClaim := range []string{"clusterID", "org", "workspace", "projectUID", "project", "sub"} {
		if strings.Contains(string(payload), `"`+forbiddenClaim+`"`) {
			t.Fatalf("capability disclosed server-only scope %q: %s", forbiddenClaim, payload)
		}
	}

	secondGeneration := "5ac4b288-a1fa-4c99-936c-07467cd3cadb"
	secondCreate := httptest.NewRequest(http.MethodPost, "/api/projects/demo/preview-bridge/sessions", strings.NewReader(
		`{"generation":"`+secondGeneration+`","protocolVersion":1,"portalInstanceID":"0b106f8d-6760-49db-8fc5-faa377e5db3e"}`,
	))
	setPreviewBridgeTestHeaders(secondCreate, "alice@example.com", "cluster-1")
	secondCreate.Header.Set("Origin", "https://console.example")
	secondCreateResponse := httptest.NewRecorder()
	router.ServeHTTP(secondCreateResponse, secondCreate)
	if secondCreateResponse.Code != http.StatusCreated {
		t.Fatalf("second tab create = %d %s", secondCreateResponse.Code, secondCreateResponse.Body.String())
	}
	var secondSession previewBridgeSessionCreateResponse
	if err := json.NewDecoder(secondCreateResponse.Body).Decode(&secondSession); err != nil {
		t.Fatal(err)
	}
	if secondSession.SessionID == session.SessionID {
		t.Fatal("two tabs unexpectedly shared one session")
	}

	delete := httptest.NewRequest(http.MethodDelete,
		"/api/projects/demo/preview-bridge/sessions/"+session.SessionID, nil)
	setPreviewBridgeTestHeaders(delete, "alice@example.com", "cluster-1")
	deleteResponse := httptest.NewRecorder()
	router.ServeHTTP(deleteResponse, delete)
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", deleteResponse.Code, deleteResponse.Body.String())
	}
}

func setPreviewBridgeTestHeaders(request *http.Request, actor, clusterID string) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Faros-User", actor)
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org-1:workspace-1")
	request.Header.Set("X-Faros-Cluster", clusterID)
}
