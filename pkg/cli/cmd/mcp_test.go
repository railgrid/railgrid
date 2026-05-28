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
			serverURL: "https://kedge.localhost:9443/clusters/root:kedge:user-default",
			edgeName:  edgeName,
			wantURL:   "https://kedge.localhost:9443/services/agent-proxy/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/edges/my-edge/mcp",
		},
		{
			name:      "trailing slash is stripped",
			serverURL: "https://kedge.localhost:9443/clusters/root:kedge:user-default/",
			edgeName:  edgeName,
			wantURL:   "https://kedge.localhost:9443/services/agent-proxy/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/edges/my-edge/mcp",
		},
		{
			name:      "root cluster",
			serverURL: "https://hub.example.com/clusters/root",
			edgeName:  "edge-a",
			wantURL:   "https://hub.example.com/services/agent-proxy/root/apis/kedge.faros.sh/v1alpha1/edges/edge-a/mcp",
		},
		{
			name:       "no /clusters/ path returns error",
			serverURL:  "https://kedge.localhost:9443",
			edgeName:   edgeName,
			wantErrMsg: "cannot determine cluster name",
		},
		{
			name:       "plain path without clusters segment",
			serverURL:  "https://kedge.localhost:9443/api/v1",
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

// TestMCPKubernetesURLFromServerURL / TestMCPLinuxURLFromServerURL
// removed alongside their helpers — the per-kind MCP endpoints
// collapsed into the MCPServer aggregate. Aggregate URL coverage
// continues in TestMCPAggregateURLFromServerURL below.

func TestMCPAggregateURLFromServerURL(t *testing.T) {
	tests := []struct {
		name          string
		serverURL     string
		mcpserverName string
		wantURL       string
		wantErrMsg    string
	}{
		{
			name:          "standard kcp URL with default",
			serverURL:     "https://kedge.localhost:9443/clusters/root:kedge:user-default",
			mcpserverName: "default",
			wantURL:       "https://kedge.localhost:9443/services/mcpserver/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/mcpservers/default/mcp",
		},
		{
			name:          "custom MCPServer name",
			serverURL:     "https://hub.example.com/clusters/root",
			mcpserverName: "prod",
			wantURL:       "https://hub.example.com/services/mcpserver/root/apis/kedge.faros.sh/v1alpha1/mcpservers/prod/mcp",
		},
		{
			name:          "no /clusters/ path returns error",
			serverURL:     "https://kedge.localhost:9443",
			mcpserverName: "default",
			wantErrMsg:    "cannot determine cluster name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mcpAggregateURLFromServerURL(tc.serverURL, tc.mcpserverName)

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
				t.Errorf("mcpAggregateURLFromServerURL(%q, %q)\n  got:  %q\n  want: %q", tc.serverURL, tc.mcpserverName, got, tc.wantURL)
			}
		})
	}
}
