/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
	asclient "github.com/faroshq/provider-app-studio/client"
)

// An empty request body must preserve the current mode. A POST that means
// "re-apply" cannot be allowed to widen access as a side effect.
func TestRequestedPreviewModePreservesCurrentOnEmptyBody(t *testing.T) {
	public := &aiv1alpha1.Project{Spec: aiv1alpha1.ProjectSpec{
		Sharing: aiv1alpha1.ProjectSharingSpec{Preview: aiv1alpha1.ProjectPreviewSharingPolicy{Mode: aiv1alpha1.ProjectSharingModePublic}},
	}}
	if got, err := requestedPreviewMode("", public); err != nil || got != aiv1alpha1.ProjectSharingModePublic {
		t.Fatalf("empty body on a public preview = %q, %v; want public preserved", got, err)
	}
	if got, err := requestedPreviewMode("", &aiv1alpha1.Project{}); err != nil || got != aiv1alpha1.ProjectSharingModePrivate {
		t.Fatalf("empty body on an unset preview = %q, %v; want private", got, err)
	}
}

func TestRequestedPreviewModeVocabulary(t *testing.T) {
	for _, in := range []string{"restricted", "members", "private", "PRIVATE", " Restricted "} {
		if got, err := requestedPreviewMode(in, nil); err != nil || got != aiv1alpha1.ProjectSharingModePrivate {
			t.Errorf("%q = %q, %v; want private", in, got, err)
		}
	}
	if got, err := requestedPreviewMode("public", nil); err != nil || got != aiv1alpha1.ProjectSharingModePublic {
		t.Errorf("public = %q, %v", got, err)
	}
	if _, err := requestedPreviewMode("everyone", nil); err == nil {
		t.Error("unknown mode accepted")
	}
}

// The grant machinery refuses grants on a public channel. Preview desiredAccess
// therefore has to come from the Project policy, not the binding values: reading
// the binding reports the pre-toggle value until the reconciler catches up, and
// a user who just switched to Restricted would be told the app is public.
func TestPreviewRuntimeDesiredAccessTracksPolicyNotBinding(t *testing.T) {
	private := &aiv1alpha1.Project{Spec: aiv1alpha1.ProjectSpec{
		Sharing: aiv1alpha1.ProjectSharingSpec{Preview: aiv1alpha1.ProjectPreviewSharingPolicy{Mode: aiv1alpha1.ProjectSharingModePrivate}},
	}}
	if got := bindings.PreviewAccess(private); got != accessPrivate {
		t.Fatalf("PreviewAccess = %q, want %q so grants are permitted", got, accessPrivate)
	}
	public := &aiv1alpha1.Project{Spec: aiv1alpha1.ProjectSpec{
		Sharing: aiv1alpha1.ProjectSharingSpec{Preview: aiv1alpha1.ProjectPreviewSharingPolicy{Mode: aiv1alpha1.ProjectSharingModePublic}},
	}}
	if got := bindings.PreviewAccess(public); got != accessPublic {
		t.Fatalf("PreviewAccess = %q, want %q so grants are refused", got, accessPublic)
	}
}

func previewTestProject(name, uid string, mode aiv1alpha1.ProjectSharingMode) *unstructured.Unstructured {
	project := &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid)},
		Spec: aiv1alpha1.ProjectSpec{
			Template: &aiv1alpha1.ProjectTemplateSpec{Name: "application"},
			Sharing:  aiv1alpha1.ProjectSharingSpec{Preview: aiv1alpha1.ProjectPreviewSharingPolicy{Mode: mode}},
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
				Name: projectDevelopmentEnvironmentName,
				Mode: aiv1alpha1.ProjectEnvironmentModeLive,
				Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
					Name:     projectDevelopmentBindingName,
					Provider: "app-studio",
					Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
					ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
						Name: name + "-dev", APIVersion: publishingTestTargetGVR.GroupVersion().String(), Kind: "Instance", Resource: "instances",
					},
					Values: rawJSONForPublishing(map[string]any{"name": name + "-dev", "access": "public"}),
				}},
			}},
		},
	}
	object, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(project)
	return &unstructured.Unstructured{Object: object}
}

// DELETE /preview is "reset to private": the mode goes back to private AND
// every preview grant is dropped, mirroring DELETE /publishing. POST
// {mode:"restricted"} alone would leave the invited people in.
func TestResetPreviewAccessGoesPrivateAndRemovesGrants(t *testing.T) {
	dyn := publishingTestDynamic(
		previewTestProject("demo", "project-uid", aiv1alpha1.ProjectSharingModePublic),
		publishingTestTarget("demo-dev", "runtime-uid-1", "public", "https://demo-dev-abc.apps.test"),
		staleGrantBinding("demo-dev", "bob"),
	)
	router := publishingTestServer(t, dyn)
	rec := publishingDo(t, router, http.MethodDelete, "/api/projects/demo/preview", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d: %s", rec.Code, rec.Body.String())
	}
	var body projectPreviewAccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Mode != "restricted" || !body.Supported || body.Converged || len(body.Grants) != 0 {
		t.Fatalf("reset response = %+v, want restricted, supported, not yet converged, no grants", body)
	}
	stored, err := dyn.Resource(asclient.ProjectGVR).Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Project: %v", err)
	}
	if mode, _, _ := unstructured.NestedString(stored.Object, "spec", "sharing", "preview", "mode"); mode != string(aiv1alpha1.ProjectSharingModePrivate) {
		t.Fatalf("stored preview mode = %q, want private", mode)
	}
	if _, err := dyn.Resource(clusterRoleBindingGVR).Get(context.Background(), appAccessBindingName("demo-dev", "bob"), metav1.GetOptions{}); err == nil {
		t.Fatal("preview grant survived the reset")
	}
	// The development instance itself is untouched; the reconciler converges it.
	if _, err := dyn.Resource(publishingTestTargetGVR).Get(context.Background(), "demo-dev", metav1.GetOptions{}); err != nil {
		t.Fatalf("development instance was touched by the reset: %v", err)
	}
}

// A project without a development environment has nothing to drop; the reset
// still records the private policy instead of failing.
func TestResetPreviewAccessWithoutDevelopmentEnvironment(t *testing.T) {
	dyn := publishingTestDynamic(publishingTestProject("demo", "project-uid", ""))
	router := publishingTestServer(t, dyn)
	rec := publishingDo(t, router, http.MethodDelete, "/api/projects/demo/preview", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d: %s", rec.Code, rec.Body.String())
	}
	var body projectPreviewAccessResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Mode != "restricted" || body.Supported {
		t.Fatalf("reset response = %+v, want restricted and unsupported", body)
	}
}
