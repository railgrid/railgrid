/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package template

// Reconciler unit tests using the in-memory client.Fake (for
// Template + status patches) + dynamic.Fake (for CRDs +
// APIResourceSchema + APIExport). The split is deliberate:
//
//   - The Reconciler reads + patches Template CRs through a typed
//     client (mgr.GetClient()), so the test wires that path through
//     controller-runtime's fake builder with the infrastructure
//     scheme registered.
//
//   - Everything else the controller touches (per-template CRD,
//     APIResourceSchema, APIExport) goes through r.Dynamic. The
//     dynamic fake supports Create + Get + Update on those resources
//     once we register a stub APIExport (so ensureAPIExportEntry's
//     Get succeeds).
//
// Coverage:
//   * happy path — Template reaches Ready=True; per-template CRD
//     written; APIResourceSchema minted; APIExport.spec.resources
//     contains a matching entry
//   * delete  — APIExport entry removed; per-template CRD deleted;
//     finalizer dropped
//   * backend missing — Template's Ready condition reports
//     BackendNotFound, no CRD created

import (
	"context"
	"encoding/json"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/backend"
	"github.com/faroshq/provider-infrastructure/backend/stub"
)

// newTestReconciler wires up the two fakes + a backend registry
// containing only the stub. The dynamic fake is seeded with an empty
// APIExport so ensureAPIExportEntry's Get succeeds — the controller
// can't materialize the APIExport itself (the hub's catalog
// controller does that in prod).
func newTestReconciler(t *testing.T, initial ...client.Object) (*Reconciler, *dynamicfake.FakeDynamicClient, *stub.Backend) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := infrav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	c := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&infrav1alpha1.Template{}).
		WithObjects(initial...).
		Build()

	// The dynamic fake needs every GVR pre-mapped via a scheme that
	// understands what plural→list-kind to use. Building one in line
	// keeps the test self-contained; the production code path uses
	// the apiserver's discovery which doesn't need this.
	dynScheme := runtime.NewScheme()
	dynScheme.AddKnownTypeWithName(crdGVR.GroupVersion().WithKind("CustomResourceDefinitionList"), &unstructured.UnstructuredList{})
	dynScheme.AddKnownTypeWithName(apiResourceSchemaGVR.GroupVersion().WithKind("APIResourceSchemaList"), &unstructured.UnstructuredList{})
	dynScheme.AddKnownTypeWithName(apiExportGVR.GroupVersion().WithKind("APIExportList"), &unstructured.UnstructuredList{})

	exportObj := &unstructured.Unstructured{}
	exportObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   apiExportGVR.Group,
		Version: apiExportGVR.Version,
		Kind:    "APIExport",
	})
	exportObj.SetName(APIExportName)
	dyn := dynamicfake.NewSimpleDynamicClient(dynScheme, exportObj)

	reg := backend.NewRegistry()
	stb := stub.New()
	if err := reg.Register(stb); err != nil {
		t.Fatalf("register stub: %v", err)
	}

	r := &Reconciler{
		Client:   c,
		Dynamic:  dyn,
		Backends: reg,
	}
	return r, dyn, stb
}

// newTestTemplate returns a minimal Template with spec.backend=stub
// and a trivial schema. Test cases override fields as needed.
func newTestTemplate(t *testing.T, name string) *infrav1alpha1.Template {
	t.Helper()
	schemaRaw, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
		},
	})
	if err != nil {
		t.Fatalf("marshal test schema: %v", err)
	}
	return &infrav1alpha1.Template{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: infrav1alpha1.TemplateSpec{
			Version: "0.1.0",
			Backend: stub.Name,
			InstanceCRD: infrav1alpha1.TemplateInstanceCRD{
				Group:    infrav1alpha1.GroupName,
				Version:  "v1alpha1",
				Resource: name + "s", // crude pluralize; fine for test names
				Kind:     "Test" + capitalize(name),
			},
			Schema: &runtime.RawExtension{Raw: schemaRaw},
		},
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-32) + s[1:]
}

// reconcileUntilSettled drives Reconcile until two consecutive calls
// don't change anything observable, or the cap is hit. Useful for
// tests that go through the "add finalizer → requeue → real work"
// progression the controller designs in. Capped at 5 iterations.
func reconcileUntilSettled(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	for range 5 {
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
		if err != nil {
			// The happy path may emit errors mid-progression (e.g.
			// requeue after AddFinalizer); the test exits on a
			// failed assertion if final state isn't right.
			continue
		}
	}
}

