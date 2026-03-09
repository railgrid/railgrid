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

// isBase64URLChar returns true when r is a valid base64url alphabet character
// (RFC 4648 §5: A-Z, a-z, 0-9, -, _ and the padding character =).
func isBase64URLChar(r rune) bool {
	return (r >= 'A' && r <= 'Z') ||
		(r >= 'a' && r <= 'z') ||
		(r >= '0' && r <= '9') ||
		r == '-' || r == '_' || r == '='
}
