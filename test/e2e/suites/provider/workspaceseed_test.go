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

package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const (
	// seedBackendWorkspace is a platform service that is not a provider: a
	// workspace under root:railgrid:system holding one APIExport, with no
	// Provider and no CatalogEntry.
	seedBackendParent    = "root:railgrid:system"
	seedBackendName      = "seedtest"
	seedBackendWorkspace = seedBackendParent + ":" + seedBackendName
	seedExportName       = "seedtest.railgrid.ai"
	seedOwnedLabel       = "seedtest.railgrid.ai/owned"
)

var (
	workspaceSeedGVR = schema.GroupVersionResource{Group: "admin.railgrid.ai", Version: "v1alpha1", Resource: "workspaceseeds"}
	widgetGVR        = schema.GroupVersionResource{Group: "seedtest.railgrid.ai", Version: "v1alpha1", Resource: "widgets"}
	workspaceGVR     = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}
	schemaGVR        = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
	exportGVR        = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	endpointSliceGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices"}
)

const seedBackendSchema = `
apiVersion: apis.kcp.io/v1alpha1
kind: APIResourceSchema
metadata:
  name: v1.widgets.seedtest.railgrid.ai
spec:
  group: seedtest.railgrid.ai
  names:
    kind: Widget
    listKind: WidgetList
    plural: widgets
    singular: widget
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    storage: true
    schema:
      type: object
      x-kubernetes-preserve-unknown-fields: true
    subresources:
      status: {}
`

const seedBackendExport = `
apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: seedtest.railgrid.ai
spec:
  resources:
  - group: seedtest.railgrid.ai
    name: widgets
    schema: v1.widgets.seedtest.railgrid.ai
    storage:
      crd: {}
  permissionClaims:
  - group: ""
    resource: secrets
    verbs: ["get", "list", "watch", "create", "update", "delete"]
`

const seedManifest = `
apiVersion: admin.railgrid.ai/v1alpha1
kind: WorkspaceSeed
metadata:
  name: seedtest
spec:
  bindings:
  - name: seedtest
    export:
      path: root:railgrid:system:seedtest
      name: seedtest.railgrid.ai
    tenantAccess: ReadOnly
    permissionClaims:
    - group: ""
      resource: secrets
      verbs: ["get", "list", "watch", "create", "update", "delete"]
      selector:
        matchExpressions:
        - key: seedtest.railgrid.ai/owned
          operator: Exists
  objects:
  - resource: widgets
    manifest:
      apiVersion: seedtest.railgrid.ai/v1alpha1
      kind: Widget
      metadata:
        name: seeded
        namespace: default
      spec:
        size: 3
  - resource: widgets
    whenBound:
    - path: root:railgrid:providers:nobody-enabled-this
      name: nothing.railgrid.ai
    manifest:
      apiVersion: seedtest.railgrid.ai/v1alpha1
      kind: Widget
      metadata:
        name: gated
        namespace: default
`

func mustUnstructured(t *testing.T, manifest string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(manifest), &u.Object); err != nil {
		t.Fatalf("decoding manifest: %v", err)
	}
	return u
}

func createOrExists(t *testing.T, res dynamic.ResourceInterface, obj *unstructured.Unstructured) {
	t.Helper()
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		_, err := res.Create(ctxWithTimeout(t, 10*time.Second), obj.DeepCopy(), metav1.CreateOptions{})
		if err == nil || apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return false, err.Error()
	}) {
		t.Fatalf("creating %s %s never succeeded", obj.GetKind(), obj.GetName())
	}
}

