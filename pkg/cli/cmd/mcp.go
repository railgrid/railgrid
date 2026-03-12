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
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"
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
	var kmcpName string

	cmd := &cobra.Command{
		Use:   "url",
		Short: "Print the MCP endpoint URL",
		Long: `Prints the MCP endpoint URL derived from the current kubeconfig context.

Use --name to print the KubernetesMCP multi-edge endpoint URL:
  https://kedge.example.com/services/mcp/root:kedge:user-default/apis/mcp.kedge.faros.sh/v1alpha1/kubernetesmcps/default/mcp

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
			if kmcpName == "" && edgeName == "" {
				return fmt.Errorf("specify --name <kubernetesmcp-name> for the multi-edge MCP endpoint, or --edge <edge-name> for the per-edge MCP endpoint")
			}
			return runMCPURL(cmd, edgeName, kmcpName)
		},
	}

	cmd.Flags().StringVar(&edgeName, "edge", "", "Name of the edge (for per-edge MCP endpoint)")
	cmd.Flags().StringVar(&kmcpName, "name", "", "Name of the KubernetesMCP object (for multi-edge MCP endpoint)")

	return cmd
}

func runMCPURL(_ *cobra.Command, edgeName, kmcpName string) error {
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
	if kmcpName != "" {
		mcpURL, mcpErr = mcpKubernetesMCPURLFromServerURL(serverURL, kmcpName)
	} else {
		mcpURL, mcpErr = mcpURLFromServerURL(serverURL, edgeName)
	}
	if mcpErr != nil {
		return mcpErr
	}

	fmt.Println(mcpURL)
	return nil
}

// mcpURLFromServerURL derives the per-edge MCP endpoint URL from a kcp server URL and edge name.
//
// Input:  https://kedge.example.com/clusters/root:kedge:user-default, "my-edge"
// Output: https://kedge.example.com/services/agent-proxy/root:kedge:user-default/apis/kedge.faros.sh/v1alpha1/edges/my-edge/mcp
//
// Returns an error if the server URL does not contain a /clusters/ path segment.
func mcpURLFromServerURL(serverURL, edgeName string) (string, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("parsing server URL %q: %w", serverURL, err)
	}

	// Extract base URL (scheme + host).
	base := parsed.Scheme + "://" + parsed.Host

	// Extract cluster name from the path.
	clusterName := ""
	if idx := strings.Index(parsed.Path, "/clusters/"); idx >= 0 {
		clusterName = strings.TrimPrefix(parsed.Path[idx:], "/clusters/")
		clusterName = strings.TrimSuffix(clusterName, "/")
	}

	if clusterName == "" {
		return "", fmt.Errorf("cannot determine cluster name from server URL %q; expected path to contain /clusters/<name>", serverURL)
	}

	return fmt.Sprintf("%s/services/agent-proxy/%s/apis/kedge.faros.sh/v1alpha1/edges/%s/mcp", base, clusterName, edgeName), nil
}

// mcpKubernetesMCPURLFromServerURL derives the KubernetesMCP endpoint URL from a
// kcp server URL and a KubernetesMCP object name.
//
// Input:  https://kedge.example.com/clusters/root:kedge:user-default, "default"
// Output: https://kedge.example.com/services/mcp/root:kedge:user-default/apis/mcp.kedge.faros.sh/v1alpha1/kubernetesmcps/default/mcp
func mcpKubernetesMCPURLFromServerURL(serverURL, kmcpName string) (string, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("parsing server URL %q: %w", serverURL, err)
	}

	base := parsed.Scheme + "://" + parsed.Host

	clusterName := ""
	if idx := strings.Index(parsed.Path, "/clusters/"); idx >= 0 {
		clusterName = strings.TrimPrefix(parsed.Path[idx:], "/clusters/")
		clusterName = strings.TrimSuffix(clusterName, "/")
	}

	if clusterName == "" {
		return "", fmt.Errorf("cannot determine cluster name from server URL %q; expected path to contain /clusters/<name>", serverURL)
	}

	return fmt.Sprintf("%s/services/mcp/%s/apis/mcp.kedge.faros.sh/v1alpha1/kubernetesmcps/%s/mcp", base, clusterName, kmcpName), nil
}
