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

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

const switchboardSeedSpec = `
bindings:
- name: switchboard
  export:
    path: root:railgrid:system:switchboard
    name: switchboard.railgrid.ai
  tenantAccess: ReadOnly
  permissionClaims:
  - group: ""
    resource: secrets
    verbs: ["get", "list", "watch", "create", "update", "delete"]
    selector:
      matchExpressions:
      - key: switchboard.railgrid.ai/request
        operator: Exists
objects:
- resource: requests
  manifest:
    apiVersion: switchboard.railgrid.ai/v1alpha1
    kind: Request
    metadata: {name: free, namespace: default}
    spec: {policy: free}
- resource: modelproviderconnections
  whenBound:
  - path: root:railgrid:providers:agents
    name: agents.railgrid.ai
  manifest:
    apiVersion: agents.railgrid.ai/v1alpha1
    kind: ModelProviderConnection
    metadata: {name: railgrid, namespace: default}
`

func platformService(t *testing.T, objs ...runtime.Object) (*Service, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	cl := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			systemWorkspaceGVR: "SystemWorkspaceList",
			workspaceSeedGVR:   "WorkspaceSeedList",
		}, objs...)
	return &Service{kcpConfig: &rest.Config{Host: "https://kcp.invalid"}, systemProvidersDynamic: cl}, cl
}

func systemWorkspaceObject(name string, ready bool) *unstructured.Unstructured {
	status := "False"
	if ready {
		status = "True"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admin.railgrid.ai/v1alpha1",
		"kind":       "SystemWorkspace",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"displayName": strings.ToUpper(name)},
		"status": map[string]any{
			"workspaceCluster": "cluster-" + name,
			"conditions": []any{map[string]any{
				"type": "Ready", "status": status, "reason": "Provisioned", "message": "ok",
				"lastTransitionTime": "2026-11-13T00:00:00Z",
			}},
		},
	}}
}

func mustSaveSeed(t *testing.T, svc *Service, name, spec string) {
	t.Helper()
	if _, err := svc.SaveWorkspaceSeed(context.Background(), name, spec, true); err != nil {
		t.Fatalf("saving seed %s: %v", name, err)
	}
}

