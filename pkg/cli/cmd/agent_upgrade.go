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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	pkgversion "github.com/faroshq/faros-kedge/pkg/version"
)

func newAgentUpgradeCommand() *cobra.Command {
	var (
		imageTag string
		wait     bool
	)

	cmd := &cobra.Command{
		Use:   "upgrade <edge-name>",
		Short: "Upgrade the agent for an edge deployed via 'kedge agent join'",
		Long: `Upgrade the kedge agent for a Kubernetes edge that was deployed using
"kedge agent join". This patches the agent Deployment in the kedge-agent
namespace with the new image tag.

For agents installed via Helm, use "helm upgrade" instead.
For server-type agents, this restarts the systemd service after you update
the binary.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			edgeName := args[0]
			ctx := context.Background()

			// Resolve image tag: default to the CLI version.
			tag := imageTag
			if tag == "" {
				tag = pkgversion.Get()
			}

			// Look up the edge on the hub to determine its type.
			dynClient, err := loadDynamicClient()
			if err != nil {
				return fmt.Errorf("not logged in — run: kedge login --hub-url <hub-url>\n(original error: %w)", err)
			}

			edge, err := dynClient.Resource(kedgeclient.EdgeGVR).Get(ctx, edgeName, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("getting edge %q: %w", edgeName, err)
			}

			edgeType := getNestedString(*edge, "spec", "type")
			agentVersion := getNestedString(*edge, "status", "agentVersion")

			if agentVersion == tag {
				fmt.Printf("Agent %q is already running %s — nothing to do.\n", edgeName, tag)
				return nil
			}

			switch edgeType {
			case "kubernetes", "":
				return agentUpgradeKubernetes(ctx, edgeName, tag, wait)
			case "server":
				return agentUpgradeServer(edgeName)
			default:
				return fmt.Errorf("unknown edge type %q", edgeType)
			}
		},
	}

	cmd.Flags().StringVar(&imageTag, "tag", "", "Image tag to upgrade to (default: CLI version)")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for the rollout to complete")

	return cmd
}

// agentUpgradeKubernetes patches the kedge-agent Deployment to use the new image tag.
func agentUpgradeKubernetes(ctx context.Context, edgeName, tag string, wait bool) error {
	deployName := "kedge-agent-" + edgeName
	namespace := "kedge-agent"

	agentImage := os.Getenv("KEDGE_AGENT_IMAGE")
	if agentImage == "" {
		agentImage = "ghcr.io/faroshq/kedge-agent"
	}
	newImage := agentImage + ":" + tag

	// Build the strategic merge patch to update the container image.
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{
						{
							"name":  "agent",
							"image": newImage,
						},
					},
				},
			},
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshalling patch: %w", err)
	}

	// Use kubectl to patch the deployment — avoids needing a full typed client
	// for the target cluster (which may use a different kubeconfig than the hub).
	args := []string{
		"patch", "deployment", deployName,
		"-n", namespace,
		"--type", "strategic",
		"-p", string(patchBytes),
	}

	out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("patching deployment %s/%s: %w\n%s", namespace, deployName, err, out)
	}

	fmt.Printf("Deployment %s/%s patched — image set to %s\n", namespace, deployName, newImage)

	if wait {
		fmt.Printf("Waiting for rollout to complete...\n")
		waitArgs := []string{
			"rollout", "status", "deployment", deployName,
			"-n", namespace,
			"--timeout", "120s",
		}
		waitCmd := exec.CommandContext(ctx, "kubectl", waitArgs...)
		waitCmd.Stdout = os.Stdout
		waitCmd.Stderr = os.Stderr
		if err := waitCmd.Run(); err != nil {
			return fmt.Errorf("rollout did not complete: %w", err)
		}
		fmt.Printf("Agent %q upgraded to %s\n", edgeName, tag)

		// Give the agent a moment to report its new version.
		fmt.Printf("Verifying agent version (may take up to 30s)...\n")
		if err := waitForAgentVersion(ctx, edgeName, tag, 60*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
			fmt.Printf("Run 'kedge edge list' to check the agent version.\n")
		} else {
			fmt.Printf("Agent %q now reports version %s\n", edgeName, tag)
		}
	}

	return nil
}

// waitForAgentVersion polls the Edge resource until the agent reports the expected version.
func waitForAgentVersion(ctx context.Context, edgeName, expectedVersion string, timeout time.Duration) error {
	dynClient, err := loadDynamicClient()
	if err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)
	for {
		edge, err := dynClient.Resource(kedgeclient.EdgeGVR).Get(ctx, edgeName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("getting edge: %w", err)
		}
		v := getNestedString(*edge, "status", "agentVersion")
		if v == expectedVersion {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for agent to report version %s (current: %s)", expectedVersion, v)
		}
		time.Sleep(3 * time.Second)
	}
}

// agentUpgradeServer prints instructions for upgrading a server-type agent.
func agentUpgradeServer(edgeName string) error {
	fmt.Printf("Server-type agents must be upgraded by replacing the binary on the host.\n\n")
	fmt.Printf("  # Download the latest binary:\n")
	fmt.Printf("  curl -fsSL https://github.com/faroshq/kedge/releases/latest/download/kubectl-kedge_linux_amd64.tar.gz | tar xz\n")
	fmt.Printf("  sudo mv kubectl-kedge /usr/local/bin/kedge\n\n")
	fmt.Printf("  # Restart the systemd service:\n")
	fmt.Printf("  sudo systemctl restart kedge-agent-%s\n\n", edgeName)
	fmt.Printf("After upgrading, verify with:\n")
	fmt.Printf("  kedge edge list\n")
	return nil
}
