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

package kcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	kubetesting "k8s.io/client-go/testing"

	adminv1alpha1 "github.com/railgrid/railgrid/apis/admin/v1alpha1"
)

var (
	seedSwitchboardExport = adminv1alpha1.SeedExportReference{Path: "root:railgrid:system:switchboard", Name: "switchboard.railgrid.ai"}
	seedAgentsExport      = adminv1alpha1.SeedExportReference{Path: "root:railgrid:providers:agents", Name: "agents.railgrid.ai"}
)

func testSeedSpec() adminv1alpha1.WorkspaceSeedSpec {
	return adminv1alpha1.WorkspaceSeedSpec{
		Bindings: []adminv1alpha1.SeedBinding{{
			Name:   "switchboard",
			Export: seedSwitchboardExport,
			PermissionClaims: []adminv1alpha1.SeedPermissionClaim{{
				Resource: "secrets",
				Verbs:    []string{"get", "list", "watch", "create", "update", "delete"},
				Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key: "switchboard.railgrid.ai/request", Operator: metav1.LabelSelectorOpExists,
				}}},
			}},
			TenantAccess: adminv1alpha1.SeedTenantAccessReadOnly,
		}},
		Objects: []adminv1alpha1.SeedObject{
			{Resource: "requests", Manifest: runtime.RawExtension{Raw: []byte(`{"apiVersion":"switchboard.railgrid.ai/v1alpha1","kind":"Request","metadata":{"name":"free","namespace":"default"},"spec":{"policy":"free"}}`)}},
			{Resource: "modelproviderconnections", WhenBound: []adminv1alpha1.SeedExportReference{seedAgentsExport},
				Manifest: runtime.RawExtension{Raw: []byte(`{"apiVersion":"agents.railgrid.ai/v1alpha1","kind":"ModelProviderConnection","metadata":{"name":"railgrid"},"spec":{"baseURL":"https://switchboard.example/v1"}}`)}},
		},
	}
}

func seedTestBinding(name string, export adminv1alpha1.SeedExportReference, phase string, annotations map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]interface{}{"name": name},
		"spec": map[string]interface{}{"reference": map[string]interface{}{"export": map[string]interface{}{
			"path": export.Path, "name": export.Name,
		}}},
		"status": map[string]interface{}{"phase": phase},
	}}
	u.SetAnnotations(annotations)
	return u
}

// applied records the server-side applies a fake client received instead of
// executing them: the fake has no schema for the seeded kinds.
type applied struct {
	gvr       schema.GroupVersionResource
	namespace string
	name      string
	manager   string
	force     bool
	body      map[string]interface{}
}

func newSeedTestClient(t *testing.T, objects ...runtime.Object) (*fake.FakeDynamicClient, *[]applied) {
	t.Helper()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{apiBindingGVR: "APIBindingList"}, objects...)
	var applies []applied
	dyn.PrependReactor("patch", "*", func(action kubetesting.Action) (bool, runtime.Object, error) {
		patch := action.(kubetesting.PatchAction)
		if patch.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		record := applied{gvr: patch.GetResource(), namespace: patch.GetNamespace(), name: patch.GetName()}
		if with, ok := action.(interface{ GetPatchOptions() metav1.PatchOptions }); ok {
			opts := with.GetPatchOptions()
			record.manager = opts.FieldManager
			record.force = opts.Force != nil && *opts.Force
		}
		if err := json.Unmarshal(patch.GetPatch(), &record.body); err != nil {
			t.Fatalf("apply body: %v", err)
		}
		applies = append(applies, record)
		return true, &unstructured.Unstructured{Object: record.body}, nil
	})
	return dyn, &applies
}

