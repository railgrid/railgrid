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

package edgesconn

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/faroshq/faros-kedge/pkg/util/identity"
)

var (
	kubernetesClusterGVR  = schema.GroupVersionResource{Group: "edges.kedge.faros.sh", Version: "v1alpha1", Resource: "kubernetesclusters"}
	linuxServerGVR        = schema.GroupVersionResource{Group: "edges.kedge.faros.sh", Version: "v1alpha1", Resource: "linuxservers"}
	workloadGVR           = schema.GroupVersionResource{Group: "edges.kedge.faros.sh", Version: "v1alpha1", Resource: "workloads"}
	apiBindingGVR         = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
	clusterRoleGVR        = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	clusterRoleBindingGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
	workspaceGVR          = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}
)

// TestKubectlThroughTunnel drives the full edges data-plane end to end: it
// enables the edges provider in a fresh tenant workspace, registers a
// KubernetesCluster, runs a real `kedge agent` against a kind cluster, and
// proves `kubectl get nodes` streams down the reverse tunnel (agent → hub
// backend proxy → out-of-process edges provider → agent → kind API server).
func TestKubectlThroughTunnel(t *testing.T) {
	if _, err := exec.LookPath("kind"); err != nil {
		t.Skip("kind not on PATH; this data-plane suite needs a kind edge target")
	}

	edgeName := "conn-k8s"
	kindName := "kedge-edgesconn"
	workDir := t.TempDir()
	kubeconfig := filepath.Join(workDir, "kedge.kubeconfig")    // tenant login context
	kindKubeconfig := filepath.Join(workDir, "kind.kubeconfig") // agent's backing cluster
	edgeKubeconfig := filepath.Join(workDir, "edge.kubeconfig") // consumer, through the tunnel

	// 1. Log in as the static tenant user; the CLI writes a workspace-scoped
	// context we drive `kedge`/`kubectl` against.
	runCLI(t, kubeconfig, kedgeBin, "login", "--hub-url", hubURL, "--insecure-skip-tls-verify", "--token", staticToken)
	tenantWS := clusterFromKubeconfig(t, kubeconfig)
	t.Logf("tenant workspace = %s", tenantWS)

	tenantAdmin := kcpDynamic(t, tenantWS, adminToken)

	// 2. Enable edges in the tenant workspace (APIBinding) + wait Bound.
	enableEdges(t, tenantAdmin)

	// 3. The edge-proxy grant the hub REST /enable path would create
	// (EnsureProviderEdgeProxyGrant). A plain APIBinding does NOT create it, and
	// without it the provider can't read the edge CR to validate the agent's
	// join token → the tunnel is rejected "invalid join token". Bind BOTH the
	// qualified and local SA forms — the join-token direct-read authorizes as
	// the local system:serviceaccount:default:provider.
	grantEdgeProxy(t, tenantAdmin)

	// 4. Register the KubernetesCluster via the CLI (new edges.kedge.faros.sh
	// group). Label it so the workload subtest's edgeSelector can target it.
	runCLI(t, kubeconfig, kedgeBin, "edge", "create", edgeName, "--type", "kubernetes", "--labels", "app="+edgeName)
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(kubernetesClusterGVR).Delete(context.Background(), edgeName, metav1.DeleteOptions{})
	})

	// 5. Wait for the token controller to issue status.joinToken.
	joinToken := waitForJoinToken(t, tenantAdmin, kubernetesClusterGVR, edgeName)

	// 6. Stand up a kind cluster as the agent's backing cluster.
	createKindCluster(t, kindName, kindKubeconfig)

	// 7. Run the agent against the tunnel.
	startAgent(t, edgeName, joinToken, tenantWS, "--type", "kubernetes", "--kubeconfig", kindKubeconfig)

	// 8. Wait for the edge to report connected.
	waitForConnected(t, tenantAdmin, kubernetesClusterGVR, edgeName)

	// 9. THE PROOF: fetch the edge kubeconfig and list nodes through the tunnel.
	runCLI(t, kubeconfig, kedgeBin, "kubeconfig", "edge", edgeName, "--output", edgeKubeconfig)
	out := kubectlThroughTunnel(t, edgeKubeconfig)
	if !strings.Contains(out, "control-plane") {
		t.Fatalf("kubectl get nodes through tunnel did not return a control-plane node:\n%s", out)
	}
	t.Logf("kubectl get nodes through the tunnel:\n%s", out)

	// 10. Workload plane: a Workload with an edgeSelector matching this edge
	// should be scheduled (provider scheduler → Placement) and materialized by
	// the agent's workload reconciler as a Deployment on the edge cluster.
	t.Run("workload deploys to the edge", func(t *testing.T) {
		wl := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "edges.kedge.faros.sh/v1alpha1",
			"kind":       "Workload",
			"metadata":   map[string]any{"name": "conn-wl", "namespace": "default"},
			"spec": map[string]any{
				"simple":   map[string]any{"image": "registry.k8s.io/pause:3.9"},
				"replicas": int64(1),
				"placement": map[string]any{
					"edgeSelector": map[string]any{"matchLabels": map[string]any{"app": edgeName}},
				},
			},
		}}
		if _, err := tenantAdmin.Resource(workloadGVR).Namespace("default").Create(ctxWithTimeout(t, 10*time.Second), wl, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create Workload: %v", err)
		}
		t.Cleanup(func() {
			_ = tenantAdmin.Resource(workloadGVR).Namespace("default").Delete(context.Background(), "conn-wl", metav1.DeleteOptions{})
		})

		// The scheduler creates a Placement, the agent creates a Deployment in
		// the edge cluster's default namespace whose pod goes Running.
		var last string
		if !waitFor(t, 3*time.Minute, func() (bool, string) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", edgeKubeconfig,
				"get", "pods", "-n", "default", "--insecure-skip-tls-verify",
				"--field-selector=status.phase=Running", "-o", "name")
			b, _ := cmd.CombinedOutput()
			last = string(b)
			return strings.TrimSpace(last) != "", last
		}) {
			t.Fatalf("workload never produced a Running pod on the edge; last:\n%s", last)
		}
		t.Logf("workload deployed a Running pod on the edge:\n%s", last)
	})

	// 11. MCP plane: the hub's MCP aggregate for this tenant should federate the
	// edges provider's kube toolset (proves the provider's /mcp is discovered +
	// proxied for the tenant's connected KubernetesCluster edge).
	t.Run("mcp aggregate federates the edge kube toolset", func(t *testing.T) {
		assertMCPAggregateListsKubeTools(t, tenantWS)
	})
}

