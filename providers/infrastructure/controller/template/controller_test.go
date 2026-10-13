/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package template

// Reconciler unit tests using the in-memory client.Fake. The controller's
// whole surface is Template + status + backend dispatch — per-template CRD
// and APIExport wiring were retired with the flattened Instance kind, so
// there is nothing dynamic-client-shaped left to fake.
//
// Coverage:
//   * happy path — Template reaches Ready=True; backend called
//   * invalid schema — SchemaValid=False / Ready=False, backend NOT called
//   * backend missing — Ready reports BackendNotFound
//   * retired sweep + delete — teardown runs, finalizer drops

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
	"github.com/railgrid/provider-infrastructure/backend"
	"github.com/railgrid/provider-infrastructure/backend/stub"
)

// newTestReconciler wires up the fake client + a backend registry
// containing only the stub.
func newTestReconciler(t *testing.T, initial ...client.Object) (*Reconciler, *stub.Backend) {
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

	reg := backend.NewRegistry()
	stb := stub.New()
	if err := reg.Register(stb); err != nil {
		t.Fatalf("register stub: %v", err)
	}

	return &Reconciler{Client: c, Backends: reg}, stb
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

// reconcileUntilSettled drives Reconcile through the "add finalizer →
// requeue → real work" progression the controller designs in. Capped at 5
// iterations.
func reconcileUntilSettled(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	for range 5 {
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
		if err != nil {
			continue
		}
	}
}

func TestReconcileHappyPath(t *testing.T) {
	tmpl := newTestTemplate(t, "redis")
	r, stb := newTestReconciler(t, tmpl)

	reconcileUntilSettled(t, r, "redis")

	var got infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "redis"}, &got); err != nil {
		t.Fatalf("get template: %v", err)
	}
	if cond := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected Ready=True; conditions=%v", got.Status.Conditions)
	}
	if cond := findCondition(got.Status.Conditions, infrav1alpha1.ConditionSchemaValid); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected SchemaValid=True; conditions=%v", got.Status.Conditions)
	}
	if got.Status.Backend.Name != stub.Name {
		t.Fatalf("expected backend=%q in status; got %q", stub.Name, got.Status.Backend.Name)
	}

	// SetupTemplate is documented as idempotent and called per reconcile
	// pass, so only assert it was called at least once with the right name.
	if len(stb.SeenSetups) < 1 || stb.SeenSetups[0] != "redis" {
		t.Fatalf("expected at least one stub SetupTemplate call for redis; got %v", stb.SeenSetups)
	}
}

func TestReconcileUniversalCodingSandboxDisabledByDefault(t *testing.T) {
	tmpl := newTestTemplate(t, infrav1alpha1.UniversalCodingSandboxTemplateName)
	r, stb := newTestReconciler(t, tmpl)
	reconcileUntilSettled(t, r, tmpl.Name)

	var got infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: tmpl.Name}, &got); err != nil {
		t.Fatalf("get template: %v", err)
	}
	ready := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != infrav1alpha1.ReasonCodingSandboxDisabled {
		t.Fatalf("disabled coding sandbox condition = %+v", ready)
	}
	if len(stb.SeenSetups) != 0 {
		t.Fatalf("disabled coding sandbox reached backend: %v", stb.SeenSetups)
	}
}

func TestReconcileUniversalCodingSandboxRejectsUnsafeContractWhenEnabled(t *testing.T) {
	tmpl := newTestTemplate(t, infrav1alpha1.UniversalCodingSandboxTemplateName)
	r, stb := newTestReconciler(t, tmpl)
	r.CodingSandboxEnabled = true
	reconcileUntilSettled(t, r, tmpl.Name)

	var got infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: tmpl.Name}, &got); err != nil {
		t.Fatalf("get template: %v", err)
	}
	ready := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != infrav1alpha1.ReasonInvalidSpec {
		t.Fatalf("unsafe enabled coding sandbox condition = %+v", ready)
	}
	if len(stb.SeenSetups) != 0 {
		t.Fatalf("unsafe enabled coding sandbox reached backend: %v", stb.SeenSetups)
	}
}

