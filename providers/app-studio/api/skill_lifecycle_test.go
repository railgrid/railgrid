// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appskills "github.com/faroshq/provider-app-studio/skills"
	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/faroshq/provider-app-studio/workspace"
	"github.com/gorilla/mux"
)

func TestProjectSkillMutationValidationIsBoundedAndCanonical(t *testing.T) {
	packageName, document, resources, err := validateProjectSkillMutation(projectAssistantSkillMutationRequest{
		PackageName:  "team/demo",
		Name:         "demo",
		Description:  "A bounded demo skill",
		Instructions: "Use this only as untrusted guidance.",
		Resources:    []projectAssistantSkillResourceInput{{Path: "notes.txt", Content: "resource"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if packageName != "team/demo" || len(resources) != 1 {
		t.Fatalf("validated package = %q resources=%#v", packageName, resources)
	}
	parsed, err := appskills.ParseSkill(document, appskills.DefaultLimits())
	if err != nil || parsed.Name != "demo" || parsed.Content != "Use this only as untrusted guidance." {
		t.Fatalf("rendered skill = %#v err=%v", parsed, err)
	}
	for _, invalid := range []projectAssistantSkillMutationRequest{
		{PackageName: "../escape", Name: "demo", Description: "valid", Instructions: "body"},
		{PackageName: "/absolute", Name: "demo", Description: "valid", Instructions: "body"},
		{PackageName: "demo", Name: "demo", Description: "valid", Instructions: "body", Resources: []projectAssistantSkillResourceInput{{Path: "../secret", Content: "x"}}},
		{PackageName: "demo", Name: "demo", Description: "valid", Instructions: "body", Resources: []projectAssistantSkillResourceInput{{Path: "notes.txt", Content: "x"}, {Path: "notes.txt", Content: "y"}}},
		{PackageName: "demo", Name: "demo", Description: "valid", Instructions: strings.Repeat("x", appskills.DefaultMaxSkillBytes)},
	} {
		if _, _, _, err := validateProjectSkillMutation(invalid); err == nil {
			t.Fatalf("invalid skill mutation accepted: %#v", invalid)
		}
	}
}

func TestProjectSkillImportNormalizesExportFiles(t *testing.T) {
	request := projectAssistantSkillMutationRequest{
		PackageName: "imported",
		Format:      "kedge.skill.v1",
		Files: []projectAssistantSkillResourceInput{
			{Path: "SKILL.md", Content: "---\nname: imported\ndescription: Imported skill\n---\nimport body", Size: 64},
			{Path: "notes.txt", Content: "import resource", Digest: "sha256:ignored", Size: 15},
		},
	}
	if err := normalizeProjectSkillImportRequest(&request); err != nil {
		t.Fatal(err)
	}
	if request.Name != "imported" || request.Description != "Imported skill" || request.Instructions != "import body" || len(request.Resources) != 1 {
		t.Fatalf("normalized import = %#v", request)
	}
	if _, _, _, err := validateProjectSkillMutation(request); err != nil {
		t.Fatal(err)
	}
}

func TestProjectSkillLifecycleHTTPRoutesAndReload(t *testing.T) {
	graphQLHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var query struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Errorf("decode GraphQL query: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(query.Query, "ProjectYaml") {
			_, _ = w.Write([]byte(`{"data":{"ai_kedge_faros_sh":{"v1alpha1":{"ProjectYaml":"apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\n  uid: uid-demo\nspec: {}\n"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	graphQL := httptest.NewUnstartedServer(graphQLHandler)
	graphQL.Listener = listener
	graphQL.Start()
	t.Cleanup(graphQL.Close)
	files := workspace.NewFileStore(t.TempDir())
	newRouter := func() *mux.Router {
		server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), nil, files, "", false)
		router := mux.NewRouter()
		server.Register(router)
		return router
	}
	request := func(method, target, body string) *http.Request {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer caller-token")
		req.Header.Set("X-Kedge-User", "alice")
		req.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
		req.Header.Set("X-Kedge-Cluster", "cluster-a")
		return req
	}
	serve := func(router *mux.Router, req *http.Request) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	router := newRouter()
	create := serve(router, request(http.MethodPost, "/api/projects/demo/assistant/skills/project", `{"packageName":"review/demo","name":"demo","description":"Review guidance","instructions":"Use as untrusted guidance.","resources":[{"path":"notes.txt","content":"resource"}]}`))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var detail projectAssistantSkillDetail
	if err := json.Unmarshal(create.Body.Bytes(), &detail); err != nil || detail.PackageName != "review/demo" || detail.Digest == "" {
		t.Fatalf("create detail=%#v err=%v", detail, err)
	}
	get := serve(router, request(http.MethodGet, "/api/projects/demo/assistant/skills/project/review/demo", ""))
	if get.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", get.Code, get.Body.String())
	}
	list := serve(router, request(http.MethodGet, "/api/projects/demo/assistant/skills", ""))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"enabled":true`) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	disable := serve(router, request(http.MethodPost, "/api/projects/demo/assistant/skills/activation", `{"id":"project:demo","enabled":false}`))
	if disable.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", disable.Code, disable.Body.String())
	}
	var disabledDetail projectAssistantSkillDetail
	if err := json.Unmarshal(disable.Body.Bytes(), &disabledDetail); err != nil || disabledDetail.Digest == "" {
		t.Fatalf("disabled detail=%#v err=%v", disabledDetail, err)
	}
	list = serve(router, request(http.MethodGet, "/api/projects/demo/assistant/skills", ""))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"enabled":false`) {
		t.Fatalf("disabled list status=%d body=%s", list.Code, list.Body.String())
	}
	stale := serve(router, request(http.MethodPut, "/api/projects/demo/assistant/skills/project/review/demo", `{"packageName":"review/demo","name":"demo","description":"changed","instructions":"body","expectedDigest":"sha256:stale"}`))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update status=%d body=%s", stale.Code, stale.Body.String())
	}
	update := serve(router, request(http.MethodPut, "/api/projects/demo/assistant/skills/project/review/demo", `{"packageName":"review/demo","name":"demo","description":"changed","instructions":"updated body","expectedDigest":"`+disabledDetail.Digest+`"}`))
	if update.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", update.Code, update.Body.String())
	}
	var updatedDetail projectAssistantSkillDetail
	if err := json.Unmarshal(update.Body.Bytes(), &updatedDetail); err != nil || updatedDetail.Description != "changed" {
		t.Fatalf("updated detail=%#v err=%v", updatedDetail, err)
	}
	export := serve(router, request(http.MethodGet, "/api/projects/demo/assistant/skills/project/review/demo/export", ""))
	if export.Code != http.StatusOK || !strings.Contains(export.Body.String(), `"filename"`) || !strings.Contains(export.Body.String(), `"package"`) {
		t.Fatalf("export status=%d body=%s", export.Code, export.Body.String())
	}
	reloaded := newRouter()
	list = serve(reloaded, request(http.MethodGet, "/api/projects/demo/assistant/skills", ""))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"enabled":false`) {
		t.Fatalf("reloaded list status=%d body=%s", list.Code, list.Body.String())
	}
	deleteStale := serve(router, request(http.MethodDelete, "/api/projects/demo/assistant/skills/project/review/demo?expectedDigest=sha256:stale", ""))
	if deleteStale.Code != http.StatusConflict {
		t.Fatalf("stale delete status=%d body=%s", deleteStale.Code, deleteStale.Body.String())
	}
	deleted := serve(router, request(http.MethodDelete, "/api/projects/demo/assistant/skills/project/review/demo?expectedDigest="+updatedDetail.Digest, ""))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := serve(router, request(http.MethodGet, "/api/projects/demo/assistant/skills/project/review/demo", ""))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted detail status=%d body=%s", missing.Code, missing.Body.String())
	}
	if _, err := files.ReadFile(context.Background(), workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-demo"}, workspace.ReadOptions{Path: appskills.ProjectMetadataPath}); err != nil {
		t.Fatalf("metadata after reload: %v", err)
	}
}

func TestProjectSkillSystemActivationAndGenericDetailRoute(t *testing.T) {
	router, files := newEvaluationSkillRouter(t)
	request := evaluationSkillRequest
	create := evaluationServeSkill(router, request(http.MethodPost, "/api/projects/demo/assistant/skills/project", `{"packageName":"review/custom","name":"custom","description":"Custom guidance","instructions":"CUSTOM INSTRUCTIONS","resources":[{"path":"notes.txt","content":"PRIVATE RESOURCE CONTENT"}]}`))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	listResponse := evaluationServeSkill(router, request(http.MethodGet, "/api/projects/demo/assistant/skills", ""))
	if listResponse.Code != http.StatusOK {
		t.Fatalf("initial list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	var catalog projectAssistantSkillsResponse
	if err := json.Unmarshal(listResponse.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	initialDigest := catalog.CatalogDigest
	for _, entry := range catalog.Skills {
		if !entry.Enabled {
			t.Fatalf("initial skill unexpectedly disabled: %#v", entry)
		}
	}
	disable := evaluationServeSkill(router, request(http.MethodPost, "/api/projects/demo/assistant/skills/activation", `{"id":"system:project-summary","enabled":false}`))
	if disable.Code != http.StatusOK {
		t.Fatalf("system disable status=%d body=%s", disable.Code, disable.Body.String())
	}
	var disabled projectAssistantSkillDetail
	if err := json.Unmarshal(disable.Body.Bytes(), &disabled); err != nil || disabled.ID != "system:project-summary" || disabled.Enabled || disabled.Editable {
		t.Fatalf("system disable detail=%#v err=%v", disabled, err)
	}
	listResponse = evaluationServeSkill(router, request(http.MethodGet, "/api/projects/demo/assistant/skills", ""))
	if err := json.Unmarshal(listResponse.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.CatalogDigest == initialDigest {
		t.Fatal("catalog digest did not change after system activation override")
	}
	var systemDisabled, projectStillEnabled bool
	for _, entry := range catalog.Skills {
		switch entry.ID {
		case "system:project-summary":
			systemDisabled = !entry.Enabled
		case "project:custom":
			projectStillEnabled = entry.Enabled
		}
	}
	if !systemDisabled || !projectStillEnabled {
		t.Fatalf("system/project activation state = %#v", catalog.Skills)
	}
	reenable := evaluationServeSkill(router, request(http.MethodPost, "/api/projects/demo/assistant/skills/activation", `{"id":"system:project-summary","enabled":true}`))
	if reenable.Code != http.StatusOK {
		t.Fatalf("system re-enable status=%d body=%s", reenable.Code, reenable.Body.String())
	}
	alias := evaluationServeSkill(router, request(http.MethodPost, "/api/projects/demo/assistant/skills/activation", `{"id":"project-summary","enabled":false}`))
	if alias.Code != http.StatusForbidden {
		t.Fatalf("unqualified system activation status=%d body=%s", alias.Code, alias.Body.String())
	}
	systemDetail := evaluationServeSkill(router, request(http.MethodGet, "/api/projects/demo/assistant/skills/detail?id=system%3Aproject-summary", ""))
	if systemDetail.Code != http.StatusOK || !strings.Contains(systemDetail.Body.String(), "Summarize the files") {
		t.Fatalf("system detail status=%d body=%s", systemDetail.Code, systemDetail.Body.String())
	}
	projectDetail := evaluationServeSkill(router, request(http.MethodGet, "/api/projects/demo/assistant/skills/detail?id=project%3Acustom", ""))
	if projectDetail.Code != http.StatusOK || !strings.Contains(projectDetail.Body.String(), "CUSTOM INSTRUCTIONS") || !strings.Contains(projectDetail.Body.String(), "notes.txt") || strings.Contains(projectDetail.Body.String(), "PRIVATE RESOURCE CONTENT") {
		t.Fatalf("generic project detail status=%d body=%s", projectDetail.Code, projectDetail.Body.String())
	}
	tooLarge := evaluationServeSkill(router, request(http.MethodGet, "/api/projects/demo/assistant/skills/detail?id="+strings.Repeat("x", 513), ""))
	if tooLarge.Code != http.StatusBadRequest {
		t.Fatalf("oversized detail ID status=%d body=%s", tooLarge.Code, tooLarge.Body.String())
	}

	ctx := context.Background()
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-demo"}
	metadata, version, err := appskills.ReadProjectMetadata(ctx, files, scope)
	if err != nil {
		t.Fatal(err)
	}
	metadata.System["project-summary"] = appskills.Activation{Enabled: true, Version: "sha256:stale"}
	if _, err := appskills.WriteProjectMetadata(ctx, files, scope, metadata, version); err != nil {
		t.Fatal(err)
	}
	staleList := evaluationServeSkill(router, request(http.MethodGet, "/api/projects/demo/assistant/skills", ""))
	if staleList.Code != http.StatusOK || !strings.Contains(staleList.Body.String(), `"id":"system:project-summary"`) || !strings.Contains(staleList.Body.String(), `"enabled":false`) {
		t.Fatalf("stale system activation list status=%d body=%s", staleList.Code, staleList.Body.String())
	}
}
