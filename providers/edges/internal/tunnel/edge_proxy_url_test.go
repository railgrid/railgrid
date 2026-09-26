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
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"
)

// testServer is a Server with the three connectable kinds and nothing else
// wired: no kcp, no callers, no tunnels. Tests set what they need.
func testServer() *Server {
	kube := schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "kubernetesclusters"}
	linux := schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "linuxservers"}
	mac := schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "macosservers"}
	return &Server{
		kinds: map[string]KindConfig{
			kube.Resource:  {GVR: kube, Kind: "KubernetesCluster"},
			linux.Resource: {GVR: linux, Kind: "LinuxServer"},
			mac.Resource:   {GVR: mac, Kind: "MacOSServer"},
		},
		group:   "edges.railgrid.ai",
		version: "v1alpha1",
	}
}

// An edge's status.URL is the hub-relative kube path of its default verb: a
// kcp custom subresource on this provider's export, which the CLI externalizes
// against the hub host and kcp routes back here.
func TestEdgeProxyStatusURL(t *testing.T) {
	s := testServer()
	const base = "/clusters/11tcw27t4rdtnacy/apis/edges.railgrid.ai/v1alpha1"

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
			want:    base + "/kubernetesclusters/dev-edge-kube-1/k8s",
		},
		{
			name:    "linux server maps to ssh subresource",
			gvr:     s.kinds["linuxservers"].GVR,
			cluster: "11tcw27t4rdtnacy",
			obj:     "dev-edge-srv-1",
			want:    base + "/linuxservers/dev-edge-srv-1/ssh",
		},
		{
			name:    "macOS server has no consumer data-plane URL",
			gvr:     s.kinds["macosservers"].GVR,
			cluster: "11tcw27t4rdtnacy",
			obj:     "dev-edge-mac-1",
			want:    "",
		},
		{
			name:    "a workspace path is not a cluster ID and yields no URL",
			gvr:     s.kinds["linuxservers"].GVR,
			cluster: "root:railgrid:tenants:acme",
			obj:     "dev-edge-srv-1",
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

			// The path a shard forwards for this URL is exactly this path, so
			// the parser the serve adapter runs must accept what this Server
			// produced. Assert the round-trip so the inverse pair cannot drift.
			parsed, err := dataplane.ParseSubresourcePath(got)
			if err != nil {
				t.Fatalf("ParseSubresourcePath(%q) refused the URL this Server produced: %v", got, err)
			}
			if parsed.ClusterID != tc.cluster || parsed.Resource != tc.gvr.Resource || parsed.Name != tc.obj {
				t.Fatalf("round-trip mismatch: got %+v", parsed)
			}
			if parsed.Group != s.group || parsed.APIVersion != s.version {
				t.Fatalf("status.URL names %s/%s, want %s/%s", parsed.Group, parsed.APIVersion, s.group, s.version)
			}
			if !verbServed(parsed.Resource, parsed.Verb) {
				t.Fatalf("status.URL names verb %q, which this provider does not serve", parsed.Verb)
			}
		})
	}
}

// The agent-tunnel route is parsed exactly as sent: a traversal segment, a
// workspace path or a wrong verb is refused rather than cleaned.
func TestParseAgentPath(t *testing.T) {
	cases := []struct {
		path                    string
		ok                      bool
		cluster, resource, name string
	}{
		{path: "/agent/clusters/11tcw27t4rdtnacy/linuxservers/edge-1/proxy", ok: true, cluster: "11tcw27t4rdtnacy", resource: "linuxservers", name: "edge-1"},
		{path: "/agent/clusters/11tcw27t4rdtnacy/linuxservers/edge-1/ssh"},
		{path: "/agent/clusters/11tcw27t4rdtnacy/linuxservers/edge-1/proxy/extra"},
		{path: "/agent/clusters/11tcw27t4rdtnacy/linuxservers/../proxy"},
		{path: "/agent/clusters/11tcw27t4rdtnacy/linuxservers//proxy"},
		{path: "/agent/clusters/root:railgrid:tenants:a/linuxservers/edge-1/proxy"},
		{path: "/agent/proxy"},
		{path: "/clusters/11tcw27t4rdtnacy/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/proxy"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			cluster, resource, name, ok := parseAgentPath(tc.path)
			if ok != tc.ok {
				t.Fatalf("parseAgentPath(%q) ok=%v, want %v", tc.path, ok, tc.ok)
			}
			if ok && (cluster != tc.cluster || resource != tc.resource || name != tc.name) {
				t.Fatalf("parseAgentPath(%q) = %q %q %q", tc.path, cluster, resource, name)
			}
		})
	}
}

// The k8s verb's upstream path comes from the parsed route's tail, never from
// searching the URL for "/k8s/": an object named after the verb cannot shift
// where the agent path begins.
func TestAgentK8sPath(t *testing.T) {
	for tail, want := range map[string]string{
		"":                   "/k8s/",
		"api":                "/k8s/api",
		"api/v1/pods":        "/k8s/api/v1/pods",
		"apis/apps/v1/k8s/x": "/k8s/apis/apps/v1/k8s/x",
	} {
		if got := agentK8sPath(tail); got != want {
			t.Errorf("agentK8sPath(%q) = %q, want %q", tail, got, want)
		}
	}
}