// TestReconcileInvalidSchema pins the values-contract gate: a Template whose
// schema claims a platform-reserved property must park on
// SchemaValid=False/InvalidSpec without ever reaching the backend — the
// instance controller could never validate values against it.
func TestReconcileInvalidSchema(t *testing.T) {
	tmpl := newTestTemplate(t, "claimsreserved")
	schemaRaw, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			infrav1alpha1.RailgridModeField: map[string]any{"type": "string"},
		},
	})
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	tmpl.Spec.Schema = &runtime.RawExtension{Raw: schemaRaw}
	r, stb := newTestReconciler(t, tmpl)

	reconcileUntilSettled(t, r, "claimsreserved")

	var got infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "claimsreserved"}, &got); err != nil {
		t.Fatalf("get template: %v", err)
	}
	cond := findCondition(got.Status.Conditions, infrav1alpha1.ConditionSchemaValid)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != infrav1alpha1.ReasonInvalidSpec {
		t.Fatalf("expected SchemaValid=False/InvalidSpec; got %+v", cond)
	}
	if ready := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady); ready == nil || ready.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False; got %+v", ready)
	}
	if len(stb.SeenSetups) != 0 {
		t.Fatalf("backend must not be called for an invalid schema; got %v", stb.SeenSetups)
	}
}

func TestReconcileBackendNotFound(t *testing.T) {
	tmpl := newTestTemplate(t, "missing")
	tmpl.Spec.Backend = "does-not-exist"
	tmpl.Status.Capabilities = []infrav1alpha1.TemplateCapability{{
		Name: infrav1alpha1.CapabilityMCPBrowserSessionIsolation, Version: infrav1alpha1.CapabilityMCPBrowserSessionIsolationV1,
	}}
	r, stb := newTestReconciler(t, tmpl)

	reconcileUntilSettled(t, r, "missing")

	var got infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "missing"}, &got); err != nil {
		t.Fatalf("get template: %v", err)
	}
	cond := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != infrav1alpha1.ReasonBackendNotFound {
		t.Fatalf("expected Ready=False/BackendNotFound; got %+v", cond)
	}
	if len(got.Status.Capabilities) != 0 {
		t.Fatalf("failed Template retained stale capabilities: %+v", got.Status.Capabilities)
	}
	if len(stb.SeenSetups) != 0 {
		t.Fatalf("stub backend must not see setups for an unknown backend name; got %v", stb.SeenSetups)
	}
}

type kroNamedStub struct{ *stub.Backend }

func (*kroNamedStub) Name() string { return "kro" }

func TestReconcilePublishesBrowserCapabilityAfterCurrentBackendReady(t *testing.T) {
	raw, err := os.ReadFile("../../install/templates/browser.yaml")
	if err != nil {
		t.Fatalf("read browser seed: %v", err)
	}
	var tmpl infrav1alpha1.Template
	if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
		t.Fatalf("decode browser seed: %v", err)
	}
	tmpl.Generation = 1
	r, stb := newTestReconciler(t, &tmpl)
	registry := backend.NewRegistry()
	if err := registry.Register(&kroNamedStub{Backend: stb}); err != nil {
		t.Fatalf("register kro test backend: %v", err)
	}
	r.Backends = registry

	reconcileUntilSettled(t, r, tmpl.Name)

	var got infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: tmpl.Name}, &got); err != nil {
		t.Fatalf("get browser Template: %v", err)
	}
	if got.Status.ObservedGeneration != got.Generation {
		t.Fatalf("browser observed generation = %d, want current %d", got.Status.ObservedGeneration, got.Generation)
	}
	if ready := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady); ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != got.Generation {
		t.Fatalf("browser Ready condition = %+v, want current Ready=True", ready)
	}
	want := []infrav1alpha1.TemplateCapability{{
		Name: infrav1alpha1.CapabilityMCPBrowserSessionIsolation, Version: infrav1alpha1.CapabilityMCPBrowserSessionIsolationV1,
	}}
	if len(got.Status.Capabilities) != len(want) || got.Status.Capabilities[0] != want[0] {
		t.Fatalf("browser capabilities = %+v, want %+v", got.Status.Capabilities, want)
	}
	readyBeforePatch := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady)
	if readyBeforePatch == nil {
		t.Fatal("browser Ready condition is missing before compatible patch")
	}
	transitionTime := readyBeforePatch.LastTransitionTime

	// A compatible Template patch advances generation without changing the
	// Ready condition's semantic value. The reconciler must still mark that
	// condition current while leaving its transition time unchanged.
	got.Spec.Version = "0.2.1"
	got.Generation++
	if err := r.Client.Update(context.Background(), &got); err != nil {
		t.Fatalf("update browser Template patch version: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: got.Name}}); err != nil {
		t.Fatalf("reconcile compatible browser Template patch: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: got.Name}, &got); err != nil {
		t.Fatalf("get patched browser Template: %v", err)
	}
	readyAfterPatch := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady)
	if readyAfterPatch == nil || readyAfterPatch.Status != metav1.ConditionTrue || readyAfterPatch.ObservedGeneration != got.Generation {
		t.Fatalf("patched browser Ready condition = %+v, want current Ready=True", readyAfterPatch)
	}
	if !readyAfterPatch.LastTransitionTime.Equal(&transitionTime) {
		t.Fatalf("compatible patch changed Ready transition time from %v to %v", transitionTime, readyAfterPatch.LastTransitionTime)
	}
	if got.Status.ObservedGeneration != got.Generation || len(got.Status.Capabilities) != 1 || got.Status.Capabilities[0] != want[0] {
		t.Fatalf("compatible patch status = observed generation %d, capabilities %+v; want generation %d and %+v", got.Status.ObservedGeneration, got.Status.Capabilities, got.Generation, want)
	}
}