func TestEnsureWorkspaceSeedCreatesTheBindingAndHoldsObjectsUntilBound(t *testing.T) {
	ctx := context.Background()
	dyn, applies := newSeedTestClient(t)

	result, err := ensureWorkspaceSeed(ctx, dyn, "switchboard", testSeedSpec())
	if err != nil {
		t.Fatal(err)
	}
	if result.BindingsCreated != 1 || result.Unbound != 1 || result.ObjectsApplied != 0 || len(*applies) != 0 {
		t.Fatalf("first pass = %+v, applies %v; want the binding created and nothing applied", result, *applies)
	}
	binding, err := dyn.Resource(apiBindingGVR).Get(ctx, "switchboard", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := binding.GetAnnotations(); got[adminv1alpha1.WorkspaceSeedAnnotation] != "switchboard" || got[adminv1alpha1.TenantAccessAnnotation] != "ReadOnly" {
		t.Fatalf("binding annotations = %v", got)
	}
	claims, _, _ := unstructured.NestedSlice(binding.Object, "spec", "permissionClaims")
	if len(claims) != 1 {
		t.Fatalf("claims = %v", claims)
	}
	claim := claims[0].(map[string]interface{})
	if claim["state"] != "Accepted" || claim["resource"] != "secrets" {
		t.Fatalf("claim = %v", claim)
	}
	selector, _ := claim["selector"].(map[string]interface{})
	if _, all := selector["matchAll"]; all || selector["matchExpressions"] == nil {
		t.Fatalf("claim selector = %v, want the seed's expressions and no matchAll", selector)
	}

	// kcp binds it. The Request goes in; the Agents connection waits for a
	// provider nobody enabled here.
	if err := unstructured.SetNestedField(binding.Object, "Bound", "status", "phase"); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(apiBindingGVR).Update(ctx, binding, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err = ensureWorkspaceSeed(ctx, dyn, "switchboard", testSeedSpec())
	if err != nil {
		t.Fatal(err)
	}
	if result.BindingsCreated != 0 || result.BindingsChanged != 0 || result.Unbound != 0 || result.ObjectsApplied != 1 {
		t.Fatalf("second pass = %+v", result)
	}
	if len(result.Waiting) != 1 || !strings.Contains(result.Waiting[0], "agents.railgrid.ai") {
		t.Fatalf("waiting = %v", result.Waiting)
	}
	if len(*applies) != 1 {
		t.Fatalf("applies = %v", *applies)
	}
	got := (*applies)[0]
	want := schema.GroupVersionResource{Group: "switchboard.railgrid.ai", Version: "v1alpha1", Resource: "requests"}
	if got.gvr != want || got.namespace != "default" || got.name != "free" || got.manager != adminv1alpha1.WorkspaceSeedFieldManager || !got.force {
		t.Fatalf("apply = %+v", got)
	}

	// The provider is enabled: its object follows, cluster-scoped.
	if _, err := dyn.Resource(apiBindingGVR).Create(ctx, seedTestBinding("agents", seedAgentsExport, "Bound", nil), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	*applies = nil
	result, err = ensureWorkspaceSeed(ctx, dyn, "switchboard", testSeedSpec())
	if err != nil {
		t.Fatal(err)
	}
	if result.ObjectsApplied != 2 || len(result.Waiting) != 0 || len(*applies) != 2 {
		t.Fatalf("third pass = %+v, applies %v", result, *applies)
	}
	if last := (*applies)[1]; last.gvr.Resource != "modelproviderconnections" || last.namespace != "" || last.name != "railgrid" {
		t.Fatalf("apply = %+v", last)
	}
}

func TestEnsureWorkspaceSeedLeavesBindingsItDoesNotOwn(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		existing *unstructured.Unstructured
		want     string
	}{
		"another export": {
			seedTestBinding("switchboard", adminv1alpha1.SeedExportReference{Path: "root:railgrid:tenants:org:providers:x", Name: "x"}, "Bound",
				map[string]string{adminv1alpha1.WorkspaceSeedAnnotation: "switchboard"}),
			"references",
		},
		"not seeded":   {seedTestBinding("switchboard", seedSwitchboardExport, "Bound", nil), "not created by a WorkspaceSeed"},
		"another seed": {seedTestBinding("switchboard", seedSwitchboardExport, "Bound", map[string]string{adminv1alpha1.WorkspaceSeedAnnotation: "other"}), `WorkspaceSeed "other"`},
	} {
		t.Run(name, func(t *testing.T) {
			dyn, applies := newSeedTestClient(t, tc.existing)
			before, _ := dyn.Resource(apiBindingGVR).Get(ctx, "switchboard", metav1.GetOptions{})
			result, err := ensureWorkspaceSeed(ctx, dyn, "switchboard", testSeedSpec())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
			if result.ObjectsApplied != 0 || len(*applies) != 0 {
				t.Fatalf("objects were applied past a binding the seed does not own: %+v", result)
			}
			after, _ := dyn.Resource(apiBindingGVR).Get(ctx, "switchboard", metav1.GetOptions{})
			if before.GetResourceVersion() != after.GetResourceVersion() || len(after.GetAnnotations()) != len(before.GetAnnotations()) {
				t.Fatal("the foreign binding was written")
			}
		})
	}
}

func TestEnsureWorkspaceSeedBringsItsBindingBackToTheSeed(t *testing.T) {
	ctx := context.Background()
	existing := seedTestBinding("switchboard", seedSwitchboardExport, "Bound", map[string]string{
		adminv1alpha1.WorkspaceSeedAnnotation: "switchboard",
		adminv1alpha1.TenantAccessAnnotation:  "ReadWrite",
	})
	dyn, applies := newSeedTestClient(t, existing)
	result, err := ensureWorkspaceSeed(ctx, dyn, "switchboard", testSeedSpec())
	if err != nil {
		t.Fatal(err)
	}
	// The claims were rewritten, so the listed Bound is not trusted and the
	// objects wait for the binding's next event.
	if result.BindingsChanged != 1 || result.Unbound != 1 || len(*applies) != 0 {
		t.Fatalf("result = %+v, applies %v", result, *applies)
	}
	got, _ := dyn.Resource(apiBindingGVR).Get(ctx, "switchboard", metav1.GetOptions{})
	if got.GetAnnotations()[adminv1alpha1.TenantAccessAnnotation] != "ReadOnly" {
		t.Fatalf("annotations = %v", got.GetAnnotations())
	}
	if claims, _, _ := unstructured.NestedSlice(got.Object, "spec", "permissionClaims"); len(claims) != 1 {
		t.Fatalf("claims = %v", claims)
	}

	result, err = ensureWorkspaceSeed(ctx, dyn, "switchboard", testSeedSpec())
	if err != nil {
		t.Fatal(err)
	}
	if result.BindingsChanged != 0 || result.Unbound != 0 || result.ObjectsApplied != 1 {
		t.Fatalf("settled pass = %+v", result)
	}
}

func TestEnsureWorkspaceSeedRefusesTheCoreBindingName(t *testing.T) {
	spec := testSeedSpec()
	spec.Bindings[0].Name = CoreBindingName
	dyn, _ := newSeedTestClient(t)
	if _, err := ensureWorkspaceSeed(context.Background(), dyn, "switchboard", spec); err == nil || !strings.Contains(err.Error(), "hub's own binding") {
		t.Fatalf("err = %v", err)
	}
	if list, _ := dyn.Resource(apiBindingGVR).List(context.Background(), metav1.ListOptions{}); len(list.Items) != 0 {
		t.Fatalf("bindings = %v", list.Items)
	}
}
