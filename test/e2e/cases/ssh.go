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

// ---------- SSH User Mapping tests ----------------------------------------

// sshMappingSSHPort base offset for user-mapping tests (avoids conflicts with
// SSHServerModeConnect which uses DefaultTestSSHPort and DefaultTestSSHPort+1).
const (
	sshMappingInheritedPort = framework.DefaultTestSSHPort + 10
	sshMappingProvidedPort  = framework.DefaultTestSSHPort + 11
	sshMappingIdentityPort  = framework.DefaultTestSSHPort + 12
)

// SSHUserMappingInherited verifies that when sshUserMapping=inherited (or unset /
// default), the hub uses credentials reported by the agent at registration time
// (Edge.Status.SSHCredentials).  The agent is started with --ssh-user=testuser
// --ssh-password=testpassword; the test runs `echo $USER` over SSH and checks
// the output is "testuser".
func SSHUserMappingInherited() features.Feature {
	const edgeName = "e2e-ssh-mapping-inherited"

	return features.New("SSH/UserMappingInherited").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			proc := &framework.ServerProcess{
				ServerName:    edgeName,
				HubURL:        clusterEnv.HubURL,
				HubKubeconfig: clusterEnv.HubKubeconfig,
				Token:         framework.DevToken,
				AgentBin:      framework.AgentBinPath(),
				SSHPort:       sshMappingInheritedPort,
				SSHUser:       "testuser",
				SSHPassword:   "testpassword",
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
					"get", "edges", edgeName, "-o", "jsonpath={.status.phase},{.status.connected}")
				if err != nil {
					return false, nil
				}
				return strings.TrimSpace(out) == "Ready,true", nil
			}); err != nil {
				proc, _ := framework.ServerProcessFromContext(ctx)
				if proc != nil {
					t.Logf("agent logs:\n%s", proc.Logs())
				}
				t.Fatalf("Edge %s did not become Ready within 2 minutes", edgeName)
			}
			return ctx
		}).
		Assess("ssh_user_is_inherited_from_agent_credentials", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// The TestSSHServer sets USER=<authenticated-username> in commands,
			// so `echo $USER` should return the inherited username "testuser".
			out, err := client.Run(ctx, "ssh", edgeName, "--", "echo $USER")
			if err != nil {
				t.Fatalf("kedge ssh failed: %v\noutput: %s", err, out)
			}
			if !strings.Contains(out, "testuser") {
				t.Fatalf("expected output to contain 'testuser' (inherited username), got:\n%s", out)
			}
			t.Logf("inherited SSH username verified: output=%q", strings.TrimSpace(out))
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if proc, ok := framework.ServerProcessFromContext(ctx); ok {
				proc.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			_, _ = framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
				"delete", "edges", edgeName, "--ignore-not-found")
			return ctx
		}).
		Feature()
}

