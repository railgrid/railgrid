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

package tunnel

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func testServer(edgeProxyPublicPath string) *Server {
	kube := schema.GroupVersionResource{Group: "edges.kedge.faros.sh", Version: "v1alpha1", Resource: "kubernetesclusters"}
	linux := schema.GroupVersionResource{Group: "edges.kedge.faros.sh", Version: "v1alpha1", Resource: "linuxservers"}
	return &Server{
		kinds: map[string]KindConfig{
			kube.Resource:  {GVR: kube, Kind: "KubernetesCluster"},
			linux.Resource: {GVR: linux, Kind: "LinuxServer"},
		},
		group:               "edges.kedge.faros.sh",
		version:             "v1alpha1",
		edgeProxyPublicPath: edgeProxyPublicPath,
	}
}

func TestEdgeProxyStatusURL(t *testing.T) {
	const base = "/services/providers/edges/edgeproxy"
	s := testServer(base)

	cases := []struct {
		name    string
		gvr     schema.GroupVersionResource
		cluster string
		obj     string
		want    string
	}{
		{
			name:    "kubernetes cluster maps to k8s subresource",
			gvr:     s.kinds["kubernetesclusters"].GVR,
			cluster: "11tcw27t4rdtnacy",
			obj:     "dev-edge-kube-1",
			want:    base + "/clusters/11tcw27t4rdtnacy/apis/edges.kedge.faros.sh/v1alpha1/kubernetesclusters/dev-edge-kube-1/k8s",
		},
		{
			name:    "linux server maps to ssh subresource",
			gvr:     s.kinds["linuxservers"].GVR,
			cluster: "11tcw27t4rdtnacy",
			obj:     "dev-edge-srv-1",
			want:    base + "/clusters/11tcw27t4rdtnacy/apis/edges.kedge.faros.sh/v1alpha1/linuxservers/dev-edge-srv-1/ssh",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.edgeProxyStatusURL(tc.gvr, tc.cluster, tc.obj)
			if got != tc.want {
				t.Fatalf("edgeProxyStatusURL()\n got  %q\n want %q", got, tc.want)
			}

			// The CLI externalizes status.URL against the hub host, then the
			// hub backend proxy strips /services/providers/edges and the
			// provider mux strips /edgeproxy — leaving the path parseEdgesProxyPath
			// must accept. Assert that round-trip so the inverse pair can't drift.
			stripped := strings.TrimPrefix(got, base)
			cluster, resource, name, subresource, ok := s.parseEdgesProxyPath(stripped)
			if !ok {
				t.Fatalf("parseEdgesProxyPath(%q) failed to parse the URL this Server produced", stripped)
			}
			if cluster != tc.cluster || resource != tc.gvr.Resource || name != tc.obj {
				t.Fatalf("round-trip mismatch: got cluster=%q resource=%q name=%q sub=%q",
					cluster, resource, name, subresource)
			}
		})
	}
}

func TestEdgeProxyStatusURLEmptyWhenUnconfigured(t *testing.T) {
	s := testServer("")
	if got := s.edgeProxyStatusURL(s.kinds["kubernetesclusters"].GVR, "c", "n"); got != "" {
		t.Fatalf("expected empty URL when edgeProxyPublicPath is unset, got %q", got)
	}
}