func TestReconcilePublishesAndRevokesRailgridAuthCapability(t *testing.T) {
	raw, err := os.ReadFile("../../install/templates/application.yaml")
	if err != nil {
		t.Fatalf("read application seed: %v", err)
	}
	var tmpl infrav1alpha1.Template
	if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
		t.Fatalf("decode application seed: %v", err)
	}
	tmpl.Generation = 1
	r, stb := newTestReconciler(t, &tmpl)
	registry := backend.NewRegistry()
	if err := registry.Register(&kroNamedStub{Backend: stb}); err != nil {
		t.Fatalf("register kro test backend: %v", err)
	}
	r.Backends = registry

	reconcileUntilSettled(t, r, tmpl.Name)
	var got infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: tmpl.Name}, &got); err != nil {
		t.Fatalf("get application Template: %v", err)
	}
	want := infrav1alpha1.TemplateCapability{
		Name: infrav1alpha1.CapabilityRailgridAuth, Version: infrav1alpha1.CapabilityRailgridAuthV1,
	}
	if got.Status.ObservedGeneration != got.Generation || len(got.Status.Capabilities) != 1 || got.Status.Capabilities[0] != want {
		t.Fatalf("application status generation=%d capabilities=%+v, want current generation %d and %+v", got.Status.ObservedGeneration, got.Status.Capabilities, got.Generation, want)
	}
	if ready := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady); ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != got.Generation {
		t.Fatalf("application Ready condition = %+v, want current Ready=True", ready)
	}

	// A malformed declaration revokes the old proof before the template is
	// ready again, even though the previous generation had the capability.
	validSchema := append([]byte(nil), got.Spec.Schema.Raw...)
	got.Spec.Schema.Raw = []byte(`{"type":"object","properties":{}}`)
	got.Generation++
	if err := r.Client.Update(context.Background(), &got); err != nil {
		t.Fatalf("update invalid auth schema: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: got.Name}}); err != nil {
		t.Fatalf("reconcile invalid auth schema: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: got.Name}, &got); err != nil {
		t.Fatalf("get invalid application Template: %v", err)
	}
	if len(got.Status.Capabilities) != 0 {
		t.Fatalf("invalid auth schema retained stale capabilities: %+v", got.Status.Capabilities)
	}
	if ready := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady); ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != infrav1alpha1.ReasonInvalidRailgridAuth {
		t.Fatalf("invalid auth schema Ready condition = %+v, want Ready=False/InvalidRailgridAuth", ready)
	}

	// Removing the declaration means unsupported. A still-valid Template can
	// be Ready, but the controller must not infer the feature from legacy fields.
	got.Spec.Schema.Raw = validSchema
	got.Spec.RailgridAuth = nil
	got.Generation++
	if err := r.Client.Update(context.Background(), &got); err != nil {
		t.Fatalf("remove auth declaration: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: got.Name}}); err != nil {
		t.Fatalf("reconcile omitted auth declaration: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: got.Name}, &got); err != nil {
		t.Fatalf("get template without auth declaration: %v", err)
	}
	if got.Status.ObservedGeneration != got.Generation || len(got.Status.Capabilities) != 0 {
		t.Fatalf("omitted declaration status generation=%d capabilities=%+v, want current and no capability", got.Status.ObservedGeneration, got.Status.Capabilities)
	}
	if ready := findCondition(got.Status.Conditions, infrav1alpha1.ConditionReady); ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != got.Generation {
		t.Fatalf("template without auth declaration Ready condition = %+v, want current Ready=True", ready)
	}
}

func TestReconcileRevokesBrowserCapabilityBeforeDeletionFinalization(t *testing.T) {
	tmpl := newTestTemplate(t, "browser")
	tmpl.Status.Capabilities = []infrav1alpha1.TemplateCapability{{
		Name: infrav1alpha1.CapabilityMCPBrowserSessionIsolation, Version: infrav1alpha1.CapabilityMCPBrowserSessionIsolationV1,
	}}
	tmpl.Finalizers = []string{infrav1alpha1.FinalizerTemplateReconcile, "external.example/finalizer"}
	r, stb := newTestReconciler(t, tmpl)
	if err := r.Client.Delete(context.Background(), tmpl); err != nil {
		t.Fatalf("delete browser Template: %v", err)
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: tmpl.Name}})
	if err != nil {
		t.Fatalf("reconcile capability revocation: %v", err)
	}
	if result != (ctrl.Result{Requeue: true}) {
		t.Fatal("revocation pass did not requeue before finalization")
	}
	var got infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: tmpl.Name}, &got); err != nil {
		t.Fatalf("Template disappeared before external finalizer completed: %v", err)
	}
	if len(got.Status.Capabilities) != 0 {
		t.Fatalf("deleting Template retained browser capability: %+v", got.Status.Capabilities)
	}
	if len(stb.SeenTeardowns) != 0 {
		t.Fatalf("teardown ran before capability revocation: %v", stb.SeenTeardowns)
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: tmpl.Name}}); err != nil {
		t.Fatalf("reconcile finalization: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: tmpl.Name}, &got); err != nil {
		t.Fatalf("Template disappeared before external finalizer completed: %v", err)
	}
	if len(got.Status.Capabilities) != 0 {
		t.Fatalf("finalizing Template regained browser capability: %+v", got.Status.Capabilities)
	}
}

