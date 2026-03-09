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
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

func newEdgeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edge",
		Short: "Manage edges",
	}

	cmd.AddCommand(
		newEdgeCreateCommand(),
		newEdgeListCommand(),
		newEdgeGetCommand(),
		newEdgeDeleteCommand(),
		newEdgeJoinCommandCommand(),
	)

	return cmd
}

func newEdgeCreateCommand() *cobra.Command {
	var labels map[string]string
	var edgeType string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an edge",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := context.Background()

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			if edgeType == "" {
				edgeType = "kubernetes"
			}

			edge := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": kedgeclient.EdgeGVR.Group + "/" + kedgeclient.EdgeGVR.Version,
					"kind":       "Edge",
					"metadata": map[string]interface{}{
						"name": name,
					},
					"spec": map[string]interface{}{
						"type": edgeType,
					},
				},
			}

			if len(labels) > 0 {
				lbls := make(map[string]interface{}, len(labels))
				for k, v := range labels {
					lbls[k] = v
				}
				edge.Object["metadata"].(map[string]interface{})["labels"] = lbls
			}

			_, err = dynClient.Resource(kedgeclient.EdgeGVR).Create(ctx, edge, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("creating edge %q: %w", name, err)
			}

			fmt.Printf("✓ Edge %q created\n", name)

			// Poll for the join token (set by the hub controller on creation).
			joinToken, err := pollJoinTokenDynamic(ctx, name, 30*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not retrieve join token: %v\n", err)
				fmt.Printf("\nRun 'kedge edge join-command %s' to print the join command once the token is available.\n", name)
				return nil
			}

			// Get hub URL from the current kubeconfig.
			hubURL := loadHubURL()

			printJoinCommand(name, edgeType, hubURL, joinToken)
			return nil
		},
	}

	cmd.Flags().StringToStringVar(&labels, "labels", nil, "Labels for this edge (key=value pairs)")
	cmd.Flags().StringVar(&edgeType, "type", "kubernetes", "Edge type: kubernetes or server")

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
		edge, err := dynClient.Resource(kedgeclient.EdgeGVR).Get(ctx, name, metav1.GetOptions{})
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
		time.Sleep(1 * time.Second)
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
func printJoinCommand(name, edgeType, hubURL, joinToken string) {
	fmt.Println()
	if edgeType == "kubernetes" {
		fmt.Printf("To install the kedge agent on your Kubernetes cluster:\n\n")
		fmt.Printf("  kedge install --type kubernetes \\\n")
		fmt.Printf("    --hub-url %s \\\n", hubURL)
		fmt.Printf("    --edge-name %s \\\n", name)
		fmt.Printf("    --token %s\n", joinToken)
		fmt.Println()
		fmt.Printf("Or run the agent directly on a node in your cluster:\n\n")
		fmt.Printf("  kedge agent join \\\n")
		fmt.Printf("    --hub-url %s \\\n", hubURL)
		fmt.Printf("    --edge-name %s \\\n", name)
		fmt.Printf("    --type kubernetes \\\n")
		fmt.Printf("    --token %s\n", joinToken)
	} else {
		fmt.Printf("To install the kedge agent on your server:\n\n")
		fmt.Printf("  # As a systemd service (recommended):\n")
		fmt.Printf("  kedge install --type server \\\n")
		fmt.Printf("    --hub-url %s \\\n", hubURL)
		fmt.Printf("    --edge-name %s \\\n", name)
		fmt.Printf("    --token %s\n", joinToken)
		fmt.Println()
		fmt.Printf("  # Or run in foreground (dev/test):\n")
		fmt.Printf("  kedge agent join \\\n")
		fmt.Printf("    --hub-url %s \\\n", hubURL)
		fmt.Printf("    --edge-name %s \\\n", name)
		fmt.Printf("    --type server \\\n")
		fmt.Printf("    --token %s\n", joinToken)
	}
	fmt.Println()
	fmt.Printf("Run 'kedge edge join-command %s' to print this again.\n", name)
}

// newEdgeJoinCommandCommand returns the 'kedge edge join-command <name>' subcommand.
func newEdgeJoinCommandCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "join-command <name>",
		Short: "Print the agent join command for an edge",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := context.Background()

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			edge, err := dynClient.Resource(kedgeclient.EdgeGVR).Get(ctx, name, metav1.GetOptions{})
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

			edgeType := getNestedString(*edge, "spec", "type")
			if edgeType == "" {
				edgeType = "kubernetes"
			}

			hubURL := loadHubURL()
			printJoinCommand(name, edgeType, hubURL, joinToken)
			return nil
		},
	}
}

func newEdgeListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all edges",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			list, err := dynClient.Resource(kedgeclient.EdgeGVR).List(ctx, metav1.ListOptions{})
			if err != nil {
				return fmt.Errorf("listing edges: %w", err)
			}

			if len(list.Items) == 0 {
				fmt.Println("No edges found.")
				return nil
			}

			tw := newTabWriter(os.Stdout)
			printRow(tw, "NAME", "TYPE", "PHASE", "CONNECTED", "AGE")

			for _, item := range list.Items {
				edgeType := getNestedString(item, "spec", "type")
				phase := getNestedString(item, "status", "phase")
				connected, _, _ := unstructuredNestedBool(item.Object, "status", "connected")
				age := formatAge(item.GetCreationTimestamp().Time)
				printRow(tw, item.GetName(), formatStringOrDash(edgeType), formatStringOrDash(phase),
					fmt.Sprintf("%v", connected), age)
			}

			_ = tw.Flush()
			return nil
		},
	}
}

func newEdgeGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get [name]",
		Short: "Get edge details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := context.Background()

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			edge, err := dynClient.Resource(kedgeclient.EdgeGVR).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("getting edge %q: %w", name, err)
			}

			edgeType := getNestedString(*edge, "spec", "type")
			phase := getNestedString(*edge, "status", "phase")
			hostname := getNestedString(*edge, "status", "hostname")
			workspaceURL := getNestedString(*edge, "status", "workspaceURL")
			connected, _, _ := unstructuredNestedBool(edge.Object, "status", "connected")

			fmt.Printf("Name:          %s\n", edge.GetName())
			fmt.Printf("Type:          %s\n", formatStringOrDash(edgeType))
			fmt.Printf("Phase:         %s\n", formatStringOrDash(phase))
			fmt.Printf("Connected:     %v\n", connected)
			fmt.Printf("Hostname:      %s\n", formatStringOrDash(hostname))
			fmt.Printf("WorkspaceURL:  %s\n", formatStringOrDash(workspaceURL))
			fmt.Printf("Created:       %s\n", edge.GetCreationTimestamp().Format("2006-01-02 15:04:05"))

			// Print labels if any
			if lbls := edge.GetLabels(); len(lbls) > 0 {
				fmt.Println("Labels:")
				for k, v := range lbls {
					fmt.Printf("  %s=%s\n", k, v)
				}
			}

			return nil
		},
	}
}

func newEdgeDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete an edge",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := context.Background()

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			if err := dynClient.Resource(kedgeclient.EdgeGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
				return fmt.Errorf("deleting edge %q: %w", name, err)
			}

			fmt.Printf("Edge %q deleted.\n", name)
			return nil
		},
	}
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