// SSHUserMappingProvided verifies that when sshUserMapping=provided, the hub
// reads the SSH username and key entirely from spec.server.sshCredentialsRef.
// The agent does NOT report any SSH credentials.
func SSHUserMappingProvided() features.Feature {
	const (
		edgeName    = "e2e-ssh-mapping-provided"
		secretName  = "e2e-ssh-mapping-provided-creds"
		secretNS    = "kedge-system"
		sshUsername = "provided-user"
	)

	return features.New("SSH/UserMappingProvided").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			// 1. Generate RSA keypair for SSH authentication.
			privKey, pubKey, privKeyPEM, err := framework.GenerateTestSSHKeypair()
			if err != nil {
				t.Fatalf("generating SSH keypair: %v", err)
			}
			_ = privKey // used via privKeyPEM in the Secret

			// 2. Configure the TestSSHServer to accept only sshUsername with pubKey.
			sshSrv := framework.NewTestSSHServer(sshMappingProvidedPort)
			sshSrv.AddUser(sshUsername, pubKey)

			// 3. Start the agent without SSH credentials.
			proc := &framework.ServerProcess{
				ServerName:    edgeName,
				HubURL:        clusterEnv.HubURL,
				HubKubeconfig: clusterEnv.HubKubeconfig,
				Token:         framework.DevToken,
				AgentBin:      framework.AgentBinPath(),
				SSHPort:       sshMappingProvidedPort,
				SSHServer:     sshSrv,
			}
			if err := proc.Start(ctx); err != nil {
				t.Fatalf("starting server process: %v", err)
			}
			if err := proc.WaitForAgentReady(ctx, 60*time.Second); err != nil {
				t.Fatalf("agent not ready: %v\nlogs:\n%s", err, proc.Logs())
			}

			// Stash the private key PEM for later Assess steps.
			ctx = framework.WithSSHPrivateKeyPEM(ctx, privKeyPEM)
			return framework.WithServerProcess(ctx, proc)
		}).
		Assess("edge_resource_becomes_Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if err := framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				out, err := framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
					"get", "edges", edgeName, "-o", "jsonpath={.status.phase},{.status.connected}")
				if err != nil {
					return false, nil
				}
				return strings.TrimSpace(out) == "Ready,true", nil
			}); err != nil {
				proc, _ := framework.ServerProcessFromContext(ctx)
				if proc != nil {
					t.Logf("agent logs:\n%s", proc.Logs())
				}
				t.Fatalf("Edge %s did not become Ready within 2 minutes", edgeName)
			}
			return ctx
		}).
		Assess("create_secret_and_patch_edge_spec", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			privKeyPEM := framework.SSHPrivateKeyPEMFromContext(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Create the Secret in the hub cluster.
			secretYAML := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: Opaque
stringData:
  username: %s
  privateKey: |
%s`, secretName, secretNS, sshUsername, framework.IndentLines(string(privKeyPEM), "    "))
			if err := client.ApplyManifest(ctx, secretYAML); err != nil {
				t.Fatalf("creating SSH credentials secret: %v", err)
			}

			// Patch the Edge spec to use provided mode.
			patchJSON := fmt.Sprintf(`{"spec":{"server":{"sshUserMapping":"provided","sshCredentialsRef":{"name":%q,"namespace":%q}}}}`,
				secretName, secretNS)
			_, err := framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
				"patch", "edges", edgeName, "--type=merge", "-p", patchJSON)
			if err != nil {
				t.Fatalf("patching edge spec: %v", err)
			}
			t.Logf("Edge %s patched with sshUserMapping=provided", edgeName)
			return ctx
		}).
		Assess("ssh_connects_as_provided_user", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			proc, _ := framework.ServerProcessFromContext(ctx)

			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Run a command over SSH; the TestSSHServer sets USER=<ssh-username>.
			out, err := client.Run(ctx, "ssh", edgeName, "--", "echo $USER")
			if err != nil {
				t.Fatalf("kedge ssh (provided) failed: %v\noutput: %s", err, out)
			}
			if !strings.Contains(out, sshUsername) {
				t.Fatalf("expected output to contain %q (provided username), got:\n%s", sshUsername, out)
			}

			// Double-check via server-side tracking.
			if proc != nil && proc.SSHServer != nil {
				found := false
				for _, u := range proc.SSHServer.ConnectedUsers() {
					if u == sshUsername {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("TestSSHServer did not record connection as %q; connected users: %v",
						sshUsername, proc.SSHServer.ConnectedUsers())
				}
			}
			t.Logf("provided SSH username verified: %q", sshUsername)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if proc, ok := framework.ServerProcessFromContext(ctx); ok {
				proc.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			_, _ = framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
				"delete", "secret", secretName, "-n", secretNS, "--ignore-not-found")
			_, _ = framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
				"delete", "edges", edgeName, "--ignore-not-found")
			return ctx
		}).
		Feature()
}

// SSHUserMappingIdentity verifies that when sshUserMapping=identity, the hub
// uses the caller's kcp/OIDC username as the SSH username.  The SSH key comes
// from spec.server.sshCredentialsRef.
func SSHUserMappingIdentity() features.Feature {
	const (
		edgeName   = "e2e-ssh-mapping-identity"
		secretName = "e2e-ssh-mapping-identity-creds"
		secretNS   = "kedge-system"
	)

	return features.New("SSH/UserMappingIdentity").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			// Resolve caller identity via TokenReview so we know what to expect.
			callerIdentity, err := framework.ResolveCallerIdentity(ctx, clusterEnv.HubKubeconfig)
			if err != nil || callerIdentity == "" {
				t.Skip("kcp TokenReview unavailable or returns empty username; skipping identity-mode test")
			}

			// 1. Generate RSA keypair.
			_, pubKey, privKeyPEM, err := framework.GenerateTestSSHKeypair()
			if err != nil {
				t.Fatalf("generating SSH keypair: %v", err)
			}

			// 2. Configure the TestSSHServer to accept any username with pubKey.
			sshSrv := framework.NewTestSSHServer(sshMappingIdentityPort)
			sshSrv.AddAnyUserKey(pubKey)

			// 3. Start the agent (no SSH credentials).
			proc := &framework.ServerProcess{
				ServerName:    edgeName,
				HubURL:        clusterEnv.HubURL,
				HubKubeconfig: clusterEnv.HubKubeconfig,
				Token:         framework.DevToken,
				AgentBin:      framework.AgentBinPath(),
				SSHPort:       sshMappingIdentityPort,
				SSHServer:     sshSrv,
			}
			if err := proc.Start(ctx); err != nil {
				t.Fatalf("starting server process: %v", err)
			}
			if err := proc.WaitForAgentReady(ctx, 60*time.Second); err != nil {
				t.Fatalf("agent not ready: %v\nlogs:\n%s", err, proc.Logs())
			}

			ctx = framework.WithSSHPrivateKeyPEM(ctx, privKeyPEM)
			ctx = framework.WithCallerIdentity(ctx, callerIdentity)
			return framework.WithServerProcess(ctx, proc)
		}).
		Assess("edge_resource_becomes_Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if err := framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				out, err := framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
					"get", "edges", edgeName, "-o", "jsonpath={.status.phase},{.status.connected}")
				if err != nil {
					return false, nil
				}
				return strings.TrimSpace(out) == "Ready,true", nil
			}); err != nil {
				proc, _ := framework.ServerProcessFromContext(ctx)
				if proc != nil {
					t.Logf("agent logs:\n%s", proc.Logs())
				}
				t.Fatalf("Edge %s did not become Ready within 2 minutes", edgeName)
			}
			return ctx
		}).
		Assess("create_secret_and_patch_edge_spec", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			privKeyPEM := framework.SSHPrivateKeyPEMFromContext(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Create the Secret with just the private key (no username — username
			// comes from the caller's identity at runtime).
			secretYAML := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: Opaque
stringData:
  privateKey: |
%s`, secretName, secretNS, framework.IndentLines(string(privKeyPEM), "    "))
			if err := client.ApplyManifest(ctx, secretYAML); err != nil {
				t.Fatalf("creating SSH key secret: %v", err)
			}

			patchJSON := fmt.Sprintf(`{"spec":{"server":{"sshUserMapping":"identity","sshCredentialsRef":{"name":%q,"namespace":%q}}}}`,
				secretName, secretNS)
			_, err := framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
				"patch", "edges", edgeName, "--type=merge", "-p", patchJSON)
			if err != nil {
				t.Fatalf("patching edge spec: %v", err)
			}
			t.Logf("Edge %s patched with sshUserMapping=identity", edgeName)
			return ctx
		}).
		Assess("ssh_connects_as_caller_identity", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			expectedUser := framework.CallerIdentityFromContext(ctx)
			proc, _ := framework.ServerProcessFromContext(ctx)

			// Verify via server-side tracking — the TestSSHServer should see the
			// caller's identity as the connecting SSH username.
			// First do a connection to trigger the username capture.
			wsClient, err := framework.DialSSH(ctx, clusterEnv.HubKubeconfig, edgeName)
			if err != nil {
				t.Fatalf("dialling SSH WebSocket (identity mode): %v", err)
			}
			defer wsClient.Close() //nolint:errcheck

			// Allow the connection to be established.
			_ = wsClient.CollectOutput(ctx, 2*time.Second)

			if proc != nil && proc.SSHServer != nil {
				found := false
				for _, u := range proc.SSHServer.ConnectedUsers() {
					if u == expectedUser {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("TestSSHServer did not record connection as %q (callerIdentity); connected users: %v",
						expectedUser, proc.SSHServer.ConnectedUsers())
				}
				t.Logf("identity SSH username verified: %q", expectedUser)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if proc, ok := framework.ServerProcessFromContext(ctx); ok {
				proc.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			_, _ = framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
				"delete", "secret", secretName, "-n", secretNS, "--ignore-not-found")
			_, _ = framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
				"delete", "edges", edgeName, "--ignore-not-found")
			return ctx
		}).
		Feature()
}
