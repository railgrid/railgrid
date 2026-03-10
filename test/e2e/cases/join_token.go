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
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// joinTokenEdgeReadyKey is the context key for the TokenAgent used in
// AgentConnectsWithJoinToken.
type joinTokenEdgeReadyKey struct{}

// joinTokenInvalidKey is the context key for the TokenAgent used in
// InvalidJoinTokenReturns401.
type joinTokenInvalidKey struct{}

// JoinTokenIsSetAfterEdgeCreation verifies that the hub controller
// (TokenReconciler) generates a non-empty join token in Edge.Status.JoinToken
// shortly after the Edge resource is created.
//
// The token must be a 44-character base64url string — the expected encoding of
// 32 cryptographically-random bytes via base64.URLEncoding.
func JoinTokenIsSetAfterEdgeCreation() features.Feature {
	const edgeName = "e2e-join-token-set"

	return features.New("JoinToken/IsSetAfterEdgeCreation").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "server"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}
			return ctx
		}).
		Assess("join_token_is_set", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			token, err := client.WaitForEdgeJoinToken(ctx, edgeName, 2*time.Minute)
			if err != nil {
				t.Fatalf("join token not populated: %v", err)
			}

			t.Logf("join token set for edge %q: %q (len=%d)", edgeName, token, len(token))

			// base64.URLEncoding of 32 bytes produces exactly 44 characters.
			if len(token) != 44 {
				t.Fatalf("expected join token length 44 (base64url of 32 bytes), got %d: %q", len(token), token)
			}

			// Verify only base64url characters are used (A-Z, a-z, 0-9, -, _, =).
			for i, c := range token {
				if !isBase64URLChar(c) {
					t.Fatalf("join token contains unexpected character %q at position %d; full token: %q", string(c), i, token)
				}
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// AgentConnectsWithJoinToken verifies that a kedge agent can bootstrap its
// connection to the hub using only the Edge join token (no hub kubeconfig /
// service-account token required). After a successful token exchange the edge
// must reach the Ready phase.
func AgentConnectsWithJoinToken() features.Feature {
	const edgeName = "e2e-join-token-connect"

	return features.New("JoinToken/AgentConnectsWithJoinToken").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "server"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}

			// Wait for the hub controller to generate the join token.
			token, err := client.WaitForEdgeJoinToken(ctx, edgeName, 2*time.Minute)
			if err != nil {
				t.Fatalf("join token not generated: %v", err)
			}
			t.Logf("join token obtained for %q: len=%d", edgeName, len(token))

			// Extract the kcp logical cluster name from the hub kubeconfig so
			// the agent can build the correct WebSocket path.  The join token
			// alone does not carry cluster information.
			clusterName := framework.ClusterNameFromKubeconfig(clusterEnv.HubKubeconfig)
			t.Logf("cluster name extracted from hub kubeconfig: %q", clusterName)

			// Start a server-type agent that authenticates with the join token.
			agent := framework.NewAgentWithToken(framework.RepoRoot(), clusterEnv.HubURL, edgeName, token).
				WithCluster(clusterName)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start token agent: %v", err)
			}

			return context.WithValue(ctx, joinTokenEdgeReadyKey{}, agent)
		}).
		Assess("edge_becomes_ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready after connecting with join token: %v", edgeName, err)
			}
			t.Logf("edge %q reached Ready phase via join token auth", edgeName)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(joinTokenEdgeReadyKey{}).(*framework.TokenAgent); ok {
				a.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// InvalidJoinTokenReturns401 verifies that the hub rejects agent connections
// that present an incorrect join token, and that the edge never reaches the
// Ready phase as a result.
func InvalidJoinTokenReturns401() features.Feature {
	const edgeName = "e2e-join-token-invalid"
	// Use a syntactically plausible but cryptographically wrong token
	// (44 chars so it passes any length check, but the value is wrong).
	const badToken = "aW52YWxpZC10b2tlbi12YWx1ZS1mb3ItdGVzdGluZz0="

	return features.New("JoinToken/InvalidJoinTokenReturns401").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "server"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}

			// Start the agent with a deliberately wrong token before the real
			// join token is generated, ensuring it cannot match.
			agent := framework.NewAgentWithToken(framework.RepoRoot(), clusterEnv.HubURL, edgeName, badToken)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start token agent with invalid token: %v", err)
			}

			return context.WithValue(ctx, joinTokenInvalidKey{}, agent)
		}).
		Assess("edge_never_becomes_ready_with_bad_token", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Poll for a short window (30 s) and assert the edge does NOT reach Ready.
			// A correctly rejected agent will never push the edge to Ready.
			pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			err := framework.Poll(pollCtx, 3*time.Second, 30*time.Second, func(ctx context.Context) (bool, error) {
				out, err := client.Kubectl(ctx,
					"get", "edge", edgeName,
					"-o", "jsonpath={.status.phase}",
					"--insecure-skip-tls-verify",
				)
				if err != nil {
					return false, nil // transient — keep polling
				}
				if out == "Ready" {
					// Edge became Ready — bad token was incorrectly accepted.
					return false, fmt.Errorf("edge %q unexpectedly reached Ready with an invalid join token", edgeName)
				}
				return false, nil // not Ready — keep the loop alive until timeout
			})

			// A context-deadline error means 30 s elapsed and the edge never
			// became Ready — that is exactly the desired outcome.
			if err == nil || pollCtx.Err() != nil {
				t.Logf("edge %q correctly rejected agent with invalid join token (never became Ready)", edgeName)
				return ctx
			}
			// Any other error (e.g. our sentinel) is a real failure.
			t.Fatalf("unexpected result while checking invalid-token rejection: %v", err)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(joinTokenInvalidKey{}).(*framework.TokenAgent); ok {
				a.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// joinTokenSSHCredsKey is the context key for JoinTokenSSHCredentialsStoredAfterConnect.
type joinTokenSSHCredsKey struct{}

// JoinTokenSSHCredentialsStoredAfterConnect verifies that when a server-type
// agent connects with a join token AND provides SSH credentials via command
// flags, the hub stores those credentials in edge.status.sshCredentials.
//
// In join-token mode the agent cannot call the kcp API directly (the token is
// not a valid kcp credential), so SSH credentials are sent as X-Kedge-SSH-*
// WebSocket headers during the initial tunnel establishment. The hub's
// agent-proxy builder reads those headers and persists the credentials as a
// k8s Secret, then links the Secret in edge.status.sshCredentials.
func JoinTokenSSHCredentialsStoredAfterConnect() features.Feature {
	const (
		edgeName    = "e2e-join-token-ssh-creds"
		testSSHUser = "e2e-testuser"
		testSSHPass = "e2e-testpass"
	)

	return features.New("JoinToken/SSHCredentialsStoredAfterConnect").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "server"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}

			token, err := client.WaitForEdgeJoinToken(ctx, edgeName, 2*time.Minute)
			if err != nil {
				t.Fatalf("join token not generated: %v", err)
			}

			clusterName := framework.ClusterNameFromKubeconfig(clusterEnv.HubKubeconfig)
			agent := framework.NewAgentWithToken(framework.RepoRoot(), clusterEnv.HubURL, edgeName, token).
				WithCluster(clusterName).
				WithSSHUser(testSSHUser).
				WithSSHPassword(testSSHPass)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start token agent: %v", err)
			}

			return context.WithValue(ctx, joinTokenSSHCredsKey{}, agent)
		}).
		Assess("edge_becomes_ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready: %v", edgeName, err)
			}
			return ctx
		}).
		Assess("ssh_credentials_stored_in_status", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Hub must store the credentials passed via X-Kedge-SSH-* headers.
			creds, err := client.WaitForEdgeSSHCredentials(ctx, edgeName, 2*time.Minute)
			if err != nil {
				t.Fatalf("SSH credentials not stored for edge %q: %v", edgeName, err)
			}
			if creds.Username != testSSHUser {
				t.Fatalf("expected SSH username %q, got %q", testSSHUser, creds.Username)
			}
			if creds.PasswordSecretRef == "" {
				t.Fatalf("expected passwordSecretRef to be set for edge %q, got empty", edgeName)
			}
			t.Logf("edge %q SSH credentials stored: username=%q passwordSecretRef=%q",
				edgeName, creds.Username, creds.PasswordSecretRef)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(joinTokenSSHCredsKey{}).(*framework.TokenAgent); ok {
				a.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// joinTokenK8sModeKey is the context key for JoinTokenKubernetesMode.
type joinTokenK8sModeKey struct{}

// JoinTokenKubernetesMode verifies that a kubernetes-type edge can bootstrap
// its connection to the hub using only a join token (no pre-provisioned hub
// kubeconfig / ServiceAccount credential). After the token exchange the edge
// must reach the Ready phase and the k8s proxy must be reachable.
func JoinTokenKubernetesMode() features.Feature {
	const edgeName = "e2e-join-token-k8s"

	return features.New("JoinToken/KubernetesMode").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv.AgentKubeconfig == "" {
				t.Skip("no agent kubeconfig available — skipping kubernetes-mode join-token test")
			}

			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "kubernetes"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}

			token, err := client.WaitForEdgeJoinToken(ctx, edgeName, 2*time.Minute)
			if err != nil {
				t.Fatalf("join token not generated for kubernetes edge %q: %v", edgeName, err)
			}
			t.Logf("join token obtained for kubernetes edge %q (len=%d)", edgeName, len(token))

			clusterName := framework.ClusterNameFromKubeconfig(clusterEnv.HubKubeconfig)
			agent := framework.NewAgentWithToken(framework.RepoRoot(), clusterEnv.HubURL, edgeName, token).
				WithType("kubernetes").
				WithAgentKubeconfig(clusterEnv.AgentKubeconfig).
				WithCluster(clusterName)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start kubernetes-mode token agent: %v", err)
			}

			return context.WithValue(ctx, joinTokenK8sModeKey{}, agent)
		}).
		Assess("edge_becomes_ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("kubernetes edge %q did not become Ready with join token: %v", edgeName, err)
			}
			t.Logf("kubernetes edge %q reached Ready via join-token auth", edgeName)
			return ctx
		}).
		Assess("k8s_proxy_reachable", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			edgeURL, err := client.GetEdgeURL(ctx, edgeName)
			if err != nil {
				t.Fatalf("getting edge proxy URL: %v", err)
			}

			out, err := client.KubectlWithURL(ctx, edgeURL, "get", "namespaces")
			if err != nil {
				t.Fatalf("k8s proxy kubectl failed for edge %q: %v\noutput: %s", edgeName, err, out)
			}
			if !strings.Contains(out, "default") {
				t.Fatalf("expected 'default' namespace in proxy output, got:\n%s", out)
			}
			t.Logf("k8s proxy reachable for kubernetes join-token edge %q", edgeName)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(joinTokenK8sModeKey{}).(*framework.TokenAgent); ok {
				a.Stop()
			}
			if path, err := framework.AgentSavedKubeconfigPath(edgeName); err == nil {
				_ = os.Remove(path)
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// isBase64URLChar returns true when r is a valid base64url alphabet character
// (RFC 4648 §5: A-Z, a-z, 0-9, -, _ and the padding character =).
func isBase64URLChar(r rune) bool {
	return (r >= 'A' && r <= 'Z') ||
		(r >= 'a' && r <= 'z') ||
		(r >= '0' && r <= '9') ||
		r == '-' || r == '_' || r == '='
}

// joinTokenClearedKey is the context key for the TokenAgent in JoinTokenClearedAfterRegistration.
type joinTokenClearedKey struct{}

// JoinTokenClearedAfterRegistration verifies that after a join-token agent
// successfully connects to the hub:
//  1. status.joinToken is cleared (so the one-time token can't be reused)
//  2. The Registered condition is set to True
func JoinTokenClearedAfterRegistration() features.Feature {
	const edgeName = "e2e-join-token-cleared"

	return features.New("JoinToken/ClearedAfterRegistration").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "server"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}

			token, err := client.WaitForEdgeJoinToken(ctx, edgeName, 2*time.Minute)
			if err != nil {
				t.Fatalf("join token not generated: %v", err)
			}

			clusterName := framework.ClusterNameFromKubeconfig(clusterEnv.HubKubeconfig)
			agent := framework.NewAgentWithToken(framework.RepoRoot(), clusterEnv.HubURL, edgeName, token).
				WithCluster(clusterName)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start token agent: %v", err)
			}

			return context.WithValue(ctx, joinTokenClearedKey{}, agent)
		}).
		Assess("edge_becomes_ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready: %v", edgeName, err)
			}
			return ctx
		}).
		Assess("join_token_is_cleared", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Hub should clear status.joinToken after the tunnel comes up to
			// prevent token reuse (one-shot bootstrap credential).
			if err := client.WaitForEdgeJoinTokenCleared(ctx, edgeName, 2*time.Minute); err != nil {
				t.Fatalf("join token for edge %q was not cleared after registration: %v", edgeName, err)
			}
			t.Logf("join token cleared for edge %q after successful registration", edgeName)
			return ctx
		}).
		Assess("registered_condition_is_true", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// The hub sets Registered=True once the tunnel is established with a
			// valid join token. This prevents the TokenReconciler from issuing a
			// new token on subsequent reconcile loops.
			if err := client.WaitForEdgeCondition(ctx, edgeName, "Registered", "True", 2*time.Minute); err != nil {
				status, _ := client.GetEdgeCondition(ctx, edgeName, "Registered")
				t.Fatalf("edge %q Registered condition not True (got %q): %v", edgeName, status, err)
			}
			t.Logf("edge %q Registered condition is True", edgeName)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(joinTokenClearedKey{}).(*framework.TokenAgent); ok {
				a.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// reconnectAgentKey is the context key pair for JoinTokenReconnectWithSavedKubeconfig.
type reconnectFirstAgentKey struct{}
type reconnectSecondAgentKey struct{}

// JoinTokenReconnectWithSavedKubeconfig verifies the full reconnect-after-restart
// flow:
//  1. Agent authenticates with a bootstrap join token.
//  2. Hub exchanges the token for a kubeconfig and returns it in
//     X-Kedge-Agent-Kubeconfig; the agent saves it to disk.
//  3. Agent process is stopped.
//  4. A fresh agent process for the same edge starts WITHOUT a token.
//  5. It auto-detects the saved kubeconfig and reconnects; edge reaches Ready.
func JoinTokenReconnectWithSavedKubeconfig() features.Feature {
	const edgeName = "e2e-join-token-reconnect"

	return features.New("JoinToken/ReconnectWithSavedKubeconfig").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "server"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}

			token, err := client.WaitForEdgeJoinToken(ctx, edgeName, 2*time.Minute)
			if err != nil {
				t.Fatalf("join token not generated: %v", err)
			}

			clusterName := framework.ClusterNameFromKubeconfig(clusterEnv.HubKubeconfig)
			firstAgent := framework.NewAgentWithToken(framework.RepoRoot(), clusterEnv.HubURL, edgeName, token).
				WithCluster(clusterName)
			if err := firstAgent.Start(ctx); err != nil {
				t.Fatalf("failed to start first token agent: %v", err)
			}

			return context.WithValue(ctx, reconnectFirstAgentKey{}, firstAgent)
		}).
		Assess("first_connection_edge_ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready on first connection: %v", edgeName, err)
			}
			t.Logf("edge %q Ready on first (join-token) connection", edgeName)
			return ctx
		}).
		Assess("kubeconfig_saved_to_disk", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// After the tunnel is established the agent saves the kubeconfig
			// returned by the hub.  Poll until the file appears.
			kubeconfigPath, err := framework.WaitForAgentSavedKubeconfig(ctx, edgeName, 2*time.Minute)
			if err != nil {
				t.Fatalf("agent did not save kubeconfig for edge %q: %v", edgeName, err)
			}
			t.Logf("saved kubeconfig found at %s", kubeconfigPath)
			return ctx
		}).
		Assess("reconnects_without_token", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Stop the first agent to simulate a restart.
			if a, ok := ctx.Value(reconnectFirstAgentKey{}).(*framework.TokenAgent); ok {
				a.Stop()
				t.Log("first agent stopped")
			}

			// Give the hub a moment to detect the disconnect (marking edge not-connected).
			time.Sleep(5 * time.Second)

			// Start a new agent WITHOUT a token — it must auto-detect the saved kubeconfig.
			secondAgent := framework.NewReconnectAgent(framework.RepoRoot(), clusterEnv.HubURL, edgeName)
			if err := secondAgent.Start(ctx); err != nil {
				t.Fatalf("failed to start reconnect agent: %v", err)
			}
			ctx = context.WithValue(ctx, reconnectSecondAgentKey{}, secondAgent)

			// Edge should reach Ready again via the saved kubeconfig (no token needed).
			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready after reconnect (no token): %v", edgeName, err)
			}
			t.Logf("edge %q Ready after reconnect via saved kubeconfig (no token)", edgeName)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(reconnectFirstAgentKey{}).(*framework.TokenAgent); ok {
				a.Stop()
			}
			if a, ok := ctx.Value(reconnectSecondAgentKey{}).(*framework.TokenAgent); ok {
				a.Stop()
			}
			// Clean up the saved kubeconfig so the test is idempotent.
			if path, err := framework.AgentSavedKubeconfigPath(edgeName); err == nil {
				_ = os.Remove(path)
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// TokenReconcilerNoReissueAfterRegistration verifies that the TokenReconciler
// does NOT generate a new join token once an edge is marked Registered=True.
// This ensures the one-shot bootstrap token cannot be recycled after the first
// successful agent registration.
func TokenReconcilerNoReissueAfterRegistration() features.Feature {
	const edgeName = "e2e-join-token-no-reissue"

	type agentKey struct{}

	return features.New("JoinToken/NoReissueAfterRegistration").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "server"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}

			token, err := client.WaitForEdgeJoinToken(ctx, edgeName, 2*time.Minute)
			if err != nil {
				t.Fatalf("join token not generated: %v", err)
			}

			clusterName := framework.ClusterNameFromKubeconfig(clusterEnv.HubKubeconfig)
			agent := framework.NewAgentWithToken(framework.RepoRoot(), clusterEnv.HubURL, edgeName, token).
				WithCluster(clusterName)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start token agent: %v", err)
			}
			return context.WithValue(ctx, agentKey{}, agent)
		}).
		Assess("edge_becomes_ready_and_token_cleared", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready: %v", edgeName, err)
			}
			if err := client.WaitForEdgeJoinTokenCleared(ctx, edgeName, 2*time.Minute); err != nil {
				t.Fatalf("join token not cleared for edge %q: %v", edgeName, err)
			}
			if err := client.WaitForEdgeCondition(ctx, edgeName, "Registered", "True", 1*time.Minute); err != nil {
				t.Fatalf("Registered condition not True for edge %q: %v", edgeName, err)
			}
			t.Logf("edge %q is Ready, join token cleared, Registered=True", edgeName)
			return ctx
		}).
		Assess("no_new_token_issued_after_registration", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Wait 30s and confirm no join token has been re-issued.
			// The TokenReconciler must skip edges that have Registered=True.
			time.Sleep(30 * time.Second)

			token, err := client.GetEdgeJoinToken(ctx, edgeName)
			if err != nil {
				t.Fatalf("checking join token for edge %q: %v", edgeName, err)
			}
			if token != "" {
				t.Fatalf("TokenReconciler re-issued join token for edge %q after registration (got %q)", edgeName, token)
			}
			t.Logf("confirmed: no new join token issued for edge %q after Registered=True", edgeName)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(agentKey{}).(*framework.TokenAgent); ok {
				a.Stop()
			}
			if path, err := framework.AgentSavedKubeconfigPath(edgeName); err == nil {
				_ = os.Remove(path)
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}
