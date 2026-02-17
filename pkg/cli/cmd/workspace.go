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
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	kedgePreviousCluster = "kedge-previous"
)

func newWorkspaceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace [name]",
		Aliases: []string{"ws"},
		Short: "Navigate between workspaces",
		Long: `Navigate between workspaces by manipulating the /clusters/ path in the kubeconfig server URL.

Without arguments, prints the current workspace path.
With an argument, navigates to the specified workspace (shorthand for "use").

Examples:
  kedge ws                              # print current workspace
  kedge ws dev-site-1                   # navigate to child workspace
  kedge ws ..                           # go up one level
  kedge ws -                            # swap to previous workspace
  kedge ws use :root:kedge:tenants:abc  # navigate to absolute path`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return runWorkspaceCurrent(cmd)
			}
			return runWorkspaceUse(cmd, args[0])
		},
	}

	cmd.AddCommand(newWsUseCommand())
	cmd.AddCommand(newWsCurrentCommand())

	return cmd
}

func newWsUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use <workspace>",
		Aliases: []string{"cd"},
		Short: "Switch to a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkspaceUse(cmd, args[0])
		},
	}
}

func newWsCurrentCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Print current workspace path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkspaceCurrent(cmd)
		},
	}
}

func runWorkspaceCurrent(cmd *cobra.Command) error {
	clientConfig := loadClientConfig()
	rawConfig, err := clientConfig.RawConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}

	clusterName, cluster, err := currentCluster(&rawConfig)
	if err != nil {
		return err
	}

	_, clusterPath, err := parseClusterURL(cluster.Server)
	if err != nil {
		return fmt.Errorf("parsing server URL for cluster %q: %w", clusterName, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Current workspace is %q.\n", clusterPath)
	return nil
}

func runWorkspaceUse(cmd *cobra.Command, name string) error {
	clientConfig := loadClientConfig()
	configAccess := clientConfig.ConfigAccess()
	rawConfig, err := clientConfig.RawConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}

	clusterName, cluster, err := currentCluster(&rawConfig)
	if err != nil {
		return err
	}

	baseURL, currentPath, err := parseClusterURL(cluster.Server)
	if err != nil {
		return fmt.Errorf("parsing server URL for cluster %q: %w", clusterName, err)
	}

	currentServerURL := cluster.Server

	// Handle "-" (swap with previous).
	if name == "-" {
		prevCluster, ok := rawConfig.Clusters[kedgePreviousCluster]
		if !ok || prevCluster.Server == "" {
			return fmt.Errorf("no previous workspace")
		}

		prevServerURL := prevCluster.Server
		_, prevPath, err := parseClusterURL(prevServerURL)
		if err != nil {
			return fmt.Errorf("parsing previous server URL: %w", err)
		}

		// Swap current and previous.
		cluster.Server = prevServerURL
		prevCluster.Server = currentServerURL

		if err := clientcmd.ModifyConfig(configAccess, rawConfig, true); err != nil {
			return fmt.Errorf("writing kubeconfig: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Current workspace is %q.\n", prevPath)
		return nil
	}

	// Preprocess: replace "/" with ":".
	name = strings.ReplaceAll(name, "/", ":")

	var newPath string
	if strings.HasPrefix(name, ":") {
		// Absolute path.
		newPath = strings.TrimPrefix(name, ":")
	} else {
		// Relative path.
		newPath = currentPath + ":" + name
	}

	newPath = resolveDots(newPath)
	if newPath == "" {
		return fmt.Errorf("cannot navigate above root workspace")
	}

	newServerURL := baseURL + "/clusters/" + newPath

	// Save current as previous.
	if rawConfig.Clusters[kedgePreviousCluster] == nil {
		rawConfig.Clusters[kedgePreviousCluster] = &clientcmdapi.Cluster{}
	}
	rawConfig.Clusters[kedgePreviousCluster].Server = currentServerURL

	// Update current.
	cluster.Server = newServerURL

	if err := clientcmd.ModifyConfig(configAccess, rawConfig, true); err != nil {
		return fmt.Errorf("writing kubeconfig: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Current workspace is %q.\n", newPath)
	return nil
}

// currentCluster returns the cluster name and cluster object for the current context.
func currentCluster(config *clientcmdapi.Config) (string, *clientcmdapi.Cluster, error) {
	if config.CurrentContext == "" {
		return "", nil, fmt.Errorf("no current context set in kubeconfig")
	}

	ctx, ok := config.Contexts[config.CurrentContext]
	if !ok {
		return "", nil, fmt.Errorf("context %q not found in kubeconfig", config.CurrentContext)
	}

	cluster, ok := config.Clusters[ctx.Cluster]
	if !ok {
		return "", nil, fmt.Errorf("cluster %q not found in kubeconfig", ctx.Cluster)
	}

	return ctx.Cluster, cluster, nil
}

// loadClientConfig creates a ClientConfig that respects the --kubeconfig flag.
func loadClientConfig() clientcmd.ClientConfig {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
}

// parseClusterURL extracts the base URL and cluster path from a server URL.
// "https://hub:8443/clusters/root:foo:bar" -> ("https://hub:8443", "root:foo:bar")
func parseClusterURL(serverURL string) (baseURL, clusterPath string, err error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", "", fmt.Errorf("parsing URL %q: %w", serverURL, err)
	}

	const prefix = "/clusters/"
	idx := strings.Index(u.Path, prefix)
	if idx < 0 {
		return "", "", fmt.Errorf("server URL %q does not contain /clusters/ path", serverURL)
	}

	clusterAndRest := u.Path[idx+len(prefix):]
	// Cluster path is up to the next "/" (or end).
	if slashIdx := strings.Index(clusterAndRest, "/"); slashIdx >= 0 {
		clusterPath = clusterAndRest[:slashIdx]
	} else {
		clusterPath = clusterAndRest
	}

	u.Path = u.Path[:idx]
	baseURL = u.String()
	return baseURL, clusterPath, nil
}

// resolveDots processes "." and ".." segments in colon-separated workspace paths.
func resolveDots(path string) string {
	parts := strings.Split(path, ":")
	var result []string
	for _, p := range parts {
		switch p {
		case "", ".":
			continue
		case "..":
			if len(result) > 0 {
				result = result[:len(result)-1]
			}
		default:
			result = append(result, p)
		}
	}
	return strings.Join(result, ":")
}
