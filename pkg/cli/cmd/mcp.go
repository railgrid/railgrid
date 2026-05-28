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
	"context"
	"fmt"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

func newMCPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "MCP (Model Context Protocol) related commands",
		Long:  `Commands for interacting with the kedge MCP endpoint.`,
	}

	cmd.AddCommand(newMCPURLCommand())
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
  https://kedge.example.com/services/mcpserver/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/mcpservers/default/mcp

Use --edge to print the per-edge MCP endpoint URL (single Kubernetes edge):
  https://kedge.example.com/services/agent-proxy/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/edges/my-edge/mcp

The previous per-kind MCP endpoints (--name for KubernetesMCP,
--linux-name for LinuxMCP) were removed; their tools now appear on the
MCPServer aggregate via the in-binary ToolFamily registry.

Usage with Claude Desktop (claude_desktop_config.json):
  {
    "mcpServers": {
      "kedge": {
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

	fmt.Println(mcpURL)

	// Resolve the bearer token from the kubeconfig for the usage hint.
	token := ""
	if currentCtx, ok := rawCfg.Contexts[rawCfg.CurrentContext]; ok {
		if u, ok := rawCfg.AuthInfos[currentCtx.AuthInfo]; ok {
			token = u.Token
		}
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
	return nil
}

// mcpServerName chooses a friendly identifier for the `claude mcp add`
// name argument. All names share a `kedge-` prefix so multiple kedge
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
		return "kedge-" + mcpserverName
	case edgeName != "":
		return "kedge-" + edgeTypeKind(edgeName) + "-" + edgeName
	}
	return "kedge"
}

// edgeTypeKind returns the singular per-edge segment matching the edge's
// spec.type. Failure to resolve the type (no kubeconfig, RBAC, etc.) falls
// back to "kubernetes-cluster" so the command still emits a usable hint.
func edgeTypeKind(edgeName string) string {
	dynClient, err := loadDynamicClient()
	if err != nil {
		return "kubernetes-cluster"
	}
	edge, err := dynClient.Resource(kedgeclient.EdgeGVR).Get(context.Background(), edgeName, metav1.GetOptions{})
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
// Input:  https://kedge.example.com/clusters/root:kedge:user-default, "my-edge"
// Output: https://kedge.example.com/services/agent-proxy/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/edges/my-edge/mcp
//
// Returns an error if the server URL does not contain a /clusters/ path segment.
func mcpURLFromServerURL(serverURL, edgeName string) (string, error) {
	base, cluster := apiurl.SplitBaseAndCluster(serverURL)
	if cluster == "default" {
		return "", fmt.Errorf("cannot determine cluster name from server URL %q; expected path to contain /clusters/<name>", serverURL)
	}
	return apiurl.EdgeAgentProxyURL(base, cluster, edgeName, "mcp"), nil
}

// mcpKubernetesURLFromServerURL / mcpLinuxURLFromServerURL were
// removed when both per-kind endpoints collapsed into the MCPServer
// aggregate. Use mcpAggregateURLFromServerURL below for the unified
// endpoint.

// mcpAggregateURLFromServerURL derives the aggregate MCPServer endpoint URL
// from a kcp server URL and an MCPServer object name.
//
// Input:  https://kedge.example.com/clusters/root:kedge:user-default, "default"
// Output: https://kedge.example.com/services/mcpserver/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/mcpservers/default/mcp
func mcpAggregateURLFromServerURL(serverURL, mcpserverName string) (string, error) {
	base, cluster := apiurl.SplitBaseAndCluster(serverURL)
	if cluster == "default" {
		return "", fmt.Errorf("cannot determine cluster name from server URL %q; expected path to contain /clusters/<name>", serverURL)
	}
	return apiurl.MCPServerURL(base, cluster, mcpserverName), nil
}
