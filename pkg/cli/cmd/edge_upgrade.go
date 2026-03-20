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

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	pkgversion "github.com/faroshq/faros-kedge/pkg/version"
)

func newEdgeUpgradeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade <name>",
		Short: "Print upgrade instructions for an edge agent",
		Long: `Print upgrade instructions for a named edge agent.

The command detects whether the edge is a Kubernetes (Helm) or server (binary)
deployment and prints the appropriate upgrade steps. If the agent is already
running the same version as this CLI binary, it reports that the agent is
up to date.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := context.Background()

			dynClient, err := loadDynamicClient()
			if err != nil {
				return fmt.Errorf("not logged in — run: kedge login --hub-url <hub-url>\n(original error: %w)", err)
			}

			edge, err := dynClient.Resource(kedgeclient.EdgeGVR).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("getting edge %q: %w", name, err)
			}

			edgeType := getNestedString(*edge, "spec", "type")
			agentVersion := getNestedString(*edge, "status", "agentVersion")
			hubVersion := pkgversion.Get()

			if agentVersion == "" {
				agentVersion = "unknown (agent has not yet reported its version)"
			}

			// Check if up to date.
			if agentVersion == hubVersion {
				fmt.Printf("Agent %q is up to date (%s)\n", name, hubVersion)
				return nil
			}

			fmt.Printf("Agent %q is running %s. Latest is %s.\n", name, agentVersion, hubVersion)
			fmt.Println()

			switch edgeType {
			case "kubernetes", "":
				printKubernetesUpgradeInstructions(name)
			case "server":
				printServerUpgradeInstructions(name, loadHubURL())
			default:
				fmt.Printf("Unknown edge type %q — cannot determine upgrade method.\n", edgeType)
			}

			return nil
		},
	}
}

func printKubernetesUpgradeInstructions(name string) {
	fmt.Printf("If the agent was installed via 'kedge agent join':\n\n")
	fmt.Printf("  kedge agent upgrade %s\n", name)
	fmt.Println()
	fmt.Printf("If the agent was installed via Helm:\n\n")
	fmt.Printf("  helm upgrade kedge-agent oci://ghcr.io/faroshq/charts/kedge-agent \\\n")
	fmt.Printf("    --reuse-values \\\n")
	fmt.Printf("    --set agent.image.tag=latest\n")
	fmt.Println()
	fmt.Printf("  Or to pin a specific version:\n")
	fmt.Printf("  helm upgrade kedge-agent oci://ghcr.io/faroshq/charts/kedge-agent \\\n")
	fmt.Printf("    --reuse-values \\\n")
	fmt.Printf("    --version <chart-version>\n")
	fmt.Println()
	fmt.Printf("After upgrading, verify with:\n")
	fmt.Printf("  kedge edge list\n")
	fmt.Printf("  # or watch the agent version column:\n")
	fmt.Printf("  watch kedge edge list\n")
}

func printServerUpgradeInstructions(name, _ string) {
	fmt.Printf("To upgrade the binary on the remote server:\n\n")
	fmt.Printf("  curl -fsSL https://github.com/faroshq/kedge/releases/latest/download/kubectl-kedge_linux_amd64.tar.gz | tar xz\n")
	fmt.Printf("  sudo mv kubectl-kedge /usr/local/bin/kedge\n")
	fmt.Println()
	fmt.Printf("Then restart the agent:\n\n")
	fmt.Printf("  sudo systemctl restart kedge-agent-%s\n", name)
	fmt.Println()
	fmt.Printf("After upgrading, verify with:\n")
	fmt.Printf("  kedge edge list\n")
}