// TestZWorkspaceSeed proves the path a platform service that is not a
// provider takes into every tenant workspace: a WorkspaceSeed binds its export
// into a workspace that already exists, accepts exactly the claims it lists
// (scoped by a label that must merely exist, as Switchboard's is), creates the
// seeded object, holds the tenant to reading it, and leaves an object gated on
// an unbound export alone.
//
// It runs last (Z): a seed reaches every tenant workspace of the suite.
func TestZWorkspaceSeed(t *testing.T) {
	tenantWS := loginStaticTokenAndGetCluster(t)
	tenant := kcpDynamic(t, tenantWS, staticToken)
	tenantAsAdmin := kcpDynamic(t, tenantWS, adminToken)
	system := kcpDynamic(t, seedBackendParent, adminToken)
	backend := kcpDynamic(t, seedBackendWorkspace, adminToken)
	seeds := systemProvidersClient(t).Resource(workspaceSeedGVR)

	// The backend service's workspace and export.
	createOrExists(t, system.Resource(workspaceGVR), &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": seedBackendName},
		"spec":       map[string]any{"type": map[string]any{"name": "universal", "path": "root"}},
	}})
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		ws, err := system.Resource(workspaceGVR).Get(ctxWithTimeout(t, 5*time.Second), seedBackendName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(ws.Object, "status", "phase")
		return phase == "Ready", "phase=" + phase
	}) {
		t.Fatal("backend workspace never became Ready")
	}
	createOrExists(t, backend.Resource(schemaGVR), mustUnstructured(t, seedBackendSchema))
	createOrExists(t, backend.Resource(exportGVR), mustUnstructured(t, seedBackendExport))

	// A seed the hub must refuse: it would hand the backend every Secret of
	// every tenant workspace.
	greedy := mustUnstructured(t, seedManifest)
	greedy.SetName("seedtest-greedy")
	bindings, _, _ := unstructured.NestedSlice(greedy.Object, "spec", "bindings")
	claims, _, _ := unstructured.NestedSlice(bindings[0].(map[string]any), "permissionClaims")
	delete(claims[0].(map[string]any), "selector")
	bindings[0].(map[string]any)["permissionClaims"] = claims
	bindings[0].(map[string]any)["name"] = "seedtest-greedy"
	_ = unstructured.SetNestedSlice(greedy.Object, bindings, "spec", "bindings")
	createOrExists(t, seeds, greedy)
	t.Cleanup(func() { _ = seeds.Delete(context.Background(), "seedtest-greedy", metav1.DeleteOptions{}) })
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		status, message := seedAccepted(t, seeds, "seedtest-greedy")
		return status == "False" && strings.Contains(message, "must carry a selector"), status + ": " + message
	}) {
		t.Fatal("a seed claiming every Secret was not refused")
	}

	createOrExists(t, seeds, mustUnstructured(t, seedManifest))
	t.Cleanup(func() {
		_ = seeds.Delete(context.Background(), "seedtest", metav1.DeleteOptions{})
		// Deleting a seed removes nothing; take the binding out by hand so a
		// reused data directory starts clean.
		_ = tenantAsAdmin.Resource(apiBindingGVR).Delete(context.Background(), "seedtest", metav1.DeleteOptions{})
	})
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		status, message := seedAccepted(t, seeds, "seedtest")
		return status == "True", status + ": " + message
	}) {
		t.Fatal("the seed was never accepted")
	}

	// The existing workspace is bound without anyone enabling anything.
	var binding *unstructured.Unstructured
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		got, err := tenantAsAdmin.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 5*time.Second), "seedtest", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		binding = got
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		claimsValid := ""
		conditions, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
		for _, raw := range conditions {
			if condition, _ := raw.(map[string]any); condition["type"] == "PermissionClaimsValid" {
				claimsValid, _ = condition["status"].(string)
			}
		}
		return phase == "Bound" && claimsValid == "True", fmt.Sprintf("phase=%s PermissionClaimsValid=%s", phase, claimsValid)
	}) {
		t.Fatal("the seeded APIBinding never reached Bound with valid permission claims")
	}
	if got := binding.GetAnnotations(); got["admin.railgrid.ai/workspace-seed"] != "seedtest" || got["admin.railgrid.ai/tenant-access"] != "ReadOnly" {
		t.Fatalf("binding annotations = %v", got)
	}
	if _, err := tenantAsAdmin.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 5*time.Second), "seedtest-greedy", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the refused seed's binding: err = %v, want NotFound", err)
	}

	// The seeded object is there, written by the hub's field manager.
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		got, err := tenantAsAdmin.Resource(widgetGVR).Namespace("default").Get(ctxWithTimeout(t, 5*time.Second), "seeded", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		for _, entry := range got.GetManagedFields() {
			if entry.Manager == "railgrid-workspace-seed" && entry.Operation == metav1.ManagedFieldsOperationApply {
				return true, ""
			}
		}
		return false, fmt.Sprintf("managedFields = %v", got.GetManagedFields())
	}) {
		t.Fatal("the seeded Widget never appeared")
	}
	if _, err := tenantAsAdmin.Resource(widgetGVR).Namespace("default").Get(ctxWithTimeout(t, 5*time.Second), "gated", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the Widget gated on an unbound export: err = %v, want NotFound", err)
	}

	// The tenant reads it and can do nothing else to it. The member role
	// follows the binding asynchronously, and it never passes through a state
	// with more than read access, so the write checks need no settling.
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		_, err := tenant.Resource(widgetGVR).Namespace("default").Get(ctxWithTimeout(t, 5*time.Second), "seeded", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}) {
		t.Fatal("the tenant never got to read the seeded Widget")
	}
	widgets := tenant.Resource(widgetGVR).Namespace("default")
	second := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "seedtest.railgrid.ai/v1alpha1", "kind": "Widget",
		"metadata": map[string]any{"name": "second", "namespace": "default"},
	}}
	if _, err := widgets.Create(ctxWithTimeout(t, 5*time.Second), second, metav1.CreateOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("tenant create Widget: err = %v, want Forbidden", err)
	}
	if err := widgets.Delete(ctxWithTimeout(t, 5*time.Second), "seeded", metav1.DeleteOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("tenant delete Widget: err = %v, want Forbidden", err)
	}
	if err := widgets.DeleteCollection(ctxWithTimeout(t, 5*time.Second), metav1.DeleteOptions{}, metav1.ListOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("tenant deletecollection Widgets: err = %v, want Forbidden", err)
	}
	seeded, err := widgets.Get(ctxWithTimeout(t, 5*time.Second), "seeded", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(seeded.Object, int64(4), "spec", "size")
	if _, err := widgets.Update(ctxWithTimeout(t, 5*time.Second), seeded, metav1.UpdateOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("tenant update Widget: err = %v, want Forbidden", err)
	}
	if err := tenant.Resource(apiBindingGVR).Delete(ctxWithTimeout(t, 5*time.Second), "seedtest", metav1.DeleteOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("tenant delete the seeded APIBinding: err = %v, want Forbidden", err)
	}
	// What the tenant may do with the rest of the workspace is untouched.
	if _, err := tenant.Resource(secretGVR).Namespace("default").List(ctxWithTimeout(t, 5*time.Second), metav1.ListOptions{}); err != nil {
		t.Errorf("tenant list Secrets: %v", err)
	}

	// The backend's side: through its export's virtual workspace it writes a
	// Secret carrying the label and is refused one without it, and sees only
	// the labelled one.
	var vwURL string
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		slice, err := backend.Resource(endpointSliceGVR).Get(ctxWithTimeout(t, 5*time.Second), seedExportName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		endpoints, _, _ := unstructured.NestedSlice(slice.Object, "status", "endpoints")
		for _, raw := range endpoints {
			if endpoint, _ := raw.(map[string]any); endpoint["url"] != nil {
				vwURL, _ = endpoint["url"].(string)
			}
		}
		return vwURL != "", "no endpoint published yet"
	}) {
		t.Fatal("the export's endpoint slice never published a URL")
	}
	vw, err := dynamic.NewForConfig(&rest.Config{
		Host:            strings.TrimSuffix(vwURL, "/") + "/clusters/" + tenantWS,
		BearerToken:     adminToken,
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	vwSecrets := vw.Resource(secretGVR).Namespace("default")
	secret := func(name string, labels map[string]any) *unstructured.Unstructured {
		meta := map[string]any{"name": name, "namespace": "default"}
		if labels != nil {
			meta["labels"] = labels
		}
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": meta, "stringData": map[string]any{"k": "v"}}}
	}
	t.Cleanup(func() {
		for _, name := range []string{"seedtest-owned", "seedtest-plain", "seedtest-tenant"} {
			_ = tenantAsAdmin.Resource(secretGVR).Namespace("default").Delete(context.Background(), name, metav1.DeleteOptions{})
		}
	})
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		_, err := vwSecrets.Create(ctxWithTimeout(t, 5*time.Second), secret("seedtest-owned", map[string]any{seedOwnedLabel: "yes"}), metav1.CreateOptions{})
		if err == nil || apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return false, err.Error()
	}) {
		t.Fatal("the backend could not write a labelled Secret through its virtual workspace")
	}
	if _, err := vwSecrets.Create(ctxWithTimeout(t, 5*time.Second), secret("seedtest-plain", nil), metav1.CreateOptions{}); err == nil {
		t.Error("the backend wrote a Secret without the claim's label")
	} else {
		t.Logf("unlabelled Secret through the virtual workspace: %v", err)
	}
	if _, err := tenant.Resource(secretGVR).Namespace("default").Create(ctxWithTimeout(t, 5*time.Second), secret("seedtest-tenant", nil), metav1.CreateOptions{}); err != nil {
		t.Fatalf("tenant create Secret: %v", err)
	}
	visible, err := vwSecrets.List(ctxWithTimeout(t, 5*time.Second), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("backend list Secrets: %v", err)
	}
	for i := range visible.Items {
		if _, labelled := visible.Items[i].GetLabels()[seedOwnedLabel]; !labelled {
			t.Errorf("the backend sees Secret %q, which does not carry the claim's label", visible.Items[i].GetName())
		}
	}
	if len(visible.Items) == 0 {
		t.Error("the backend does not see its own labelled Secret")
	}
}

func seedAccepted(t *testing.T, seeds dynamic.ResourceInterface, name string) (status, message string) {
	t.Helper()
	seed, err := seeds.Get(ctxWithTimeout(t, 5*time.Second), name, metav1.GetOptions{})
	if err != nil {
		return "", err.Error()
	}
	conditions, _, _ := unstructured.NestedSlice(seed.Object, "status", "conditions")
	for _, raw := range conditions {
		if condition, _ := raw.(map[string]any); condition["type"] == "Accepted" {
			status, _ = condition["status"].(string)
			message, _ = condition["message"].(string)
		}
	}
	return status, message
}
