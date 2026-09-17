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

package addons

import (
	"context"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// recordingAddon stands in for a real implementation so the manager's own
// decisions are observable: whether it materialized anything at all is exactly
// "did Reconcile get called".
type recordingAddon struct {
	mu         sync.Mutex
	reconciles []Spec
	stops      int
	status     Status
	err        error
}

func (r *recordingAddon) Type() string { return TypeRunner }

func (r *recordingAddon) Reconcile(_ context.Context, spec Spec) (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reconciles = append(r.reconciles, spec)
	return r.status, r.err
}

func (r *recordingAddon) Stop(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stops++
	return nil
}

func (r *recordingAddon) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reconciles), r.stops
}

func addonObject(name, edgeKind, edgeName string, generation int64, mutate ...func(map[string]any)) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": edgesGroup + "/" + edgesVersion,
		"kind":       "Addon",
		"metadata": map[string]any{
			"name":       name,
			"uid":        "uid-" + name,
			"generation": generation,
		},
		"spec": map[string]any{
			"edgeRef": map[string]any{"kind": edgeKind, "name": edgeName},
			"type":    TypeRunner,
			"runner":  map[string]any{"port": int64(8787)},
		},
	}
	for _, m := range mutate {
		m(obj)
	}
	return &unstructured.Unstructured{Object: obj}
}

func newTestManager(t *testing.T, allowed []string, objects ...runtime.Object) (*Manager, dynamic.Interface, *recordingAddon) {
	t.Helper()
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{AddonGVR: "AddonList"}, objects...)
	manager, err := NewManager(client, Options{EdgeKind: "LinuxServer", EdgeName: "build-01", Allowed: allowed})
	if err != nil {
		t.Fatal(err)
	}
	instance := &recordingAddon{status: Status{Phase: PhaseRunning, Version: "v1.2.3"}}
	manager.Register(TypeRunner, func(string) (Addon, error) { return instance, nil })
	return manager, client, instance
}

