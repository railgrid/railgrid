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
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/faroshq/faros/pkg/apiurl"
	farosclient "github.com/faroshq/faros/pkg/client"
)

const (
	edgeTypeKubernetes = "kubernetes"
	edgeTypeServer     = "server"
	edgeTypeMacOS      = "macos"
)

func newEdgeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "edge",
		Aliases: []string{"edges"},
		Short:   "Create, list, inspect and remove edges (clusters and servers)",
		Long: `An edge is a Kubernetes cluster or a Linux server that runs the faros agent
and dials out to the hub. Once connected, 'faros connect' points kubectl at a
cluster edge and 'faros ssh' opens a shell on a server edge.

  faros edge create my-cluster                  # prints the join command
  faros edge create my-vps --type server
  faros edge list
  faros edge get my-cluster -o yaml
  faros edge kubeconfig my-cluster -o ./my-cluster.kubeconfig
  faros edge delete my-vps`,
	}

	cmd.AddCommand(
		newEdgeCreateCommand(),
		newEdgeListCommand(),
		newEdgeGetCommand(),
		newEdgeDeleteCommand(),
		newEdgeJoinCommandCommand(),
		newEdgeUpgradeCommand(),
		newEdgeKubeconfigCommand(),
	)

	return cmd
}

// edgeTypeOf derives the user-facing type from the kind: the connectable
// kind IS the type (KubernetesCluster → kubernetes, LinuxServer → server,
// MacOSServer → macos).
func edgeTypeOf(u *unstructured.Unstructured) string {
	return farosclient.EdgeTypeForGVR(edgeGVRForKind(u.GetKind()))
}

func newEdgeCreateCommand() *cobra.Command {
	var labels map[string]string
	var edgeType string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an edge and print its join command",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmdContext(cmd)
			out := cmd.OutOrStdout()

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			// The connectable kind IS the type: KubernetesCluster, LinuxServer,
			// or MacOSServer (there is no spec.type discriminator).
			switch edgeType {
			case "":
				edgeType = edgeTypeKubernetes
			case edgeTypeKubernetes, edgeTypeServer, edgeTypeMacOS:
			default:
				return fmt.Errorf("unknown edge type %q (want kubernetes, server or macos)", edgeType)
			}
			kind, gvr := farosclient.EdgeKindForType(edgeType), farosclient.EdgeGVRForType(edgeType)

			edge := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": gvr.Group + "/" + gvr.Version,
					"kind":       kind,
					"metadata": map[string]interface{}{
						"name": name,
					},
					"spec": map[string]interface{}{},
				},
			}

			if len(labels) > 0 {
				lbls := make(map[string]interface{}, len(labels))
				for k, v := range labels {
					lbls[k] = v
				}
				edge.Object["metadata"].(map[string]interface{})["labels"] = lbls
			}

			_, err = dynClient.Resource(gvr).Create(ctx, edge, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("creating edge %q: %w", name, err)
			}

			_, _ = fmt.Fprintf(out, "✓ Edge %q created\n", name)

			// Poll for the join token (set by the hub controller on creation).
			joinToken, err := pollJoinTokenDynamic(ctx, name, 30*time.Second)
			if err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not retrieve join token: %v\n", err)
				_, _ = fmt.Fprintf(out, "\nRun 'faros edge join-command %s' to print the join command once the token is available.\n", name)
				return nil
			}

			// Get hub URL from the current kubeconfig.
			hubURL := loadHubURL()

			printJoinCommand(out, name, edgeType, hubURL, joinToken)
			return nil
		},
	}

	cmd.Flags().StringToStringVar(&labels, "labels", nil, "Labels for this edge (key=value pairs)")
	cmd.Flags().StringVar(&edgeType, "type", edgeTypeKubernetes, "Edge type: kubernetes, server (Linux host with SSH) or macos (macOS service host)")
	_ = cmd.RegisterFlagCompletionFunc("type", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{edgeTypeKubernetes, edgeTypeServer, edgeTypeMacOS}, cobra.ShellCompDirectiveNoFileComp
	})

	return cmd
}