func TestParseWorkspaceSeedSpec(t *testing.T) {
	spec, err := ParseWorkspaceSeedSpec(switchboardSeedSpec)
	if err != nil {
		t.Fatalf("a valid spec was refused: %v", err)
	}
	if len(spec.Bindings) != 1 || len(spec.Objects) != 2 || spec.Bindings[0].PermissionClaims[0].Selector == nil {
		t.Errorf("spec lost content: %+v", spec)
	}

	refused := map[string]string{
		"empty":                    "  \n",
		"not yaml":                 "bindings: [",
		"nothing declared":         "bindings: []",
		"misspelt field":           strings.Replace(switchboardSeedSpec, "tenantAccess:", "tenantAcess:", 1),
		"the hub's own binding":    strings.Replace(switchboardSeedSpec, "- name: switchboard", "- name: railgrid", 1),
		"every secret, unselected": "bindings:\n- name: x\n  export: {path: root:railgrid:system:x, name: x}\n  permissionClaims:\n  - resource: secrets\n    verbs: [get]\n",
		"an RBAC object":           "objects:\n- resource: clusterroles\n  manifest: {apiVersion: rbac.authorization.k8s.io/v1, kind: ClusterRole, metadata: {name: x}}\n",
	}
	for name, in := range refused {
		if _, err := ParseWorkspaceSeedSpec(in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: got %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestSaveWorkspaceSeedCreatesThenReplaces(t *testing.T) {
	svc, cl := platformService(t)
	ctx := context.Background()

	if _, err := svc.SaveWorkspaceSeed(ctx, "switchboard", switchboardSeedSpec, false); !apierrors.IsNotFound(err) {
		t.Fatalf("updating a missing seed: got %v, want NotFound", err)
	}
	info, err := svc.SaveWorkspaceSeed(ctx, "switchboard", switchboardSeedSpec, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Bindings) != 1 || info.Bindings[0].TenantAccess != "ReadOnly" || info.Bindings[0].Claims != 1 {
		t.Errorf("bindings summary = %+v", info.Bindings)
	}
	if len(info.Objects) != 2 || info.Objects[0].Kind != "Request" || info.Objects[0].Gated || !info.Objects[1].Gated {
		t.Errorf("objects summary = %+v", info.Objects)
	}
	if _, err := svc.SaveWorkspaceSeed(ctx, "switchboard", switchboardSeedSpec, true); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating an existing seed: got %v, want AlreadyExists", err)
	}

	// The rendered spec is what the editor shows; saving it back unchanged
	// must be accepted and must keep the manifests as objects.
	if _, err := svc.SaveWorkspaceSeed(ctx, "switchboard", info.Spec, false); err != nil {
		t.Fatalf("saving the rendered spec back: %v", err)
	}
	stored, err := cl.Resource(workspaceSeedGVR).Get(ctx, "switchboard", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	objects, _, _ := unstructured.NestedSlice(stored.Object, "spec", "objects")
	manifest, _ := objects[0].(map[string]any)["manifest"].(map[string]any)
	if manifest["kind"] != "Request" {
		t.Errorf("stored manifest = %#v, want an object with kind Request", objects[0].(map[string]any)["manifest"])
	}

	if _, err := svc.SaveWorkspaceSeed(ctx, "Not A Name", switchboardSeedSpec, true); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a bad name: got %v, want ErrInvalidInput", err)
	}
}

func TestSeedAcceptedIsOnlyReportedForTheCurrentGeneration(t *testing.T) {
	svc, cl := platformService(t)
	ctx := context.Background()
	mustSaveSeed(t, svc, "switchboard", switchboardSeedSpec)

	setCondition := func(generation, observed int64) {
		obj, err := cl.Resource(workspaceSeedGVR).Get(ctx, "switchboard", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		obj.SetGeneration(generation)
		obj.Object["status"] = map[string]any{"conditions": []any{map[string]any{
			"type": "Accepted", "status": "True", "reason": "Valid", "message": "applied",
			"observedGeneration": observed, "lastTransitionTime": "2026-11-13T00:00:00Z",
		}}}
		if _, err := cl.Resource(workspaceSeedGVR).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	accepted := func() string {
		seeds, err := svc.ListWorkspaceSeeds(ctx)
		if err != nil || len(seeds) != 1 {
			t.Fatalf("list: %v %v", seeds, err)
		}
		return seeds[0].Accepted
	}

	setCondition(2, 2)
	if got := accepted(); got != "True" {
		t.Errorf("current generation: accepted = %q, want True", got)
	}
	setCondition(3, 2)
	if got := accepted(); got != "" {
		t.Errorf("stale condition: accepted = %q, want empty (pending)", got)
	}
}

func TestSystemWorkspacesListAndCreate(t *testing.T) {
	svc, _ := platformService(t, systemWorkspaceObject("switchboard", true), systemWorkspaceObject("idle", false))
	ctx := context.Background()
	mustSaveSeed(t, svc, "switchboard", switchboardSeedSpec)

	list, err := svc.ListSystemWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "idle" || list[1].Name != "switchboard" {
		t.Fatalf("list = %+v", list)
	}
	sb := list[1]
	if !sb.Ready || sb.WorkspacePath != "root:railgrid:system:switchboard" || sb.DisplayName != "SWITCHBOARD" {
		t.Errorf("switchboard = %+v", sb)
	}
	if len(sb.Seeds) != 1 || sb.Seeds[0] != "switchboard" || len(list[0].Seeds) != 0 {
		t.Errorf("seed references: switchboard=%v idle=%v", sb.Seeds, list[0].Seeds)
	}

	for _, name := range []string{"controllers", "providers", "tenants", "Has Spaces", "a:b", ""} {
		if err := svc.CreateSystemWorkspace(ctx, name, ""); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("create %q: got %v, want ErrInvalidInput", name, err)
		}
	}
	if err := svc.CreateSystemWorkspace(ctx, "gateway", "Gateway"); err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateSystemWorkspace(ctx, "gateway", "Gateway"); err != nil {
		t.Errorf("create is not idempotent: %v", err)
	}
}

func TestDeleteSystemWorkspaceIsRefusedWhileASeedReferencesIt(t *testing.T) {
	svc, cl := platformService(t, systemWorkspaceObject("switchboard", true), systemWorkspaceObject("idle", true))
	ctx := context.Background()
	mustSaveSeed(t, svc, "switchboard", switchboardSeedSpec)

	err := svc.DeleteSystemWorkspace(ctx, "switchboard")
	if !errors.Is(err, ErrSystemWorkspaceInUse) || !strings.Contains(err.Error(), "switchboard") {
		t.Fatalf("got %v, want ErrSystemWorkspaceInUse naming the seed", err)
	}
	if _, err := cl.Resource(systemWorkspaceGVR).Get(ctx, "switchboard", metav1.GetOptions{}); err != nil {
		t.Fatalf("the referenced SystemWorkspace was deleted: %v", err)
	}

	if err := svc.DeleteSystemWorkspace(ctx, "idle"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteWorkspaceSeed(ctx, "switchboard"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSystemWorkspace(ctx, "switchboard"); err != nil {
		t.Fatalf("delete after the seed is gone: %v", err)
	}
	if err := svc.DeleteSystemWorkspace(ctx, "switchboard"); err != nil {
		t.Errorf("delete is not idempotent: %v", err)
	}
}

func TestSeedReferencesCountsWhenBoundGates(t *testing.T) {
	svc, _ := platformService(t)
	mustSaveSeed(t, svc, "switchboard", switchboardSeedSpec)
	seeds, err := svc.listSeedObjects(context.Background(), svc.systemProvidersDynamic)
	if err != nil {
		t.Fatal(err)
	}
	if got := seedsReferencing(seeds, "root:railgrid:providers:agents"); len(got) != 1 {
		t.Errorf("a whenBound reference was not counted: %v", got)
	}
	if got := seedsReferencing(seeds, "root:railgrid:system:other"); len(got) != 0 {
		t.Errorf("an unrelated path was counted: %v", got)
	}
}

func TestPlatformServiceRoutesMapErrorsToStatusCodes(t *testing.T) {
	svc, _ := platformService(t, systemWorkspaceObject("switchboard", true))
	mustSaveSeed(t, svc, "switchboard", switchboardSeedSpec)
	router := mux.NewRouter()
	(&Handler{svc: svc}).registerPlatformServices(router)

	body := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"list system workspaces", http.MethodGet, "/system-workspaces", "", http.StatusOK},
		{"create with a reserved name", http.MethodPost, "/system-workspaces", body(map[string]string{"name": "tenants"}), http.StatusBadRequest},
		{"create without a name", http.MethodPost, "/system-workspaces", "{}", http.StatusBadRequest},
		{"create", http.MethodPost, "/system-workspaces", body(map[string]string{"name": "gateway"}), http.StatusCreated},
		{"delete while a seed references it", http.MethodDelete, "/system-workspaces/switchboard", "", http.StatusConflict},
		{"kubeconfig with an unknown server mode", http.MethodGet, "/system-workspaces/switchboard/kubeconfig?server=sideways", "", http.StatusBadRequest},
		{"rotate is POST only", http.MethodGet, "/system-workspaces/switchboard/credentials/rotate", "", http.StatusMethodNotAllowed},
		{"rotate a missing one", http.MethodPost, "/system-workspaces/nope/credentials/rotate", "", http.StatusNotFound},
		{"list seeds", http.MethodGet, "/workspace-seeds", "", http.StatusOK},
		{"create a seed the hub would refuse", http.MethodPost, "/workspace-seeds", body(map[string]string{"name": "bad", "spec": "bindings:\n- name: railgrid\n  export: {path: root:x, name: y}\n"}), http.StatusBadRequest},
		{"create a seed that exists", http.MethodPost, "/workspace-seeds", body(map[string]string{"name": "switchboard", "spec": switchboardSeedSpec}), http.StatusConflict},
		{"create a seed without a name", http.MethodPost, "/workspace-seeds", body(map[string]string{"spec": switchboardSeedSpec}), http.StatusBadRequest},
		{"update a missing seed", http.MethodPut, "/workspace-seeds/nope", body(map[string]string{"spec": switchboardSeedSpec}), http.StatusNotFound},
		{"update", http.MethodPut, "/workspace-seeds/switchboard", body(map[string]string{"spec": switchboardSeedSpec}), http.StatusOK},
		{"delete a seed", http.MethodDelete, "/workspace-seeds/switchboard", "", http.StatusNoContent},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if rec.Code != tc.want {
			t.Errorf("%s: %s %s = %d, want %d (%s)", tc.name, tc.method, tc.path, rec.Code, tc.want, strings.TrimSpace(rec.Body.String()))
		}
	}
}