func TestReconcileHappyPath(t *testing.T) {
	tmpl := newTestTemplate(t, "redis")
	r, dyn, stb := newTestReconciler(t, tmpl)

	reconcileUntilSettled(t, r, "redis")

	// Template should be Ready=True and the backend should have been
	// called exactly once with the right name.
	var got infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "redis"}, &got); err != nil {
		t.Fatalf("get template: %v", err)
	}
	if cond := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected Ready=True; conditions=%v", got.Status.Conditions)
	}
	if !got.Status.Registered.CRDEstablished {
		t.Fatalf("expected CRDEstablished=true")
	}
	if !got.Status.Registered.SchemaInAPIExport {
		t.Fatalf("expected SchemaInAPIExport=true")
	}
	if got.Status.Backend.Name != stub.Name {
		t.Fatalf("expected backend=%q in status; got %q", stub.Name, got.Status.Backend.Name)
	}

	// SetupTemplate is documented as idempotent and called per
	// reconcile pass, so we only assert that it was called at least
	// once with the right name — exact count is an implementation
	// detail of how many requeues the controller does.
	if len(stb.SeenSetups) < 1 || stb.SeenSetups[0] != "redis" {
		t.Fatalf("expected at least one stub SetupTemplate call for redis; got %v", stb.SeenSetups)
	}

	// Per-template CRD must be in the dynamic fake.
	crdName := perTemplateCRDName(&got)
	_, err := dyn.Resource(crdGVR).Get(context.Background(), crdName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("per-template CRD %q not created: %v", crdName, err)
	}

	// APIExport.spec.resources must have one entry pointing at the
	// minted APIResourceSchema.
	export, err := dyn.Resource(apiExportGVR).Get(context.Background(), APIExportName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get APIExport: %v", err)
	}
	resources, err := getAPIExportResources(export)
	if err != nil {
		t.Fatalf("decode resources: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected one resource entry; got %d (%v)", len(resources), resources)
	}
	if resources[0].Name != "rediss" || resources[0].Group != infrav1alpha1.GroupName {
		t.Fatalf("resource entry name/group wrong: %+v", resources[0])
	}
	if resources[0].Schema == "" {
		t.Fatalf("resource entry missing schema name")
	}
}

func TestReconcileBackendNotFound(t *testing.T) {
	tmpl := newTestTemplate(t, "missing")
	tmpl.Spec.Backend = "does-not-exist"
	r, dyn, _ := newTestReconciler(t, tmpl)

	reconcileUntilSettled(t, r, "missing")

	var got infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "missing"}, &got); err != nil {
		t.Fatalf("get template: %v", err)
	}
	cond := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != infrav1alpha1.ReasonBackendNotFound {
		t.Fatalf("expected Ready=False/BackendNotFound; got %+v", cond)
	}

	// No per-template CRD must have been created.
	crdName := perTemplateCRDName(&got)
	_, err := dyn.Resource(crdGVR).Get(context.Background(), crdName, metav1.GetOptions{})
	if err == nil {
		t.Fatalf("per-template CRD %q was created despite BackendNotFound", crdName)
	}
}

// TestReconcileRetiredTemplateIsSwept pins the retirement mechanism
// (retired.go): a Template whose name is on the retired list — e.g. left
// behind in a workspace seeded before the template was removed from the
// catalog — is deleted by the reconciler itself and dismantled through the
// normal finalize chain (backend teardown + CRD/APIExport cleanup), without
// any operator action.
func TestReconcileRetiredTemplateIsSwept(t *testing.T) {
	if _, ok := retiredTemplates["sandbox-runner"]; !ok {
		t.Fatal("sandbox-runner is no longer on the retired list; pick another retired name for this test")
	}
	tmpl := newTestTemplate(t, "sandbox-runner")
	tmpl.Spec.InstanceCRD.Kind = "SandboxRunner"
	tmpl.Spec.InstanceCRD.Resource = "sandboxrunners"
	r, dyn, stb := newTestReconciler(t, tmpl)

	reconcileUntilSettled(t, r, "sandbox-runner")

	// The Template must be gone — deleted by the controller, finalizer
	// removed by the finalize chain.
	var post infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "sandbox-runner"}, &post); err == nil {
		t.Fatalf("retired template still present after reconcile (deletionTimestamp=%v, finalizers=%v)",
			post.DeletionTimestamp, post.Finalizers)
	}

	// The finalize chain ran: the backend saw a teardown, and no
	// per-template CRD or APIExport entry survives. Retirement fires before
	// the CRD is ever authored on a fresh workspace, so absence — not
	// deletion — is the invariant.
	if len(stb.SeenTeardowns) < 1 {
		t.Fatalf("expected the finalize chain to call TeardownTemplate; got %v", stb.SeenTeardowns)
	}
	if _, err := dyn.Resource(crdGVR).Get(context.Background(), perTemplateCRDName(tmpl), metav1.GetOptions{}); err == nil {
		t.Fatal("per-template CRD exists for a retired template")
	}
	export, err := dyn.Resource(apiExportGVR).Get(context.Background(), APIExportName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get APIExport: %v", err)
	}
	resources, err := getAPIExportResources(export)
	if err != nil {
		t.Fatalf("decode resources: %v", err)
	}
	if len(resources) != 0 {
		t.Fatalf("APIExport still carries entries for a retired template: %v", resources)
	}

	// Re-applying the retired template must sweep it again — retirement is
	// enforced by the watch loop, not a one-shot migration.
	again := newTestTemplate(t, "sandbox-runner")
	if err := r.Client.Create(context.Background(), again); err != nil {
		t.Fatalf("re-create retired template: %v", err)
	}
	reconcileUntilSettled(t, r, "sandbox-runner")
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "sandbox-runner"}, &post); err == nil {
		t.Fatal("re-applied retired template survived reconciliation")
	}
}

