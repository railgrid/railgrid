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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	cliauth "github.com/faroshq/faros-kedge/pkg/cli/auth"
	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// sshOIDCIdentityPort is the embedded SSH server port for SSHOIDCUsernameMapping.
// Offset +13 from DefaultTestSSHPort to avoid conflicts with other SSH tests.
const sshOIDCIdentityPort = framework.DefaultTestSSHPort + 13

// sshOIDCContextKey is the context key for SSHOIDCUsernameMapping setup data.
type sshOIDCContextKey struct{}

// sshOIDCSetupData holds state passed from Setup → Assess → Teardown.
type sshOIDCSetupData struct {
	// oidcKubeconfig is the path to the OIDC user's kubeconfig written to disk.
	oidcKubeconfig string
	// expectedUser is the kcp username that the hub will assign via TokenReview.
	expectedUser string
	// edgeName is the name of the Edge resource created during Setup.
	edgeName string
	// secretName and secretNS identify the SSH key Secret created during Assess.
	secretName string
	secretNS   string
}

// SSHOIDCUsernameMapping verifies that when sshUserMapping=identity, the SSH
// session is established as the caller's OIDC identity (kcp username obtained
// via TokenReview).  This test requires the OIDC suite (Dex); it is skipped in
// the standalone suite.
//
// Flow:
//  1. Headless OIDC login as the Dex test user → kubeconfig + ID token.
//  2. ResolveCallerIdentity against the OIDC kubeconfig → expectedUser.
//  3. Generate SSH keypair; configure TestSSHServer to accept any username.
//  4. Start a server-mode agent using the OIDC kubeconfig (registers the Edge
//     in the OIDC user's kcp workspace).
//  5. Wait for the Edge to become Ready.
//  6. Create a Secret containing the SSH private key in the OIDC workspace.
//  7. Patch the Edge with sshUserMapping=identity + sshCredentialsRef.
//  8. DialSSH using the OIDC kubeconfig (hub receives OIDC token, performs
//     TokenReview, and uses the result as the SSH username).
//  9. Assert TestSSHServer recorded the connection as expectedUser.
func SSHOIDCUsernameMapping() features.Feature {
	const (
		edgeName   = "e2e-ssh-oidc-identity"
		secretName = "e2e-ssh-oidc-identity-creds"
	)

	return features.New("SSH/OIDCUsernameMapping").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			dexEnv := framework.DexEnvFrom(ctx)
			if clusterEnv == nil || dexEnv == nil {
				t.Skip("SSHOIDCUsernameMapping requires the OIDC suite (Dex env not found in context)")
			}

			// ── Step 1: Headless OIDC login ─────────────────────────────────
			loginCtx, cancelLogin := context.WithTimeout(ctx, 90*time.Second)
			defer cancelLogin()

			result, err := framework.HeadlessOIDCLogin(loginCtx, clusterEnv.HubURL, dexEnv.UserEmail, dexEnv.UserPassword)
			if err != nil {
				t.Fatalf("OIDC login failed: %v", err)
			}
			if len(result.Kubeconfig) == 0 {
				t.Fatal("OIDC login returned an empty kubeconfig")
			}

			// Write the OIDC kubeconfig to a temp file so the agent and kubectl
			// commands can use it.
			oidcKubeconfigPath := filepath.Join(t.TempDir(), "oidc.kubeconfig")
			if err := os.WriteFile(oidcKubeconfigPath, result.Kubeconfig, 0600); err != nil {
				t.Fatalf("writing OIDC kubeconfig: %v", err)
			}
			t.Logf("OIDC login succeeded; kubeconfig written to %s", oidcKubeconfigPath)

			// Cache the OIDC token so the exec-credential plugin (`kedge get-token`)
			// can refresh it when making API calls via the kubeconfig.
			if result.IDToken != "" {
				tokenCache := &cliauth.TokenCache{
					IDToken:      result.IDToken,
					RefreshToken: result.RefreshToken,
					ExpiresAt:    result.ExpiresAt,
					IssuerURL:    result.IssuerURL,
					ClientID:     result.ClientID,
				}
				if err := cliauth.SaveTokenCache(tokenCache); err != nil {
					t.Fatalf("caching OIDC token: %v", err)
				}
			}

			// ── Step 2: Resolve caller identity ─────────────────────────────
			// The hub will perform a TokenReview when identity mode is active and
			// use the returned username as the SSH username.  We resolve it here
			// so we know what to assert against at the end.
			callerIdentity, err := framework.ResolveCallerIdentity(ctx, oidcKubeconfigPath)
			if err != nil || callerIdentity == "" {
				t.Skip("kcp TokenReview unavailable or returned an empty username for the OIDC token; skipping OIDC identity-mode test")
			}
			t.Logf("OIDC caller identity: %q", callerIdentity)

			// ── Step 3: SSH keypair + TestSSHServer ──────────────────────────
			_, pubKey, privKeyPEM, err := framework.GenerateTestSSHKeypair()
			if err != nil {
				t.Fatalf("generating SSH keypair: %v", err)
			}

			// Accept any username so the hub can connect as the OIDC identity.
			sshSrv := framework.NewTestSSHServer(sshOIDCIdentityPort)
			sshSrv.AddAnyUserKey(pubKey)

			// ── Step 4: Start server-mode agent using OIDC kubeconfig ────────
			// Using HubKubeconfig = oidcKubeconfigPath ensures the agent registers
			// the Edge in the OIDC user's kcp workspace (the cluster name is
			// derived from the kubeconfig server URL).
			proc := &framework.ServerProcess{
				ServerName:    edgeName,
				HubURL:        clusterEnv.HubURL,
				HubKubeconfig: oidcKubeconfigPath,
				AgentBin:      framework.AgentBinPath(),
				SSHPort:       sshOIDCIdentityPort,
				SSHServer:     sshSrv,
			}
			if err := proc.Start(ctx); err != nil {
				t.Fatalf("starting server-mode agent: %v", err)
			}
			if err := proc.WaitForAgentReady(ctx, 90*time.Second); err != nil {
				t.Fatalf("agent not ready: %v\nlogs:\n%s", err, proc.Logs())
			}
			t.Logf("server-mode agent is ready")

			// Store setup data in context for Assess and Teardown steps.
			setupData := &sshOIDCSetupData{
				oidcKubeconfig: oidcKubeconfigPath,
				expectedUser:   callerIdentity,
				edgeName:       edgeName,
				secretName:     secretName,
				secretNS:       "default",
			}
			ctx = context.WithValue(ctx, sshOIDCContextKey{}, setupData)
			ctx = framework.WithServerProcess(ctx, proc)
			ctx = framework.WithSSHPrivateKeyPEM(ctx, privKeyPEM)
			ctx = framework.WithCallerIdentity(ctx, callerIdentity)
			return ctx
		}).
		Assess("edge_resource_becomes_Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			setupData, ok := ctx.Value(sshOIDCContextKey{}).(*sshOIDCSetupData)
			if !ok {
				t.Skip("setup data not found (setup may have been skipped)")
			}

			// Poll using the OIDC kubeconfig so we stay within the OIDC user's
			// kcp workspace.
			if err := framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				out, err := framework.KubectlWithConfig(ctx, setupData.oidcKubeconfig,
					"get", "edges", setupData.edgeName,
					"--insecure-skip-tls-verify",
					"-o", "jsonpath={.status.phase},{.status.connected}",
				)
				if err != nil {
					return false, nil
				}
				return strings.TrimSpace(out) == "Ready,true", nil
			}); err != nil {
				if proc, ok := framework.ServerProcessFromContext(ctx); ok {
					t.Logf("agent logs:\n%s", proc.Logs())
				}
				t.Fatalf("Edge %s did not become Ready within 2 minutes", setupData.edgeName)
			}
			t.Logf("Edge %s is Ready", setupData.edgeName)
			return ctx
		}).
		Assess("create_secret_and_patch_edge_spec", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			setupData, ok := ctx.Value(sshOIDCContextKey{}).(*sshOIDCSetupData)
			if !ok {
				t.Skip("setup data not found (setup may have been skipped)")
			}

			privKeyPEM := framework.SSHPrivateKeyPEMFromContext(ctx)
			// Use the OIDC kubeconfig so resources are created in the OIDC user's
			// kcp workspace.
			client := framework.NewKedgeClient(framework.RepoRoot(), setupData.oidcKubeconfig, "")

			// Create the Secret containing only the private key (no username — the
			// SSH username is derived from the caller's OIDC identity at runtime).
			secretYAML := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: Opaque
stringData:
  privateKey: |
%s`, setupData.secretName, setupData.secretNS, framework.IndentLines(string(privKeyPEM), "    "))
			if err := client.ApplyManifest(ctx, secretYAML); err != nil {
				t.Fatalf("creating SSH key secret: %v", err)
			}
			t.Logf("Secret %s/%s created", setupData.secretNS, setupData.secretName)

			// Patch the Edge spec to enable identity mode with the SSH key Secret.
			patchJSON := fmt.Sprintf(
				`{"spec":{"server":{"sshUserMapping":"identity","sshCredentialsRef":{"name":%q,"namespace":%q}}}}`,
				setupData.secretName, setupData.secretNS,
			)
			if _, err := framework.KubectlWithConfig(ctx, setupData.oidcKubeconfig,
				"patch", "edges", setupData.edgeName,
				"--insecure-skip-tls-verify",
				"--type=merge", "-p", patchJSON,
			); err != nil {
				t.Fatalf("patching edge spec with identity mode: %v", err)
			}
			t.Logf("Edge %s patched with sshUserMapping=identity", setupData.edgeName)
			return ctx
		}).
		Assess("oidc_user_lands_as_oidc_username", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			setupData, ok := ctx.Value(sshOIDCContextKey{}).(*sshOIDCSetupData)
			if !ok {
				t.Skip("setup data not found (setup may have been skipped)")
			}

			expectedUser := setupData.expectedUser
			proc, _ := framework.ServerProcessFromContext(ctx)

			t.Logf("Dialling SSH for edge %q; expecting to land as %q", setupData.edgeName, expectedUser)

			// DialSSH loads the bearer token from the kubeconfig and sends it to
			// the hub.  The hub (in identity mode) performs a TokenReview and uses
			// the returned kcp username as the SSH username forwarded to the agent.
			wsClient, err := framework.DialSSH(ctx, setupData.oidcKubeconfig, setupData.edgeName)
			if err != nil {
				t.Fatalf("dialling SSH WebSocket (OIDC identity mode): %v", err)
			}
			defer wsClient.Close() //nolint:errcheck

			// Allow time for the SSH handshake to complete and the username to be
			// recorded by the TestSSHServer.
			_ = wsClient.CollectOutput(ctx, 3*time.Second)

			// Verify via server-side tracking: the TestSSHServer records the
			// authenticated username on every successful connection.
			if proc != nil && proc.SSHServer != nil {
				if err := framework.Poll(ctx, time.Second, 10*time.Second, func(ctx context.Context) (bool, error) {
					for _, u := range proc.SSHServer.ConnectedUsers() {
						if u == expectedUser {
							return true, nil
						}
					}
					return false, nil
				}); err != nil {
					t.Fatalf(
						"TestSSHServer did not record a connection as %q (OIDC identity); "+
							"connected users: %v",
						expectedUser, proc.SSHServer.ConnectedUsers(),
					)
				}
				t.Logf("OIDC identity SSH username verified: %q ✓", expectedUser)
			} else {
				// Fallback: send `echo $USER` and check the output contains the
				// expected username (works when proc.SSHServer is unavailable).
				if err := wsClient.SendResize(80, 24); err != nil {
					t.Fatalf("sending terminal resize: %v", err)
				}
				_ = wsClient.CollectOutput(ctx, time.Second) // drain initial prompt

				cmd := "echo $USER\n"
				if err := wsClient.SendInput([]byte(cmd)); err != nil {
					t.Fatalf("sending 'echo $USER' command: %v", err)
				}

				out := wsClient.CollectOutput(ctx, 5*time.Second)
				if !strings.Contains(out, expectedUser) {
					t.Fatalf("expected SSH session output to contain OIDC username %q; got:\n%s",
						expectedUser, out)
				}
				t.Logf("OIDC identity SSH username verified via echo $USER: %q ✓", expectedUser)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if proc, ok := framework.ServerProcessFromContext(ctx); ok {
				proc.Stop()
			}

			setupData, ok := ctx.Value(sshOIDCContextKey{}).(*sshOIDCSetupData)
			if !ok {
				return ctx // setup was skipped, nothing to clean up
			}

			_, _ = framework.KubectlWithConfig(ctx, setupData.oidcKubeconfig,
				"delete", "secret", setupData.secretName,
				"-n", setupData.secretNS,
				"--insecure-skip-tls-verify",
				"--ignore-not-found",
			)
			_, _ = framework.KubectlWithConfig(ctx, setupData.oidcKubeconfig,
				"delete", "edges", setupData.edgeName,
				"--insecure-skip-tls-verify",
				"--ignore-not-found",
			)
			return ctx
		}).
		Feature()
}
