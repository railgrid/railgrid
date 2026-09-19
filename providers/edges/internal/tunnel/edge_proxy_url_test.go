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

package tunnel

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"
)

func testServer(edgeProxyPublicPath string) *Server {
	kube := schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "kubernetesclusters"}
	linux := schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "linuxservers"}
	mac := schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "macosservers"}
	return &Server{
		kinds: map[string]KindConfig{
			kube.Resource:  {GVR: kube, Kind: "KubernetesCluster"},
			linux.Resource: {GVR: linux, Kind: "LinuxServer"},
			mac.Resource:   {GVR: mac, Kind: "MacOSServer"},
		},
		group:               "edges.railgrid.ai",
		version:             "v1alpha1",
		edgeProxyPublicPath: edgeProxyPublicPath,
	}
}

func TestEdgeProxyStatusURL(t *testing.T) {
	const base = "/services/providers/edges/" + DataPlaneRoot
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
			want:    base + "/clusters/11tcw27t4rdtnacy/kubernetesclusters/dev-edge-kube-1/k8s",
		},
		{
			name:    "linux server maps to ssh subresource",
			gvr:     s.kinds["linuxservers"].GVR,
			cluster: "11tcw27t4rdtnacy",
			obj:     "dev-edge-srv-1",
			want:    base + "/clusters/11tcw27t4rdtnacy/linuxservers/dev-edge-srv-1/ssh",
		},
		{
			name:    "macOS server has no consumer data-plane URL",
			gvr:     s.kinds["macosservers"].GVR,
			cluster: "11tcw27t4rdtnacy",
			obj:     "dev-edge-mac-1",
			want:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.edgeProxyStatusURL(tc.gvr, tc.cluster, tc.obj)
			if got != tc.want {
				t.Fatalf("edgeProxyStatusURL()\n got  %q\n want %q", got, tc.want)
			}
			if tc.want == "" {
				return
			}

			// The CLI externalizes status.URL against the hub host; the hub
			// backend proxy then strips /services/providers/edges and hands
			// the provider the rest verbatim, which is exactly what
			// dataplane.ParsePath must accept. Assert that round-trip so the
			// inverse pair cannot drift.
			stripped := strings.TrimPrefix(got, "/services/providers/edges")
			parsed, ok := dataplane.ParsePath(DataPlaneRoot, stripped)
			if !ok {
				t.Fatalf("ParsePath(%q) failed to parse the URL this Server produced", stripped)
			}
			if parsed.ClusterID != tc.cluster || parsed.Resource != tc.gvr.Resource || parsed.Name != tc.obj {
				t.Fatalf("round-trip mismatch: got %+v", parsed)
			}
			if !verbServed(parsed.Resource, parsed.Verb) {
				t.Fatalf("status.URL names verb %q, which this provider does not serve", parsed.Verb)
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