// pollJoinTokenDynamic polls the Edge resource until Status.JoinToken is set or timeout expires.
func pollJoinTokenDynamic(ctx context.Context, name string, timeout time.Duration) (string, error) {
	dynClient, err := loadDynamicClient()
	if err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	for {
		edge, _, err := getEdgeByName(ctx, dynClient, name)
		if err != nil {
			return "", fmt.Errorf("getting edge: %w", err)
		}
		token := getNestedString(*edge, "status", "joinToken")
		if token != "" {
			return token, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for join token after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

// loadHubURL returns the hub server URL from the current kubeconfig.
// Falls back to "<hub-url>" placeholder on error.
func loadHubURL() string {
	cfg, err := loadRestConfig()
	if err != nil {
		return "<hub-url>"
	}
	if cfg.Host != "" {
		return cfg.Host
	}
	return "<hub-url>"
}

// printJoinCommand prints the formatted join instructions for an edge.
func printJoinCommand(w io.Writer, name, edgeType, hubURL, joinToken string) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("\n")
	p("# Step 1: Install the faros CLI (if not already installed)\n\n")
	p("  # Linux/macOS — download from GitHub Releases:\n")
	p("  curl -fsSL https://github.com/faroshq/faros/releases/latest/download/kubectl-faros_$(uname -s)_$(uname -m).tar.gz | tar xz\n")
	p("  sudo mv kubectl-faros /usr/local/bin/faros\n")
	p("\n")
	p("  # Or via krew:\n")
	p("  kubectl krew index add faros https://github.com/faroshq/krew-index.git\n")
	p("  kubectl krew install faros/faros\n")
	p("\n")

	switch edgeType {
	case edgeTypeKubernetes:
		p("# Step 2: Connect this Kubernetes cluster as an edge\n\n")
		p("  # Option A — Helm (recommended for production):\n")
		p("  helm install faros-agent oci://ghcr.io/faroshq/charts/faros-agent \\\n")
		p("    --namespace faros-agent --create-namespace \\\n")
		p("    --set agent.edgeName=%s \\\n", name)
		p("    --set agent.hub.url=%s \\\n", hubURL)
		p("    --set agent.hub.token=%s\n", joinToken)
		p("\n")
		p("  # Option B — CLI persistent install (creates a Deployment in faros-agent):\n")
		p("  faros agent join \\\n")
		p("    --hub-url %s \\\n", hubURL)
		p("    --edge-name %s \\\n", name)
		p("    --type kubernetes \\\n")
		p("    --token %s\n", joinToken)
		p("\n")
		p("  # Option C — foreground process (dev/containers):\n")
		p("  faros agent run \\\n")
		p("    --hub-url %s \\\n", hubURL)
		p("    --edge-name %s \\\n", name)
		p("    --type kubernetes \\\n")
		p("    --token %s\n", joinToken)
	case edgeTypeServer:
		p("# Step 2: Connect this Linux server as an edge\n\n")
		p("  # Option A — persistent install as a systemd service (recommended):\n")
		p("  faros agent join \\\n")
		p("    --hub-url %s \\\n", hubURL)
		p("    --edge-name %s \\\n", name)
		p("    --type server \\\n")
		p("    --token %s\n", joinToken)
		p("\n")
		p("  # Option B — foreground process (dev/containers):\n")
		p("  faros agent run \\\n")
		p("    --hub-url %s \\\n", hubURL)
		p("    --edge-name %s \\\n", name)
		p("    --type server \\\n")
		p("    --token %s\n", joinToken)
	default:
		p("# Step 2: Connect this macOS host as a service edge\n\n")
		p("  # Persistent launchd service (configured non-root worker account):\n")
		p("  sudo faros agent join \\\n")
		p("    --hub-url %s \\\n", hubURL)
		p("    --edge-name %s \\\n", name)
		p("    --type macos \\\n")
		p("    --worker-user \"$USER\" \\\n")
		if _, cluster := apiurl.SplitBaseAndCluster(hubURL); cluster != "" && cluster != "default" {
			p("    --cluster %s \\\n", cluster)
		}
		p("    --token %s\n", joinToken)
		p("\n")
		p("  # Foreground process (dev/validation):\n")
		p("  faros agent run \\\n")
		p("    --hub-url %s \\\n", hubURL)
		p("    --edge-name %s \\\n", name)
		p("    --type macos \\\n")
		p("    --token %s\n", joinToken)
	}
	p("\n")
	p("Run 'faros edge join-command %s' to print this again.\n", name)
}

// newEdgeJoinCommandCommand returns the 'faros edge join-command <name>' subcommand.
func newEdgeJoinCommandCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "join-command <name>",
		Short:             "Print the agent join command for an edge",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeEdgeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmdContext(cmd)

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			edge, _, err := getEdgeByName(ctx, dynClient, name)
			if err != nil {
				return fmt.Errorf("getting edge %q: %w", name, err)
			}

			joinToken := getNestedString(*edge, "status", "joinToken")
			if joinToken == "" {
				// Token not yet generated — poll briefly.
				joinToken, err = pollJoinTokenDynamic(ctx, name, 10*time.Second)
				if err != nil {
					return fmt.Errorf("join token not available for edge %q: %w", name, err)
				}
			}

			printJoinCommand(cmd.OutOrStdout(), name, edgeTypeOf(edge), loadHubURL(), joinToken)
			return nil
		},
	}
	return cmd
}

