/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bindings

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

func testProject() *aiv1alpha1.Project {
	return &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-1")},
	}
}

func testBinding() aiv1alpha1.ProjectProviderBindingSpec {
	return aiv1alpha1.ProjectProviderBindingSpec{
		Name:     "development",
		Provider: "infrastructure",
		Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
		ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
			Name:       "demo-dev",
			APIVersion: "infrastructure.kedge.faros.sh/v1alpha1",
			Kind:       "Application",
			Resource:   "applications",
		},
		Values: runtime.RawExtension{Raw: []byte(`{"kedgeMode":"development","webImage":"x"}`)},
	}
}

func TestDesiredIsSelfContained(t *testing.T) {
	p := testProject()
	want, gvr, err := Desired(p, testBinding())
	if err != nil {
		t.Fatalf("Desired: %v", err)
	}
	if gvr.Group != "infrastructure.kedge.faros.sh" || gvr.Resource != "applications" || gvr.Version != "v1alpha1" {
		t.Fatalf("gvr = %v", gvr)
	}
	if want.GetName() != "demo-dev" {
		t.Fatalf("name = %q, want demo-dev", want.GetName())
	}
	if want.GetLabels()[ProjectLabel] != "demo" {
		t.Fatalf("labels = %v, want %s=demo", want.GetLabels(), ProjectLabel)
	}
	owners := want.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Kind != "Project" || owners[0].Name != "demo" {
		t.Fatalf("ownerReferences = %+v", owners)
	}
	mode, _, _ := unstructured.NestedString(want.Object, "spec", "kedgeMode")
	if mode != "development" {
		t.Fatalf("spec.kedgeMode = %q", mode)
	}
}

func TestDesiredNameFallbacks(t *testing.T) {
	p := testProject()

	b := testBinding()
	b.ResourceRef.Name = ""
	b.Values = runtime.RawExtension{Raw: []byte(`{"name":"explicit"}`)}
	want, _, err := Desired(p, b)
	if err != nil {
		t.Fatalf("Desired: %v", err)
	}
	if want.GetName() != "explicit" {
		t.Fatalf("name = %q, want explicit (values fallback)", want.GetName())
	}

	b.Values = runtime.RawExtension{}
	want, _, err = Desired(p, b)
	if err != nil {
		t.Fatalf("Desired: %v", err)
	}
	if want.GetName() != "demo-development" {
		t.Fatalf("name = %q, want demo-development (project-binding fallback)", want.GetName())
	}
}

func TestDesiredInvalidBinding(t *testing.T) {
	p := testProject()

	b := testBinding()
	b.ResourceRef = nil
	if _, _, err := Desired(p, b); !IsInvalidBinding(err) {
		t.Fatalf("nil resourceRef: err = %v, want InvalidBindingError", err)
	}

	b = testBinding()
	b.Values = runtime.RawExtension{Raw: []byte(`{broken`)}
	if _, _, err := Desired(p, b); !IsInvalidBinding(err) {
		t.Fatalf("broken values: err = %v, want InvalidBindingError", err)
	}

	b = testBinding()
	b.ResourceRef.Resource = ""
	if _, _, err := Desired(p, b); !IsInvalidBinding(err) {
		t.Fatalf("missing resource: err = %v, want InvalidBindingError", err)
	}
}

func TestPhase(t *testing.T) {
	cases := []struct {
		name   string
		status map[string]any
		want   string
	}{
		{"explicit phase", map[string]any{"phase": "Ready"}, "Ready"},
		{"ready condition", map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}, "Ready"},
		{"unready condition", map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False"}}}, "Pending"},
		{"active state", map[string]any{"state": "ACTIVE"}, "Ready"},
		{"nothing", map[string]any{}, ""},
	}
	for _, tc := range cases {
		obj := &unstructured.Unstructured{Object: map[string]any{"status": tc.status}}
		if got := Phase(obj); got != tc.want {
			t.Errorf("%s: Phase = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStatusFromObjectNotFoundIsPending(t *testing.T) {
	st := StatusFromObject(testBinding(), nil)
	if st.Phase != "Pending" || st.Name != "development" || st.Provider != "infrastructure" {
		t.Fatalf("status = %+v", st)
	}
}

func TestMergeEnvironmentStatusesPreservesUnmanaged(t *testing.T) {
	existing := []aiv1alpha1.ProjectEnvironmentStatus{
		{Name: "development", Phase: "Pending"},
		{Name: "other", Phase: "Ready"},
	}
	live := []aiv1alpha1.ProjectEnvironmentStatus{
		{Name: "development", Phase: "Ready"},
		{Name: "production", Phase: "Pending"},
	}
	out := MergeEnvironmentStatuses(existing, live)
	if len(out) != 3 {
		t.Fatalf("merged = %+v, want 3 entries", out)
	}
	if out[0].Name != "development" || out[0].Phase != "Ready" {
		t.Fatalf("development not overlaid: %+v", out[0])
	}
	if out[1].Name != "other" || out[1].Phase != "Ready" {
		t.Fatalf("unmanaged entry not preserved in order: %+v", out[1])
	}
}
