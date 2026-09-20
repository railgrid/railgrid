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

package api

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

func TestProjectViewProjectsImmutableUIDAndDeletionMetadata(t *testing.T) {
	deletionTimestamp := metav1.Now()
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "todo",
			UID:               types.UID("project-old"),
			DeletionTimestamp: &deletionTimestamp,
		},
		Spec: aiv1alpha1.ProjectSpec{DisplayName: "Todo"},
	}

	view := projectView(context.Background(), nil, project, identity{})
	if got, want := view.UID, "project-old"; got != want {
		t.Fatalf("UID = %q, want %q", got, want)
	}
	if !view.Deleting {
		t.Fatal("Deleting = false, want true for metadata.deletionTimestamp")
	}

	project.DeletionTimestamp = nil
	view = projectView(context.Background(), nil, project, identity{})
	if view.Deleting {
		t.Fatal("Deleting = true, want false when metadata.deletionTimestamp is absent")
	}
	if got, want := view.UID, "project-old"; got != want {
		t.Fatalf("UID after status-only refresh = %q, want %q", got, want)
	}
}
