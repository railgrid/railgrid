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

func TestEnsureProviderServePropagatesPlatformPreviewConsoleJWKS(t *testing.T) {
	const jwks = `{"keys":[{"kid":"current"}]}`
	t.Setenv("KEDGE_PREVIEW_CONSOLE_VERIFICATION_JWKS", "  "+jwks+"  ")
	client := fake.NewSimpleClientset()
	provider := &v1alpha1.InfrastructureProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "test-infrastructure"},
		Spec: v1alpha1.InfrastructureProviderSpec{
			Provider: v1alpha1.ProviderServeSpec{
				Image: v1alpha1.ImageSpec{Repository: "example.test/infrastructure", Tag: "test"},
			},
		},
	}

	if err := EnsureProviderServe(context.Background(), client, provider, []byte("provider-kubeconfig"), nil, nil); err != nil {
		t.Fatalf("EnsureProviderServe: %v", err)
	}
	deployment, err := client.AppsV1().Deployments(ServeNamespace).Get(
		context.Background(),
		provider.Name,
		metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("get managed provider Deployment: %v", err)
	}
	for _, env := range deployment.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "KEDGE_PREVIEW_CONSOLE_VERIFICATION_JWKS" {
			if env.Value != jwks {
				t.Errorf("verification JWKS = %q, want trimmed platform value %q", env.Value, jwks)
			}
			return
		}
	}
	t.Error("managed provider Deployment lacks KEDGE_PREVIEW_CONSOLE_VERIFICATION_JWKS")
}
