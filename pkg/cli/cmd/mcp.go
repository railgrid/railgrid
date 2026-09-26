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

package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/railgrid/railgrid/pkg/apiurl"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
)

func newMCPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "MCP endpoints for AI clients (Claude Code, Cursor, Codex)",
		Long:  `Commands for interacting with the railgrid MCP endpoint.`,
	}

	cmd.AddCommand(newMCPURLCommand())
	cmd.AddCommand(newMCPClaudeCommand())
	cmd.AddCommand(newMCPCodexCommand())
	cmd.AddCommand(newMCPProxyCommand())
	return cmd
}

func newMCPURLCommand() *cobra.Command {
	var edgeName string
	var mcpserverName string

	cmd := &cobra.Command{
		Use:   "url",
		Short: "Print the MCP endpoint URL",
		Long: `Prints the MCP endpoint URL derived from the current kubeconfig context.

Use --mcpserver-name to print the aggregate MCPServer endpoint URL — one
endpoint that exposes both kube and linux edges plus a list_targets tool the
AI uses to discover what's reachable.  This is the entry point for
Claude / Cursor / similar MCP clients:
  https://railgrid.example.com/services/mcpserver/root:railgrid:user-default/apis/railgrid.ai/v1alpha1/mcpservers/default/mcp
The configuration hints then carry the workspace's long-lived MCP token from
the hub's connect endpoint, which also works after an OIDC login.

Use --edge to print the per-edge MCP endpoint URL (single Kubernetes edge),
the edge's "mcp" verb on the hub's kcp front door:
  https://railgrid.example.com/clusters/11tcw27t4rdtnacy/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/my-edge/mcp

The previous per-kind MCP endpoints (--name for KubernetesMCP,
--linux-name for LinuxMCP) were removed; their tools now appear on the
MCPServer aggregate via the in-binary ToolFamily registry.

Usage with Claude Desktop (claude_desktop_config.json):
  {
    "mcpServers": {
      "railgrid": {
        "url": "<output of this command>"
      }
    }
  }
`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			set := 0
			if mcpserverName != "" {
				set++
			}
			if edgeName != "" {
				set++
			}
			if set == 0 {
				return fmt.Errorf("specify exactly one of --mcpserver-name <aggregate-mcp-name> or --edge <edge-name>")
			}
			if set > 1 {
				return fmt.Errorf("--mcpserver-name and --edge are mutually exclusive")
			}
			return runMCPURL(cmd, edgeName, mcpserverName)
		},
	}

	cmd.Flags().StringVar(&edgeName, "edge", "", "Name of the edge (for per-edge MCP endpoint)")
	cmd.Flags().StringVar(&mcpserverName, "mcpserver-name", "", "Name of the aggregate MCPServer object (kube + linux + list_targets)")

	return cmd
}

