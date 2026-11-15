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
	"encoding/base64"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	systemWorkspaceGVR = schema.GroupVersionResource{Group: "admin.railgrid.ai", Version: "v1alpha1", Resource: "systemworkspaces"}
	catalogEntryE2EGVR = schema.GroupVersionResource{Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries"}
	coreSecretGVR      = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
)

func systemWorkspaceObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admin.railgrid.ai/v1alpha1",
		"kind":       "SystemWorkspace",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"displayName": "E2E " + name},
	}}
}

// systemWorkspaceReady reports the Ready condition of a SystemWorkspace.
func systemWorkspaceReady(t *testing.T, res dynamic.ResourceInterface, name string) (status, reason, message string) {
	t.Helper()
	obj, err := res.Get(ctxWithTimeout(t, 5*time.Second), name, metav1.GetOptions{})
	if err != nil {
		return "", "", err.Error()
	}
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		cond, _ := c.(map[string]any)
		if cond["type"] == "Ready" {
			status, _ = cond["status"].(string)
			reason, _ = cond["reason"].(string)
			message, _ = cond["message"].(string)
		}
	}
	return status, reason, message
}

// TestYSystemWorkspace proves the home the hub gives a platform service that
// is not a provider: a SystemWorkspace object becomes a workspace under
// root:railgrid:system with a kubeconfig an installer can create APIExports
// with, that workspace is not a provider's (no catalog, no child workspaces,
// no reach outside itself), the hub refuses its own names and workspaces it
// did not create, and deleting the object removes what it provisioned and
// nothing else.
func TestYSystemWorkspace(t *testing.T) {
	const name = "e2e-gateway"
	const path = "root:railgrid:system:" + name
	objects := systemProvidersClient(t).Resource(systemWorkspaceGVR)
	system := kcpDynamic(t, "root:railgrid:system", adminToken)
	secrets := systemProvidersClient(t).Resource(coreSecretGVR).Namespace("default")

	createOrExists(t, objects, systemWorkspaceObject(name))
	if !waitForCondition(t, 2*time.Minute, func() (bool, string) {
		status, reason, message := systemWorkspaceReady(t, objects, name)
		return status == "True", reason + ": " + message
	}) {
		t.Fatal("SystemWorkspace never became Ready")
	}

	ws, err := system.Resource(workspaceGVR).Get(ctxWithTimeout(t, 10*time.Second), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("workspace %s: %v", path, err)
	}
	if typeName, _, _ := unstructured.NestedString(ws.Object, "spec", "type", "name"); typeName != "system-service" {
		t.Errorf("workspace type = %q, want system-service", typeName)
	}
	if got := ws.GetAnnotations()["admin.railgrid.ai/system-workspace"]; got != name {
		t.Errorf("ownership annotation = %q, want %q", got, name)
	}
	obj, err := objects.Get(ctxWithTimeout(t, 10*time.Second), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := unstructured.NestedString(obj.Object, "status", "workspacePath"); got != path {
		t.Errorf("status.workspacePath = %q, want %q", got, path)
	}

	// The kubeconfig Secret sits next to the object, under its own name.
	secret, err := secrets.Get(ctxWithTimeout(t, 10*time.Second), name+"-system-kubeconfig", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("kubeconfig Secret: %v", err)
	}
	encoded, _, _ := unstructured.NestedString(secret.Object, "data", "kubeconfig")
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 {
		t.Fatalf("kubeconfig Secret has no kubeconfig: %v", err)
	}
	kc, err := clientcmd.Load(raw)
	if err != nil {
		t.Fatalf("minted kubeconfig does not parse: %v", err)
	}
	var token string
	for _, user := range kc.AuthInfos {
		token = user.Token
	}
	if token == "" {
		t.Fatal("minted kubeconfig carries no token")
	}

	// What a service's installer does with it: schemas and an export.
	service := kcpDynamic(t, path, token)
	schemaObj := mustUnstructured(t, seedBackendSchema)
	if _, err := service.Resource(schemaGVR).Create(ctxWithTimeout(t, 10*time.Second), schemaObj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("the service credential cannot create an APIResourceSchema in its workspace: %v", err)
	}
	if _, err := service.Resource(exportGVR).Create(ctxWithTimeout(t, 10*time.Second), mustUnstructured(t, seedBackendExport), metav1.CreateOptions{}); err != nil {
		t.Fatalf("the service credential cannot create an APIExport in its workspace: %v", err)
	}

	// It is not a provider workspace: there is no CatalogEntry API to register
	// with, and no Workspace API to grow children from.
	if _, err := service.Resource(catalogEntryE2EGVR).List(ctxWithTimeout(t, 10*time.Second), metav1.ListOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("listing CatalogEntries in a system workspace: got %v, want NotFound (the API must not be bound)", err)
	}
	child := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": "child"},
	}}
	if _, err := service.Resource(workspaceGVR).Create(ctxWithTimeout(t, 10*time.Second), child, metav1.CreateOptions{}); err == nil {
		t.Error("the service credential created a child workspace")
	}
	// And the credential ends at its own workspace.
	outside := kcpDynamic(t, "root:railgrid:system:providers", token)
	if _, err := outside.Resource(coreSecretGVR).Namespace("default").List(ctxWithTimeout(t, 10*time.Second), metav1.ListOptions{}); err == nil {
		t.Error("the service credential can list Secrets in root:railgrid:system:providers")
	}

	// The hub's own names are refused.
	createOrExists(t, objects, systemWorkspaceObject("tenants"))
	if !waitForCondition(t, time.Minute, func() (bool, string) {
		status, reason, _ := systemWorkspaceReady(t, objects, "tenants")
		return status == "False" && reason == "NameReserved", status + "/" + reason
	}) {
		t.Error("a SystemWorkspace named after the hub's own workspace was not refused")
	}
	if err := objects.Delete(ctxWithTimeout(t, 10*time.Second), "tenants", metav1.DeleteOptions{}); err != nil {
		t.Errorf("deleting the refused SystemWorkspace: %v", err)
	}

	// A workspace somebody else created is neither adopted nor, when the
	// SystemWorkspace goes, deleted.
	const foreign = "e2e-handmade"
	createOrExists(t, system.Resource(workspaceGVR), &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": foreign},
		"spec":       map[string]any{"type": map[string]any{"name": "universal", "path": "root"}},
	}})
	createOrExists(t, objects, systemWorkspaceObject(foreign))
	if !waitForCondition(t, 2*time.Minute, func() (bool, string) {
		status, reason, _ := systemWorkspaceReady(t, objects, foreign)
		return status == "False" && reason == "WorkspaceNotOwned", status + "/" + reason
	}) {
		t.Error("a SystemWorkspace named after a hand-made workspace was not refused")
	}
	if _, err := secrets.Get(ctxWithTimeout(t, 10*time.Second), foreign+"-system-kubeconfig", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("a kubeconfig was minted for a workspace the hub does not own: %v", err)
	}
	if err := objects.Delete(ctxWithTimeout(t, 10*time.Second), foreign, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if !waitForCondition(t, time.Minute, func() (bool, string) {
		_, err := objects.Get(ctxWithTimeout(t, 5*time.Second), foreign, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "SystemWorkspace still present"
	}) {
		t.Fatal("the refused SystemWorkspace was never removed")
	}
	if _, err := system.Resource(workspaceGVR).Get(ctxWithTimeout(t, 10*time.Second), foreign, metav1.GetOptions{}); err != nil {
		t.Errorf("the hand-made workspace did not survive its SystemWorkspace being deleted: %v", err)
	}
	_ = system.Resource(workspaceGVR).Delete(ctxWithTimeout(t, 10*time.Second), foreign, metav1.DeleteOptions{})

	// Deleting the object removes the workspace and the Secret.
	if err := objects.Delete(ctxWithTimeout(t, 10*time.Second), name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if !waitForCondition(t, 2*time.Minute, func() (bool, string) {
		_, wsErr := system.Resource(workspaceGVR).Get(ctxWithTimeout(t, 5*time.Second), name, metav1.GetOptions{})
		_, secretErr := secrets.Get(ctxWithTimeout(t, 5*time.Second), name+"-system-kubeconfig", metav1.GetOptions{})
		_, objErr := objects.Get(ctxWithTimeout(t, 5*time.Second), name, metav1.GetOptions{})
		done := apierrors.IsNotFound(wsErr) && apierrors.IsNotFound(secretErr) && apierrors.IsNotFound(objErr)
		return done, "workspace, Secret or object still present"
	}) {
		t.Error("deleting the SystemWorkspace did not remove its workspace and kubeconfig Secret")
	}
}
