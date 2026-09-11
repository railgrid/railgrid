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

package servicectrl

import (
	"testing"

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
)

func TestDiscoveredServiceNamesSeparateSameNamedLinuxAndMacEdges(t *testing.T) {
	linux := discoveredName(edgesv1alpha1.LinuxServerResource, "build", "HomeAssistant")
	mac := discoveredName(edgesv1alpha1.MacOSServerResource, "build", "HomeAssistant")

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
