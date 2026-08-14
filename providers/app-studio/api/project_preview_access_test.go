/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
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
