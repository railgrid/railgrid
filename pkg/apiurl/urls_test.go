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
			input:       "https://kedge.localhost:6444/clusters/root:kedge:user-default",
			wantBase:    "https://kedge.localhost:6444",
			wantCluster: "root:kedge:user-default",
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
			cluster: "root:kedge:user-default",
			want:    "https://hub:9443/clusters/root:kedge:user-default",
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

func TestEdgeAgentProxyPath(t *testing.T) {
	tests := []struct {
		name        string
		cluster     string
		edgeName    string
		subresource string
		want        string
	}{
		{
			name:        "proxy subresource",
			cluster:     "abc123",
			edgeName:    "my-edge",
			subresource: "proxy",
			want:        "/services/agent-proxy/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge/proxy",
		},
		{
			name:        "status subresource",
			cluster:     "root:kedge:user-default",
			edgeName:    "edge-1",
			subresource: "status",
			want:        "/services/agent-proxy/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/edges/edge-1/status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EdgeAgentProxyPath(tt.cluster, tt.edgeName, tt.subresource)
			if got != tt.want {
				t.Errorf("EdgeAgentProxyPath(%q, %q, %q) = %q, want %q",
					tt.cluster, tt.edgeName, tt.subresource, got, tt.want)
			}
		})
	}
}

func TestEdgeAgentProxyURL(t *testing.T) {
	tests := []struct {
		name        string
		hubBase     string
		cluster     string
		edgeName    string
		subresource string
		want        string
	}{
		{
			name:        "standard proxy URL",
			hubBase:     "https://hub:9443",
			cluster:     "abc123",
			edgeName:    "my-edge",
			subresource: "proxy",
			want:        "https://hub:9443/services/agent-proxy/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge/proxy",
		},
		{
			name:        "hub base with trailing slash",
			hubBase:     "https://hub:9443/",
			cluster:     "abc123",
			edgeName:    "my-edge",
			subresource: "proxy",
			want:        "https://hub:9443/services/agent-proxy/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge/proxy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EdgeAgentProxyURL(tt.hubBase, tt.cluster, tt.edgeName, tt.subresource)
			if got != tt.want {
				t.Errorf("EdgeAgentProxyURL(%q, %q, %q, %q) = %q, want %q",
					tt.hubBase, tt.cluster, tt.edgeName, tt.subresource, got, tt.want)
			}
		})
	}
}

func TestEdgeProxyPath(t *testing.T) {
	tests := []struct {
		name        string
		cluster     string
		edgeName    string
		subresource string
		want        string
	}{
		{
			name:        "k8s subresource",
			cluster:     "abc123",
			edgeName:    "my-edge",
			subresource: "k8s",
			want:        "/services/edges-proxy/clusters/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge/k8s",
		},
		{
			name:        "ssh subresource",
			cluster:     "abc123",
			edgeName:    "my-edge",
			subresource: "ssh",
			want:        "/services/edges-proxy/clusters/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge/ssh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EdgeProxyPath(tt.cluster, tt.edgeName, tt.subresource)
			if got != tt.want {
				t.Errorf("EdgeProxyPath(%q, %q, %q) = %q, want %q",
					tt.cluster, tt.edgeName, tt.subresource, got, tt.want)
			}
		})
	}
}

