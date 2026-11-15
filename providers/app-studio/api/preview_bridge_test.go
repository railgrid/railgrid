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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func TestPreviewBridgeToolIsRemoved(t *testing.T) {
	if projectAssistantLocalToolRegistry(&Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}).Has("get_preview_bridge_logs") {
		t.Fatal("legacy preview bridge tool is still registered")
	}
}

func TestProjectAssistantMetricsRouteExposesSkillMetrics(t *testing.T) {
	router := mux.NewRouter()
	(&Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}).Register(router)
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

// The preview bridge is a browser-only MessagePort between the portal and the
// injected dev-server script, trusted by exact origin. App Studio serves no
// session or capability endpoint for it.
func TestPreviewBridgeHasNoServerSessionRoute(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}
	router := mux.NewRouter()
	server.Register(router)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/preview-bridge/sessions", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("response = %d %s, want no preview-bridge session route", response.Code, response.Body.String())
	}
	for _, entry := range projectVerbs {
		if entry.verb == "preview-bridge-sessions" {
			t.Fatal("preview-bridge-sessions verb is still served")
		}
	}
}
