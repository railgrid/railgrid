/*
Copyright 2026 The Railgrid Authors.

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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	"sigs.k8s.io/yaml"

	"github.com/railgrid/railgrid/pkg/apiurl"
)

// Kuery runs beside the edges provider in this suite; :18098 is edges.
const kueryPort = "18099"

const (
	kueryWorkspacePath = "root:railgrid:providers:kuery"
	kueryAPIExportName = "kuery.providers.railgrid.ai"
)

var (
	engagementGVR     = schema.GroupVersionResource{Group: "kuery.providers.railgrid.ai", Version: "v1alpha1", Resource: "engagements"}
	savedViewGVR      = schema.GroupVersionResource{Group: "kuery.providers.railgrid.ai", Version: "v1alpha1", Resource: "savedviews"}
	logicalClusterGVR = schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}
)

// queryStatus mirrors the kuery v1alpha1.QueryStatus wire shape. Declared
// locally so the suite does not take a dependency on the kuery module just to
// read a response.
type queryStatus struct {
	Objects  []objectResult `json:"objects"`
	Warnings []string       `json:"warnings"`
}

type objectResult struct {
	ID        string                    `json:"id"`
	Cluster   string                    `json:"cluster"`
	Object    json.RawMessage           `json:"object"`
	Relations map[string][]objectResult `json:"relations"`
}

// name digs metadata.name out of the projected object payload.
func (o objectResult) name() string {
	var obj struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	_ = json.Unmarshal(o.Object, &obj)
	return obj.Metadata.Name
}

func (o objectResult) kind() string {
	var obj struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(o.Object, &obj)
	return obj.Kind
}

// TestKueryAggregatesEdgeObjects is the proof that kuery actually aggregates:
// it engages a real edge, creates a Deployment on that edge cluster, and then
// asserts kuery's own query API returns the object with the right cluster
// attribution AND can walk the ownership chain down to the Pod.
//
// The relation walk is the point. Returning the Deployment only proves the
// sync loop copied an object; resolving Deployment -> ReplicaSet -> Pod proves
// kuery built the relationship graph that a plain list cannot answer.
func TestKueryAggregatesEdgeObjects(t *testing.T) {
	if _, err := exec.LookPath("kind"); err != nil {
		t.Skip("kind not on PATH; kuery aggregation needs a real edge target")
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "bin", "kuery-provider")); err != nil {
		t.Skip("bin/kuery-provider not built; run `make build-kuery-provider`")
	}

	edgeName := "kuery-k8s"
	kindName := "railgrid-kueryagg"
	workDir := suiteTempDir(t, "kuery-aggregation")
	kubeconfig := filepath.Join(workDir, "railgrid.kubeconfig")
	kindKubeconfig := filepath.Join(workDir, "kind.kubeconfig")

	// 1. Tenant login.
	runCLI(t, kubeconfig, railgridBin, "login", "--hub-url", hubURL, "--insecure-skip-tls-verify", "--token", staticToken)
	tenantWS := clusterFromKubeconfig(t, kubeconfig)
	t.Logf("tenant workspace = %s", tenantWS)
	tenantAdmin := kcpDynamic(t, tenantWS, adminToken)

	// 2. Edges must be enabled and the edge-proxy grant present before an
	// agent can join — kuery reads the fleet through that same proxy.
	enableEdges(t, tenantAdmin)
	grantEdgeProxy(t, tenantAdmin)

	// 3. Register the edge and bring up a real backing cluster + agent.
	runCLI(t, kubeconfig, railgridBin, "edge", "create", edgeName, "--type", "kubernetes", "--labels", "app="+edgeName)
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(kubernetesClusterGVR).Delete(context.Background(), edgeName, metav1.DeleteOptions{})
	})
	joinToken := waitForJoinToken(t, tenantAdmin, kubernetesClusterGVR, edgeName)
	createKindCluster(t, kindName, kindKubeconfig)
	startAgent(t, edgeName, joinToken, tenantWS, "--type", "kubernetes", "--kubeconfig", kindKubeconfig)
	waitForConnected(t, tenantAdmin, kubernetesClusterGVR, edgeName)

	// 4. Put a known workload on the edge. A Deployment gives a three-level
	// ownership chain (Deployment -> ReplicaSet -> Pod) for the relation walk.
	const deployName = "kuery-agg-target"
	kubectlKind(t, kindKubeconfig, "create", "deployment", deployName,
		"--image=registry.k8s.io/pause:3.9", "-n", "default")
	// Wait for the ReplicaSet and Pod to exist on the edge, otherwise the
	// relation assertion races the edge's own controllers rather than kuery.
	if !waitFor(t, 2*time.Minute, func() (bool, string) {
		out := kubectlKind(t, kindKubeconfig, "get", "pods", "-n", "default",
			"-l", "app="+deployName, "-o", "name")
		return strings.Contains(out, "pod/"), "pods on edge: " + strings.TrimSpace(out)
	}) {
		t.Fatal("deployment never produced a pod on the edge cluster")
	}

	// 5. Bring kuery up beside the edges provider and enable it for the tenant
	// the way the portal does: through the hub's Enable endpoint, accepting
	// the edges composition kuery declares. That consent is what lets the hub
	// mint kuery's engagement identity for this workspace (identity policy
	// clause E); a bare APIBinding would leave kuery refused forever.
	startKueryProvider(t, workDir)
	enableKueryViaHub(t, tenantAdmin, tenantWS)

	// 6. Wait for kuery to engage the edge. The Engagement records in kuery's
	// provider workspace are the authority on which edges it syncs; until the
	// engagement controller has minted its identity and dialled the edge,
	// queries legitimately refuse, so this is a wait rather than an assertion.
	// Results attribute objects as "{clusterID}/{edge}" — kuery's store name.
	engaged := tenantWS + "/" + edgeName
	kueryAdmin := kcpDynamic(t, kueryWorkspacePath, adminToken)
	if !waitFor(t, 3*time.Minute, func() (bool, string) {
		list, err := kueryAdmin.Resource(engagementGVR).List(ctxWithTimeout(t, 10*time.Second), metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		var seen []string
		for _, item := range list.Items {
			cluster, _, _ := unstructured.NestedString(item.Object, "spec", "cluster")
			edge, _, _ := unstructured.NestedString(item.Object, "spec", "edge")
			phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
			message, _, _ := unstructured.NestedString(item.Object, "status", "message")
			seen = append(seen, fmt.Sprintf("%s/%s=%s(%s)", cluster, edge, phase, message))
			if cluster == tenantWS && edge == edgeName && phase == "Engaged" {
				return true, ""
			}
		}
		return false, "engagements: " + strings.Join(seen, ", ")
	}) {
		t.Logf("kuery-provider log tail:\n%s", tailFile(filepath.Join(workDir, "kuery-provider.log"), 40))
		t.Fatal("kuery never engaged the edge")
	}
	t.Logf("kuery engaged edge %q as cluster %q", edgeName, engaged)

	// 7. THE PROOF: a SavedView in the tenant workspace that finds the
	// Deployment and expands its descendants, run through kuery's one tenant
	// verb — the kcp custom subresource savedviews/run on its APIExport,
	// POST /clusters/{clusterID}/apis/kuery.providers.railgrid.ai/v1alpha1/savedviews/{name}/run.
	const viewName = "kuery-agg-view"
	view := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kuery.providers.railgrid.ai/v1alpha1",
		"kind":       "SavedView",
		"metadata":   map[string]any{"name": viewName},
		"spec": map[string]any{
			"displayName": "edge aggregation e2e",
			"query": map[string]any{
				"filter": map[string]any{
					"objects": []any{map[string]any{
						"groupKind": map[string]any{"apiGroup": "apps", "kind": "Deployment"},
						"name":      deployName,
						"namespace": "default",
					}},
				},
				"objects": map[string]any{
					"cluster":   true,
					"relations": map[string]any{"descendants+": map[string]any{}},
				},
				"maxDepth": int64(3),
			},
		},
	}}
	if _, err := tenantAdmin.Resource(savedViewGVR).Create(ctxWithTimeout(t, 15*time.Second), view, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create SavedView: %v", err)
	}
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(savedViewGVR).Delete(context.Background(), viewName, metav1.DeleteOptions{})
	})

	var got objectResult
	if !waitFor(t, 3*time.Minute, func() (bool, string) {
		status, body := runSavedView(t, tenantWS, viewName)
		if status != http.StatusOK {
			return false, fmt.Sprintf("run %d: %s", status, trunc(body))
		}
		var envelope struct {
			Result queryStatus `json:"result"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return false, "decode: " + err.Error()
		}
		for _, o := range envelope.Result.Objects {
			if o.name() == deployName {
				got = o
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d objects, none named %s", len(envelope.Result.Objects), deployName)
	}) {
		t.Fatal("kuery never returned the Deployment created on the edge")
	}

	// Attribution: the object must be reported against the engaged edge, not
	// some other cluster — this is what makes a fleet-wide result usable.
	if engaged != "" && got.Cluster != engaged {
		t.Errorf("Deployment reported on cluster %q, want %q", got.Cluster, engaged)
	}
	if !strings.Contains(got.Cluster, edgeName) {
		t.Errorf("cluster attribution %q does not reference edge %q", got.Cluster, edgeName)
	}

	// Relationship graph: descendants+ must reach the ReplicaSet and the Pod.
	kinds := map[string]bool{}
	var walk func(objs []objectResult)
	walk = func(objs []objectResult) {
		for _, o := range objs {
			if k := o.kind(); k != "" {
				kinds[k] = true
			}
			for _, nested := range o.Relations {
				walk(nested)
			}
		}
	}
	walk(flattenRelations(got))

	for _, want := range []string{"ReplicaSet", "Pod"} {
		if !kinds[want] {
			t.Errorf("descendants+ of the Deployment did not include a %s (saw %v); "+
				"kuery returned the object but not its ownership graph", want, keys(kinds))
		}
	}
}

// flattenRelations returns every relation bucket of an object as one slice.
func flattenRelations(o objectResult) []objectResult {
	var out []objectResult
	for _, v := range o.Relations {
		out = append(out, v...)
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// kubectlKind runs kubectl against the edge's backing kind cluster.
func kubectlKind(t *testing.T, kubeconfig string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", kubeconfig}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "already exists") {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// startKueryProvider provisions, initialises and serves kuery beside the edges
// provider: Provider + CatalogEntry into the system workspace, mint the SA
// kubeconfig the Provider controller populates, `kuery-provider init` for the
// APIExport, then serve on kueryPort with a scratch sqlite store.
func startKueryProvider(t *testing.T, workDir string) {
	t.Helper()

	if err := applyKueryManifests(); err != nil {
		t.Fatalf("apply kuery manifests: %v", err)
	}

	runtimeKubeconfig := filepath.Join(workDir, "kuery-runtime.kubeconfig")
	if err := mintKueryKubeconfig(runtimeKubeconfig, 2*time.Minute); err != nil {
		t.Fatalf("mint kuery kubeconfig: %v", err)
	}

	initLog, _ := os.Create(filepath.Join(workDir, "kuery-init.log"))
	initCmd := exec.Command(filepath.Join(repoRoot, "bin", "kuery-provider"), "init")
	initCmd.Env = append(os.Environ(),
		"RAILGRID_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		"KUERY_WORKSPACE_PATH="+kueryWorkspacePath,
		"RAILGRID_KCP_DIR="+filepath.Join(repoRoot, "providers", "kuery", "deploy", "chart", "files"),
	)
	initCmd.Stdout = initLog
	initCmd.Stderr = initLog
	if err := initCmd.Run(); err != nil {
		t.Fatalf("kuery init failed: %v (log: %s)", err, initLog.Name())
	}

	provLog, _ := os.Create(filepath.Join(workDir, "kuery-provider.log"))
	cmd := exec.Command(filepath.Join(repoRoot, "bin", "kuery-provider"))
	cmd.Env = append(os.Environ(),
		"PORT="+kueryPort,
		"RAILGRID_HUB_URL="+hubURL,
		// Deliberately NO RAILGRID_HUB_TOKEN. hubclient.ResolveHubToken
		// prefers that variable over RAILGRID_PROVIDER_KUBECONFIG, so setting
		// it to a tenant's static token makes every hub call authenticate as
		// that USER. The heartbeat tolerates it; POST /api/identities does
		// not, and must not — it attests that the caller is the provider's own
		// ServiceAccount, so kuery's engagement controller was refused with
		// `wrong_identity` and no workspace was ever engaged. Leaving it unset
		// resolves the provider SA bearer out of the runtime kubeconfig, which
		// is the credential the chart actually mounts.
		"RAILGRID_HUB_INSECURE=true",
		"RAILGRID_PROVIDER_NAME=kuery",
		"RAILGRID_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		// Scratch store so the suite never writes kuery.db into the repo.
		"KUERY_STORE_DRIVER=sqlite",
		"KUERY_STORE_DSN="+filepath.Join(workDir, "kuery.db"),
	)
	cmd.Stdout = provLog
	cmd.Stderr = provLog
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start kuery-provider: %v", err)
	}
	t.Logf("kuery-provider started (pid=%d, port=:%s, log=%s)", cmd.Process.Pid, kueryPort, provLog.Name())
	t.Cleanup(func() { killGroup(cmd) })

	if err := waitReady("http://127.0.0.1:"+kueryPort+"/healthz", 90*time.Second); err != nil {
		t.Fatalf("kuery-provider never ready: %v (log: %s)", err, provLog.Name())
	}
}

// applyKueryManifests mirrors `make install-provider-kuery`, rewriting the
// dev-loop URLs onto this suite's port.
func applyKueryManifests() error {
	cl, err := kcpDynamicRaw("root:railgrid:system:providers", adminToken)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	gvrByKind := map[string]schema.GroupVersionResource{
		"Provider":     {Group: "admin.railgrid.ai", Version: "v1alpha1", Resource: "providers"},
		"CatalogEntry": {Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries"},
	}
	for _, file := range []string{"provider.yaml", "manifest.yaml"} {
		raw, err := os.ReadFile(filepath.Join(repoRoot, "providers", "kuery", file))
		if err != nil {
			return fmt.Errorf("read %s: %w", file, err)
		}
		for _, doc := range bytes.Split(raw, []byte("\n---")) {
			if !bytes.Contains(doc, []byte("apiVersion:")) {
				continue
			}
			obj := &unstructured.Unstructured{}
			if err := yaml.Unmarshal(doc, &obj.Object); err != nil {
				return fmt.Errorf("parse %s: %w", file, err)
			}
			if obj.GetKind() == "" {
				continue
			}
			gvr, ok := gvrByKind[obj.GetKind()]
			if !ok {
				return fmt.Errorf("%s: unexpected kind %q", file, obj.GetKind())
			}
			if obj.GetKind() == "CatalogEntry" {
				overrideURL := "http://localhost:" + kueryPort
				_ = unstructured.SetNestedField(obj.Object, overrideURL, "spec", "serving", "ui", "url")
				_ = unstructured.SetNestedField(obj.Object, overrideURL, "spec", "serving", "backend", "url")
			}
			deadline := time.Now().Add(90 * time.Second)
			for {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				_, err = cl.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
				cancel()
				if err == nil || apierrors.IsAlreadyExists(err) {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("create %s %s: %w", obj.GetKind(), obj.GetName(), err)
				}
				time.Sleep(2 * time.Second)
			}
		}
	}
	return nil
}

// mintKueryKubeconfig waits for the Provider controller to populate kuery's
// provider-token Secret and wraps it in a workspace-scoped kubeconfig. The
// insecure flag matters beyond kcp: the engagement controller reuses this
// config's TLS setting for the hub edge-proxy, which serves the suite's
// self-signed cert.
func mintKueryKubeconfig(path string, timeout time.Duration) error {
	cl, err := kcpDynamicRaw(kueryWorkspacePath, adminToken)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	var token, lastErr string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		sec, err := cl.Resource(secretGVR).Namespace("default").Get(ctx, "provider-token", metav1.GetOptions{})
		cancel()
		if err != nil {
			lastErr = err.Error()
		} else {
			enc, _, _ := unstructured.NestedString(sec.Object, "data", "token")
			if enc != "" {
				raw, err := base64.StdEncoding.DecodeString(enc)
				if err != nil {
					return fmt.Errorf("decode provider-token: %w", err)
				}
				token = string(raw)
				break
			}
			lastErr = "provider-token Secret exists but token not yet populated"
		}
		time.Sleep(2 * time.Second)
	}
	if token == "" {
		return fmt.Errorf("provider-token never populated: %s", lastErr)
	}
	kc := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: railgrid
  cluster:
    server: %s/clusters/%s
    insecure-skip-tls-verify: true
contexts:
- name: railgrid
  context:
    cluster: railgrid
    user: railgrid
current-context: railgrid
users:
- name: railgrid
  user:
    token: %s
`, kcpServer, kueryWorkspacePath, token)
	return os.WriteFile(path, []byte(kc), 0o600)
}

// enableKueryViaHub drives POST .../providers/kuery/enable as the tenant user,
// which creates the kuery APIBinding AND records the accepted edges
// composition in the workspace's Grant. The org and workspace UUIDs come from
// the tenant cluster's own path annotation (root:railgrid:tenants:<org>:<ws>).
// Idempotent, as the endpoint is.
func enableKueryViaHub(t *testing.T, tenant dynamic.Interface, tenantWS string) {
	t.Helper()
	lc, err := tenant.Resource(logicalClusterGVR).Get(ctxWithTimeout(t, 10*time.Second), "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read tenant LogicalCluster: %v", err)
	}
	wsPath := lc.GetAnnotations()["kcp.io/path"]
	rest, ok := strings.CutPrefix(wsPath, "root:railgrid:tenants:")
	orgUUID, workspaceUUID, found := strings.Cut(rest, ":")
	if !ok || !found || orgUUID == "" || workspaceUUID == "" || strings.Contains(workspaceUUID, ":") {
		t.Fatalf("tenant workspace %s has path %q, want root:railgrid:tenants:<org>:<ws>", tenantWS, wsPath)
	}

	body, _ := json.Marshal(map[string]any{
		"acceptedClaims": []any{},
		"acceptedCompositions": []map[string]string{{
			"provider": "edges", "group": "edges.railgrid.ai", "resource": "kubernetesclusters",
		}},
	})
	path := fmt.Sprintf("/api/orgs/%s/workspaces/%s/providers/kuery/enable", orgUUID, workspaceUUID)
	var status int
	var raw []byte
	// The catalog controller registers kuery on its own cadence after the
	// manifests land, and Enable refuses until it has, so retry.
	if !waitFor(t, 2*time.Minute, func() (bool, string) {
		req, err := http.NewRequestWithContext(ctxWithTimeout(t, 60*time.Second), http.MethodPost, hubURL+path, bytes.NewReader(body))
		if err != nil {
			return false, err.Error()
		}
		req.Header.Set("Authorization", "Bearer "+staticToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Railgrid-Org", orgUUID)
		req.Header.Set("X-Railgrid-Workspace", workspaceUUID)
		resp, err := insecureClient(60 * time.Second).Do(req)
		if err != nil {
			return false, err.Error()
		}
		defer func() { _ = resp.Body.Close() }()
		status = resp.StatusCode
		raw, _ = io.ReadAll(resp.Body)
		return status == http.StatusOK || status == http.StatusCreated, fmt.Sprintf("enable %d: %s", status, trunc(raw))
	}) {
		t.Fatalf("enable kuery never succeeded: last status %d: %s", status, trunc(raw))
	}
	var out struct {
		BindingName string `json:"bindingName"`
	}
	_ = json.Unmarshal(raw, &out)
	bindingName := out.BindingName
	if bindingName == "" {
		bindingName = "kuery"
	}
	t.Cleanup(func() {
		_ = tenant.Resource(apiBindingGVR).Delete(context.Background(), bindingName, metav1.DeleteOptions{})
	})
	if !waitFor(t, 90*time.Second, func() (bool, string) {
		got, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 5*time.Second), bindingName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return phase == "Bound", "phase=" + phase
	}) {
		t.Fatal("kuery APIBinding never reached Bound")
	}
}

// runSavedView POSTs the saved view's run verb on kcp's own front door: the
// verb is a custom subresource on kuery's APIExport, so kcp authenticates the
// bearer (the kcp admin token — a hub user token means nothing there),
// authorizes the POST on savedviews/run with RBAC, and reverse-proxies the
// request to the kuery provider with the caller's identity stamped. The
// provider is never addressed directly: a verb has no bearer-authenticated
// spelling of its own, and its gate runs on the identity kcp forwards.
func runSavedView(t *testing.T, tenantWS, view string) (int, []byte) {
	t.Helper()
	path := apiurl.ProviderVerbPath(tenantWS, "kuery.providers.railgrid.ai", "v1alpha1", "savedviews", view, "run")
	req, err := http.NewRequest(http.MethodPost, kcpServer+path, bytes.NewReader([]byte(`{"input":{}}`)))
	if err != nil {
		t.Fatalf("new run request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := insecureClient(60 * time.Second).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// tailFile returns the last n lines of a log file, for failure output.
func tailFile(path string, n int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "(" + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func trunc(b []byte) string {
	const max = 400
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}