// TestManagerIgnoresOtherEdges: an Addon for a different edge must not be
// materialized AND must not have its status touched — that object belongs to
// another agent, which owns its status conditions.
func TestManagerIgnoresOtherEdges(t *testing.T) {
	obj := addonObject("other", "LinuxServer", "build-02", 1)
	manager, client, instance := newTestManager(t, []string{TypeRunner}, obj)

	if err := manager.Reconcile(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	if reconciles, _ := instance.counts(); reconciles != 0 {
		t.Errorf("materialized another edge's add-on (%d reconciles)", reconciles)
	}
	got, err := client.Resource(AddonGVR).Get(context.Background(), "other", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := unstructured.NestedMap(got.Object, "status"); found {
		t.Error("wrote status onto another edge's add-on")
	}

	// A different KIND with the same name is a different edge too.
	macObj := addonObject("mac", "MacOSServer", "build-01", 1)
	macManager, _, macInstance := newTestManager(t, []string{TypeRunner}, macObj)
	if err := macManager.Reconcile(context.Background(), "mac"); err != nil {
		t.Fatal(err)
	}
	if reconciles, _ := macInstance.counts(); reconciles != 0 {
		t.Errorf("a MacOSServer add-on was materialized by a LinuxServer agent (%d reconciles)", reconciles)
	}
}

// TestManagerRefusesATypeTheMachineOwnerDidNotAllow is the machine owner's half
// of the trust model: with no --allow-addon the agent starts nothing and writes
// nothing, and says exactly why with a reason a portal can key on.
func TestManagerRefusesATypeTheMachineOwnerDidNotAllow(t *testing.T) {
	obj := addonObject("code", "LinuxServer", "build-01", 7)
	manager, client, instance := newTestManager(t, nil, obj)

	if err := manager.Reconcile(context.Background(), "code"); err != nil {
		t.Fatal(err)
	}
	if reconciles, _ := instance.counts(); reconciles != 0 {
		t.Fatalf("a disallowed add-on was materialized (%d reconciles)", reconciles)
	}

	got, err := client.Resource(AddonGVR).Get(context.Background(), "code", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if phase, _, _ := unstructured.NestedString(got.Object, "status", "phase"); phase != PhaseBlocked {
		t.Errorf("phase = %q, want %q", phase, PhaseBlocked)
	}
	cond := findCondition(t, got, ConditionAllowed)
	if cond.Status != string(metav1.ConditionFalse) {
		t.Errorf("Allowed = %q, want False", cond.Status)
	}
	if cond.Reason != ReasonNotAllowedOnEdge {
		t.Errorf("Allowed reason = %q, want %q", cond.Reason, ReasonNotAllowedOnEdge)
	}
	if !strings.Contains(cond.Message, "--allow-addon") {
		t.Errorf("Allowed message does not name the flag: %q", cond.Message)
	}
	if generation, _, _ := unstructured.NestedInt64(got.Object, "status", "observedGeneration"); generation != 7 {
		t.Errorf("observedGeneration = %d, want 7", generation)
	}
}

// TestManagerReportsObservedGenerationAndVersion: the happy path patches the
// status subresource with what the implementation reported plus the generation
// it acted on, so a stale status is recognisable as stale.
func TestManagerReportsObservedGenerationAndVersion(t *testing.T) {
	// Seed the provider-owned Published condition: the agent rewrites the whole
	// conditions array on every status patch and must merge into it, not
	// replace it.
	obj := addonObject("code", "LinuxServer", "build-01", 4, func(o map[string]any) {
		o["status"] = map[string]any{"conditions": []any{map[string]any{
			"type":               "Published",
			"status":             "True",
			"reason":             "ServicePublished",
			"message":            "Service code points at this add-on",
			"lastTransitionTime": "2026-09-01T00:00:00Z",
		}}}
	})
	manager, client, instance := newTestManager(t, []string{TypeRunner}, obj)
	instance.status = Status{
		Phase:   PhaseRunning,
		Version: "v9.9.9",
		Message: "runner answering",
		Conditions: []Condition{
			{Type: ConditionConfigured, Status: metav1.ConditionTrue, Reason: ReasonConfigured, Message: "ok"},
			{Type: ConditionRunning, Status: metav1.ConditionTrue, Reason: ReasonProbeOK, Message: "ok"},
		},
	}

	if err := manager.Reconcile(context.Background(), "code"); err != nil {
		t.Fatal(err)
	}
	got, err := client.Resource(AddonGVR).Get(context.Background(), "code", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if phase, _, _ := unstructured.NestedString(got.Object, "status", "phase"); phase != PhaseRunning {
		t.Errorf("phase = %q, want Running", phase)
	}
	if version, _, _ := unstructured.NestedString(got.Object, "status", "version"); version != "v9.9.9" {
		t.Errorf("version = %q, want v9.9.9", version)
	}
	if generation, _, _ := unstructured.NestedInt64(got.Object, "status", "observedGeneration"); generation != 4 {
		t.Errorf("observedGeneration = %d, want 4", generation)
	}
	if c := findCondition(t, got, ConditionAllowed); c.Status != string(metav1.ConditionTrue) {
		t.Errorf("Allowed = %q, want True", c.Status)
	}
	if c := findCondition(t, got, ConditionRunning); c.Status != string(metav1.ConditionTrue) {
		t.Errorf("Running = %q, want True", c.Status)
	}
	// The provider owns Published; the agent must not drop it while rewriting
	// the conditions array.
	if c := findCondition(t, got, "Published"); c.Reason != "ServicePublished" {
		t.Errorf("the agent dropped the provider's Published condition: %+v", c)
	}
}

// TestManagerStopsOnPauseAndDelete: pausing stops the child but keeps the
// instance (and therefore its state directory); deleting releases it.
func TestManagerStopsOnPauseAndDelete(t *testing.T) {
	obj := addonObject("code", "LinuxServer", "build-01", 1)
	manager, client, instance := newTestManager(t, []string{TypeRunner}, obj)
	ctx := context.Background()

	if err := manager.Reconcile(ctx, "code"); err != nil {
		t.Fatal(err)
	}
	if reconciles, _ := instance.counts(); reconciles != 1 {
		t.Fatalf("expected one reconcile, got %d", reconciles)
	}

	// Pause.
	live, err := client.Resource(AddonGVR).Get(ctx, "code", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(live.Object, true, "spec", "paused"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(AddonGVR).Update(ctx, live, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(ctx, "code"); err != nil {
		t.Fatal(err)
	}
	reconciles, stops := instance.counts()
	if reconciles != 1 {
		t.Errorf("a paused add-on was reconciled again (%d)", reconciles)
	}
	if stops != 1 {
		t.Errorf("a paused add-on was not stopped (%d stops)", stops)
	}
	got, err := client.Resource(AddonGVR).Get(ctx, "code", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if phase, _, _ := unstructured.NestedString(got.Object, "status", "phase"); phase != PhasePaused {
		t.Errorf("phase = %q, want Paused", phase)
	}

	// Delete.
	if err := client.Resource(AddonGVR).Delete(ctx, "code", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(ctx, "code"); err != nil {
		t.Fatal(err)
	}
	if _, stops := instance.counts(); stops != 2 {
		t.Errorf("a deleted add-on was not stopped (%d stops)", stops)
	}
}

// TestNormalizedAllowedTypes: the reported list is what the heartbeat puts on
// the edge, so it has to be deterministic.
func TestAllowedTypesAreSorted(t *testing.T) {
	manager, _, _ := newTestManager(t, []string{TypeRunner, "", TypeRunner})
	if got := manager.AllowedTypes(); len(got) != 1 || got[0] != TypeRunner {
		t.Errorf("AllowedTypes = %v, want [runner]", got)
	}
}

// TestValidateName rejects anything that would escape the add-on's directory or
// produce an invalid Secret name.
func TestValidateName(t *testing.T) {
	for _, bad := range []string{"", "..", "a/b", "Upper", "-lead", "trail-", "a b"} {
		if err := validateName(bad); err == nil {
			t.Errorf("validateName(%q) accepted an unsafe name", bad)
		}
	}
	for _, ok := range []string{"a", "code-runner", "runner1"} {
		if err := validateName(ok); err != nil {
			t.Errorf("validateName(%q) = %v", ok, err)
		}
	}
}

type conditionView struct {
	Type    string
	Status  string
	Reason  string
	Message string
}

func findCondition(t *testing.T, obj *unstructured.Unstructured, condType string) conditionView {
	t.Helper()
	conditions, _, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range conditions {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if entry["type"] == condType {
			view := conditionView{Type: condType}
			view.Status, _ = entry["status"].(string)
			view.Reason, _ = entry["reason"].(string)
			view.Message, _ = entry["message"].(string)
			return view
		}
	}
	t.Fatalf("condition %q not found in %v", condType, conditions)
	return conditionView{}
}
