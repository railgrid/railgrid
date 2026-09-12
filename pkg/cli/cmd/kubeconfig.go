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
	"os"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// edgeContextPrefix prefixes the kubeconfig context, cluster and user entries
// that 'faros connect' and 'faros edge kubeconfig --merge' write for an edge.
const edgeContextPrefix = "faros-"

// edgeContextName is the kubeconfig context name for an edge.
func edgeContextName(edge string) string {
	return edgeContextPrefix + edge
}

// isEdgeContext reports whether a context name was written for an edge.
func isEdgeContext(name string) bool {
	return strings.HasPrefix(name, edgeContextPrefix)
}

// edgeAccess is what the CLI needs to reach a Kubernetes edge through the hub:
// the edge object and its externalized proxy URL.
type edgeAccess struct {
	edge *unstructured.Unstructured
	url  string
}

// resolveKubernetesEdge fetches the edge by name, checks that it is a
// Kubernetes cluster with a proxy URL, and externalizes that URL against the
// hub address in the kubeconfig.
func resolveKubernetesEdge(ctx context.Context, name string, raw *clientcmdapi.Config) (*edgeAccess, error) {
	dynClient, err := loadDynamicClient()
	if err != nil {
		return nil, err
	}
	edge, _, err := getEdgeByName(ctx, dynClient, name)
	if err != nil {
		return nil, err
	}
	switch edgeTypeOf(edge) {
	case edgeTypeKubernetes:
	case edgeTypeServer:
		return nil, fmt.Errorf("edge %q is a Linux server, not a Kubernetes cluster; use: faros ssh %s", name, name)
	default:
		return nil, fmt.Errorf("edge %q is a %s edge, not a Kubernetes cluster; it is service-only (no kubectl or SSH)", name, edgeTypeOf(edge))
	}
	edgeURL := getNestedString(*edge, "status", "URL")
	if edgeURL == "" {
		connected, _, _ := unstructuredNestedBool(edge.Object, "status", "connected")
		if !connected {
			return nil, fmt.Errorf("edge %q is not connected (phase %s); start the agent on it and retry — 'faros edge join-command %s' prints how",
				name, formatStringOrDash(getNestedString(*edge, "status", "phase")), name)
		}
		return nil, fmt.Errorf("edge %q has no proxy URL in status yet; retry shortly", name)
	}
	external, err := externalizeEdgeURL(edgeURL, raw)
	if err != nil {
		return nil, fmt.Errorf("constructing external edge URL: %w", err)
	}
	return &edgeAccess{edge: edge, url: external}, nil
}

// farosClusterAndAuth returns the cluster and user entries of the faros
// context (or the current context when there is none).
func farosClusterAndAuth(raw *clientcmdapi.Config) (ctxName string, cluster *clientcmdapi.Cluster, authName string, auth *clientcmdapi.AuthInfo, err error) {
	ctxName, kctx, err := resolveFarosContext(raw)
	if err != nil {
		return "", nil, "", nil, err
	}
	cluster = raw.Clusters[kctx.Cluster]
	if cluster == nil {
		return "", nil, "", nil, fmt.Errorf("kubeconfig context %q references missing cluster %q", ctxName, kctx.Cluster)
	}
	auth = raw.AuthInfos[kctx.AuthInfo]
	if auth == nil {
		return "", nil, "", nil, fmt.Errorf("kubeconfig context %q references missing user %q; run 'faros login'", ctxName, kctx.AuthInfo)
	}
	return ctxName, cluster, kctx.AuthInfo, auth, nil
}

// edgeClusterEntry builds the cluster entry for an edge: the proxy URL with
// the hub's TLS settings, since the proxy is served by the hub itself.
func edgeClusterEntry(hub *clientcmdapi.Cluster, edgeURL string) *clientcmdapi.Cluster {
	c := &clientcmdapi.Cluster{
		Server:                   edgeURL,
		CertificateAuthority:     hub.CertificateAuthority,
		CertificateAuthorityData: hub.CertificateAuthorityData,
		InsecureSkipTLSVerify:    hub.InsecureSkipTLSVerify || globalInsecureTLS,
	}
	if c.InsecureSkipTLSVerify {
		c.CertificateAuthority, c.CertificateAuthorityData = "", nil
	}
	return c
}

