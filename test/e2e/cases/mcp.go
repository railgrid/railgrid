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
	"crypto/tls"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// mcpAgentKey is the context key for the Agent started in MCP tests.
type mcpAgentKey struct{}

// mcpClientKey is the context key for passing the mcpClient between Assess steps.
type mcpClientKey struct{}

// mcpClient wraps a connected go-sdk MCP client session.
// Using the proper go-sdk StreamableClientTransport avoids all raw HTTP/SSE
// parsing issues and correctly satisfies the MCP streamable-HTTP spec.
type mcpClient struct {
	baseURL string // for logging only
	session *gosdk.ClientSession
	client  *gosdk.Client
}

// newMCPClient creates an mcpClient using the NodePort URL of the hub and the
// kcp cluster name derived from the hub kubeconfig.
func newMCPClient(hubKubeconfig, edgeName string) (*mcpClient, error) {
	return newMCPClientWithKubernetes(hubKubeconfig, edgeName, "")
}

// newMCPClientKubernetes creates an mcpClient that targets the Kubernetes
// multi-edge MCP endpoint.
func newMCPClientKubernetes(hubKubeconfig, kubernetesName string) (*mcpClient, error) {
	return newMCPClientWithKubernetes(hubKubeconfig, "", kubernetesName)
}

func newMCPClientWithKubernetes(hubKubeconfig, edgeName, kubernetesName string) (*mcpClient, error) {
	nodePortBase := framework.HubNodePortURL()
	if nodePortBase == "" {
		return nil, fmt.Errorf("could not determine hub NodePort URL (docker inspect failed)")
	}

	clusterName, err := clusterNameFromKubeconfig(hubKubeconfig)
	if err != nil {
		return nil, fmt.Errorf("resolving cluster name from kubeconfig: %w", err)
	}

	restCfg, err := clientcmd.BuildConfigFromFlags("", hubKubeconfig)
	if err != nil {
		return nil, fmt.Errorf("building rest config from kubeconfig: %w", err)
	}
	token := restCfg.BearerToken

	var mcpURL string
	if kubernetesName != "" {
		mcpURL = fmt.Sprintf("%s/services/mcp/%s/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/%s/mcp",
			nodePortBase, clusterName, kubernetesName)
	} else {
		mcpURL = fmt.Sprintf("%s/services/agent-proxy/%s/apis/kedge.faros.sh/v1alpha1/edges/%s/mcp",
			nodePortBase, clusterName, edgeName)
	}

	httpClient := &http.Client{
		Transport: &authRoundTripper{
			token: token,
			base: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // e2e dev certs
			},
		},
		Timeout: 30 * time.Second,
	}

	sdkClient := gosdk.NewClient(&gosdk.Implementation{Name: "e2e-test", Version: "1.0"}, nil)
	transport := &gosdk.StreamableClientTransport{
		Endpoint:   mcpURL,
		HTTPClient: httpClient,
	}

	session, err := sdkClient.Connect(context.Background(), transport, nil)
	if err != nil {
		return nil, fmt.Errorf("MCP connect to %s: %w", mcpURL, err)
	}

	return &mcpClient{
		baseURL: mcpURL,
		session: session,
		client:  sdkClient,
	}, nil
}

// authRoundTripper injects a Bearer token into every request.
type authRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (a *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+a.token)
	return a.base.RoundTrip(req)
}

// initialize is a no-op — the go-sdk Connect() already performs initialize.
func (c *mcpClient) initialize(_ context.Context) error {
	result := c.session.InitializeResult()
	if result == nil || result.ServerInfo.Name == "" {
		return fmt.Errorf("initialize: serverInfo missing in InitializeResult")
	}
	return nil
}

