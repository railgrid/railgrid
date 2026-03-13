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

package cmd

import (
	"testing"
)

func TestMCPURLFromServerURL(t *testing.T) {
	const edgeName = "my-edge"

	tests := []struct {
		name       string
		serverURL  string
		edgeName   string
		wantURL    string
		wantErrMsg string
	}{
		{
			name:      "standard kcp URL",
			serverURL: "https://kedge.localhost:8443/clusters/root:kedge:user-default",
			edgeName:  edgeName,
			wantURL:   "https://kedge.localhost:8443/services/agent-proxy/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/edges/my-edge/mcp",
		},
		{
			name:      "trailing slash is stripped",
			serverURL: "https://kedge.localhost:8443/clusters/root:kedge:user-default/",
			edgeName:  edgeName,
			wantURL:   "https://kedge.localhost:8443/services/agent-proxy/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/edges/my-edge/mcp",
		},
		{
			name:      "root cluster",
			serverURL: "https://hub.example.com/clusters/root",
			edgeName:  "edge-a",
			wantURL:   "https://hub.example.com/services/agent-proxy/root/apis/kedge.faros.sh/v1alpha1/edges/edge-a/mcp",
		},
		{
			name:       "no /clusters/ path returns error",
			serverURL:  "https://kedge.localhost:8443",
			edgeName:   edgeName,
			wantErrMsg: "cannot determine cluster name",
		},
		{
			name:       "plain path without clusters segment",
			serverURL:  "https://kedge.localhost:8443/api/v1",
			edgeName:   edgeName,
			wantErrMsg: "cannot determine cluster name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mcpURLFromServerURL(tc.serverURL, tc.edgeName)

			if tc.wantErrMsg != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (url=%q)", tc.wantErrMsg, got)
				}
				if msg := err.Error(); len(msg) == 0 {
					t.Fatalf("expected error message, got empty string")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantURL {
				t.Errorf("mcpURLFromServerURL(%q, %q)\n  got:  %q\n  want: %q", tc.serverURL, tc.edgeName, got, tc.wantURL)
			}
		})
	}
}

func TestMCPKubernetesURLFromServerURL(t *testing.T) {
	tests := []struct {
		name           string
		serverURL      string
		kubernetesName string
		wantURL        string
		wantErrMsg     string
	}{
		{
			name:           "standard kcp URL with default",
			serverURL:      "https://kedge.localhost:8443/clusters/root:kedge:user-default",
			kubernetesName: "default",
			wantURL:        "https://kedge.localhost:8443/services/mcp/root:kedge:user-default/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/default/mcp",
		},
		{
			name:           "trailing slash is stripped",
			serverURL:      "https://kedge.localhost:8443/clusters/root:kedge:user-default/",
			kubernetesName: "default",
			wantURL:        "https://kedge.localhost:8443/services/mcp/root:kedge:user-default/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/default/mcp",
		},
		{
			name:           "custom Kubernetes MCP name",
			serverURL:      "https://hub.example.com/clusters/root",
			kubernetesName: "my-mcp",
			wantURL:        "https://hub.example.com/services/mcp/root/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/my-mcp/mcp",
		},
		{
			name:           "no /clusters/ path returns error",
			serverURL:      "https://kedge.localhost:8443",
			kubernetesName: "default",
			wantErrMsg:     "cannot determine cluster name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mcpKubernetesURLFromServerURL(tc.serverURL, tc.kubernetesName)

			if tc.wantErrMsg != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (url=%q)", tc.wantErrMsg, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantURL {
				t.Errorf("mcpKubernetesURLFromServerURL(%q, %q)\n  got:  %q\n  want: %q", tc.serverURL, tc.kubernetesName, got, tc.wantURL)
			}
		})
	}
}
