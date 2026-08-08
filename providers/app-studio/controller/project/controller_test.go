/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

func binding(name string) aiv1alpha1.ProjectProviderBindingSpec {
	return aiv1alpha1.ProjectProviderBindingSpec{
		Name:     name,
		Provider: "infrastructure",
		Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
		ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
			APIVersion: "infrastructure.kedge.faros.sh/v1alpha1",
			Kind:       "Application",
			Resource:   "applications",
		},
		Values: runtime.RawExtension{Raw: []byte(`{}`)},
	}
}

// providerBindings must select provider-resource bindings from EVERY
// environment — promotion appends an artifact-mode production binding and
// relies on this reconciler to provision it (the HTTP layer no longer does).
func TestProviderBindingsSpansAllEnvironmentModes(t *testing.T) {
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{
				{
					Name:     "development",
					Mode:     aiv1alpha1.ProjectEnvironmentModeLive,
					Bindings: []aiv1alpha1.ProjectProviderBindingSpec{binding("development")},
				},
				{
					Name:     "production",
					Mode:     aiv1alpha1.ProjectEnvironmentModeArtifact,
					Bindings: []aiv1alpha1.ProjectProviderBindingSpec{binding("production")},
				},
				{
					// No resourceRef → not lifecycled.
					Name: "empty",
					Mode: aiv1alpha1.ProjectEnvironmentModeLive,
					Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
						Name: "unbound", Provider: "infrastructure",
						Kind: aiv1alpha1.ProjectBindingKindProviderResource,
					}},
				},
			},
		},
	}
	got := providerBindings(p)
	if len(got) != 2 {
		t.Fatalf("providerBindings = %d envs, want 2 (development + production)", len(got))
	}
	if got[0].spec.Name != "development" || got[1].spec.Name != "production" {
		t.Fatalf("selected envs = %s, %s", got[0].spec.Name, got[1].spec.Name)
	}
}

func TestEqualSpecAndMetaDetectsDrift(t *testing.T) {
	base := func() *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]any{
				"name":   "demo-dev",
				"labels": map[string]any{"app-studio.kedge.faros.sh/project": "demo"},
			},
			"spec": map[string]any{"webImage": "x"},
			// Instance-owned fields must not count as drift.
			"status": map[string]any{"phase": "Ready"},
		}}
	}

	same := base()
	if !equalSpecAndMeta(base(), same) {
		t.Fatal("identical objects reported as drifted")
	}

	specDrift := base()
	specDrift.Object["spec"] = map[string]any{"webImage": "y"}
	if equalSpecAndMeta(base(), specDrift) {
		t.Fatal("spec drift not detected")
	}

	labelDrift := base()
	labelDrift.SetLabels(map[string]string{"other": "label"})
	if equalSpecAndMeta(base(), labelDrift) {
		t.Fatal("label drift not detected")
	}

	statusOnly := base()
	statusOnly.Object["status"] = map[string]any{"phase": "Failed"}
	if !equalSpecAndMeta(base(), statusOnly) {
		t.Fatal("status-only difference must not count as drift")
	}
}