// --- steps ---

func enableEdges(t *testing.T, tenant dynamic.Interface) {
	t.Helper()
	claim := func(group, resource string) map[string]any {
		return map[string]any{
			"group": group, "resource": resource,
			"verbs":    []any{"get", "list", "watch", "create", "update", "patch", "delete"},
			"selector": map[string]any{"matchAll": true},
			"state":    "Accepted",
		}
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": "edges"},
		"spec": map[string]any{
			"reference": map[string]any{"export": map[string]any{"path": edgesWorkspacePath, "name": edgesAPIExportName}},
			"permissionClaims": []any{
				claim("", "namespaces"), claim("", "serviceaccounts"), claim("", "secrets"),
				claim("rbac.authorization.k8s.io", "clusterroles"), claim("rbac.authorization.k8s.io", "clusterrolebindings"),
			},
		},
	}}
	if _, err := tenant.Resource(apiBindingGVR).Create(ctxWithTimeout(t, 10*time.Second), binding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create APIBinding: %v", err)
	}
	if !waitFor(t, 30*time.Second, func() (bool, string) {
		got, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 2*time.Second), "edges", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return phase == "Bound", "phase=" + phase
	}) {
		t.Fatal("edges APIBinding never reached Bound")
	}
}

func grantEdgeProxy(t *testing.T, tenant dynamic.Interface) {
	t.Helper()
	// Provider workspace cluster ID → the qualified subject.
	providersWS := kcpDynamic(t, "root:kedge:providers", adminToken)
	ws, err := providersWS.Resource(workspaceGVR).Get(ctxWithTimeout(t, 10*time.Second), "edges", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get provider workspace: %v", err)
	}
	providerCluster, _, _ := unstructured.NestedString(ws.Object, "spec", "cluster")
	if providerCluster == "" {
		t.Fatal("provider workspace has no spec.cluster")
	}
	qualified := identity.QualifiedServiceAccount(providerCluster, "default", "provider")

	name := "kedge:provider:edges:edgeproxy"
	rules := []any{
		map[string]any{"nonResourceURLs": []any{"/"}, "verbs": []any{"access"}},
		map[string]any{"apiGroups": []any{"edges.kedge.faros.sh"}, "resources": []any{"kubernetesclusters", "linuxservers"}, "verbs": []any{"get", "list", "watch", "proxy"}},
		map[string]any{"apiGroups": []any{"edges.kedge.faros.sh"}, "resources": []any{"kubernetesclusters/status", "linuxservers/status"}, "verbs": []any{"get", "update", "patch"}},
		map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"}, "verbs": []any{"get", "list", "watch", "create", "update"}},
		map[string]any{"apiGroups": []any{""}, "resources": []any{"namespaces"}, "verbs": []any{"get", "create"}},
		map[string]any{"apiGroups": []any{"authentication.k8s.io"}, "resources": []any{"tokenreviews"}, "verbs": []any{"create"}},
		map[string]any{"apiGroups": []any{"authorization.k8s.io"}, "resources": []any{"subjectaccessreviews"}, "verbs": []any{"create"}},
	}
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": name}, "rules": rules,
	}}
	if _, err := tenant.Resource(clusterRoleGVR).Create(ctxWithTimeout(t, 10*time.Second), role, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create ClusterRole: %v", err)
	}
	crb := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": name},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": name},
		"subjects": []any{
			map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": qualified},
			map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": "system:serviceaccount:default:provider"},
		},
	}}
	if _, err := tenant.Resource(clusterRoleBindingGVR).Create(ctxWithTimeout(t, 10*time.Second), crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create ClusterRoleBinding: %v", err)
	}
}

