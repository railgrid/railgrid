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

package apiurl

import (
	"testing"
)

func TestSplitBaseAndCluster(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantBase    string
		wantCluster string
	}{
		{
			name:        "full URL with cluster",
			input:       "https://hub:9443/clusters/abc123",
			wantBase:    "https://hub:9443",
			wantCluster: "abc123",
		},
		{
			name:        "URL with cluster and extra path",
			input:       "https://hub:9443/clusters/abc123/extra/path",
			wantBase:    "https://hub:9443",
			wantCluster: "abc123",
		},
		{
			name:        "URL without cluster",
			input:       "https://hub:9443",
			wantBase:    "https://hub:9443",
			wantCluster: "default",
		},
		{
			name:        "URL with trailing slash, no cluster",
			input:       "https://hub:9443/",
			wantBase:    "https://hub:9443",
			wantCluster: "default",
		},
		{
			name:        "URL with trailing slash and cluster",
			input:       "https://hub:9443/clusters/abc123/",
			wantBase:    "https://hub:9443",
			wantCluster: "abc123",
		},
		{
			name:        "localhost URL with cluster",
			input:       "https://railgrid.localhost:6444/clusters/root:railgrid:user-default",
			wantBase:    "https://railgrid.localhost:6444",
			wantCluster: "root:railgrid:user-default",
		},
		{
			name:        "http scheme",
			input:       "http://hub:8080/clusters/mycluster",
			wantBase:    "http://hub:8080",
			wantCluster: "mycluster",
		},
		{
			name:        "empty cluster segment (trailing slash after /clusters/)",
			input:       "https://hub:9443/clusters/",
			wantBase:    "https://hub:9443",
			wantCluster: "default",
		},
		{
			name:        "internal kcp URL with /clusters/ (no /api prefix)",
			input:       "https://localhost:6443/clusters/root:railgrid:providers",
			wantBase:    "https://localhost:6443",
			wantCluster: "root:railgrid:providers",
		},
		{
			name:        "internal kcp URL with /clusters/ and extra path",
			input:       "https://localhost:6443/clusters/root:railgrid/api/v1",
			wantBase:    "https://localhost:6443",
			wantCluster: "root:railgrid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBase, gotCluster := SplitBaseAndCluster(tt.input)
			if gotBase != tt.wantBase {
				t.Errorf("SplitBaseAndCluster(%q) base = %q, want %q", tt.input, gotBase, tt.wantBase)
			}
			if gotCluster != tt.wantCluster {
				t.Errorf("SplitBaseAndCluster(%q) cluster = %q, want %q", tt.input, gotCluster, tt.wantCluster)
			}
		})
	}
}

func TestHubServerURL(t *testing.T) {
	tests := []struct {
		name    string
		hubBase string
		cluster string
		want    string
	}{
		{
			name:    "plain base",
			hubBase: "https://hub:9443",
			cluster: "abc123",
			want:    "https://hub:9443/clusters/abc123",
		},
		{
			name:    "base with trailing slash",
			hubBase: "https://hub:9443/",
			cluster: "abc123",
			want:    "https://hub:9443/clusters/abc123",
		},
		{
			name:    "base already has /clusters/ — replaced",
			hubBase: "https://hub:9443/clusters/old",
			cluster: "new",
			want:    "https://hub:9443/clusters/new",
		},
		{
			name:    "kcp colon-path cluster",
			hubBase: "https://hub:9443",
			cluster: "root:railgrid:user-default",
			want:    "https://hub:9443/clusters/root:railgrid:user-default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HubServerURL(tt.hubBase, tt.cluster)
			if got != tt.want {
				t.Errorf("HubServerURL(%q, %q) = %q, want %q", tt.hubBase, tt.cluster, got, tt.want)
			}
		})
	}
}

func TestEdgeProviderCoordinates(t *testing.T) {
	tests := []struct {
		name     string
		edgeType string
		resource string
	}{
		{name: "kubernetes", edgeType: "kubernetes", resource: "kubernetesclusters"},
		{name: "linux", edgeType: "server", resource: "linuxservers"},
		{name: "macOS", edgeType: "macos", resource: "macosservers"},
		{name: "legacy default", edgeType: "", resource: "kubernetesclusters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, group, resource := EdgeProviderCoordinates(tt.edgeType)
			if provider != "edges" || group != "edges.railgrid.ai" || resource != tt.resource {
				t.Fatalf("EdgeProviderCoordinates(%q) = (%q, %q, %q), want (edges, edges.railgrid.ai, %s)",
					tt.edgeType, provider, group, resource, tt.resource)
			}
		})
	}
}

func TestProviderAgentProxyURLUsesMacOSResource(t *testing.T) {
	got := ProviderAgentProxyURL("https://hub:9443/", "macos", "root:railgrid:tenant", "mac-mini", "proxy")
	want := "https://hub:9443/services/providers/edges/agent/clusters/root:railgrid:tenant/macosservers/mac-mini/proxy"
	if got != want {
		t.Fatalf("ProviderAgentProxyURL() = %q, want %q", got, want)
	}
}

