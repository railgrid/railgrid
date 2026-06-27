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

// sandboxRunnerContract mirrors the dataPlane block annotated onto
// install/templates/sandbox-runner.yaml.
func sandboxRunnerContract() *infrav1alpha1.TemplateDataPlane {
	return &infrav1alpha1.TemplateDataPlane{
		RuntimeNamespacePath: "status.runtimeNamespace",
		TokenSecretPath:      "status.controlSecretRef",
		Endpoints: map[string]infrav1alpha1.TemplateDataPlaneEndpoint{
			"log":     {ServicePath: "status.controlServiceRef", Port: "control", UpstreamPath: "/logs", Methods: []string{"GET"}, Stream: true},
			"sync":    {ServicePath: "status.controlServiceRef", Port: "control", UpstreamPath: "/sync", Methods: []string{"POST"}},
			"restart": {ServicePath: "status.controlServiceRef", Port: "control", UpstreamPath: "/restart", Methods: []string{"POST"}},
			"proxy":   {ServicePath: "status.previewServiceRef", Port: "preview", UpstreamPath: "/", Methods: []string{"GET", "POST", "HEAD"}, Upgrade: true},
			"status":  {FromStatus: true},
		},
	}
}

// runnerInstance builds a SandboxRunner whose status carries the refs the kro
// backend publishes, all in the runner's runtime namespace.
func runnerInstance(runtimeNamespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
		"kind":       "SandboxRunner",
		"metadata":   map[string]any{"name": "kedge-sandbox-a1c31ddaaaa007d4"},
		"status": map[string]any{
			"runtimeNamespace":  runtimeNamespace,
			"controlServiceRef": map[string]any{"name": "kedge-sandbox-a1c31ddaaaa007d4-control", "namespace": runtimeNamespace},
			"previewServiceRef": map[string]any{"name": "kedge-sandbox-a1c31ddaaaa007d4-preview", "namespace": runtimeNamespace},
			"controlSecretRef":  map[string]any{"name": "kedge-sandbox-a1c31ddaaaa007d4-control", "namespace": runtimeNamespace},
		},
	}}
}

func TestResolveControlVerbs(t *testing.T) {
	ns := "kedge-sandbox-a1c31ddaaaa007d4"
	contract := sandboxRunnerContract()
	instance := runnerInstance(ns)

	for _, tc := range []struct {
		verb         string
		upstreamPath string
		stream       bool
	}{
		{"log", "/logs", true},
		{"sync", "/sync", false},
		{"restart", "/restart", false},
	} {
		got, err := Resolve(contract, instance, tc.verb)
		if err != nil {
			t.Fatalf("Resolve(%q) error: %v", tc.verb, err)
		}
		if got.FromStatus {
			t.Fatalf("Resolve(%q): unexpected FromStatus", tc.verb)
		}
		if got.ServiceNamespace != ns || got.ServiceName != ns+"-control" || got.ServicePort != "control" {
			t.Errorf("Resolve(%q): service = %s/%s:%s, want %s/%s-control:control", tc.verb, got.ServiceNamespace, got.ServiceName, got.ServicePort, ns, ns)
		}
		if got.UpstreamPath != tc.upstreamPath {
			t.Errorf("Resolve(%q): upstreamPath = %q, want %q", tc.verb, got.UpstreamPath, tc.upstreamPath)
		}
		if got.Stream != tc.stream {
			t.Errorf("Resolve(%q): stream = %v, want %v", tc.verb, got.Stream, tc.stream)
		}
		if got.TokenSecretNamespace != ns || got.TokenSecretName != ns+"-control" {
			t.Errorf("Resolve(%q): token secret = %s/%s, want %s/%s-control", tc.verb, got.TokenSecretNamespace, got.TokenSecretName, ns, ns)
		}
	}
}

func TestResolvePreviewProxy(t *testing.T) {
	ns := "kedge-sandbox-a1c31ddaaaa007d4"
	got, err := Resolve(sandboxRunnerContract(), runnerInstance(ns), "proxy")
	if err != nil {
		t.Fatalf("Resolve(proxy) error: %v", err)
	}
	if got.ServiceName != ns+"-preview" || got.ServicePort != "preview" {
		t.Errorf("Resolve(proxy): service = %s:%s, want %s-preview:preview", got.ServiceName, got.ServicePort, ns)
	}
	if !got.Upgrade {
		t.Errorf("Resolve(proxy): Upgrade = false, want true")
	}
	if got.UpstreamPath != "/" {
		t.Errorf("Resolve(proxy): upstreamPath = %q, want /", got.UpstreamPath)
	}
}