func TestEdgeProxyURL(t *testing.T) {
	tests := []struct {
		name        string
		hubBase     string
		cluster     string
		edgeName    string
		subresource string
		want        string
	}{
		{
			name:        "k8s URL",
			hubBase:     "https://hub:9443",
			cluster:     "abc123",
			edgeName:    "my-edge",
			subresource: "k8s",
			want:        "https://hub:9443/services/edges-proxy/clusters/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge/k8s",
		},
		{
			name:        "ssh URL",
			hubBase:     "https://hub:9443",
			cluster:     "abc123",
			edgeName:    "server-1",
			subresource: "ssh",
			want:        "https://hub:9443/services/edges-proxy/clusters/abc123/apis/kedge.faros.sh/v1alpha1/edges/server-1/ssh",
		},
		{
			name:        "hub base with trailing slash",
			hubBase:     "https://hub:9443/",
			cluster:     "abc123",
			edgeName:    "my-edge",
			subresource: "k8s",
			want:        "https://hub:9443/services/edges-proxy/clusters/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge/k8s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EdgeProxyURL(tt.hubBase, tt.cluster, tt.edgeName, tt.subresource)
			if got != tt.want {
				t.Errorf("EdgeProxyURL(%q, %q, %q, %q) = %q, want %q",
					tt.hubBase, tt.cluster, tt.edgeName, tt.subresource, got, tt.want)
			}
		})
	}
}

func TestKubernetesMCPPath(t *testing.T) {
	tests := []struct {
		name           string
		cluster        string
		kubernetesName string
		want           string
	}{
		{
			name:           "default kubernetes",
			cluster:        "abc123",
			kubernetesName: "default",
			want:           "/services/mcp/abc123/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/default/mcp",
		},
		{
			name:           "named kubernetes",
			cluster:        "abc123",
			kubernetesName: "my-cluster",
			want:           "/services/mcp/abc123/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/my-cluster/mcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := KubernetesMCPPath(tt.cluster, tt.kubernetesName)
			if got != tt.want {
				t.Errorf("KubernetesMCPPath(%q, %q) = %q, want %q",
					tt.cluster, tt.kubernetesName, got, tt.want)
			}
		})
	}
}

func TestKubernetesMCPURL(t *testing.T) {
	got := KubernetesMCPURL("https://hub:9443", "abc123", "default")
	want := "https://hub:9443/services/mcp/abc123/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/default/mcp"
	if got != want {
		t.Errorf("KubernetesMCPURL = %q, want %q", got, want)
	}
}

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
			want:     "/clusters/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge",
		},
		{
			name:     "kcp colon-path cluster",
			cluster:  "root:kedge:user-default",
			edgeName: "edge-1",
			want:     "/clusters/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/edges/edge-1",
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
			name:    "services path gets externalized",
			edgeURL: "/services/edges-proxy/clusters/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge/k8s",
			hubBase: "https://hub:9443",
			want:    "https://hub:9443/services/edges-proxy/clusters/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge/k8s",
		},
		{
			name:    "absolute URL returned unchanged",
			edgeURL: "https://other-host/some/path",
			hubBase: "https://hub:9443",
			want:    "https://other-host/some/path",
		},
		{
			name:    "non-services path returned unchanged",
			edgeURL: "/clusters/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge",
			hubBase: "https://hub:9443",
			want:    "/clusters/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge",
		},
		{
			name:    "hub base with trailing slash",
			edgeURL: "/services/agent-proxy/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge/proxy",
			hubBase: "https://hub:9443/",
			want:    "https://hub:9443/services/agent-proxy/abc123/apis/kedge.faros.sh/v1alpha1/edges/my-edge/proxy",
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
	if PathPrefixAgentProxy != "/services/agent-proxy" {
		t.Errorf("PathPrefixAgentProxy = %q, want %q", PathPrefixAgentProxy, "/services/agent-proxy")
	}
	if PathPrefixEdgesProxy != "/services/edges-proxy" {
		t.Errorf("PathPrefixEdgesProxy = %q, want %q", PathPrefixEdgesProxy, "/services/edges-proxy")
	}
	if PathPrefixMCP != "/services/mcp" {
		t.Errorf("PathPrefixMCP = %q, want %q", PathPrefixMCP, "/services/mcp")
	}
	if PathAuthCallback != "/auth/callback" {
		t.Errorf("PathAuthCallback = %q, want %q", PathAuthCallback, "/auth/callback")
	}
	if PathAuthTokenLogin != "/auth/token-login" {
		t.Errorf("PathAuthTokenLogin = %q, want %q", PathAuthTokenLogin, "/auth/token-login")
	}
}
