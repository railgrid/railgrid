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

package cases

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// cliFlowAgentKey is the context key for the TokenAgent in AgentCLIFlow.
type cliFlowAgentKey struct{}

// AgentCLIFlow returns a feature that exercises the full user-facing CLI journey:
//
//  1. kedge login --hub-url <hub> --token <token>
//  2. kedge edge create <name> --type kubernetes
//  3. kedge edge join-command <name>  → capture output, parse Option C args
//  4. Start agent using parsed flags (kedge agent run)
//  5. Wait for edge to become Ready
//  6. kedge kubeconfig edge <name> --output <path>
//  7. kubectl --kubeconfig <path> get nodes → verify cluster access
//
// This closes the gap where "join-command" output was never executed in tests:
// any format change in the output would now be caught immediately.
func AgentCLIFlow() features.Feature {
	const edgeName = "e2e-cli-flow"

	return features.New("Agent/CLIFlow").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}

			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Step 1: kedge login.
			t.Log("step 1: kedge login")
			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}

			// Step 2: kedge edge create.
			t.Log("step 2: kedge edge create")
			if err := client.EdgeCreate(ctx, edgeName, "kubernetes"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}

			// Step 3: kedge edge join-command — capture output.
			t.Log("step 3: kedge edge join-command")
			joinOut, err := client.EdgeJoinCommand(ctx, edgeName)
			if err != nil {
				t.Fatalf("edge join-command failed: %v", err)
			}
			t.Logf("join-command output:\n%s", joinOut)

			// Step 4: Parse Option C from the output and start the agent.
			t.Log("step 4: parse join-command output and start agent")
			parsedHubURL, parsedEdgeName, parsedToken, parsedType, err := parseJoinCommandOutput(joinOut)
			if err != nil {
				t.Fatalf("parsing join-command output: %v", err)
			}

			agent := framework.NewAgentWithToken(framework.RepoRoot(), parsedHubURL, parsedEdgeName, parsedToken).
				WithType(parsedType).
				WithAgentKubeconfig(clusterEnv.AgentKubeconfig)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start agent: %v", err)
			}

			return context.WithValue(ctx, cliFlowAgentKey{}, agent)
		}).
		Assess("edge becomes Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			t.Log("step 5: wait for edge Ready")
			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready: %v", edgeName, err)
			}
			return ctx
		}).
		Assess("kubeconfig edge is usable", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Step 6: kedge kubeconfig edge → write kubeconfig.
			kubeconfigPath := filepath.Join(clusterEnv.WorkDir, "e2e-cli-flow.kubeconfig")
			t.Log("step 6: kedge kubeconfig edge")
			if err := client.WaitForEdgeKubeconfig(ctx, edgeName, kubeconfigPath, 2*time.Minute); err != nil {
				t.Fatalf("waiting for edge kubeconfig: %v", err)
			}

			// Step 7: kubectl --kubeconfig <path> get nodes → assert Ready.
			// The edge API proxy may not be immediately available after the kubeconfig
			// is written (the tunnel needs to be fully established), so we retry.
			t.Log("step 7: kubectl get nodes")
			var lastOut string
			if err := framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				out, err := framework.KubectlWithConfig(ctx, kubeconfigPath, "--insecure-skip-tls-verify", "get", "nodes")
				if err != nil {
					lastOut = out
					return false, nil // keep retrying on transient errors
				}
				lastOut = out
				return strings.Contains(out, "Ready"), nil
			}); err != nil {
				t.Fatalf("kubectl get nodes did not return Ready nodes within timeout: %v\nlast output:\n%s", err, lastOut)
			}
			t.Logf("kubectl get nodes output:\n%s", lastOut)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(cliFlowAgentKey{}).(*framework.TokenAgent); ok {
				a.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// parseJoinCommandOutput extracts --hub-url, --edge-name, --type, and --token
// from the output of `kedge edge join-command`. It looks for the "Option C"
// section containing "kedge agent run" and parses the multiline
// backslash-continuation flags.
//
// Expected output format:
//
//	# Option C — foreground process (dev/containers):
//	kedge agent run \
//	  --hub-url https://kedge.localhost:9443 \
//	  --edge-name my-edge \
//	  --type kubernetes \
//	  --token abc123
func parseJoinCommandOutput(output string) (hubURL, edgeName, token, agentType string, err error) {
	lines := strings.Split(output, "\n")
	inBlock := false

	for _, line := range lines {
		// Strip trailing backslash continuation and surrounding whitespace.
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "\\"))

		if strings.HasPrefix(line, "kedge agent run") {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}

		switch {
		case strings.HasPrefix(line, "--hub-url "):
			hubURL = strings.TrimPrefix(line, "--hub-url ")
		case strings.HasPrefix(line, "--edge-name "):
			edgeName = strings.TrimPrefix(line, "--edge-name ")
		case strings.HasPrefix(line, "--type "):
			agentType = strings.TrimPrefix(line, "--type ")
		case strings.HasPrefix(line, "--token "):
			token = strings.TrimPrefix(line, "--token ")
		}

		// Stop at empty line or next comment section header.
		if line == "" || strings.HasPrefix(line, "#") {
			if hubURL != "" && edgeName != "" && token != "" {
				break
			}
			if strings.HasPrefix(line, "#") && inBlock {
				inBlock = false
			}
		}
	}

	if hubURL == "" || edgeName == "" || token == "" {
		return "", "", "", "", fmt.Errorf(
			"could not parse join-command output: hubURL=%q edgeName=%q token=%q\noutput:\n%s",
			hubURL, edgeName, token, output,
		)
	}
	// Default to "kubernetes" if type was not printed (forward-compat).
	if agentType == "" {
		agentType = "kubernetes"
	}
	return hubURL, edgeName, token, agentType, nil
}