func waitForJoinToken(t *testing.T, tenant dynamic.Interface, gvr schema.GroupVersionResource, edgeName string) string {
	t.Helper()
	var token string
	if !waitFor(t, 60*time.Second, func() (bool, string) {
		got, err := tenant.Resource(gvr).Get(ctxWithTimeout(t, 5*time.Second), edgeName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		token, _, _ = unstructured.NestedString(got.Object, "status", "joinToken")
		return token != "", "joinToken empty"
	}) {
		t.Fatal("join token never issued")
	}
	return token
}

func createKindCluster(t *testing.T, name, kubeconfig string) {
	t.Helper()
	// Reuse if it already exists (previous local run), else create.
	t.Logf("creating kind cluster %q (this takes ~30-60s)", name)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kind", "create", "cluster", "--name", name, "--kubeconfig", kubeconfig, "--wait", "60s")
	if out, err := cmd.CombinedOutput(); err != nil {
		// If it already exists, just export the kubeconfig.
		if strings.Contains(string(out), "already exist") {
			exp := exec.Command("kind", "export", "kubeconfig", "--name", name, "--kubeconfig", kubeconfig)
			if o2, e2 := exp.CombinedOutput(); e2 != nil {
				t.Fatalf("kind export kubeconfig: %v\n%s", e2, o2)
			}
		} else {
			t.Fatalf("kind create cluster: %v\n%s", err, out)
		}
	}
	t.Cleanup(func() {
		_ = exec.Command("kind", "delete", "cluster", "--name", name).Run()
	})
}

func startAgent(t *testing.T, edgeName, joinToken, tenantWS string, extra ...string) *exec.Cmd {
	t.Helper()
	logf, _ := os.Create(filepath.Join(t.TempDir(), "agent.log"))
	args := append([]string{
		"agent", "run",
		"--hub-url", hubURL,
		"--hub-insecure-skip-tls-verify",
		"--token", joinToken,
		"--tunnel-url", hubURL,
		"--edge-name", edgeName,
		"--cluster", tenantWS,
	}, extra...)
	cmd := exec.Command(kedgeBin, args...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start agent: %v", err)
	}
	t.Logf("agent started (pid=%d, log=%s)", cmd.Process.Pid, logf.Name())
	t.Cleanup(func() { killGroup(cmd) })
	return cmd
}

func waitForConnected(t *testing.T, tenant dynamic.Interface, gvr schema.GroupVersionResource, edgeName string) {
	t.Helper()
	if !waitFor(t, 3*time.Minute, func() (bool, string) {
		got, err := tenant.Resource(gvr).Get(ctxWithTimeout(t, 5*time.Second), edgeName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		conn, _, _ := unstructured.NestedBool(got.Object, "status", "connected")
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return conn, fmt.Sprintf("connected=%v phase=%s", conn, phase)
	}) {
		t.Fatal("edge never became connected")
	}
}

func kubectlThroughTunnel(t *testing.T, edgeKubeconfig string) string {
	t.Helper()
	var last string
	// The consumer stream can 502 for a beat right after connect; retry briefly.
	if !waitFor(t, 60*time.Second, func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", edgeKubeconfig, "get", "nodes", "--insecure-skip-tls-verify")
		out, err := cmd.CombinedOutput()
		last = string(out)
		return err == nil && strings.Contains(last, "Ready"), last
	}) {
		t.Fatalf("kubectl get nodes through tunnel never succeeded; last output:\n%s", last)
	}
	return last
}

// --- CLI + kubeconfig helpers ---

// runCLI runs a kedge/kubectl command with an isolated KUBECONFIG.
func runCLI(t *testing.T, kubeconfig string, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", filepath.Base(name), strings.Join(args, " "), err, string(out))
	}
	return string(out)
}

// clusterFromKubeconfig extracts the logical cluster name from the server URL
// (https://.../clusters/<cluster>) of the kedge login context.
func clusterFromKubeconfig(t *testing.T, kubeconfig string) string {
	t.Helper()
	b, err := os.ReadFile(kubeconfig)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.Index(line, "/clusters/"); i >= 0 {
			rest := line[i+len("/clusters/"):]
			for j, r := range rest {
				if r == ' ' || r == '\n' || r == '/' || r == '"' {
					return strings.TrimSpace(rest[:j])
				}
			}
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no /clusters/ in kubeconfig:\n%s", string(b))
	return ""
}

func insecureClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // test-only
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() (bool, string)) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if ok, msg := cond(); ok {
			return true
		} else {
			last = msg
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("wait timeout after %s; last: %s", timeout, last)
	return false
}
