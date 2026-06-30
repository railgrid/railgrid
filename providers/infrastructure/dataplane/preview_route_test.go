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

package dataplane

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

func TestPreviewRouteManagerCreatesGrantFromRealizedRouteNamespace(t *testing.T) {
	runnerName := "kedge-sandbox-1e81e471f80f99ed"
	routeNamespace := "2bul5qspgunhky27-default"
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(),
		testHTTPRoute(runnerName, routeNamespace, "kedge-preview", "sandbox-preview-gateway", 8080, "True"),
	)
	manager := NewPreviewRouteManager(client, PreviewRouteConfig{
		BaseDomain:       "preview.localhost",
		ParentGateway:    GatewayParentRef{Name: "app-studio-preview", Namespace: "envoy-gateway-system", SectionName: "https"},
		BackendNamespace: "kedge-preview",
		BackendService:   "sandbox-preview-gateway",
		BackendPort:      8080,
	})

	if err := manager.Ensure(context.Background(), testPreviewRouteInstance(runnerName, routeNamespace)); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}

	grant, err := client.Resource(referenceGrantGVR).Namespace("kedge-preview").Get(context.Background(), runnerName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ReferenceGrant: %v", err)
	}
	from, _, _ := unstructured.NestedSlice(grant.Object, "spec", "from")
	gotFrom := from[0].(map[string]any)
	if gotFrom["namespace"] != routeNamespace {
		t.Fatalf("grant from namespace = %q, want %q", gotFrom["namespace"], routeNamespace)
	}
	to, _, _ := unstructured.NestedSlice(grant.Object, "spec", "to")
	gotTo := to[0].(map[string]any)
	if gotTo["name"] != "sandbox-preview-gateway" {
		t.Fatalf("grant to service = %q, want sandbox-preview-gateway", gotTo["name"])
	}
}

func TestPreviewRouteManagerRejectsUnexpectedBackend(t *testing.T) {
	runnerName := "kedge-sandbox-1e81e471f80f99ed"
	routeNamespace := "2bul5qspgunhky27-default"
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(),
		testHTTPRoute(runnerName, routeNamespace, "kube-system", "kube-dns", 53, "True"),
	)
	manager := NewPreviewRouteManager(client, PreviewRouteConfig{
		BaseDomain:       "preview.localhost",
		ParentGateway:    GatewayParentRef{Name: "app-studio-preview", Namespace: "envoy-gateway-system", SectionName: "https"},
		BackendNamespace: "kedge-preview",
		BackendService:   "sandbox-preview-gateway",
		BackendPort:      8080,
	})

	err := manager.Ensure(context.Background(), testPreviewRouteInstance(runnerName, routeNamespace))
	if err == nil || !strings.Contains(err.Error(), "unexpected backend") {
		t.Fatalf("Ensure error = %v, want unexpected backend", err)
	}
	if _, err := client.Resource(referenceGrantGVR).Namespace("kedge-preview").Get(context.Background(), runnerName, metav1.GetOptions{}); err == nil {
		t.Fatal("ReferenceGrant was created for an unexpected backend")
	}
}

func TestPreviewRouteManagerReportsUnresolvedRouteAsNotReady(t *testing.T) {
	runnerName := "kedge-sandbox-1e81e471f80f99ed"
	routeNamespace := "2bul5qspgunhky27-default"
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(),
		testHTTPRoute(runnerName, routeNamespace, "kedge-preview", "sandbox-preview-gateway", 8080, "False"),
	)
	manager := NewPreviewRouteManager(client, PreviewRouteConfig{
		BaseDomain:       "preview.localhost",
		ParentGateway:    GatewayParentRef{Name: "app-studio-preview", Namespace: "envoy-gateway-system", SectionName: "https"},
		BackendNamespace: "kedge-preview",
		BackendService:   "sandbox-preview-gateway",
		BackendPort:      8080,
	})

	err := manager.Ensure(context.Background(), testPreviewRouteInstance(runnerName, routeNamespace))
	if !IsPreviewRouteNotReady(err) {
		t.Fatalf("Ensure error = %v, want preview route not ready", err)
	}
}

func TestPreviewRouteManagerReportsUnacceptedRouteAsNotReady(t *testing.T) {
	runnerName := "kedge-sandbox-1e81e471f80f99ed"
	routeNamespace := "2bul5qspgunhky27-default"
	route := testHTTPRoute(runnerName, routeNamespace, "kedge-preview", "sandbox-preview-gateway", 8080, "True")
	route.Object["status"].(map[string]any)["parents"] = []any{
		map[string]any{
			"conditions": []any{
				map[string]any{"type": "Accepted", "status": "False"},
				map[string]any{"type": "ResolvedRefs", "status": "True"},
			},
		},
	}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), route)
	manager := NewPreviewRouteManager(client, PreviewRouteConfig{
		BaseDomain:       "preview.localhost",
		ParentGateway:    GatewayParentRef{Name: "app-studio-preview", Namespace: "envoy-gateway-system", SectionName: "https"},
		BackendNamespace: "kedge-preview",
		BackendService:   "sandbox-preview-gateway",
		BackendPort:      8080,
	})

	err := manager.Ensure(context.Background(), testPreviewRouteInstance(runnerName, routeNamespace))
	if !IsPreviewRouteNotReady(err) {
		t.Fatalf("Ensure error = %v, want preview route not ready", err)
	}
}

func testPreviewRouteInstance(name, routeNamespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
		"kind":       "SandboxRunner",
		"metadata":   map[string]any{"name": name},
		"status": map[string]any{
			"previewRoute": map[string]any{
				"host": name + ".preview.localhost",
				"httpRouteRef": map[string]any{
					"name":      name,
					"namespace": routeNamespace,
				},
				"gatewayRef": map[string]any{
					"name":      "app-studio-preview",
					"namespace": "envoy-gateway-system",
				},
			},
		},
	}}
}

func testHTTPRoute(name, namespace, backendNamespace, backendName string, backendPort int64, resolvedRefs string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]any{
			"parentRefs": []any{
				map[string]any{
					"name":        "app-studio-preview",
					"namespace":   "envoy-gateway-system",
					"sectionName": "https",
				},
			},
			"hostnames": []any{name + ".preview.localhost"},
			"rules": []any{
				map[string]any{
					"backendRefs": []any{
						map[string]any{
							"name":      backendName,
							"namespace": backendNamespace,
							"port":      backendPort,
							"kind":      "Service",
						},
					},
				},
			},
		},
		"status": map[string]any{
			"parents": []any{
				map[string]any{
					"conditions": []any{
						map[string]any{
							"type":   "Accepted",
							"status": "True",
						},
						map[string]any{
							"type":   "ResolvedRefs",
							"status": resolvedRefs,
						},
					},
				},
			},
		},
	}}
}
