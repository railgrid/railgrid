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
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

const (
	sshServerName = "e2e-ssh-server"
	sshTestMarker = "kedge_ssh_e2e_ok"
)

// SSHServerModeConnect verifies the full SSH path end-to-end:
//  1. Start an embedded test SSH server + kedge agent (--mode=server) as subprocesses
//  2. Wait for the Edge resource to become Ready on the hub
//  3. Run `kedge ssh <name> -- echo <marker>` and verify the marker in output
//  4. Verify interactive PTY (WebSocket, resize, keystrokes, output)
//  5. Hold the session for the configured duration and assert it stays alive
func SSHServerModeConnect() features.Feature {
	return features.New("SSH/ServerModeConnect").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			proc := &framework.ServerProcess{
				ServerName:    sshServerName,
				HubURL:        clusterEnv.HubURL,
				HubKubeconfig: clusterEnv.HubKubeconfig,
				Token:         framework.DevToken,
				AgentBin:      framework.AgentBinPath(),
				SSHPort:       framework.DefaultTestSSHPort,
			}

			if err := proc.Start(ctx); err != nil {
				t.Fatalf("starting server process: %v", err)
			}

			if err := proc.WaitForAgentReady(ctx, 60*time.Second); err != nil {
				t.Fatalf("agent not ready: %v\nlogs:\n%s", err, proc.Logs())
			}

			return framework.WithServerProcess(ctx, proc)
		}).
		Assess("edge_resource_becomes_Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			if err := framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				out, err := framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
					"get", "edges", sshServerName,
					"-o", "jsonpath={.status.phase},{.status.connected}",
				)
				if err != nil {
					return false, nil
				}
				return strings.TrimSpace(out) == "Ready,true", nil
			}); err != nil {
				proc, _ := framework.ServerProcessFromContext(ctx)
				if proc != nil {
					t.Logf("agent logs:\n%s", proc.Logs())
				}
				t.Fatalf("Edge %s did not become Ready within 2 minutes", sshServerName)
			}

			return ctx
		}).
		Assess("ssh_command_returns_expected_output", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			out, err := client.Run(ctx,
				"ssh", sshServerName,
				"--", fmt.Sprintf("echo %s", sshTestMarker),
			)
			if err != nil {
				t.Fatalf("kedge ssh failed: %v\noutput: %s", err, out)
			}
			if !strings.Contains(out, sshTestMarker) {
				t.Fatalf("expected output to contain %q, got:\n%s", sshTestMarker, out)
			}

			return ctx
		}).
		Assess("interactive_pty_sends_keystrokes_and_receives_output", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			const interactiveMarker = "kedge_ssh_interactive_ok"

			client, err := framework.DialSSH(ctx, clusterEnv.HubKubeconfig, sshServerName)
			if err != nil {
				t.Fatalf("dialling SSH WebSocket: %v", err)
			}
			defer client.Close() //nolint:errcheck

			if err := client.SendResize(80, 24); err != nil {
				t.Fatalf("sending resize: %v", err)
			}

			// Wait for the shell to emit its initial prompt.
			_ = client.CollectOutput(ctx, time.Second)

			cmd := fmt.Sprintf("echo %s\n", interactiveMarker)
			if err := client.SendInput([]byte(cmd)); err != nil {
				t.Fatalf("sending input: %v", err)
			}

			out := client.CollectOutput(ctx, 3*time.Second)
			if !strings.Contains(out, interactiveMarker) {
				t.Fatalf("interactive PTY: expected output to contain %q, got:\n%s",
					interactiveMarker, out)
			}

			return ctx
		}).
		Assess("long_lived_connection_stays_alive", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			holdDuration := framework.SSHKeepaliveDuration
			const aliveMarker = "kedge_ssh_still_alive"

			t.Logf("Holding SSH session open for %s to verify keepalive...", holdDuration)

			client, err := framework.DialSSH(ctx, clusterEnv.HubKubeconfig, sshServerName)
			if err != nil {
				t.Fatalf("dialling SSH WebSocket: %v", err)
			}
			defer client.Close() //nolint:errcheck

			if err := client.SendResize(80, 24); err != nil {
				t.Fatalf("sending resize: %v", err)
			}
			_ = client.CollectOutput(ctx, time.Second)

			holdCtx, cancel := context.WithTimeout(ctx, holdDuration)
			defer cancel()

			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
		hold:
			for {
				select {
				case <-holdCtx.Done():
					break hold
				case <-ticker.C:
					_ = client.CollectOutput(ctx, 100*time.Millisecond)
				}
			}

			t.Logf("Hold complete (%s elapsed). Verifying session is still responsive...", holdDuration)

			cmd := fmt.Sprintf("echo %s\n", aliveMarker)
			if err := client.SendInput([]byte(cmd)); err != nil {
				t.Fatalf("session dead after %s: SendInput failed: %v", holdDuration, err)
			}

			out := client.CollectOutput(ctx, 5*time.Second)
			if !strings.Contains(out, aliveMarker) {
				t.Fatalf("session dead after %s: expected %q in output, got:\n%s",
					holdDuration, aliveMarker, out)
			}

			t.Logf("Session still alive after %s ✓", holdDuration)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if proc, ok := framework.ServerProcessFromContext(ctx); ok {
				proc.Stop()
			}

			clusterEnv := framework.ClusterEnvFrom(ctx)
			_, _ = framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
				"delete", "edges", sshServerName, "--ignore-not-found",
			)
			return ctx
		}).
		Feature()
}