func runMCPURL(_ *cobra.Command, edgeName, mcpserverName string) error {
	// Load the current kubeconfig.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	clientCfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{},
	)

	rawCfg, err := clientCfg.RawConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}

	// Resolve the current context.
	currentCtx := rawCfg.CurrentContext
	if currentCtx == "" {
		return fmt.Errorf("no current context in kubeconfig")
	}

	ctx, ok := rawCfg.Contexts[currentCtx]
	if !ok {
		return fmt.Errorf("context %q not found in kubeconfig", currentCtx)
	}

	cluster, ok := rawCfg.Clusters[ctx.Cluster]
	if !ok {
		return fmt.Errorf("cluster %q not found in kubeconfig", ctx.Cluster)
	}

	serverURL := cluster.Server
	if serverURL == "" {
		return fmt.Errorf("cluster %q has no server URL in kubeconfig", ctx.Cluster)
	}

	var mcpURL string
	var mcpErr error
	switch {
	case mcpserverName != "":
		mcpURL, mcpErr = mcpAggregateURLFromServerURL(serverURL, mcpserverName)
	default:
		mcpURL, mcpErr = mcpURLFromServerURL(serverURL, edgeName)
	}
	if mcpErr != nil {
		return mcpErr
	}

	// Resolve the bearer token for the usage hint. The aggregate endpoint has
	// a long-lived, workspace-scoped token behind the hub's connect endpoint;
	// prefer it, because the kubeconfig's static token is empty for OIDC
	// (exec plugin) logins and a user token would expire anyway.
	token := ""
	tokenNote := ""
	if mcpserverName != "" {
		info, err := connectMCPForURL(serverURL, mcpserverName)
		switch {
		case err != nil:
			tokenNote = fmt.Sprintf("Could not fetch the MCP token from the hub (%v).", err)
		case info.Token == "":
			tokenNote = "The hub has not minted this MCP server's token yet; re-run shortly."
		default:
			token = info.Token
			if info.EndpointURL != "" {
				mcpURL = info.EndpointURL
			}
		}
	}
	if token == "" {
		if u, ok := rawCfg.AuthInfos[ctx.AuthInfo]; ok {
			token = u.Token
		}
	}
	if token == "" && tokenNote == "" {
		tokenNote = "Your kubeconfig logs in through OIDC (no static token). 'railgrid env' prints a current TOKEN; it expires."
	}

	fmt.Println(mcpURL)
	if tokenNote != "" {
		fmt.Fprintln(os.Stderr, tokenNote)
	}

	// Derive the MCP server name for the `claude mcp add` hint.
	// For per-edge endpoints, query the edge type so the name reflects
	// what's being connected (<edge>-kubernetes-cluster or <edge>-server).
	mcpName := mcpServerName(edgeName, mcpserverName)

	fmt.Println()
	fmt.Println("To add this MCP server to Claude Code:")
	if token != "" {
		fmt.Printf("  claude mcp add --transport http %s \"%s\" -H \"Authorization: Bearer %s\"\n", mcpName, mcpURL, token)
	} else {
		fmt.Printf("  claude mcp add --transport http %s \"%s\" -H \"Authorization: Bearer <your-token>\"\n", mcpName, mcpURL)
	}
	fmt.Println()
	fmt.Println("To add to Claude Desktop (claude_desktop_config.json):")
	fmt.Println("  {")
	fmt.Println("    \"mcpServers\": {")
	fmt.Printf("      \"%s\": {\n", mcpName)
	fmt.Printf("        \"url\": \"%s\",\n", mcpURL)
	if token != "" {
		fmt.Printf("        \"headers\": { \"Authorization\": \"Bearer %s\" }\n", token)
	} else {
		fmt.Println("        \"headers\": { \"Authorization\": \"Bearer <your-token>\" }")
	}
	fmt.Println("      }")
	fmt.Println("    }")
	fmt.Println("  }")
	fmt.Println()
	fmt.Println("To add to Codex:")
	if token != "" {
		fmt.Printf("  export RAILGRID_MCP_TOKEN=%s\n", shellSingleQuote(token))
	} else {
		fmt.Println("  export RAILGRID_MCP_TOKEN='<your-token>'")
	}
	fmt.Printf("  codex mcp add %s \\\n", mcpName)
	fmt.Printf("    --url %s \\\n", shellSingleQuote(mcpURL))
	fmt.Println("    --bearer-token-env-var RAILGRID_MCP_TOKEN")
	if mcpserverName != "" {
		fmt.Println()
		fmt.Println("To connect as yourself instead (your own login, refreshed automatically; also")
		fmt.Println("federates your organization's own providers), run the proxy as a stdio server:")
		fmt.Printf("  claude mcp add %s -- railgrid mcp proxy --mcpserver-name %s\n", mcpName, mcpserverName)
	}
	return nil
}

