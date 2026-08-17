// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package project

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
)

func devProject() *vibev1alpha1.Project {
	p := &vibev1alpha1.Project{}
	p.Name = "barber-1234"
	p.Spec = vibev1alpha1.ProjectSpec{
		DisplayName: "Barber",
		Template:    &vibev1alpha1.ProjectTemplateSpec{Name: "application"},
		Environments: []vibev1alpha1.ProjectEnvironmentSpec{{
			Name: "development",
			Mode: vibev1alpha1.ProjectEnvironmentModeLive,
			Bindings: []vibev1alpha1.ProjectProviderBindingSpec{{
				Name:     "runtime",
				Provider: "infrastructure",
				Kind:     vibev1alpha1.ProjectBindingKindProviderResource,
				ResourceRef: &vibev1alpha1.ProjectProviderResourceReference{
					Name:       "barber-1234",
					APIVersion: "infrastructure.faros.sh/v1alpha1",
					Kind:       "Instance",
					Resource:   "instances",
				},
				Values: runtime.RawExtension{Raw: []byte(`{"name":"barber-1234","farosMode":"development"}`)},
			}},
		}},
	}
	return p
}

func TestInstanceRefs(t *testing.T) {
	p := devProject()
	refs := InstanceRefs(p)
	if len(refs) != 1 {
		t.Fatalf("refs = %d, want 1", len(refs))
	}
	ref := refs[0]
	if ref.GVR.Group != "infrastructure.faros.sh" || ref.GVR.Resource != "instances" ||
		ref.Kind != "Instance" || ref.Name != "barber-1234" {
		t.Fatalf("ref = %+v", ref)
	}
	if ref.Values["farosMode"] != "development" {
		t.Fatalf("values not decoded: %+v", ref.Values)
	}

	// Non-infrastructure and unresolved bindings are skipped.
	p.Spec.Environments[0].Bindings = append(p.Spec.Environments[0].Bindings,
		vibev1alpha1.ProjectProviderBindingSpec{Name: "x", Provider: "code", Kind: vibev1alpha1.ProjectBindingKindProviderResource},
		vibev1alpha1.ProjectProviderBindingSpec{Name: "y", Provider: "infrastructure", Kind: vibev1alpha1.ProjectBindingKindProviderResource},
	)
	if got := len(InstanceRefs(p)); got != 1 {
		t.Fatalf("refs after noise = %d, want 1", got)
	}
}

func TestDesiredInstance(t *testing.T) {
	p := devProject()
	ref := InstanceRefs(p)[0]
	inst := DesiredInstance(p, ref)
	if inst.GetAPIVersion() != "infrastructure.faros.sh/v1alpha1" || inst.GetKind() != "Instance" {
		t.Fatalf("gvk = %s/%s", inst.GetAPIVersion(), inst.GetKind())
	}
	if inst.GetLabels()[templateLabel] != "application" || inst.GetLabels()[projectLabel] != "barber-1234" {
		t.Fatalf("labels = %v", inst.GetLabels())
	}
	tmplName, _, _ := unstructured.NestedString(inst.Object, "spec", "template")
	if tmplName != "application" {
		t.Fatalf("spec.template = %q", tmplName)
	}
	mode, _, _ := unstructured.NestedString(inst.Object, "spec", "values", "farosMode")
	if mode != "development" {
		t.Fatalf("spec.values.farosMode = %q", mode)
	}
}

func TestMirrorStatus(t *testing.T) {
	p := devProject()
	now := metav1.NewTime(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))

	// No instance observed yet → Provisioning, binding Pending.
	st := MirrorStatus(p, nil, now)
	if st.Phase != vibev1alpha1.ProjectPhaseProvisioning {
		t.Fatalf("phase = %s", st.Phase)
	}
	if len(st.Environments) != 1 || st.Environments[0].Bindings[0].Phase != "Pending" {
		t.Fatalf("status = %+v", st)
	}

	// Ready instance with url + extra scalar status → Ready + outputs.
	inst := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"phase": "Ready",
			"url":   "https://barber.apps.example",
			"host":  "barber.apps.example",
		},
	}}
	st = MirrorStatus(p, map[string]*unstructured.Unstructured{"development/runtime": inst}, now)
	if st.Phase != vibev1alpha1.ProjectPhaseReady {
		t.Fatalf("phase = %s, want Ready", st.Phase)
	}
	b := st.Environments[0].Bindings[0]
	if b.URL != "https://barber.apps.example" || b.Outputs["host"] != "barber.apps.example" {
		t.Fatalf("binding = %+v", b)
	}

	// Ready via condition when phase is absent.
	condInst := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
		},
	}}
	st = MirrorStatus(p, map[string]*unstructured.Unstructured{"development/runtime": condInst}, now)
	if st.Phase != vibev1alpha1.ProjectPhaseReady {
		t.Fatalf("condition-ready phase = %s", st.Phase)
	}

	// A project with no refs is not Ready by vacuity.
	empty := &vibev1alpha1.Project{}
	if st := MirrorStatus(empty, nil, now); st.Phase != vibev1alpha1.ProjectPhaseProvisioning {
		t.Fatalf("empty project phase = %s", st.Phase)
	}
}

func TestDesiredInstanceAttributesItsOwnTemplate(t *testing.T) {
	p := &vibev1alpha1.Project{}
	p.Name = "shop"
	p.Spec.Template = &vibev1alpha1.ProjectTemplateSpec{Name: "application"}

	// The search backend is a searxng instance: labelling it with the app's
	// template files it under the wrong thing in the infrastructure listings.
	search := InstanceRef{Environment: "development", Binding: "search", Template: "searxng", Name: "shop-search"}
	if got := DesiredInstance(p, search).GetLabels()[templateLabel]; got != "searxng" {
		t.Errorf("search instance template label = %q, want searxng", got)
	}
	runtime := InstanceRef{Environment: "development", Binding: "runtime", Template: "application", Name: "shop"}
	if got := DesiredInstance(p, runtime).GetLabels()[templateLabel]; got != "application" {
		t.Errorf("runtime instance template label = %q, want application", got)
	}
	// Bindings written before the field existed: the runtime falls back to
	// the Project's template, anything else goes unlabelled rather than
	// wrongly labelled.
	legacy := InstanceRef{Environment: "development", Binding: "runtime", Name: "shop"}
	if got := DesiredInstance(p, legacy).GetLabels()[templateLabel]; got != "application" {
		t.Errorf("legacy runtime label = %q, want the project's template", got)
	}
	legacySearch := InstanceRef{Environment: "development", Binding: "search", Name: "shop-search"}
	if got, ok := DesiredInstance(p, legacySearch).GetLabels()[templateLabel]; ok {
		t.Errorf("legacy search label = %q, want no label rather than a wrong one", got)
	}
}
