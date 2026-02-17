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

package reconciler

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
)

func TestConvertToDeployment_Simple(t *testing.T) {
	replicas := int32(3)
	vw := &kedgev1alpha1.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-test"},
		Spec: kedgev1alpha1.VirtualWorkloadSpec{
			Simple: &kedgev1alpha1.SimpleWorkloadSpec{
				Image: "nginx:latest",
				Ports: []corev1.ContainerPort{
					{ContainerPort: 80},
				},
			},
			Replicas: &replicas,
		},
	}

	placement := &kedgev1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nginx-test-site1",
			UID:  "test-uid",
		},
		Spec: kedgev1alpha1.PlacementObjSpec{
			SiteName: "site1",
		},
	}

	deploy, err := ConvertToDeployment(vw, placement)
	if err != nil {
		t.Fatal(err)
	}

	if deploy.Name != "nginx-test" {
		t.Errorf("name = %q, want %q", deploy.Name, "nginx-test")
	}
	if *deploy.Spec.Replicas != 3 {
		t.Errorf("replicas = %d, want 3", *deploy.Spec.Replicas)
	}
	if len(deploy.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(deploy.Spec.Template.Spec.Containers))
	}
	if deploy.Spec.Template.Spec.Containers[0].Image != "nginx:latest" {
		t.Errorf("image = %q, want %q", deploy.Spec.Template.Spec.Containers[0].Image, "nginx:latest")
	}
	if len(deploy.Spec.Template.Spec.Containers[0].Ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(deploy.Spec.Template.Spec.Containers[0].Ports))
	}
}

func TestConvertToDeployment_Template(t *testing.T) {
	vw := &kedgev1alpha1.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-app"},
		Spec: kedgev1alpha1.VirtualWorkloadSpec{
			Template: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "myapp:v1",
						},
						{
							Name:  "sidecar",
							Image: "sidecar:v1",
						},
					},
				},
			},
		},
	}

	placement := &kedgev1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-app-site1"},
		Spec: kedgev1alpha1.PlacementObjSpec{
			SiteName: "site1",
		},
	}

	deploy, err := ConvertToDeployment(vw, placement)
	if err != nil {
		t.Fatal(err)
	}

	if len(deploy.Spec.Template.Spec.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(deploy.Spec.Template.Spec.Containers))
	}
}

func TestConvertToDeployment_NoSpec(t *testing.T) {
	vw := &kedgev1alpha1.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "empty"},
		Spec:       kedgev1alpha1.VirtualWorkloadSpec{},
	}

	placement := &kedgev1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{Name: "empty-site1"},
		Spec: kedgev1alpha1.PlacementObjSpec{
			SiteName: "site1",
		},
	}

	_, err := ConvertToDeployment(vw, placement)
	if err == nil {
		t.Error("expected error for VirtualWorkload with no simple or template spec")
	}
}

func TestConvertToDeployment_PlacementReplicaOverride(t *testing.T) {
	vwReplicas := int32(5)
	placementReplicas := int32(2)

	vw := &kedgev1alpha1.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "app"},
		Spec: kedgev1alpha1.VirtualWorkloadSpec{
			Simple:   &kedgev1alpha1.SimpleWorkloadSpec{Image: "app:v1"},
			Replicas: &vwReplicas,
		},
	}

	placement := &kedgev1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{Name: "app-site1"},
		Spec: kedgev1alpha1.PlacementObjSpec{
			SiteName: "site1",
			Replicas: &placementReplicas,
		},
	}

	deploy, err := ConvertToDeployment(vw, placement)
	if err != nil {
		t.Fatal(err)
	}

	// Placement replicas should override VW replicas
	if *deploy.Spec.Replicas != 2 {
		t.Errorf("replicas = %d, want 2 (placement override)", *deploy.Spec.Replicas)
	}
}
