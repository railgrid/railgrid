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

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// edgeURLAgentKey is a context key for the Agent in EdgeURLSet / K8sProxyAccess.
type edgeURLAgentKey struct{}

// EdgeURLSet verifies that status.URL is populated after a kubernetes-type edge
// connects and that the URL ends in "/k8s".
func EdgeURLSet() features.Feature {
	const edgeName = "e2e-url-edge"

	return features.New("edge URL set").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
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
			return context.WithValue(ctx, edgeURLAgentKey{}, agent)
		}).
		Assess("edge becomes Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready: %v", edgeName, err)
			}
			return ctx
		}).
		Assess("status.URL is populated and ends with /k8s", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			edgeURL, err := client.GetEdgeURL(ctx, edgeName)
			if err != nil {
				t.Fatalf("getting edge URL: %v", err)
			}
			if !strings.HasSuffix(edgeURL, "/k8s") {
				t.Fatalf("expected edge URL to end with '/k8s', got: %s", edgeURL)
			}
			t.Logf("edge URL: %s", edgeURL)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(edgeURLAgentKey{}).(*framework.Agent); ok {
				a.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// k8sProxyAgentKey is a context key for the Agent in K8sProxyAccess.
type k8sProxyAgentKey struct{}

// K8sProxyAccess verifies that kubectl against status.URL returns the edge
// cluster's resources (nodes and namespaces).  This is an end-to-end test of
// the k8s proxy path through the hub.
//
// Only use this in suites that set up both a hub and at least one agent cluster.
func K8sProxyAccess() features.Feature {
	const edgeName = "e2e-proxy-edge"

	return features.New("k8s proxy access").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
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
			return context.WithValue(ctx, k8sProxyAgentKey{}, agent)
		}).
		Assess("edge becomes Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready: %v", edgeName, err)
			}
			return ctx
		}).
		Assess("kubectl get nodes via status.URL returns edge cluster nodes", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			edgeURL, err := client.GetEdgeURL(ctx, edgeName)
			if err != nil {
				t.Fatalf("getting edge URL: %v", err)
			}
			if !strings.HasSuffix(edgeURL, "/k8s") {
				t.Fatalf("expected edge URL to end with '/k8s', got: %s", edgeURL)
			}

			out, err := client.KubectlWithURL(ctx, edgeURL, "get", "nodes")
			if err != nil {
				t.Fatalf("kubectl get nodes via edge proxy failed: %v", err)
			}
			if out == "" {
				t.Fatalf("expected non-empty node list via edge proxy, got empty output")
			}
			t.Logf("kubectl get nodes via edge proxy:\n%s", out)
			return ctx
		}).
		Assess("kubectl get namespaces via status.URL returns default namespace", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			edgeURL, err := client.GetEdgeURL(ctx, edgeName)
			if err != nil {
				t.Fatalf("getting edge URL: %v", err)
			}

			out, err := client.KubectlWithURL(ctx, edgeURL, "get", "namespaces")
			if err != nil {
				t.Fatalf("kubectl get namespaces via edge proxy failed: %v", err)
			}
			if !strings.Contains(out, "default") {
				t.Fatalf("expected 'default' namespace in proxy output, got:\n%s", out)
			}
			t.Logf("kubectl get namespaces via edge proxy:\n%s", out)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(k8sProxyAgentKey{}).(*framework.Agent); ok {
				a.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// k8sProxyWriteAgentKey is a context key for the Agent in K8sProxyWrite.
type k8sProxyWriteAgentKey struct{}

// K8sProxyWrite verifies that a resource can be written to the edge cluster
// via the k8s proxy endpoint (kubectl apply through status.URL).
//
// Flow:
//  1. Create edge + start agent → wait for Ready.
//  2. Get status.URL.
//  3. kubectl apply a ConfigMap via KubectlWithURL.
//  4. kubectl get configmap to confirm it exists.
//  5. Cleanup: delete the ConfigMap, stop agent, delete edge.
func K8sProxyWrite() features.Feature {
	const (
		edgeName  = "e2e-proxy-write-edge"
		cmName    = "e2e-proxy-write-cm"
		ns        = "default"
		markerVal = "e2e_proxy_write_ok"
	)

	return features.New("k8s proxy write").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
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
			return context.WithValue(ctx, k8sProxyWriteAgentKey{}, agent)
		}).
		Assess("edge becomes Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready: %v", edgeName, err)
			}
			return ctx
		}).
		Assess("kubectl apply ConfigMap via status.URL creates the resource", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			edgeURL, err := client.GetEdgeURL(ctx, edgeName)
			if err != nil {
				t.Fatalf("getting edge URL: %v", err)
			}
			if !strings.HasSuffix(edgeURL, "/k8s") {
				t.Fatalf("expected edge URL to end with '/k8s', got: %s", edgeURL)
			}
			t.Logf("edge URL: %s", edgeURL)

			// Write a ConfigMap manifest to a temp file.
			manifest := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
data:
  key: %s
`, cmName, ns, markerVal)

			f, err := os.CreateTemp("", "k8s-proxy-write-*.yaml")
			if err != nil {
				t.Fatalf("creating temp manifest file: %v", err)
			}
			defer os.Remove(f.Name()) //nolint:errcheck
			if _, err := f.WriteString(manifest); err != nil {
				t.Fatalf("writing manifest: %v", err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("closing manifest: %v", err)
			}

			// Apply the ConfigMap to the edge cluster via the proxy.
			out, err := client.KubectlWithURL(ctx, edgeURL, "apply", "-f", f.Name())
			if err != nil {
				t.Fatalf("kubectl apply via edge proxy failed: %v\noutput: %s", err, out)
			}
			t.Logf("kubectl apply output: %s", out)

			// Verify the ConfigMap exists on the edge cluster.
			out, err = client.KubectlWithURL(ctx, edgeURL,
				"get", "configmap", cmName,
				"-n", ns,
				"-o", "jsonpath={.data.key}",
			)
			if err != nil {
				t.Fatalf("kubectl get configmap via edge proxy failed: %v\noutput: %s", err, out)
			}
			if !strings.Contains(out, markerVal) {
				t.Fatalf("expected configmap data to contain %q, got: %s", markerVal, out)
			}
			t.Logf("ConfigMap %q confirmed on edge cluster (data.key=%s)", cmName, strings.TrimSpace(out))
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Best-effort cleanup: delete the ConfigMap on the edge cluster.
			if edgeURL, err := client.GetEdgeURL(ctx, edgeName); err == nil {
				_, _ = client.KubectlWithURL(ctx, edgeURL,
					"delete", "configmap", cmName,
					"-n", ns,
					"--ignore-not-found",
				)
			}

			if a, ok := ctx.Value(k8sProxyWriteAgentKey{}).(*framework.Agent); ok {
				a.Stop()
			}
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// k8sProxyExecAgentKey is a context key for the Agent in K8sProxyExec.
type k8sProxyExecAgentKey struct{}

// K8sProxyExec verifies that kubectl exec works through the k8s proxy endpoint,
// testing the SPDY upgrade/hijack path in the hub reverse proxy.
//
// Flow:
//  1. Create edge + start agent → wait for Ready.
//  2. Get status.URL.
//  3. kubectl apply a busybox Pod via KubectlWithURL.
//  4. Wait for the Pod to be Running (max 5 min — busybox may need to pull).
//  5. kubectl exec <pod> -- echo kedge_exec_ok via KubectlWithURL.
//  6. Assert output contains the marker.
//  7. Cleanup: delete the Pod, stop agent, delete edge.
func K8sProxyExec() features.Feature {
	const (
		edgeName   = "e2e-proxy-exec-edge"
		podName    = "e2e-proxy-exec-pod"
		ns         = "default"
		execMarker = "kedge_exec_ok"
	)

	return features.New("k8s proxy exec").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
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
			return context.WithValue(ctx, k8sProxyExecAgentKey{}, agent)
		}).
		Assess("edge becomes Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready: %v", edgeName, err)
			}
			return ctx
		}).
		Assess("kubectl exec via status.URL succeeds (SPDY upgrade path)", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			edgeURL, err := client.GetEdgeURL(ctx, edgeName)
			if err != nil {
				t.Fatalf("getting edge URL: %v", err)
			}
			if !strings.HasSuffix(edgeURL, "/k8s") {
				t.Fatalf("expected edge URL to end with '/k8s', got: %s", edgeURL)
			}
			t.Logf("edge URL: %s", edgeURL)

			// Write a busybox Pod manifest to a temp file.
			manifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  containers:
  - name: main
    image: busybox:1.36
    command: ["sleep", "3600"]
    imagePullPolicy: IfNotPresent
  restartPolicy: Never
`, podName, ns)

			f, err := os.CreateTemp("", "k8s-proxy-exec-*.yaml")
			if err != nil {
				t.Fatalf("creating temp pod manifest: %v", err)
			}
			defer os.Remove(f.Name()) //nolint:errcheck
			if _, err := f.WriteString(manifest); err != nil {
				t.Fatalf("writing pod manifest: %v", err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("closing pod manifest: %v", err)
			}

			// Apply the Pod to the edge cluster via the proxy.
			out, err := client.KubectlWithURL(ctx, edgeURL, "apply", "-f", f.Name())
			if err != nil {
				t.Fatalf("kubectl apply pod via edge proxy failed: %v\noutput: %s", err, out)
			}
			t.Logf("Pod apply output: %s", out)

			// Wait for the Pod to be Running (busybox may need to pull the image).
			t.Logf("Waiting for pod %q to be Running (up to 5 min, image may need to pull)...", podName)
			if err := framework.Poll(ctx, 10*time.Second, 5*time.Minute, func(ctx context.Context) (bool, error) {
				phase, err := client.KubectlWithURL(ctx, edgeURL,
					"get", "pod", podName,
					"-n", ns,
					"-o", "jsonpath={.status.phase}",
				)
				if err != nil {
					return false, nil
				}
				phase = strings.TrimSpace(phase)
				if phase == "Running" {
					return true, nil
				}
				t.Logf("pod phase: %s", phase)
				return false, nil
			}); err != nil {
				if out2, err2 := client.KubectlWithURL(ctx, edgeURL, "describe", "pod", podName, "-n", ns); err2 == nil {
					t.Logf("[diag] pod describe:\n%s", out2)
				}
				t.Fatalf("pod %q did not reach Running state within 5 min: %v", podName, err)
			}
			t.Logf("Pod %q is Running", podName)

			// Execute a command via the proxy. This exercises the SPDY upgrade/hijack
			// path in edgesHandleK8sUpgrade (pkg/virtual/builder/edges_proxy_builder.go).
			out, err = client.KubectlWithURL(ctx, edgeURL,
				"exec", podName,
				"-n", ns,
				"--", "echo", execMarker,
			)
			if err != nil {
				t.Fatalf("kubectl exec via edge proxy failed: %v\noutput: %s", err, out)
			}
			if !strings.Contains(out, execMarker) {
				t.Fatalf("expected exec output to contain %q, got: %s", execMarker, out)
			}
			t.Logf("kubectl exec via edge proxy succeeded: %s", strings.TrimSpace(out))
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Best-effort cleanup: delete the pod on the edge cluster.
			if edgeURL, err := client.GetEdgeURL(ctx, edgeName); err == nil {
				_, _ = client.KubectlWithURL(ctx, edgeURL,
					"delete", "pod", podName,
					"-n", ns,
					"--ignore-not-found",
					"--grace-period=0",
				)
			}

			if a, ok := ctx.Value(k8sProxyExecAgentKey{}).(*framework.Agent); ok {
				a.Stop()
			}
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}