func newEdgeListCommand() *cobra.Command {
	output := newOutputFlags()
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List edges",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)

			dynClient, err := loadDynamicClient()
			if err != nil {
				return fmt.Errorf("not logged in — run: faros login --hub-url <hub-url>\n(original error: %w)", err)
			}

			items, err := listAllEdges(ctx, dynClient)
			if err != nil {
				return err
			}
			sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
			return printEdgeList(cmd.OutOrStdout(), output, items)
		},
	}
	output.addFlag(cmd)
	return cmd
}

func printEdgeList(w io.Writer, output *outputFlags, items []unstructured.Unstructured) error {
	switch {
	case output.structured():
		return output.printStructured(w, unstructuredList(items))
	case output.names():
		names := make([]string, len(items))
		for i := range items {
			names[i] = items[i].GetName()
		}
		return printNames(w, names)
	}
	if len(items) == 0 {
		_, err := fmt.Fprintln(w, "No edges found. Create one with: faros edge create <name> [--type server]")
		return err
	}
	t := &table{headers: []string{"NAME", "TYPE", "PHASE", "CONNECTED", "AGENT VERSION", "AGE"}}
	if output.wide() {
		t.headers = append(t.headers, "HOSTNAME", "LAST HEARTBEAT", "LABELS")
	}
	for i := range items {
		item := &items[i]
		connected, _, _ := unstructuredNestedBool(item.Object, "status", "connected")
		cols := []string{
			item.GetName(),
			edgeTypeOf(item),
			getNestedString(*item, "status", "phase"),
			fmt.Sprintf("%v", connected),
			getNestedString(*item, "status", "agentVersion"),
			formatAge(item.GetCreationTimestamp().Time),
		}
		if output.wide() {
			cols = append(cols,
				getNestedString(*item, "status", "hostname"),
				formatHeartbeat(getNestedString(*item, "status", "lastHeartbeatTime")),
				formatLabels(item.GetLabels()),
			)
		}
		t.add(cols...)
	}
	return t.write(w)
}

// unstructuredList wraps items in a v1 List so json/yaml output matches
// what kubectl prints for a multi-kind listing.
func unstructuredList(items []unstructured.Unstructured) *unstructured.UnstructuredList {
	list := &unstructured.UnstructuredList{Items: items}
	list.SetAPIVersion("v1")
	list.SetKind("List")
	return list
}

func formatHeartbeat(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return formatAge(t) + " ago"
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + labels[k]
	}
	return strings.Join(parts, ",")
}

func newEdgeGetCommand() *cobra.Command {
	output := newOutputFlags(outputJSON, outputYAML)
	cmd := &cobra.Command{
		Use:               "get <name>",
		Aliases:           []string{"describe", "show"},
		Short:             "Show an edge's connection status and details",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeEdgeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmdContext(cmd)

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			edge, _, err := getEdgeByName(ctx, dynClient, name)
			if err != nil {
				return fmt.Errorf("getting edge %q: %w", name, err)
			}
			if output.structured() {
				return output.printStructured(cmd.OutOrStdout(), edge.Object)
			}
			raw, _, err := loadRawKubeconfig()
			if err != nil {
				return err
			}
			printEdgeDetails(cmd.OutOrStdout(), edge, raw)
			return nil
		},
	}
	output.addFlag(cmd)
	return cmd
}

