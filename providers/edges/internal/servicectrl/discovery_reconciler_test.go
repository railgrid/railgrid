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

package servicectrl

import (
	"testing"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
)

func TestDiscoveredServiceNamesSeparateSameNamedLinuxAndMacEdges(t *testing.T) {
	ha := discoveredService{Type: "HomeAssistant"}
	linux, _ := discoveredName(edgesv1alpha1.LinuxServerResource, "build", ha)
	mac, _ := discoveredName(edgesv1alpha1.MacOSServerResource, "build", ha)

	if linux != "build-homeassistant" {
		t.Fatalf("LinuxServer discovered name = %q, want %q", linux, "build-homeassistant")
	}
	if mac != "macos-edge-build-homeassistant" {
		t.Fatalf("MacOSServer discovered name = %q, want %q", mac, "macos-edge-build-homeassistant")
	}
	if linux == mac {
		t.Fatalf("same-named LinuxServer and MacOSServer discovered Services collide: %q", linux)
	}
}

// One machine supervises one runner per enabled harness, so the two must land on
// two Service objects. Naming them both "<edge>-runner" would make the second
// upsert overwrite the first and the pair would flap between harnesses forever.
func TestRunnerServicesAreNamedAfterTheirHarness(t *testing.T) {
	runner := string(edgesv1alpha1.ServiceTypeRunner)

	claude, ok := discoveredName(edgesv1alpha1.LinuxServerResource, "build", discoveredService{Type: runner, Harness: edgesv1alpha1.HarnessClaude})
	if !ok || claude != "build-claude" {
		t.Fatalf("claude runner name = %q (ok=%v), want %q", claude, ok, "build-claude")
	}
	codex, ok := discoveredName(edgesv1alpha1.LinuxServerResource, "build", discoveredService{Type: runner, Harness: edgesv1alpha1.HarnessCodex})
	if !ok || codex != "build-codex" {
		t.Fatalf("codex runner name = %q (ok=%v), want %q", codex, ok, "build-codex")
	}
	if claude == codex {
		t.Fatal("both harnesses on one machine resolved to a single Service name")
	}
	if mac, ok := discoveredName(edgesv1alpha1.MacOSServerResource, "build", discoveredService{Type: runner, Harness: edgesv1alpha1.HarnessClaude}); !ok || mac != "macos-edge-build-claude" {
		t.Fatalf("MacOSServer runner name = %q (ok=%v), want %q", mac, ok, "macos-edge-build-claude")
	}

	// A runner advertisement with no harness is not publishable: any name we
	// invented for it could collide with the machine's real runners.
	if name, ok := discoveredName(edgesv1alpha1.LinuxServerResource, "build", discoveredService{Type: runner}); ok {
		t.Fatalf("a harness-less runner was named %q; it must be skipped", name)
	}
}

// The harness status seeded at discovery is what a reader sees before the first
// probe; it must exist for a runner and must NOT appear on anything else.
func TestAdvertisedHarnessIsRunnerOnly(t *testing.T) {
	harness := advertisedHarness(discoveredService{
		Type: string(edgesv1alpha1.ServiceTypeRunner), Harness: edgesv1alpha1.HarnessCodex,
		Ready: false, Reasons: []string{"harness not installed"},
	})
	if harness == nil || harness.Name != edgesv1alpha1.HarnessCodex || harness.Ready {
		t.Fatalf("runner harness status = %+v, want codex and not ready", harness)
	}
	if len(harness.Reasons) != 1 {
		t.Fatalf("the agent's reasons were dropped: %+v", harness.Reasons)
	}
	if got := advertisedHarness(discoveredService{Type: "home-assistant"}); got != nil {
		t.Fatalf("a non-runner Service grew a harness status: %+v", got)
	}
}

func TestServiceTargetKindsStayAlignedWithTunnelResources(t *testing.T) {
	for _, tc := range []struct {
		kind     string
		resource string
	}{
		{kind: "", resource: edgesv1alpha1.LinuxServerResource},
		{kind: linuxServerKind, resource: edgesv1alpha1.LinuxServerResource},
		{kind: macOSServerKind, resource: edgesv1alpha1.MacOSServerResource},
		{kind: kubernetesClusterKind, resource: edgesv1alpha1.KubernetesClusterResource},
	} {
		es := &edgesv1alpha1.Service{}
		es.Spec.EdgeRef.Kind = tc.kind
		if got := connResource(es); got != tc.resource {
			t.Errorf("connResource(%q) = %q, want %q", tc.kind, got, tc.resource)
		}
		if !supportedEdgeKind(es) {
			t.Errorf("supportedEdgeKind(%q) = false, want true", tc.kind)
		}
	}

	unknown := &edgesv1alpha1.Service{}
	unknown.Spec.EdgeRef.Kind = "UnexpectedKind"
	if got := connResource(unknown); got != "" || supportedEdgeKind(unknown) {
		t.Fatalf("unknown Service edge kind resolved as resource %q or supported", got)
	}
}
