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

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

func newKubeconfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Generate kubeconfig files for kedge resources",
	}

	cmd.AddCommand(newKubeconfigEdgeCommand())

	return cmd
}

func newKubeconfigEdgeCommand() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "edge <name>",
		Short: "Generate a kubeconfig for connecting to an edge",
		Long: `Generate a kubeconfig file that points directly to an edge's
Kubernetes API (for type=kubernetes edges) or SSH endpoint (for type=server edges).

The edge URL is read from Edge.Status.URL, which is set by the hub once the edge
is Ready and a kcp mount workspace has been assigned.

The current credentials from your kubeconfig are reused so you can connect to the
edge proxy with the same authentication token used for the hub.

Examples:
  # Print kubeconfig to stdout
  kedge kubeconfig edge my-edge

  # Write kubeconfig to a file
  kedge kubeconfig edge my-edge --output ~/.kube/my-edge.kubeconfig

  # Use with kubectl
  KUBECONFIG=$(kedge kubeconfig edge my-edge) kubectl get pods`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := context.Background()

			// 1. Fetch the Edge resource.
			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			edge, err := dynClient.Resource(kedgeclient.EdgeGVR).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("getting edge %q: %w", name, err)
			}

			// 2. Read edge.Status.URL (JSON field name "URL" — note capital U per the API type).
			edgeURL, _, _ := unstructuredNestedField(edge.Object, "status", "URL")
			edgeURLStr, _ := edgeURL.(string)
			if edgeURLStr == "" {
				return fmt.Errorf("edge %q has no URL set in status (is the edge Ready and the mount workspace initialised?)", name)
			}

			// 3. Load the current kubeconfig to reuse credentials from the active context.
			loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
			if kubeconfig != "" {
				loadingRules.ExplicitPath = kubeconfig
			}
			rawConfig, err := loadingRules.GetStartingConfig()
			if err != nil {
				return fmt.Errorf("loading kubeconfig: %w", err)
			}

			// 4. Extract the current context's auth info.
			var authInfo *clientcmdapi.AuthInfo
			if currentCtx, ok := rawConfig.Contexts[rawConfig.CurrentContext]; ok {
				if ai, ok := rawConfig.AuthInfos[currentCtx.AuthInfo]; ok {
					authInfo = ai
				}
			}

			// 5. Build a kubeconfig pointing to the edge URL with the current credentials.
			contextName := name + "-edge"
			newConfig := clientcmdapi.NewConfig()

			// Use InsecureSkipTLSVerify by default; inherit CA from existing cluster if available.
			clusterEntry := &clientcmdapi.Cluster{
				Server:                edgeURLStr,
				InsecureSkipTLSVerify: true,
			}
			if currentCtx, ok := rawConfig.Contexts[rawConfig.CurrentContext]; ok {
				if cl, ok := rawConfig.Clusters[currentCtx.Cluster]; ok && len(cl.CertificateAuthorityData) > 0 {
					clusterEntry.CertificateAuthorityData = cl.CertificateAuthorityData
					clusterEntry.InsecureSkipTLSVerify = false
				}
			}

			newConfig.Clusters[contextName] = clusterEntry
			if authInfo != nil {
				newConfig.AuthInfos[contextName] = authInfo
			} else {
				newConfig.AuthInfos[contextName] = &clientcmdapi.AuthInfo{}
			}
			newConfig.Contexts[contextName] = &clientcmdapi.Context{
				Cluster:  contextName,
				AuthInfo: contextName,
			}
			newConfig.CurrentContext = contextName

			// 6. Serialize the kubeconfig to YAML.
			kubeconfigBytes, err := clientcmd.Write(*newConfig)
			if err != nil {
				return fmt.Errorf("serializing kubeconfig: %w", err)
			}

			// 7. Output to stdout or a file.
			if output == "" || output == "-" {
				_, err = os.Stdout.Write(kubeconfigBytes)
				return err
			}

			if err := os.WriteFile(output, kubeconfigBytes, 0600); err != nil {
				return fmt.Errorf("writing kubeconfig to %s: %w", output, err)
			}
			fmt.Fprintf(os.Stderr, "Kubeconfig written to %s\n", output)
			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path (default: stdout, use '-' for stdout explicitly)")

	return cmd
}
