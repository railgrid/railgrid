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
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// applicationContract mirrors the dataPlane a multi-tier template declares:
// instance-level status/proxy verbs plus per-component control verbs under
// status.components.<name>.* (the backend's dev-overlay status convention).
func applicationContract() *infrav1alpha1.TemplateDataPlane {
	control := func(component, upstream, method string, stream bool) infrav1alpha1.TemplateDataPlaneEndpoint {
		return infrav1alpha1.TemplateDataPlaneEndpoint{
			ServicePath:  "status.components." + component + ".controlServiceRef",
			Port:         "control",
			UpstreamPath: upstream,
			Methods:      []string{method},
			Stream:       stream,
		}
	}
	return &infrav1alpha1.TemplateDataPlane{
		RuntimeNamespacePath: "status.runtimeNamespace",
		TokenSecretPath:      "status.controlSecretRef",
		Endpoints: map[string]infrav1alpha1.TemplateDataPlaneEndpoint{
			"status": {FromStatus: true},
			"proxy":  {ServicePath: "status.previewServiceRef", Port: "preview", UpstreamPath: "/", Methods: []string{"GET", "POST", "HEAD"}, Upgrade: true},
		},
		Components: map[string]infrav1alpha1.TemplateDataPlaneComponent{
			"frontend": {Endpoints: map[string]infrav1alpha1.TemplateDataPlaneEndpoint{
				"sync": control("frontend", "/sync", "POST", false),
				"log":  control("frontend", "/logs", "GET", true),
			}},
			"backend": {Endpoints: map[string]infrav1alpha1.TemplateDataPlaneEndpoint{
				"sync":    control("backend", "/sync", "POST", false),
				"restart": control("backend", "/restart", "POST", false),
			}},
		},
	}
}

// applicationInstance carries per-component control refs alongside the
// instance-level ones, all inside the runtime namespace.
func applicationInstance(runtimeNamespace string) *unstructured.Unstructured {
	ref := func(name string) map[string]any {
		return map[string]any{"name": name, "namespace": runtimeNamespace}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "shop"},
		"status": map[string]any{
			"runtimeNamespace":  runtimeNamespace,
			"previewServiceRef": ref("shop-frontend"),
			"controlSecretRef":  ref("shop-control"),
			"components": map[string]any{
				"frontend": map[string]any{"controlServiceRef": ref("shop-frontend-control")},
				"backend":  map[string]any{"controlServiceRef": ref("shop-backend-control")},
			},
		},
	}}
}

func TestResolveComponentVerbs(t *testing.T) {
	ns := "kedge-tenant-shop"
	contract := applicationContract()
	instance := applicationInstance(ns)

	for _, tc := range []struct {
		component, verb string
		wantService     string
		wantUpstream    string
	}{
		{"frontend", "sync", "shop-frontend-control", "/sync"},
		{"frontend", "log", "shop-frontend-control", "/logs"},
		{"backend", "sync", "shop-backend-control", "/sync"},
		{"backend", "restart", "shop-backend-control", "/restart"},
	} {
		got, err := ResolveComponent(contract, instance, tc.component, tc.verb)
		if err != nil {
			t.Fatalf("ResolveComponent(%s/%s) error: %v", tc.component, tc.verb, err)
		}
		if got.ServiceNamespace != ns || got.ServiceName != tc.wantService || got.ServicePort != "control" {
			t.Errorf("ResolveComponent(%s/%s): service = %s/%s:%s, want %s/%s:control",
				tc.component, tc.verb, got.ServiceNamespace, got.ServiceName, got.ServicePort, ns, tc.wantService)
		}
		if got.UpstreamPath != tc.wantUpstream {
			t.Errorf("ResolveComponent(%s/%s): upstreamPath = %q, want %q", tc.component, tc.verb, got.UpstreamPath, tc.wantUpstream)
		}
		// The instance-wide control token applies to component verbs too.
		if got.TokenSecretName != "shop-control" || got.TokenSecretNamespace != ns {
			t.Errorf("ResolveComponent(%s/%s): token secret = %s/%s, want %s/shop-control",
				tc.component, tc.verb, got.TokenSecretNamespace, got.TokenSecretName, ns)
		}
	}
}

func TestResolveComponentUnknown(t *testing.T) {
	contract := applicationContract()
	instance := applicationInstance("ns")

	if _, err := ResolveComponent(contract, instance, "worker", "sync"); err == nil {
		t.Fatal("ResolveComponent(worker/sync): expected error for an undeclared component, got nil")
	}
	if _, err := ResolveComponent(contract, instance, "frontend", "restart"); err == nil {
		t.Fatal("ResolveComponent(frontend/restart): expected error for an undeclared verb, got nil")
	}
	if _, err := ResolveComponent(nil, instance, "frontend", "sync"); err == nil {
		t.Fatal("ResolveComponent with nil contract: expected error, got nil")
	}
}

func TestResolveComponentRejectsNamespaceEscape(t *testing.T) {
	instance := applicationInstance("kedge-tenant-shop")
	unstructured.SetNestedField(instance.Object, "kube-system", "status", "components", "backend", "controlServiceRef", "namespace") //nolint:errcheck

	if _, err := ResolveComponent(applicationContract(), instance, "backend", "sync"); err == nil {
		t.Fatal("ResolveComponent(backend/sync): expected error for a ref escaping the runtime namespace, got nil")
	}
}

func TestComponentMethodAllowed(t *testing.T) {
	contract := applicationContract()
	for _, tc := range []struct {
		component, verb, method string
		want                    bool
	}{
		{"frontend", "sync", http.MethodPost, true},
		{"frontend", "sync", http.MethodGet, false},
		{"frontend", "log", http.MethodGet, true},
		{"backend", "restart", http.MethodPost, true},
		{"backend", "log", http.MethodGet, false},  // verb not declared on backend
		{"worker", "sync", http.MethodPost, false}, // unknown component
	} {
		if got := ComponentMethodAllowed(contract, tc.component, tc.verb, tc.method); got != tc.want {
			t.Errorf("ComponentMethodAllowed(%s/%s, %s) = %v, want %v", tc.component, tc.verb, tc.method, got, tc.want)
		}
	}
}