// toolsList calls tools/list and returns the list of tool names.
func (c *mcpClient) toolsList(ctx context.Context) ([]string, error) {
	result, err := c.session.ListTools(ctx, &gosdk.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, t := range result.Tools {
		names = append(names, t.Name)
	}
	return names, nil
}

// toolsCall calls tools/call and returns the text content from the result.
func (c *mcpClient) toolsCall(ctx context.Context, toolName string, args map[string]any) (string, error) {
	result, err := c.session.CallTool(ctx, &gosdk.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("tools/call %q: %w", toolName, err)
	}
	var sb strings.Builder
	for _, content := range result.Content {
		if tc, ok := content.(*gosdk.TextContent); ok && tc.Text != "" {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), nil
}

// MCPEndpoint verifies the per-tenant MCP endpoint with a full protocol flow:
// initialize → tools/list → tools/call namespaces_list → tools/call pods_list_in_namespace.
func MCPEndpoint() features.Feature {
	const edgeName = "e2e-mcp-edge"

	return features.New("MCP/Endpoint").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "kubernetes", "env=e2e"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}

			edgeKubeconfigPath := filepath.Join(clusterEnv.WorkDir, "edge-"+edgeName+".kubeconfig")
			if err := client.ExtractEdgeKubeconfig(ctx, edgeName, edgeKubeconfigPath); err != nil {
				t.Fatalf("failed to extract edge kubeconfig: %v", err)
			}

			agent := framework.NewAgent(framework.RepoRoot(), edgeKubeconfigPath, clusterEnv.AgentKubeconfig, edgeName)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start agent: %v", err)
			}
			return context.WithValue(ctx, mcpAgentKey{}, agent)
		}).
		Assess("edge becomes Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready: %v", edgeName, err)
			}
			return ctx
		}).
		Assess("MCP initialize returns 200 with serverInfo", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			mcp, err := newMCPClient(clusterEnv.HubKubeconfig, edgeName)
			if err != nil {
				t.Fatalf("creating MCP client: %v", err)
			}
			t.Logf("MCP URL: %s", mcp.baseURL)

			// initialize() verifies serverInfo on the already-connected session.
			if err := mcp.initialize(ctx); err != nil {
				t.Fatalf("MCP initialize failed: %v", err)
			}

			// Store the connected client for subsequent Assess steps.
			return context.WithValue(ctx, mcpClientKey{}, mcp)
		}).
		Assess("MCP tools/list returns namespaces_list and pods_list_in_namespace", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			mcp, ok := ctx.Value(mcpClientKey{}).(*mcpClient)
			if !ok || mcp == nil {
				t.Fatal("mcpClient not found in context — initialize step may have failed")
			}

			names, err := mcp.toolsList(ctx)
			if err != nil {
				t.Fatalf("MCP tools/list failed: %v", err)
			}
			t.Logf("MCP tools: %v", names)

			nameSet := make(map[string]bool, len(names))
			for _, n := range names {
				nameSet[n] = true
			}
			for _, required := range []string{"namespaces_list", "pods_list_in_namespace"} {
				if !nameSet[required] {
					t.Errorf("expected tool %q in tools/list, got: %v", required, names)
				}
			}
			return ctx
		}).
		Assess("MCP tools/call namespaces_list contains kube-system", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			mcp, ok := ctx.Value(mcpClientKey{}).(*mcpClient)
			if !ok || mcp == nil {
				t.Fatal("mcpClient not found in context — initialize step may have failed")
			}

			result, err := mcp.toolsCall(ctx, "namespaces_list", map[string]any{
				"cluster": edgeName,
			})
			if err != nil {
				t.Fatalf("tools/call namespaces_list failed: %v", err)
			}
			t.Logf("namespaces_list result: %s", result)

			if !strings.Contains(result, "kube-system") {
				t.Errorf("expected namespaces_list to contain 'kube-system', got: %s", result)
			}
			return ctx
		}).
		Assess("MCP tools/call pods_list_in_namespace kube-system returns pods", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			mcp, ok := ctx.Value(mcpClientKey{}).(*mcpClient)
			if !ok || mcp == nil {
				t.Fatal("mcpClient not found in context — initialize step may have failed")
			}

			result, err := mcp.toolsCall(ctx, "pods_list_in_namespace", map[string]any{
				"namespace": "kube-system",
				"cluster":   edgeName,
			})
			if err != nil {
				t.Fatalf("tools/call pods_list_in_namespace failed: %v", err)
			}
			t.Logf("pods_list_in_namespace result: %s", result)

			if strings.TrimSpace(result) == "" {
				t.Error("expected pods_list_in_namespace to return non-empty content for kube-system")
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(mcpAgentKey{}).(*framework.Agent); ok {
				a.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// MCPURL verifies that `kedge mcp url` prints valid MCP endpoint URLs.
func MCPURL() features.Feature {
	const edgeName = "e2e-mcp-edge"

	return features.New("MCP/URL").
		Assess("kedge mcp url --edge prints expected URL", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}

			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			out, err := client.Run(ctx, "mcp", "url", "--edge", edgeName)
			if err != nil {
				t.Fatalf("kedge mcp url --edge failed: %v (output: %s)", err, out)
			}
			out = strings.TrimSpace(out)
			t.Logf("kedge mcp url --edge output: %s", out)

			// The output must be a valid per-edge MCP URL.
			if !strings.HasPrefix(out, "https://") {
				t.Errorf("expected URL to start with https://, got: %s", out)
			}
			if !strings.Contains(out, "/services/agent-proxy/") {
				t.Errorf("expected URL to contain /services/agent-proxy/, got: %s", out)
			}
			if !strings.Contains(out, "/edges/"+edgeName+"/mcp") {
				t.Errorf("expected URL to contain /edges/%s/mcp, got: %s", edgeName, out)
			}
			return ctx
		}).
		Assess("kedge mcp url --name default prints KubernetesMCP URL", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}

			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			out, err := client.Run(ctx, "mcp", "url", "--name", "default")
			if err != nil {
				t.Fatalf("kedge mcp url --name failed: %v (output: %s)", err, out)
			}
			out = strings.TrimSpace(out)
			t.Logf("kedge mcp url --name output: %s", out)

			if !strings.HasPrefix(out, "https://") {
				t.Errorf("expected URL to start with https://, got: %s", out)
			}
			if !strings.Contains(out, "/services/mcp/") {
				t.Errorf("expected URL to contain /services/mcp/, got: %s", out)
			}
			if !strings.Contains(out, "/kubernetes/default/mcp") {
				t.Errorf("expected URL to contain /kubernetes/default/mcp, got: %s", out)
			}
			return ctx
		}).
		Feature()
}