func TestResolveFromStatus(t *testing.T) {
	got, err := Resolve(sandboxRunnerContract(), runnerInstance("ns"), "status")
	if err != nil {
		t.Fatalf("Resolve(status) error: %v", err)
	}
	if !got.FromStatus {
		t.Errorf("Resolve(status): FromStatus = false, want true")
	}
	if got.ServiceName != "" {
		t.Errorf("Resolve(status): expected zero proxy fields, got service %q", got.ServiceName)
	}
}

func TestResolveRejectsNamespaceEscape(t *testing.T) {
	ns := "kedge-sandbox-a1c31ddaaaa007d4"
	instance := runnerInstance(ns)
	// Forge the control service ref to point at kube-system.
	unstructured.SetNestedField(instance.Object, "kube-system", "status", "controlServiceRef", "namespace") //nolint:errcheck

	if _, err := Resolve(sandboxRunnerContract(), instance, "log"); err == nil {
		t.Fatal("Resolve(log): expected error for a ref escaping the runtime namespace, got nil")
	}
}

func TestResolveRejectsTokenSecretEscape(t *testing.T) {
	ns := "kedge-sandbox-a1c31ddaaaa007d4"
	instance := runnerInstance(ns)
	unstructured.SetNestedField(instance.Object, "kube-system", "status", "controlSecretRef", "namespace") //nolint:errcheck

	if _, err := Resolve(sandboxRunnerContract(), instance, "log"); err == nil {
		t.Fatal("Resolve(log): expected error for a token secret escaping the runtime namespace, got nil")
	}
}

func TestResolveDefaultsRefNamespaceToRuntime(t *testing.T) {
	ns := "kedge-sandbox-a1c31ddaaaa007d4"
	instance := runnerInstance(ns)
	// A ref that omits its namespace defaults to the runtime namespace.
	unstructured.RemoveNestedField(instance.Object, "status", "controlServiceRef", "namespace")

	got, err := Resolve(sandboxRunnerContract(), instance, "log")
	if err != nil {
		t.Fatalf("Resolve(log) error: %v", err)
	}
	if got.ServiceNamespace != ns {
		t.Errorf("Resolve(log): service namespace = %q, want %q", got.ServiceNamespace, ns)
	}
}

func TestResolveNotReadyWhenRuntimeNamespaceAbsent(t *testing.T) {
	instance := runnerInstance("ns")
	unstructured.RemoveNestedField(instance.Object, "status", "runtimeNamespace")

	if _, err := Resolve(sandboxRunnerContract(), instance, "log"); err == nil {
		t.Fatal("Resolve(log): expected not-ready error when runtimeNamespace is absent, got nil")
	}
}

func TestResolveUnknownVerb(t *testing.T) {
	if _, err := Resolve(sandboxRunnerContract(), runnerInstance("ns"), "exec"); err == nil {
		t.Fatal("Resolve(exec): expected error for an undeclared verb, got nil")
	}
}

func TestResolveNilContract(t *testing.T) {
	if _, err := Resolve(nil, runnerInstance("ns"), "log"); err == nil {
		t.Fatal("Resolve with nil contract: expected error, got nil")
	}
}

func TestMethodAllowed(t *testing.T) {
	contract := sandboxRunnerContract()
	for _, tc := range []struct {
		verb, method string
		want         bool
	}{
		{"log", http.MethodGet, true},
		{"log", http.MethodPost, false},
		{"sync", http.MethodPost, true},
		{"sync", http.MethodGet, false},
		{"proxy", http.MethodHead, true},
		{"status", http.MethodGet, true}, // FromStatus, empty Methods => GET
		{"status", http.MethodPost, false},
		{"unknown", http.MethodGet, false},
	} {
		if got := MethodAllowed(contract, tc.verb, tc.method); got != tc.want {
			t.Errorf("MethodAllowed(%q, %q) = %v, want %v", tc.verb, tc.method, got, tc.want)
		}
	}
}
