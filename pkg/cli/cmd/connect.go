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
	"os"
	"sort"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"

	workspacecmd "github.com/kcp-dev/cli/pkg/workspace/cmd"
)

func newConnectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect [<edge>]",
		Short: "Point kubectl at a Kubernetes edge",
		Long: `Add a kubeconfig context named faros-<edge> for the edge's Kubernetes API
(reached through the hub's edge proxy with your hub credentials) and make it
the current context, so plain kubectl talks to that cluster:

  faros connect my-cluster
  kubectl get nodes
  faros disconnect            # back to the hub workspace

Without an argument an interactive picker lists the connected clusters.
The context stays in your kubeconfig; switch between edges with
'kubectl config use-context faros-<edge>' or connect again.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeKubernetesEdgeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			raw, path, err := loadRawKubeconfig()
			if err != nil {
				return err
			}

			var name string
			if len(args) == 1 {
				name = args[0]
			} else {
				name, err = pickKubernetesEdge(cmd)
				if err != nil {
					return err
				}
			}

			access, err := resolveKubernetesEdge(ctx, name, raw)
			if err != nil {
				return err
			}
			ctxName, err := mergeEdgeContext(raw, name, access.url)
			if err != nil {
				return err
			}
			raw.CurrentContext = ctxName
			if err := clientcmd.WriteToFile(*raw, path); err != nil {
				return fmt.Errorf("writing kubeconfig to %s: %w", path, err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Connected to edge %q: kubectl now uses context %q.\nRun 'faros disconnect' to return to the hub workspace.\n", name, ctxName)
			return err
		},
	}
	return cmd
}

// pickKubernetesEdge lists the Kubernetes edges (connected first) in the
// interactive picker.
func pickKubernetesEdge(cmd *cobra.Command) (string, error) {
	if !stdinIsTerminal() {
		return "", fmt.Errorf("no interactive terminal; pass the edge name: faros connect <edge>")
	}
	dynClient, err := loadDynamicClient()
	if err != nil {
		return "", err
	}
	items, err := listAllEdges(cmdContext(cmd), dynClient)
	if err != nil {
		return "", fmt.Errorf("listing edges: %w", err)
	}
	type candidate struct {
		name      string
		connected bool
		phase     string
	}
	var cands []candidate
	for i := range items {
		if edgeTypeOf(&items[i]) != edgeTypeKubernetes {
			continue
		}
		connected, _, _ := unstructuredNestedBool(items[i].Object, "status", "connected")
		cands = append(cands, candidate{name: items[i].GetName(), connected: connected, phase: getNestedString(items[i], "status", "phase")})
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("no Kubernetes edges in this workspace; create one with 'faros edge create <name>'")
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].connected != cands[j].connected {
			return cands[i].connected
		}
		return cands[i].name < cands[j].name
	})
	pick := make([]pickerItem, len(cands))
	for i, c := range cands {
		desc := formatStringOrDash(c.phase)
		if !c.connected {
			desc += " · not connected"
		}
		pick[i] = pickerItem{title: c.name, desc: desc}
	}
	idx, err := runPicker("Connect to which cluster?", pick)
	if err != nil {
		return "", err
	}
	return cands[idx].name, nil
}

func newDisconnectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect",
		Short: "Point kubectl back at the hub workspace",
		Long: `Make the faros hub context current again after 'faros connect'. The edge
contexts stay in your kubeconfig for 'kubectl --context faros-<edge>'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, path, err := loadRawKubeconfig()
			if err != nil {
				return err
			}
			if _, ok := raw.Contexts[farosContextName]; !ok {
				return fmt.Errorf("no %q context in the kubeconfig; run 'faros login'", farosContextName)
			}
			if raw.CurrentContext == farosContextName {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "kubectl already uses the hub context %q.\n", farosContextName)
				return err
			}
			previous := raw.CurrentContext
			raw.CurrentContext = farosContextName
			if err := clientcmd.WriteToFile(*raw, path); err != nil {
				return fmt.Errorf("writing kubeconfig to %s: %w", path, err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Disconnected from %q: kubectl now uses the hub context %q.\n", previous, farosContextName)
			return err
		},
	}
}

// newKCPWorkspaceCommand exposes kcp's 'kubectl ws' navigation for hubs that
// mount edges as kcp workspaces. It rewrites the *current* kubeconfig context
// (kcp convention), which is why it is hidden: 'faros connect' and
// 'faros use' are the supported ways to move around.
func newKCPWorkspaceCommand() *cobra.Command {
	wsCmd, err := workspacecmd.New(genericclioptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr})
	if err != nil {
		// This only fails if the create subcommand can't be built, which
		// shouldn't happen. Panic rather than silently returning nil.
		panic(err)
	}
	wsCmd.Use = "kcp-workspace [<edge>|:|..|.|-|~|<root:absolute:workspace>] [-i|--interactive]"
	wsCmd.Short = "Navigate kcp workspaces directly (advanced; rewrites the current kubectl context)"
	wsCmd.Aliases = []string{"kcp-ws"}
	wsCmd.Hidden = true
	return wsCmd
}
