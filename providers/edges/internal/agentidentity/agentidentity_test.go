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

package agentidentity

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
)

const testEdgeName = "build"

// The rules an edge agent's identity carries are the whole security boundary
// now: there is no ServiceAccount, ClusterRole or token Secret this provider
// writes, so what the hub is asked for IS the grant. Three properties are
// non-negotiable and are pinned here.
func TestRules(t *testing.T) {
	kinds := []struct {
		kind string
		gvr  schema.GroupVersionResource
	}{
		{"KubernetesCluster", edgesv1alpha1.KubernetesClusterGVR},
		{"LinuxServer", edgesv1alpha1.LinuxServerGVR},
		{"MacOSServer", edgesv1alpha1.MacOSServerGVR},
	}

	for _, k := range kinds {
		t.Run(k.kind, func(t *testing.T) {
			rules := Rules(k.gvr, testEdgeName)

			// 1. Clause A only. Every rule is on the group this provider
			// exports; the hub's policy admits the request as a whole or not
			// at all, and a foreign group would make it "not at all".
			//
			// The core group above all: identity policy X-4 refuses to mint a
			// Secrets rule for anyone, and the agent used to hold
			// get/create/update on Secrets plus get/create on Namespaces so it
			// could write its own SSH credentials. That write is a gated verb
			// now.
			for _, rule := range rules {
				for _, group := range rule.APIGroups {
					if group != k.gvr.Group {
						t.Errorf("rule on foreign group %q: %+v", group, rule)
					}
				}
				if slices.Contains(rule.Verbs, "*") || slices.Contains(rule.Resources, "*") {
					t.Errorf("wildcard rule: %+v", rule)
				}
			}

			// 2. Every per-edge verb is name-scoped to THIS edge, so one
			// edge's agent can do nothing to another's object — including a
			// same-named object of a different kind, which is why the
			// subresources carry the kind's own resource segment.
			var sawGet, sawCreate bool
			for _, rule := range rules {
				switch {
				case slices.Equal(rule.Verbs, []string{"get"}) && slices.Equal(rule.Resources, []string{k.gvr.Resource}):
					sawGet = true
					if !slices.Equal(rule.ResourceNames, []string{testEdgeName}) {
						t.Errorf("gate-1 get is not name-scoped: %+v", rule)
					}
				case slices.Equal(rule.Verbs, []string{"create"}):
					sawCreate = true
					if !slices.Equal(rule.ResourceNames, []string{testEdgeName}) {
						t.Errorf("data-plane verbs are not name-scoped: %+v", rule)
					}
					for _, resource := range rule.Resources {
						if !strings.HasPrefix(resource, k.gvr.Resource+"/") {
							t.Errorf("verb %q does not belong to %s", resource, k.gvr.Resource)
						}
					}
					want := make([]string, 0, len(DataPlaneVerbs)+1)
					if k.gvr.Resource == "macosservers" || k.gvr.Resource == "linuxservers" {
						want = append(want, k.gvr.Resource+"/addon-credentials")
					}
					for _, verb := range DataPlaneVerbs {
						want = append(want, k.gvr.Resource+"/"+verb)
					}
					if !slices.Equal(rule.Resources, want) {
						t.Errorf("declared verbs = %v, want %v", rule.Resources, want)
					}
				}
			}
			if !sawGet {
				t.Error("no gate-1 get rule: every data-plane call starts with a real GET of the edge, as the agent")
			}
			if !sawCreate {
				t.Error("no data-plane verb rule")
			}

			// 3. The two verbs the credential protocol itself depends on. If
			// agent-token were missing the agent could never refresh and would
			// lock itself out at TTL; if proxy were missing it could not
			// reconnect at all.
			for _, required := range []string{"agent-token", "proxy"} {
				if !slices.Contains(DataPlaneVerbs, required) {
					t.Errorf("the agent identity must carry %q", required)
				}
			}
		})
	}
}

// Credentials are kind-qualified: a LinuxServer named "build" and a
// KubernetesCluster named "build" share a workspace, and neither agent may
// reach the other's object.
func TestAgentIdentityRulesAreDisjointAcrossKinds(t *testing.T) {
	linux := Rules(edgesv1alpha1.LinuxServerGVR, testEdgeName)
	kube := Rules(edgesv1alpha1.KubernetesClusterGVR, testEdgeName)

	kubeResources := map[string]bool{}
	for _, rule := range kube {
		for _, resource := range rule.Resources {
			if strings.Contains(resource, "/") && strings.HasPrefix(resource, "kubernetesclusters/") {
				kubeResources[resource] = true
			}
		}
	}
	for _, rule := range linux {
		for _, resource := range rule.Resources {
			if kubeResources[resource] {
				t.Errorf("LinuxServer identity carries the KubernetesCluster coordinate %q", resource)
			}
		}
	}
}

// The owner tuple includes the UID, which is what makes a deleted-and-
// recreated edge get a new ServiceAccount instead of inheriting its
// predecessor's credential (the hub hashes the tuple into the SA name).
func TestAgentIdentityOwnerCarriesTheUID(t *testing.T) {
	owner := OwnerFor(edgesv1alpha1.LinuxServerGVR, "LinuxServer", testEdgeName, "uid-1")
	if owner.UID != "uid-1" {
		t.Fatalf("owner UID = %q, want the edge's UID", owner.UID)
	}
	if owner.Provider != ProviderName {
		t.Fatalf("owner provider = %q, want %q — the hub verifies this against the bearer", owner.Provider, ProviderName)
	}
	if owner.Resource != "linuxservers" || owner.Kind != "LinuxServer" {
		t.Fatalf("owner tuple = %+v, want the edge's own coordinates", owner)
	}
}
