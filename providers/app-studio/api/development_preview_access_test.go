/*
Copyright 2026 The Faros Authors.

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

package api

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

func TestProjectDevelopmentPreviewAuthorizationStateReportsAccessConvergence(t *testing.T) {
	target := projectDevelopmentSyncTargetInfo{PreviewAccessModes: []string{"private", "public"}}
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: aiv1alpha1.ProjectSpec{Sharing: aiv1alpha1.ProjectSharingSpec{
			Preview: aiv1alpha1.ProjectPreviewSharingPolicy{Mode: aiv1alpha1.ProjectSharingModePrivate},
		}},
	}
	ready := projectDevelopmentPreviewAuthorizationState(project, target, projectSandboxPreviewURLResponse{
		Ready: true, PreviewURL: "https://preview.example", ObservedAccess: "private",
	})
	if !ready.Ready || !ready.AccessConverged || ready.DesiredAccess != "private" || ready.ObservedAccess != "private" {
		t.Fatalf("ready response = %+v", ready)
	}

	pending := projectDevelopmentPreviewAuthorizationState(project, target, projectSandboxPreviewURLResponse{
		Ready: true, PreviewURL: "https://preview.example", ObservedAccess: "public",
	})
	if pending.Ready || pending.AccessConverged || pending.Reason != "preview_access_updating" || pending.Message != "Updating preview access…" {
		t.Fatalf("pending response = %+v", pending)
	}

	unsupported := projectDevelopmentPreviewAuthorizationState(project, projectDevelopmentSyncTargetInfo{}, projectSandboxPreviewURLResponse{Ready: true})
	if !unsupported.AccessConverged || unsupported.DesiredAccess != "" {
		t.Fatalf("unsupported response = %+v", unsupported)
	}
}