// SSHEdgeURLSet verifies that status.URL is populated for server-type edges
// and that the URL ends in "/ssh".  It follows the same setup as
// SSHServerModeConnect but only asserts on the URL field.
func SSHEdgeURLSet() features.Feature {
	const sshURLEdgeName = "e2e-ssh-url-server"

	return features.New("SSH/EdgeURLSet").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			proc := &framework.ServerProcess{
				ServerName:    sshURLEdgeName,
				HubURL:        clusterEnv.HubURL,
				HubKubeconfig: clusterEnv.HubKubeconfig,
				Token:         framework.DevToken,
				AgentBin:      framework.AgentBinPath(),
				SSHPort:       framework.DefaultTestSSHPort + 1, // avoid port conflict with SSHServerModeConnect
			}

			if err := proc.Start(ctx); err != nil {
				t.Fatalf("starting server process: %v", err)
			}

			if err := proc.WaitForAgentReady(ctx, 60*time.Second); err != nil {
				t.Fatalf("agent not ready: %v\nlogs:\n%s", err, proc.Logs())
			}

			return framework.WithServerProcess(ctx, proc)
		}).
		Assess("edge_resource_becomes_Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			if err := framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				out, err := framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
					"get", "edges", sshURLEdgeName,
					"-o", "jsonpath={.status.phase},{.status.connected}",
				)
				if err != nil {
					return false, nil
				}
				return strings.TrimSpace(out) == "Ready,true", nil
			}); err != nil {
				proc, _ := framework.ServerProcessFromContext(ctx)
				if proc != nil {
					t.Logf("agent logs:\n%s", proc.Logs())
				}
				t.Fatalf("Edge %s did not become Ready within 2 minutes", sshURLEdgeName)
			}

			return ctx
		}).
		Assess("status_url_is_populated_and_ends_with_ssh", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			edgeURL, err := client.GetEdgeURL(ctx, sshURLEdgeName)
			if err != nil {
				t.Fatalf("getting edge URL for server-type edge %q: %v", sshURLEdgeName, err)
			}
			if !strings.HasSuffix(edgeURL, "/ssh") {
				t.Fatalf("expected server-type edge URL to end with '/ssh', got: %s", edgeURL)
			}
			t.Logf("server-type edge URL: %s", edgeURL)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if proc, ok := framework.ServerProcessFromContext(ctx); ok {
				proc.Stop()
			}

			clusterEnv := framework.ClusterEnvFrom(ctx)
			_, _ = framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
				"delete", "edges", sshURLEdgeName, "--ignore-not-found",
			)
			return ctx
		}).
		Feature()
}

// SSHDockerServerModeConnect is the Docker-based variant of SSHServerModeConnect.
// It runs lscr.io/linuxserver/openssh-server in a container (--network host)
// alongside a kedge server-mode agent, and verifies the full SSH path through
// the hub tunnel.
func SSHDockerServerModeConnect() features.Feature {
	const dockerServerName = "e2e-ssh-docker-server"

	return features.New("SSH/DockerServerModeConnect").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			container := &framework.ServerContainer{
				Name:       "kedge-e2e-ssh-docker",
				ServerName: dockerServerName,
				HubURL:     clusterEnv.HubURL,
				HubCluster: framework.ClusterNameFromKubeconfig(clusterEnv.HubKubeconfig),
				Token:      framework.DevToken,
				AgentBin:   framework.AgentBinPath(),
			}

			if err := container.Start(ctx); err != nil {
				t.Fatalf("starting Docker SSH container: %v", err)
			}

			if err := container.WaitForAgentReady(ctx, 90*time.Second); err != nil {
				logs, _ := container.AgentLogs(ctx)
				t.Fatalf("agent not ready in container: %v\nlogs:\n%s", err, logs)
			}

			return framework.WithServerContainer(ctx, container)
		}).
		Assess("docker_edge_resource_becomes_Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			if err := framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				out, err := framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
					"get", "edges", dockerServerName,
					"-o", "jsonpath={.status.phase},{.status.connected}",
				)
				if err != nil {
					return false, nil
				}
				return strings.TrimSpace(out) == "Ready,true", nil
			}); err != nil {
				t.Fatalf("Docker Edge %s did not become Ready", dockerServerName)
			}

			return ctx
		}).
		Assess("docker_ssh_command_returns_expected_output", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			const dockerMarker = "kedge_ssh_docker_ok"
			out, err := client.Run(ctx,
				"ssh", dockerServerName,
				"--", fmt.Sprintf("echo %s", dockerMarker),
			)
			if err != nil {
				t.Fatalf("kedge ssh (docker) failed: %v\noutput: %s", err, out)
			}
			if !strings.Contains(out, dockerMarker) {
				t.Fatalf("expected output to contain %q, got:\n%s", dockerMarker, out)
			}

			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if container, ok := framework.ServerContainerFromContext(ctx); ok {
				if err := container.Stop(ctx); err != nil {
					t.Logf("warning: stopping container: %v", err)
				}
			}

			clusterEnv := framework.ClusterEnvFrom(ctx)
			_, _ = framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
				"delete", "edges", dockerServerName, "--ignore-not-found",
			)
			return ctx
		}).
		Feature()
}