func TestReconcileDelete(t *testing.T) {
	tmpl := newTestTemplate(t, "delgone")
	r, dyn, stb := newTestReconciler(t, tmpl)

	// Reach Ready first so the per-template CRD + APIExport entry
	// exist.
	reconcileUntilSettled(t, r, "delgone")

	// Confirm CRD is present pre-delete.
	crdName := perTemplateCRDName(tmpl)
	if _, err := dyn.Resource(crdGVR).Get(context.Background(), crdName, metav1.GetOptions{}); err != nil {
		t.Fatalf("setup: per-template CRD missing: %v", err)
	}

	if err := r.Client.Delete(context.Background(), tmpl); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileUntilSettled(t, r, "delgone")

	// CRD should be gone.
	if _, err := dyn.Resource(crdGVR).Get(context.Background(), crdName, metav1.GetOptions{}); err == nil {
		t.Fatalf("per-template CRD %q still present after delete", crdName)
	}

	// APIExport entry should be empty.
	export, err := dyn.Resource(apiExportGVR).Get(context.Background(), APIExportName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get APIExport: %v", err)
	}
	resources, err := getAPIExportResources(export)
	if err != nil {
		t.Fatalf("decode resources: %v", err)
	}
	if len(resources) != 0 {
		t.Fatalf("expected empty resources after delete; got %v", resources)
	}

	// Stub backend should have seen at least one TeardownTemplate
	// (same idempotency note as SetupTemplate).
	if len(stb.SeenTeardowns) < 1 {
		t.Fatalf("expected at least one teardown; got %v", stb.SeenTeardowns)
	}

	// Template itself should be gone (the finalizer was removed).
	var post infrav1alpha1.Template
	err = r.Client.Get(context.Background(), types.NamespacedName{Name: "delgone"}, &post)
	if err == nil {
		t.Fatalf("template still present after finalizer removal")
	}
}

func TestPerTemplateCRDShape(t *testing.T) {
	tmpl := newTestTemplate(t, "shape")
	crd, err := buildPerTemplateCRD(tmpl)
	if err != nil {
		t.Fatalf("build crd: %v", err)
	}
	if crd.Spec.Group != infrav1alpha1.GroupName {
		t.Fatalf("crd group = %q; want %q", crd.Spec.Group, infrav1alpha1.GroupName)
	}
	if crd.Spec.Scope != apiextensionsv1.ClusterScoped {
		t.Fatalf("crd scope = %v; want Cluster", crd.Spec.Scope)
	}
	if len(crd.Spec.Versions) != 1 {
		t.Fatalf("expected one version; got %d", len(crd.Spec.Versions))
	}
	v := crd.Spec.Versions[0]
	if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
		t.Fatalf("missing openAPI schema")
	}
	if _, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]; !ok {
		t.Fatalf("openAPI missing spec property")
	}
	if _, ok := v.Schema.OpenAPIV3Schema.Properties["status"]; !ok {
		t.Fatalf("openAPI missing platform-provided status property")
	}
}

func TestSchemaPrefixIsStable(t *testing.T) {
	tmpl := newTestTemplate(t, "stable")
	crd1, _ := buildPerTemplateCRD(tmpl)
	crd2, _ := buildPerTemplateCRD(tmpl) // identical input
	if schemaPrefix(crd1) != schemaPrefix(crd2) {
		t.Fatalf("schemaPrefix must be deterministic for identical CRDs")
	}

	// Changing the schema content must change the prefix.
	tmpl.Spec.InstanceCRD.Version = "v1beta1"
	crd3, _ := buildPerTemplateCRD(tmpl)
	if schemaPrefix(crd1) == schemaPrefix(crd3) {
		t.Fatalf("schemaPrefix must change when CRD content changes")
	}
}

// findCondition is a tiny test helper. Not exported because tests of
// other packages have their own preferences for condition lookup.
func findCondition(conds []metav1.Condition, condType string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == condType {
			return &conds[i]
		}
	}
	return nil
}
