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

func TestEnsureProviderServePropagatesPlatformPreviewBridgeJWKS(t *testing.T) {
	const jwks = `{"keys":[{"kid":"current"}]}`
	t.Setenv("FAROS_PREVIEW_BRIDGE_VERIFICATION_JWKS", "  "+jwks+"  ")
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
		if env.Name == "FAROS_PREVIEW_BRIDGE_VERIFICATION_JWKS" {
			if env.Value != jwks {
				t.Errorf("verification JWKS = %q, want trimmed platform value %q", env.Value, jwks)
			}
			return
		}
	}
	t.Error("managed provider Deployment lacks FAROS_PREVIEW_BRIDGE_VERIFICATION_JWKS")
}

func TestEnsureProviderServePropagatesPlatformPublishingConfig(t *testing.T) {
	client := fake.NewSimpleClientset()
	provider := &v1alpha1.InfrastructureProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "test-infrastructure"},
		Spec: v1alpha1.InfrastructureProviderSpec{
			Hub: v1alpha1.HubSpec{URL: "https://heartbeat-hub.internal"},
			Provider: v1alpha1.ProviderServeSpec{
				Image: v1alpha1.ImageSpec{Repository: "example.test/infrastructure", Tag: "test"},
			},
			Application: v1alpha1.ApplicationSpec{BaseDomain: "legacy.example.test"},
			Publishing: v1alpha1.PublishingSpec{
				BaseDomain: "apps.example.test", AccessProxyImage: "example.test/access-proxy@sha256:deadbeef",
				HubURL: "https://access-hub.internal", HubInsecure: true, PublicScheme: "https", PublicPort: 10443,
				Gateway: v1alpha1.GatewayRef{Name: "shared", Namespace: "gateway-system"},
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
	env := map[string]string{}
	counts := map[string]int{}
	for _, variable := range deployment.Spec.Template.Spec.Containers[0].Env {
		env[variable.Name] = variable.Value
		counts[variable.Name]++
	}
	want := map[string]string{
		"FAROS_APP_BASE_DOMAIN":      "apps.example.test",
		"FAROS_ACCESS_PROXY_IMAGE":   "example.test/access-proxy@sha256:deadbeef",
		"FAROS_ACCESS_HUB_URL":       "https://access-hub.internal",
		"FAROS_ACCESS_HUB_INSECURE":  "true",
		"FAROS_ACCESS_PUBLIC_SCHEME": "https",
		"FAROS_APP_PUBLIC_PORT":      "10443",
		"FAROS_GATEWAY_NAME":         "shared",
		"FAROS_GATEWAY_NAMESPACE":    "gateway-system",
	}
	for name, value := range want {
		if env[name] != value {
			t.Errorf("%s = %q, want %q", name, env[name], value)
		}
		if counts[name] != 1 {
			t.Errorf("%s occurs %d times, want once", name, counts[name])
		}
	}
}

func TestEnsureProviderServePropagatesCodingSandboxConfig(t *testing.T) {
	client := fake.NewSimpleClientset()
	provider := &v1alpha1.InfrastructureProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "test-infrastructure"},
		Spec: v1alpha1.InfrastructureProviderSpec{
			CodingSandbox: v1alpha1.CodingSandboxSpec{Enabled: true},
			Development: v1alpha1.DevelopmentSpec{
				AgentImage: "example.test/dev-agent@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				Images: map[string]string{
					"universal": "example.test/universal@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
			},
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
	env := map[string]string{}
	for _, variable := range deployment.Spec.Template.Spec.Containers[0].Env {
		env[variable.Name] = variable.Value
	}
	if got := env["FAROS_CODING_SANDBOX_ENABLED"]; got != "true" {
		t.Errorf("FAROS_CODING_SANDBOX_ENABLED = %q, want true", got)
	}
	if got := env["FAROS_DEV_IMAGE_UNIVERSAL"]; got != provider.Spec.Development.Images["universal"] {
		t.Errorf("FAROS_DEV_IMAGE_UNIVERSAL = %q, want %q", got, provider.Spec.Development.Images["universal"])
	}
	if got := env["FAROS_DEV_AGENT_IMAGE"]; got != provider.Spec.Development.AgentImage {
		t.Errorf("FAROS_DEV_AGENT_IMAGE = %q, want %q", got, provider.Spec.Development.AgentImage)
	}
}