func TestProviderVerbPath(t *testing.T) {
	got := ProviderVerbPath("abc123", "infrastructure.railgrid.ai", "v1alpha1", "instances", "site", "exec")
	want := "/clusters/abc123/apis/infrastructure.railgrid.ai/v1alpha1/instances/site/exec"
	if got != want {
		t.Fatalf("ProviderVerbPath() = %q, want %q", got, want)
	}
	if got := ProviderVerbURL("https://hub:9443/", "abc123", "infrastructure.railgrid.ai", "v1alpha1", "instances", "site", "exec"); got != "https://hub:9443"+want {
		t.Fatalf("ProviderVerbURL() = %q", got)
	}
}

func TestEdgeVerbPaths(t *testing.T) {
	if got, want := EdgeVerbPath("abc123", "linuxservers", "box", "ssh"), "/clusters/abc123/apis/edges.railgrid.ai/v1alpha1/linuxservers/box/ssh"; got != want {
		t.Fatalf("EdgeVerbPath() = %q, want %q", got, want)
	}
	if got, want := EdgeVerbURL("https://hub:9443", "macos", "abc123", "mac-mini", "agent-token"), "https://hub:9443/clusters/abc123/apis/edges.railgrid.ai/v1alpha1/macosservers/mac-mini/agent-token"; got != want {
		t.Fatalf("EdgeVerbURL() = %q, want %q", got, want)
	}
	if got, want := EdgeServiceProxyPath("abc123", "grafana", "proxy"), "/clusters/abc123/apis/edges.railgrid.ai/v1alpha1/services/grafana/proxy"; got != want {
		t.Fatalf("EdgeServiceProxyPath() = %q, want %q", got, want)
	}
	if got, want := EdgeServiceProxyURL("https://hub:9443/", "abc123", "grafana", "mcp"), "https://hub:9443/clusters/abc123/apis/edges.railgrid.ai/v1alpha1/services/grafana/mcp"; got != want {
		t.Fatalf("EdgeServiceProxyURL() = %q, want %q", got, want)
	}
}

// TestKubernetesMCPPath / TestKubernetesMCPURL / TestLinuxMCPPath /
// TestLinuxMCPURL removed alongside their helpers when the per-kind
// MCP endpoints collapsed into the MCPServer aggregate. MCPServer
// helper coverage lives in TestMCPServerPath / TestMCPServerURL.

func TestEdgeAPIPath(t *testing.T) {
	tests := []struct {
		name     string
		cluster  string
		edgeName string
		want     string
	}{
		{
			name:     "standard edge",
			cluster:  "abc123",
			edgeName: "my-edge",
			want:     "/clusters/abc123/apis/railgrid.ai/v1alpha1/edges/my-edge",
		},
		{
			name:     "kcp colon-path cluster",
			cluster:  "root:railgrid:user-default",
			edgeName: "edge-1",
			want:     "/clusters/root:railgrid:user-default/apis/railgrid.ai/v1alpha1/edges/edge-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EdgeAPIPath(tt.cluster, tt.edgeName)
			if got != tt.want {
				t.Errorf("EdgeAPIPath(%q, %q) = %q, want %q", tt.cluster, tt.edgeName, got, tt.want)
			}
		})
	}
}

func TestExternalizeURL(t *testing.T) {
	tests := []struct {
		name    string
		edgeURL string
		hubBase string
		want    string
		wantErr bool
	}{
		{
			name:    "api services path gets externalized",
			edgeURL: "/services/edges-proxy/clusters/abc123/apis/railgrid.ai/v1alpha1/edges/my-edge/k8s",
			hubBase: "https://hub:9443",
			want:    "https://hub:9443/services/edges-proxy/clusters/abc123/apis/railgrid.ai/v1alpha1/edges/my-edge/k8s",
		},
		{
			name:    "absolute URL returned unchanged",
			edgeURL: "https://other-host/some/path",
			hubBase: "https://hub:9443",
			want:    "https://other-host/some/path",
		},
		{
			name:    "kube verb path gets externalized",
			edgeURL: "/clusters/abc123/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/my-edge/k8s",
			hubBase: "https://hub:9443",
			want:    "https://hub:9443/clusters/abc123/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/my-edge/k8s",
		},
		{
			name:    "unrelated relative path returned unchanged",
			edgeURL: "/ui/providers/edges/main.js",
			hubBase: "https://hub:9443",
			want:    "/ui/providers/edges/main.js",
		},
		{
			name:    "hub base with trailing slash",
			edgeURL: "/services/agent-proxy/abc123/apis/railgrid.ai/v1alpha1/edges/my-edge/proxy",
			hubBase: "https://hub:9443/",
			want:    "https://hub:9443/services/agent-proxy/abc123/apis/railgrid.ai/v1alpha1/edges/my-edge/proxy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExternalizeURL(tt.edgeURL, tt.hubBase)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExternalizeURL(%q, %q) error = %v, wantErr %v", tt.edgeURL, tt.hubBase, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ExternalizeURL(%q, %q) = %q, want %q", tt.edgeURL, tt.hubBase, got, tt.want)
			}
		})
	}
}

func TestConstants(t *testing.T) {
	if PathPrefixMCPServer != "/services/mcpserver" {
		t.Errorf("PathPrefixMCPServer = %q, want %q", PathPrefixMCPServer, "/services/mcpserver")
	}
	if PathAuthCallback != "/auth/callback" {
		t.Errorf("PathAuthCallback = %q, want %q", PathAuthCallback, "/auth/callback")
	}
	if PathAuthTokenLogin != "/auth/token-login" {
		t.Errorf("PathAuthTokenLogin = %q, want %q", PathAuthTokenLogin, "/auth/token-login")
	}
}