// TestReconcileRetiredTemplateIsSwept pins the retirement mechanism
// (retired.go): a Template whose name is on the retired list is deleted by
// the reconciler itself and dismantled through the normal finalize chain
// (backend teardown), without any operator action.
func TestReconcileRetiredTemplateIsSwept(t *testing.T) {
	if _, ok := retiredTemplates["sandbox-runner"]; !ok {
		t.Fatal("sandbox-runner is no longer on the retired list; pick another retired name for this test")
	}
	tmpl := newTestTemplate(t, "sandbox-runner")
	tmpl.Spec.InstanceCRD.Kind = "SandboxRunner"
	tmpl.Spec.InstanceCRD.Resource = "sandboxrunners"
	r, stb := newTestReconciler(t, tmpl)

	reconcileUntilSettled(t, r, "sandbox-runner")

	var post infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "sandbox-runner"}, &post); err == nil {
		t.Fatalf("retired template still present after reconcile (deletionTimestamp=%v, finalizers=%v)",
			post.DeletionTimestamp, post.Finalizers)
	}
	if len(stb.SeenTeardowns) < 1 {
		t.Fatalf("expected the finalize chain to call TeardownTemplate; got %v", stb.SeenTeardowns)
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
	r, stb := newTestReconciler(t, tmpl)

	reconcileUntilSettled(t, r, "delgone")

	if err := r.Client.Delete(context.Background(), tmpl); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileUntilSettled(t, r, "delgone")

	// Stub backend should have seen at least one TeardownTemplate.
	if len(stb.SeenTeardowns) < 1 {
		t.Fatalf("expected at least one teardown; got %v", stb.SeenTeardowns)
	}

	// Template itself should be gone (the finalizer was removed).
	var post infrav1alpha1.Template
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "delgone"}, &post); err == nil {
		t.Fatalf("template still present after finalizer removal")
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
