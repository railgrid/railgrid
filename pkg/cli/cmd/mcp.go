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
	var kubernetesName string
	var linuxName string
	var mcpserverName string

	cmd := &cobra.Command{
		Use:   "url",
		Short: "Print the MCP endpoint URL",
		Long: `Prints the MCP endpoint URL derived from the current kubeconfig context.

Use --mcpserver-name to print the aggregate MCPServer endpoint URL — one
endpoint that exposes both kube and linux edges plus a list_targets tool the
AI uses to discover what's reachable.  This is the recommended entry point
for Claude / Cursor / similar MCP clients:
  https://kedge.example.com/services/mcpserver/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/mcpservers/default/mcp

Use --name to print the KubernetesMCP multi-edge MCP endpoint URL (kubernetes-type edges):
  https://kedge.example.com/services/mcp/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/kubernetesmcps/default/mcp

Use --linux-name to print the LinuxMCP multi-edge MCP endpoint URL (server-type edges, SSH transport):
  https://kedge.example.com/services/linux-mcp/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/linuxmcps/default/mcp

Use --edge to print the per-edge MCP endpoint URL:
  https://kedge.example.com/services/agent-proxy/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/edges/my-edge/mcp

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
			if kubernetesName != "" {
				set++
			}
			if linuxName != "" {
				set++
			}
			if mcpserverName != "" {
				set++
			}
			if edgeName != "" {
				set++
			}
			if set == 0 {
				return fmt.Errorf("specify exactly one of --mcpserver-name <aggregate-mcp-name>, --name <kubernetes-mcp-name>, --linux-name <linux-mcp-name>, or --edge <edge-name>")
			}
			if set > 1 {
				return fmt.Errorf("--mcpserver-name, --name, --linux-name, and --edge are mutually exclusive")
			}
			return runMCPURL(cmd, edgeName, kubernetesName, linuxName, mcpserverName)
		},
	}

	cmd.Flags().StringVar(&edgeName, "edge", "", "Name of the edge (for per-edge MCP endpoint)")
	cmd.Flags().StringVar(&kubernetesName, "name", "", "Name of the KubernetesMCP object (kubernetes-type edges)")
	cmd.Flags().StringVar(&linuxName, "linux-name", "", "Name of the LinuxMCP object (server-type edges, SSH transport)")
	cmd.Flags().StringVar(&mcpserverName, "mcpserver-name", "", "Name of the aggregate MCPServer object (recommended — kube + linux + list_targets)")

	return cmd
}

func runMCPURL(_ *cobra.Command, edgeName, kubernetesName, linuxName, mcpserverName string) error {
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
	case kubernetesName != "":
		mcpURL, mcpErr = mcpKubernetesURLFromServerURL(serverURL, kubernetesName)
	case linuxName != "":
		mcpURL, mcpErr = mcpLinuxURLFromServerURL(serverURL, linuxName)
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
	mcpName := mcpServerName(edgeName, kubernetesName, linuxName, mcpserverName)

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

// mcpServerName chooses a friendly identifier for the `claude mcp add` name
// argument. Per-edge endpoints are suffixed with the edge type so users can
// tell at a glance what each registered server connects to.
func mcpServerName(edgeName, kubernetesName, linuxName, mcpserverName string) string {
	switch {
	case mcpserverName != "":
		return mcpserverName
	case kubernetesName != "":
		return kubernetesName + "-kubernetes-cluster"
	case linuxName != "":
		return linuxName + "-server"
	case edgeName != "":
		suffix := edgeTypeSuffix(edgeName)
		return edgeName + suffix
	}
	return "kedge"
}

// edgeTypeSuffix returns a suffix matching the edge's spec.type. Failure to
// resolve the type (e.g. no kubeconfig, RBAC) falls back to a generic suffix
// so the command still emits a usable hint.
func edgeTypeSuffix(edgeName string) string {
	dynClient, err := loadDynamicClient()
	if err != nil {
		return "-kubernetes-cluster"
	}
	edge, err := dynClient.Resource(kedgeclient.EdgeGVR).Get(context.Background(), edgeName, metav1.GetOptions{})
	if err != nil {
		return "-kubernetes-cluster"
	}
	switch getNestedString(*edge, "spec", "type") {
	case "server":
		return "-server"
	default:
		return "-kubernetes-cluster"
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

// mcpKubernetesURLFromServerURL derives the KubernetesMCP endpoint URL from a
// kcp server URL and a KubernetesMCP object name.
//
// Input:  https://kedge.example.com/clusters/root:kedge:user-default, "default"
// Output: https://kedge.example.com/services/mcp/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/kubernetesmcps/default/mcp
func mcpKubernetesURLFromServerURL(serverURL, kubernetesName string) (string, error) {
	base, cluster := apiurl.SplitBaseAndCluster(serverURL)
	if cluster == "default" {
		return "", fmt.Errorf("cannot determine cluster name from server URL %q; expected path to contain /clusters/<name>", serverURL)
	}
	return apiurl.KubernetesMCPURL(base, cluster, kubernetesName), nil
}

// mcpLinuxURLFromServerURL derives the LinuxMCP endpoint URL from a kcp
// server URL and a LinuxMCP object name.
//
// Input:  https://kedge.example.com/clusters/root:kedge:user-default, "default"
// Output: https://kedge.example.com/services/linux-mcp/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/linuxmcps/default/mcp
func mcpLinuxURLFromServerURL(serverURL, linuxName string) (string, error) {
	base, cluster := apiurl.SplitBaseAndCluster(serverURL)
	if cluster == "default" {
		return "", fmt.Errorf("cannot determine cluster name from server URL %q; expected path to contain /clusters/<name>", serverURL)
	}
	return apiurl.LinuxMCPURL(base, cluster, linuxName), nil
}

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