// connectMCPForURL asks the hub for the aggregate MCPServer's endpoint and
// long-lived token, provided the railgrid context targets the same workspace as
// serverURL (the kubeconfig's current context).
func connectMCPForURL(serverURL, mcpserverName string) (*mcpConnectInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := newHubSession(ctx, hubTarget{})
	if err != nil {
		return nil, err
	}
	if _, cluster := apiurl.SplitBaseAndCluster(serverURL); cluster != s.Cluster {
		return nil, fmt.Errorf("current context targets %s, the railgrid context %s", cluster, s.Cluster)
	}
	return s.mcpConnect(ctx, mcpserverName)
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// mcpServerName chooses a friendly identifier for the `claude mcp add`
// name argument. All names share a `railgrid-` prefix so multiple railgrid
// MCP servers registered in a single client config sort together.
// Aggregate MCPServer entries take the CR name directly; per-edge
// entries derive their middle segment from the edge's spec.type
// ("kubernetes-cluster" or "server").
//
// KubernetesMCP / LinuxMCP cases were removed when both per-kind CRDs
// collapsed into the MCPServer aggregate.
func mcpServerName(edgeName, mcpserverName string) string {
	switch {
	case mcpserverName != "":
		return "railgrid-" + mcpserverName
	case edgeName != "":
		return "railgrid-" + edgeTypeKind(edgeName) + "-" + edgeName
	}
	return "railgrid"
}

// edgeTypeKind returns the singular per-edge segment matching the edge's
// spec.type. Failure to resolve the type (no kubeconfig, RBAC, etc.) falls
// back to "kubernetes-cluster" so the command still emits a usable hint.
func edgeTypeKind(edgeName string) string {
	dynClient, err := loadDynamicClient()
	if err != nil {
		return "kubernetes-cluster"
	}
	edge, err := dynClient.Resource(railgridclient.KubernetesClusterGVR).Get(context.Background(), edgeName, metav1.GetOptions{})
	if err != nil {
		return "kubernetes-cluster"
	}
	switch getNestedString(*edge, "spec", "type") {
	case "server":
		return "server"
	default:
		return "kubernetes-cluster"
	}
}

// mcpURLFromServerURL derives the per-edge MCP endpoint URL from a kcp server URL and edge name.
//
// Input:  https://railgrid.example.com/clusters/11tcw27t4rdtnacy, "my-edge"
// Output: https://railgrid.example.com/clusters/11tcw27t4rdtnacy/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/my-edge/mcp
//
// Per-edge MCP exposes the kube toolset against a single KubernetesCluster edge,
// so the URL targets the `kubernetesclusters` resource on the edges provider
// (server edges have no Kubernetes API and are rejected by the handler).
//
// It is the edge's "mcp" data-plane verb: a kcp custom subresource on the
// edges APIExport, authorized with the caller's own RBAC and reverse-proxied
// by kcp to the provider. It never hangs off the provider's /agent tunnel,
// whose credential is an edge ServiceAccount, not a person's.
//
// Returns an error if the server URL does not contain a /clusters/ path segment.
func mcpURLFromServerURL(serverURL, edgeName string) (string, error) {
	base, cluster := apiurl.SplitBaseAndCluster(serverURL)
	if cluster == "default" {
		return "", fmt.Errorf("cannot determine cluster name from server URL %q; expected path to contain /clusters/<name>", serverURL)
	}
	return apiurl.EdgeVerbURL(base, "kubernetes", cluster, edgeName, "mcp"), nil
}

// mcpKubernetesURLFromServerURL / mcpLinuxURLFromServerURL were
// removed when both per-kind endpoints collapsed into the MCPServer
// aggregate. Use mcpAggregateURLFromServerURL below for the unified
// endpoint.

// mcpAggregateURLFromServerURL derives the aggregate MCPServer endpoint URL
// from a kcp server URL and an MCPServer object name.
//
// Input:  https://railgrid.example.com/clusters/root:railgrid:user-default, "default"
// Output: https://railgrid.example.com/services/mcpserver/root:railgrid:user-default/apis/railgrid.ai/v1alpha1/mcpservers/default/mcp
func mcpAggregateURLFromServerURL(serverURL, mcpserverName string) (string, error) {
	base, cluster := apiurl.SplitBaseAndCluster(serverURL)
	if cluster == "default" {
		return "", fmt.Errorf("cannot determine cluster name from server URL %q; expected path to contain /clusters/<name>", serverURL)
	}
	return apiurl.MCPServerURL(base, cluster, mcpserverName), nil
}