// MCPKubernetes verifies the Kubernetes multi-edge MCP endpoint.
// It uses the auto-created "default" Kubernetes MCP object (all edges).
func MCPKubernetes() features.Feature {
	const edgeName = "e2e-kmcp-edge"

	return features.New("MCP/KubernetesMCP").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "kubernetes", "env=e2e"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}

			edgeKubeconfigPath := filepath.Join(clusterEnv.WorkDir, "edge-"+edgeName+"-kmcp.kubeconfig")
			if err := client.ExtractEdgeKubeconfig(ctx, edgeName, edgeKubeconfigPath); err != nil {
				t.Fatalf("failed to extract edge kubeconfig: %v", err)
			}

			agent := framework.NewAgent(framework.RepoRoot(), edgeKubeconfigPath, clusterEnv.AgentKubeconfig, edgeName)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start agent: %v", err)
			}
			return context.WithValue(ctx, mcpAgentKey{}, agent)
		}).
		Assess("edge becomes Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready: %v", edgeName, err)
			}
			return ctx
		}).
		Assess("Kubernetes default MCP initialize succeeds", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)

			mcp, err := newMCPClientKubernetes(clusterEnv.HubKubeconfig, "default")
			if err != nil {
				t.Fatalf("creating Kubernetes MCP client: %v", err)
			}
			t.Logf("Kubernetes MCP URL: %s", mcp.baseURL)

			if err := mcp.initialize(ctx); err != nil {
				t.Fatalf("Kubernetes MCP initialize failed: %v", err)
			}

			return context.WithValue(ctx, mcpClientKey{}, mcp)
		}).
		Assess("Kubernetes tools/list returns namespaces_list", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			mcp, ok := ctx.Value(mcpClientKey{}).(*mcpClient)
			if !ok || mcp == nil {
				t.Fatal("mcpClient not found in context — initialize step may have failed")
			}

			names, err := mcp.toolsList(ctx)
			if err != nil {
				t.Fatalf("Kubernetes tools/list failed: %v", err)
			}
			t.Logf("Kubernetes tools: %v", names)

			nameSet := make(map[string]bool, len(names))
			for _, n := range names {
				nameSet[n] = true
			}
			if !nameSet["namespaces_list"] {
				t.Errorf("expected tool 'namespaces_list' in tools/list, got: %v", names)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(mcpAgentKey{}).(*framework.Agent); ok {
				a.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// clusterNameFromKubeconfig extracts the kcp cluster name from the server URL
// in the current context of the given kubeconfig file.
// Returns the path segment after /clusters/ in the server URL.
func clusterNameFromKubeconfig(kubeconfigPath string) (string, error) {
	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
	clientCfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{},
	)

	rawCfg, err := clientCfg.RawConfig()
	if err != nil {
		return "", fmt.Errorf("loading kubeconfig %q: %w", kubeconfigPath, err)
	}

	currentCtx := rawCfg.CurrentContext
	if currentCtx == "" {
		return "", fmt.Errorf("no current context in kubeconfig %q", kubeconfigPath)
	}

	ctxObj, ok := rawCfg.Contexts[currentCtx]
	if !ok {
		return "", fmt.Errorf("context %q not found in kubeconfig", currentCtx)
	}

	clusterObj, ok := rawCfg.Clusters[ctxObj.Cluster]
	if !ok {
		return "", fmt.Errorf("cluster %q not found in kubeconfig", ctxObj.Cluster)
	}

	serverURL := clusterObj.Server
	idx := strings.Index(serverURL, "/clusters/")
	if idx < 0 {
		return "", fmt.Errorf("server URL %q does not contain /clusters/ segment", serverURL)
	}
	clusterName := strings.TrimSuffix(strings.TrimPrefix(serverURL[idx:], "/clusters/"), "/")
	if clusterName == "" {
		return "", fmt.Errorf("empty cluster name in server URL %q", serverURL)
	}
	return clusterName, nil
}
