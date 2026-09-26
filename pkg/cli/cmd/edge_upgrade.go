/*
Copyright 2026 The Railgrid Authors.

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

	railgridclient "github.com/railgrid/railgrid/pkg/client"
	pkgversion "github.com/railgrid/railgrid/pkg/version"
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
				return fmt.Errorf("not logged in — run: railgrid login --hub-url <hub-url>\n(original error: %w)", err)
			}

			edge, gvr, err := getEdgeByName(ctx, dynClient, name)
			if err != nil {
				return fmt.Errorf("getting edge %q: %w", name, err)
			}
			// A qualified reference ("server/minis") may have named the edge;
			// everything downstream wants the plain name.
			name = edge.GetName()

			edgeType := railgridclient.EdgeTypeForGVR(gvr)
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
			case "macos":
				printMacOSUpgradeInstructions(name)
			default:
				fmt.Printf("Unknown edge type %q — cannot determine upgrade method.\n", edgeType)
			}

			return nil
		},
	}
}

func printKubernetesUpgradeInstructions(name string) {
	fmt.Printf("If the agent was installed via 'railgrid agent join':\n\n")
	fmt.Printf("  railgrid agent upgrade %s\n", name)
	fmt.Println()
	fmt.Printf("If the agent was installed via Helm:\n\n")
	fmt.Printf("  helm upgrade railgrid-agent oci://ghcr.io/railgrid/charts/railgrid-agent \\\n")
	fmt.Printf("    --namespace railgrid-system \\\n")
	fmt.Printf("    --reuse-values \\\n")
	fmt.Printf("    --set agent.image.tag=latest\n")
	fmt.Println()
	fmt.Printf("  Or to pin a specific version:\n")
	fmt.Printf("  helm upgrade railgrid-agent oci://ghcr.io/railgrid/charts/railgrid-agent \\\n")
	fmt.Printf("    --namespace railgrid-system \\\n")
	fmt.Printf("    --reuse-values \\\n")
	fmt.Printf("    --version <chart-version>\n")
	fmt.Println()
	fmt.Printf("After upgrading, verify with:\n")
	fmt.Printf("  railgrid edge list\n")
	fmt.Printf("  # or watch the agent version column:\n")
	fmt.Printf("  watch railgrid edge list\n")
}

func printServerUpgradeInstructions(name, _ string) {
	fmt.Printf("To upgrade the binary on the remote server:\n\n")
	fmt.Printf("  curl -fsSL https://github.com/railgrid/railgrid/releases/latest/download/kubectl-railgrid_linux_amd64.tar.gz | tar xz\n")
	fmt.Printf("  sudo mv kubectl-railgrid /usr/local/bin/railgrid\n")
	fmt.Println()
	fmt.Printf("Then restart the agent:\n\n")
	fmt.Printf("  sudo systemctl restart railgrid-agent-%s\n", name)
	fmt.Println()
	fmt.Printf("After upgrading, verify with:\n")
	fmt.Printf("  railgrid edge list\n")
}

func printMacOSUpgradeInstructions(name string) {
	fmt.Printf("To upgrade the macOS agent binary on the worker host:\n\n")
	fmt.Printf("  curl -fsSL https://github.com/railgrid/railgrid/releases/latest/download/kubectl-railgrid_$(uname -s)_$(uname -m).tar.gz | tar xz\n")
	fmt.Printf("  sudo mv kubectl-railgrid /usr/local/bin/railgrid\n")
	fmt.Println()
	fmt.Printf("Then restart the launchd service:\n\n")
	fmt.Printf("  sudo launchctl kickstart -k system/com.railgrid.agent.%s\n", name)
	fmt.Println()
	fmt.Printf("After upgrading, verify with:\n")
	fmt.Printf("  railgrid edge list\n")
}