func printEdgeDetails(w io.Writer, edge *unstructured.Unstructured, raw *clientcmdapi.Config) {
	connected, _, _ := unstructuredNestedBool(edge.Object, "status", "connected")
	proxyURL := getNestedString(*edge, "status", "URL")
	if proxyURL != "" {
		if external, err := externalizeEdgeURL(proxyURL, raw); err == nil {
			proxyURL = external
		}
	}
	p := func(label, value string) { _, _ = fmt.Fprintf(w, "%-16s%s\n", label+":", formatStringOrDash(value)) }
	p("Name", edge.GetName())
	p("Type", edgeTypeOf(edge))
	p("Phase", getNestedString(*edge, "status", "phase"))
	p("Connected", fmt.Sprintf("%v", connected))
	p("Agent version", getNestedString(*edge, "status", "agentVersion"))
	p("Hostname", getNestedString(*edge, "status", "hostname"))
	p("Last heartbeat", formatHeartbeat(getNestedString(*edge, "status", "lastHeartbeatTime")))
	p("Proxy URL", proxyURL)
	p("Created", edge.GetCreationTimestamp().Format("2006-01-02 15:04:05"))
	p("Labels", formatLabels(edge.GetLabels()))

	conditions, _, _ := unstructured.NestedSlice(edge.Object, "status", "conditions")
	if len(conditions) > 0 {
		_, _ = fmt.Fprintln(w, "Conditions:")
		for _, c := range conditions {
			m, _ := c.(map[string]interface{})
			cond := unstructured.Unstructured{Object: m}
			line := fmt.Sprintf("  %s=%s", getNestedString(cond, "type"), getNestedString(cond, "status"))
			if reason := getNestedString(cond, "reason"); reason != "" {
				line += " (" + reason + ")"
			}
			if msg := getNestedString(cond, "message"); msg != "" {
				line += ": " + msg
			}
			_, _ = fmt.Fprintln(w, line)
		}
	}

	switch edgeTypeOf(edge) {
	case edgeTypeKubernetes:
		_, _ = fmt.Fprintf(w, "\nNext: faros connect %s   (or: faros edge kubeconfig %s -o <file>)\n", edge.GetName(), edge.GetName())
	case edgeTypeServer:
		_, _ = fmt.Fprintf(w, "\nNext: faros ssh %s\n", edge.GetName())
	case edgeTypeMacOS:
		_, _ = fmt.Fprintln(w, "\nmacOS hosts are service-only: reach them through the EdgeServices they publish (no kubectl or SSH).")
	}
}

func newEdgeDeleteCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:               "delete <name>",
		Aliases:           []string{"rm", "remove"},
		Short:             "Delete an edge (the agent on it loses hub access)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeEdgeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmdContext(cmd)

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			_, gvr, err := getEdgeByName(ctx, dynClient, name)
			if err != nil {
				return err
			}
			if !yes {
				ok, err := confirm(cmd, fmt.Sprintf("Delete edge %q? This cannot be undone.", name))
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("aborted")
				}
			}
			if err := dynClient.Resource(gvr).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
				return fmt.Errorf("deleting edge %q: %w", name, err)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Edge %q deleted.\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Do not ask for confirmation")
	return cmd
}

// completeEdgeNames offers edge names for shell completion. Failures are
// silent: completion must never print errors into the user's command line.
func completeEdgeNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeEdgeNamesOfType(cmd, "", toComplete)
}

func completeEdgeNamesOfType(cmd *cobra.Command, edgeType, toComplete string) ([]string, cobra.ShellCompDirective) {
	ctx, cancel := context.WithTimeout(cmdContext(cmd), 5*time.Second)
	defer cancel()
	dynClient, err := loadDynamicClient()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	items, err := listAllEdges(ctx, dynClient)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for i := range items {
		if edgeType != "" && edgeTypeOf(&items[i]) != edgeType {
			continue
		}
		if strings.HasPrefix(items[i].GetName(), toComplete) {
			names = append(names, items[i].GetName())
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func unstructuredNestedBool(obj map[string]interface{}, fields ...string) (bool, bool, error) {
	val, found, err := unstructuredNestedField(obj, fields...)
	if err != nil || !found {
		return false, found, err
	}
	b, ok := val.(bool)
	if !ok {
		return false, true, fmt.Errorf("expected bool, got %T", val)
	}
	return b, true, nil
}

func unstructuredNestedField(obj map[string]interface{}, fields ...string) (interface{}, bool, error) {
	var val interface{} = obj
	for _, field := range fields {
		m, ok := val.(map[string]interface{})
		if !ok {
			return nil, false, nil
		}
		val, ok = m[field]
		if !ok {
			return nil, false, nil
		}
	}
	return val, true, nil
}