// standaloneEdgeKubeconfig returns a one-context kubeconfig for the edge that
// copies the faros credentials, so it works on its own (KUBECONFIG=…).
func standaloneEdgeKubeconfig(raw *clientcmdapi.Config, edgeName, edgeURL string) (*clientcmdapi.Config, error) {
	_, hubCluster, _, auth, err := farosClusterAndAuth(raw)
	if err != nil {
		return nil, err
	}
	name := edgeContextName(edgeName)
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[name] = edgeClusterEntry(hubCluster, edgeURL)
	cfg.AuthInfos[name] = auth.DeepCopy()
	cfg.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	cfg.CurrentContext = name
	return cfg, nil
}

// mergeEdgeContext adds or refreshes the edge's context in raw. The context
// references the faros user entry by name rather than copying it, so a
// re-login refreshes credentials for every connected edge at once.
func mergeEdgeContext(raw *clientcmdapi.Config, edgeName, edgeURL string) (string, error) {
	_, hubCluster, authName, _, err := farosClusterAndAuth(raw)
	if err != nil {
		return "", err
	}
	name := edgeContextName(edgeName)
	raw.Clusters[name] = edgeClusterEntry(hubCluster, edgeURL)
	raw.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: authName}
	return name, nil
}

func newEdgeKubeconfigCommand() *cobra.Command {
	var output string
	var merge bool

	cmd := &cobra.Command{
		Use:   "kubeconfig <name>",
		Short: "Print or merge a kubeconfig for a Kubernetes edge",
		Long: `Produce a kubeconfig whose server is the edge's Kubernetes API, reached
through the hub's edge proxy with your own hub credentials (the hub checks
that you may 'proxy' to the edge and forwards requests as you).

By default the kubeconfig is printed; -o writes it to a file. --merge adds a
context named faros-<name> to your kubeconfig without switching to it — use
'faros connect <name>' to merge and switch in one step.

Examples:
  faros edge kubeconfig my-edge > my-edge.kubeconfig
  faros edge kubeconfig my-edge -o ~/.kube/my-edge.kubeconfig
  KUBECONFIG=my-edge.kubeconfig kubectl get nodes
  faros edge kubeconfig my-edge --merge && kubectl --context faros-my-edge get nodes`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKubernetesEdgeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmdContext(cmd)

			raw, path, err := loadRawKubeconfig()
			if err != nil {
				return err
			}
			access, err := resolveKubernetesEdge(ctx, name, raw)
			if err != nil {
				return err
			}

			if merge {
				ctxName, err := mergeEdgeContext(raw, name, access.url)
				if err != nil {
					return err
				}
				if err := clientcmd.WriteToFile(*raw, path); err != nil {
					return fmt.Errorf("writing kubeconfig to %s: %w", path, err)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Context %q added to %s\nUse it with: kubectl --context %s get nodes\n", ctxName, path, ctxName)
				return nil
			}

			cfg, err := standaloneEdgeKubeconfig(raw, name, access.url)
			if err != nil {
				return err
			}
			kubeconfigBytes, err := clientcmd.Write(*cfg)
			if err != nil {
				return fmt.Errorf("serializing kubeconfig: %w", err)
			}
			if output == "" || output == "-" {
				_, err = cmd.OutOrStdout().Write(kubeconfigBytes)
				return err
			}
			if err := os.WriteFile(output, kubeconfigBytes, 0600); err != nil {
				return fmt.Errorf("writing kubeconfig to %s: %w", output, err)
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Kubeconfig written to %s\n", output)
			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Write the kubeconfig to this file instead of stdout")
	cmd.Flags().BoolVar(&merge, "merge", false, "Merge a faros-<name> context into your kubeconfig instead of printing")
	cmd.MarkFlagsMutuallyExclusive("output", "merge")
	return cmd
}

// newKubeconfigCommand keeps the pre-1.0 'faros kubeconfig edge <name>'
// spelling working; the command now lives at 'faros edge kubeconfig'.
func newKubeconfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "kubeconfig",
		Short:  "Generate kubeconfig files for faros resources",
		Hidden: true,
	}
	edge := newEdgeKubeconfigCommand()
	edge.Use = "edge <name>"
	edge.Deprecated = "use 'faros edge kubeconfig <name>' (or 'faros connect <name>')"
	cmd.AddCommand(edge)
	return cmd
}

func completeKubernetesEdgeNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeEdgeNamesOfType(cmd, edgeTypeKubernetes, toComplete)
}

func completeServerEdgeNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeEdgeNamesOfType(cmd, edgeTypeServer, toComplete)
}
