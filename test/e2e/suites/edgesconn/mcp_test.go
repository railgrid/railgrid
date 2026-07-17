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

package edgesconn

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"testing"
	"time"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
)

// assertMCPAggregateListsKubeTools connects to the hub's MCP aggregate for the
// tenant's default MCPServer and asserts the edges provider's kube toolset is
// federated in (tools/list). Proves the decoupled provider's /mcp endpoint is
// discovered + proxied by the hub aggregate. Requires a connected
// KubernetesCluster edge in the tenant workspace.
func assertMCPAggregateListsKubeTools(t *testing.T, tenantWS string) {
	t.Helper()

	mcpURL := apiurl.MCPServerURL(hubURL, tenantWS, "default")
	httpClient := &http.Client{
		Transport: &bearerRoundTripper{
			token: staticToken,
			base:  &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // e2e dev certs
		},
		Timeout: 30 * time.Second,
	}

	client := gosdk.NewClient(&gosdk.Implementation{Name: "edgesconn-e2e", Version: "1.0"}, nil)
	transport := &gosdk.StreamableClientTransport{Endpoint: mcpURL, HTTPClient: httpClient}

	// Federation/discovery can lag the edge connecting; retry the whole
	// connect+list until the kube tools show up.
	var names []string
	ok := waitFor(t, 90*time.Second, func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		session, err := client.Connect(ctx, transport, nil)
		if err != nil {
			return false, "connect: " + err.Error()
		}
		defer func() { _ = session.Close() }()
		res, err := session.ListTools(ctx, &gosdk.ListToolsParams{})
		if err != nil {
			return false, "list tools: " + err.Error()
		}
		names = names[:0]
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
		}
		return containsKubeTool(names), "tools=" + strings.Join(names, ",")
	})
	if !ok {
		t.Fatalf("MCP aggregate never listed a kube tool; last tools: %v", names)
	}
	t.Logf("MCP aggregate federated %d tools including a kube toolset: %v", len(names), names)
}

// containsKubeTool reports whether the federated tool set includes a
// recognizable kubernetes tool (names are provider-prefixed, e.g.
// "edges__namespaces_list").
func containsKubeTool(names []string) bool {
	for _, n := range names {
		l := strings.ToLower(n)
		if strings.Contains(l, "namespace") || strings.Contains(l, "pods") || strings.Contains(l, "resources_list") {
			return true
		}
	}
	return false
}

// bearerRoundTripper injects a Bearer token into every request.
type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (rt *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+rt.token)
	return rt.base.RoundTrip(r)
}
