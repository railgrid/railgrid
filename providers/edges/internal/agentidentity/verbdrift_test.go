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

// This file is the EXTERNAL test package on purpose: internal/tunnel imports
// agentidentity to mint an agent's credential, so only a test outside the
// package under test may import the tunnel back without a cycle.
package agentidentity_test

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
	"github.com/railgrid/provider-edges/internal/agentidentity"
	"github.com/railgrid/provider-edges/internal/tunnel"
)

// The granted verb set and the served verb matrix are one fact seen from two
// sides, and drift between them fails in two different directions, neither of
// them loud:
//
//   - a verb GRANTED but not SERVED makes the hub's identity policy refuse to
//     mint the capability (clause C verifies the owning provider declares the
//     coordinate), so the whole identity request fails and no agent on any edge
//     can get a credential. That is exactly what happened when runner-auth and
//     runner-token outlived the handlers that served them.
//   - a verb SERVED but not GRANTED is a hard 403 on the agent's first call to
//     it, discovered in production rather than here.
//
// So the two sets must be EQUAL, not merely overlapping. grammar.go is the
// authority — it is the in-process gate that 404s an unserved coordinate before
// any object is touched — and manifest.yaml is already pinned to grammar.go by
// TestDeclaredDataPlaneVerbsMatchWhatIsServed, so pinning the grant here pins
// all three.
func TestGrantedVerbsAreServed(t *testing.T) {
	granted := append([]string(nil), agentidentity.DataPlaneVerbs...)
	sort.Strings(granted)

	// The served set is the union over every resource: the grant is per-agent
	// rather than per-kind (a coordinate its own kind does not serve simply
	// 404s), so the union is what "served by this provider" means here.
	servedSet := map[string]bool{}
	for _, verbs := range tunnel.DataPlaneVerbs() {
		for _, verb := range verbs {
			servedSet[verb] = true
		}
	}
	served := make([]string, 0, len(servedSet))
	for verb := range servedSet {
		served = append(served, verb)
	}
	sort.Strings(served)

	if strings.Join(granted, ",") != strings.Join(served, ",") {
		t.Errorf("agentidentity.DataPlaneVerbs grants %v, grammar.go serves %v", granted, served)
		for _, verb := range granted {
			if !servedSet[verb] {
				t.Errorf("granted %q is served by nobody: the hub's policy will refuse the whole identity request", verb)
			}
		}
		for _, verb := range served {
			if !slices.Contains(granted, verb) {
				t.Errorf("served %q is not granted: the agent's first call to it is a 403", verb)
			}
		}
	}
}

// Every coordinate the Rules ask for must be one grammar.go serves on THAT
// resource or a non-verb subresource (/status) — a verb rule naming a resource
// that does not serve it is dead grant surface, and dead grant surface is how
// the runner-auth drift survived a refactor.
func TestRuleSubresourcesAreVerbsOrStatus(t *testing.T) {
	anywhere := map[string]bool{}
	for _, verbs := range tunnel.DataPlaneVerbs() {
		for _, verb := range verbs {
			anywhere[verb] = true
		}
	}
	for _, gvr := range []schema.GroupVersionResource{
		edgesv1alpha1.KubernetesClusterGVR,
		edgesv1alpha1.LinuxServerGVR,
		edgesv1alpha1.MacOSServerGVR,
	} {
		resource := gvr.Resource
		for _, rule := range agentidentity.Rules(gvr, "build") {
			for _, res := range rule.Resources {
				name, sub, ok := strings.Cut(res, "/")
				if !ok || sub == "status" {
					continue
				}
				if !anywhere[sub] {
					t.Errorf("%s: rule names %q, which grammar.go serves on no resource", resource, res)
				}
				if name != resource {
					t.Errorf("%s: rule names another kind's coordinate %q", resource, res)
				}
			}
		}
	}
}
