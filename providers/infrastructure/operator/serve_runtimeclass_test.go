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

package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	v1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

func TestEnsureProviderServePropagatesSandboxRuntimeClass(t *testing.T) {
	for _, tt := range []struct {
		name        string
		sandbox     *v1alpha1.SandboxSpec
		want        string
		wantPresent bool
	}{
		{name: "set", sandbox: &v1alpha1.SandboxSpec{RuntimeClassName: "gvisor"}, want: "gvisor", wantPresent: true},
		{name: "trimmed", sandbox: &v1alpha1.SandboxSpec{RuntimeClassName: "  kata  "}, want: "kata", wantPresent: true},
		{name: "empty keeps cluster default", sandbox: &v1alpha1.SandboxSpec{RuntimeClassName: ""}, wantPresent: false},
		{name: "whitespace only keeps cluster default", sandbox: &v1alpha1.SandboxSpec{RuntimeClassName: "   "}, wantPresent: false},
		{name: "tabs and newlines only keep cluster default", sandbox: &v1alpha1.SandboxSpec{RuntimeClassName: "\t\n "}, wantPresent: false},
		{name: "absent block keeps cluster default", sandbox: nil, wantPresent: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			provider := &v1alpha1.InfrastructureProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "test-infrastructure"},
				Spec: v1alpha1.InfrastructureProviderSpec{
					Sandbox: tt.sandbox,
					Provider: v1alpha1.ProviderServeSpec{
						Image: v1alpha1.ImageSpec{Repository: "example.test/infrastructure", Tag: "test"},
					},
				},
			}
			if err := EnsureProviderServe(context.Background(), client, provider, []byte("provider-kubeconfig"), nil, nil); err != nil {
				t.Fatalf("EnsureProviderServe: %v", err)
			}
			deployment, err := client.AppsV1().Deployments(ServeNamespace).Get(context.Background(), provider.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, present := "", false
			for _, variable := range deployment.Spec.Template.Spec.Containers[0].Env {
				if variable.Name == "FAROS_SANDBOX_RUNTIME_CLASS_NAME" {
					got, present = variable.Value, true
				}
			}
			if present != tt.wantPresent || got != tt.want {
				t.Fatalf("FAROS_SANDBOX_RUNTIME_CLASS_NAME = %q (present=%t), want %q (present=%t)", got, present, tt.want, tt.wantPresent)
			}
		})
	}
}
