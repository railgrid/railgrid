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

package edges

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/faroshq/faros-kedge/pkg/util/identity"
)

var (
	kubernetesClusterGVR = schema.GroupVersionResource{Group: "edges.kedge.faros.sh", Version: "v1alpha1", Resource: "kubernetesclusters"}
	linuxServerGVR       = schema.GroupVersionResource{Group: "edges.kedge.faros.sh", Version: "v1alpha1", Resource: "linuxservers"}
	apiBindingGVR        = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
)

// TestACatalogProvisioning asserts the kcp-side artefacts the edges bootstrap
// leaves behind: the CatalogEntry becomes Ready, and the provider sub-workspace
// holds the APIResourceSchemas + APIExport (with the 5 permission claims) that
// `edges-provider init` authored.
func TestACatalogProvisioning(t *testing.T) {
	cl := kcpDynamic(t, "root:kedge:system:providers", adminToken)
	gvr := schema.GroupVersionResource{Group: "providers.kedge.faros.sh", Version: "v1alpha1", Resource: "catalogentries"}
	ready := waitForCondition(t, 90*time.Second, func() (bool, string) {
		got, err := cl.Resource(gvr).Get(ctxWithTimeout(t, 5*time.Second), "edges", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
		for _, c := range conds {
			m, _ := c.(map[string]any)
			if m["type"] == "Ready" {
				return m["status"] == "True", fmt.Sprintf("Ready=%v reason=%v message=%v", m["status"], m["reason"], m["message"])
			}
		}
		return false, "no Ready condition yet"
	})
	if !ready {
		t.Fatal("edges CatalogEntry never became Ready")
	}

	sub := kcpDynamic(t, edgesWorkspacePath, adminToken)

	t.Run("APIResourceSchemas present for both edge kinds", func(t *testing.T) {
		arsGVR := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
		list, err := sub.Resource(arsGVR).List(ctxWithTimeout(t, 5*time.Second), metav1.ListOptions{})
		if err != nil {
			t.Fatalf("list APIResourceSchemas: %v", err)
		}
		want := map[string]bool{"kubernetesclusters": false, "linuxservers": false}
		for _, it := range list.Items {
			for k := range want {
				if strings.Contains(it.GetName(), "."+k+".edges.kedge.faros.sh") {
					want[k] = true
				}
			}
		}
		for k, found := range want {
			if !found {
				t.Errorf("missing APIResourceSchema for %s (got %d items)", k, len(list.Items))
			}
		}
	})

	t.Run("APIExport present with the 5 permissionClaims", func(t *testing.T) {
		gvr := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
		got, err := sub.Resource(gvr).Get(ctxWithTimeout(t, 5*time.Second), edgesAPIExportName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get APIExport %s: %v", edgesAPIExportName, err)
		}
		claims, _, _ := unstructured.NestedSlice(got.Object, "spec", "permissionClaims")
		if len(claims) != 5 {
			t.Fatalf("expected 5 permissionClaims, got %d", len(claims))
		}
		resources, _, _ := unstructured.NestedSlice(got.Object, "spec", "resources")
		if len(resources) < 2 {
			t.Errorf("expected at least kubernetesclusters+linuxservers in APIExport resources, got %d", len(resources))
		}
	})
}

// TestBAPIProvidersDTO asserts the edges provider surfaces on the hub's
// /api/providers DTO with the Edges category and its apiExport path.
func TestBAPIProvidersDTO(t *testing.T) {
	body := httpGetJSON(t, hubURL+"/api/providers", staticToken)
	items, _ := body["items"].([]any)
	byName := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byName[m["name"].(string)] = m
	}
	e := byName["edges"]
	if e == nil {
		var keys []string
		for k := range byName {
			keys = append(keys, k)
		}
		t.Fatalf("edges not in /api/providers: keys=%v", keys)
	}
	if e["ready"] != true {
		t.Errorf("edges ready = %v, want true", e["ready"])
	}
	if e["apiExportPath"] != edgesWorkspacePath {
		t.Errorf("apiExportPath = %v, want %s", e["apiExportPath"], edgesWorkspacePath)
	}
	if e["category"] != "Edges" {
		t.Errorf("category = %v, want Edges", e["category"])
	}
}

// TestCTenantEnableAndCRsUsable binds the edges APIExport into a tenant
// workspace and proves both edge kinds become creatable there.
func TestCTenantEnableAndCRsUsable(t *testing.T) {
	tenantWS := loginStaticTokenAndGetCluster(t)
	t.Logf("tenant workspace = %s", tenantWS)
	tenant := kcpDynamic(t, tenantWS, staticToken)

	// Clean any stale binding from a previous run.
	_ = tenant.Resource(apiBindingGVR).Delete(ctxWithTimeout(t, 5*time.Second), "edges", metav1.DeleteOptions{})

	claim := func(group, resource string) map[string]any {
		return map[string]any{
			"group":    group,
			"resource": resource,
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
			"reference": map[string]any{
				"export": map[string]any{"path": edgesWorkspacePath, "name": edgesAPIExportName},
			},
			"permissionClaims": []any{
				claim("", "namespaces"),
				claim("", "serviceaccounts"),
				claim("", "secrets"),
				claim("rbac.authorization.k8s.io", "clusterroles"),
				claim("rbac.authorization.k8s.io", "clusterrolebindings"),
			},
		},
	}}
	if _, err := tenant.Resource(apiBindingGVR).Create(ctxWithTimeout(t, 10*time.Second), binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create APIBinding: %v", err)
	}
	t.Cleanup(func() {
		_ = tenant.Resource(apiBindingGVR).Delete(context.Background(), "edges", metav1.DeleteOptions{})
	})

	if !waitForCondition(t, 30*time.Second, func() (bool, string) {
		got, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 2*time.Second), "edges", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return phase == "Bound", "phase=" + phase
	}) {
		t.Fatal("edges APIBinding never reached Bound")
	}

	for _, tc := range []struct {
		name, apiVersion, kind string
		gvr                    schema.GroupVersionResource
	}{
		{"kube-edge-1", "edges.kedge.faros.sh/v1alpha1", "KubernetesCluster", kubernetesClusterGVR},
		{"srv-edge-1", "edges.kedge.faros.sh/v1alpha1", "LinuxServer", linuxServerGVR},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			cr := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": tc.apiVersion,
				"kind":       tc.kind,
				"metadata":   map[string]any{"name": tc.name},
				"spec":       map[string]any{},
			}}
			if _, err := tenant.Resource(tc.gvr).Create(ctxWithTimeout(t, 10*time.Second), cr, metav1.CreateOptions{}); err != nil {
				t.Fatalf("create %s: %v", tc.kind, err)
			}
			t.Cleanup(func() {
				_ = tenant.Resource(tc.gvr).Delete(context.Background(), tc.name, metav1.DeleteOptions{})
			})
			if _, err := tenant.Resource(tc.gvr).Get(ctxWithTimeout(t, 5*time.Second), tc.name, metav1.GetOptions{}); err != nil {
				t.Fatalf("read back %s: %v", tc.kind, err)
			}
		})
	}
}

// TestDEdgeProxyAuthBoundary proves the Enable-time edge-proxy grant end-to-end
// against real kcp on the NEW decoupled path: the provider SA token targeting a
// tenant's edgeproxy is 403 before the grant and 502 after (authorization runs
// before the tunnel lookup, so a missing tunnel means auth PASSED). Adapted from
// the pre-decouple hub-served test, now against
// /services/providers/edges/edgeproxy/... with group edges.kedge.faros.sh.
func TestDEdgeProxyAuthBoundary(t *testing.T) {
	tenantWS := loginStaticTokenAndGetCluster(t)

	// Provider workspace cluster ID — the value kcp embeds in the SA token
	// claims and the grant subject must carry.
	providersWS := kcpDynamic(t, "root:kedge:providers", adminToken)
	workspaceGVR := schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}
	ws, err := providersWS.Resource(workspaceGVR).Get(ctxWithTimeout(t, 10*time.Second), "edges", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get provider workspace: %v", err)
	}
	providerCluster, _, _ := unstructured.NestedString(ws.Object, "spec", "cluster")
	if providerCluster == "" {
		t.Fatal("provider workspace has no spec.cluster")
	}

	// Fetch the provider SA's minted long-lived token (a NON-static, kcp-authn
	// credential; the static token would bypass authz and always 502).
	sub := kcpDynamic(t, edgesWorkspacePath, adminToken)
	var saToken string
	if !waitForCondition(t, 30*time.Second, func() (bool, string) {
		sec, err := sub.Resource(secretGVR).Namespace("default").Get(ctxWithTimeout(t, 5*time.Second), "provider-token", metav1.GetOptions{})
		if err != nil {
			return false, "get provider-token: " + err.Error()
		}
		tok, _, _ := unstructured.NestedString(sec.Object, "data", "token")
		if tok == "" {
			return false, "token not populated"
		}
		raw, derr := base64.StdEncoding.DecodeString(tok)
		if derr != nil {
			return false, "decode: " + derr.Error()
		}
		saToken = string(raw)
		return true, ""
	}) {
		t.Fatal("provider SA token never appeared")
	}

	// The edge name is irrelevant: authorization runs BEFORE the tunnel lookup.
	proxyURL := hubURL + "/services/providers/edges/edgeproxy/clusters/" + tenantWS +
		"/apis/edges.kedge.faros.sh/v1alpha1/kubernetesclusters/e2e-no-such-edge/k8s/api"
	probe := func() int {
		req, _ := http.NewRequest(http.MethodGet, proxyURL, nil)
		req.Header.Set("Authorization", "Bearer "+saToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("edgeproxy probe: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	// 1. No grant → authorization must fail.
	if code := probe(); code != http.StatusForbidden {
		t.Fatalf("expected 403 before grant, got %d", code)
	}

	// 2. Materialize the grant in the tenant workspace exactly as the Enable
	// endpoint does (same qualified subject, proxy verb on both edge kinds).
	tenantAdmin := kcpDynamic(t, tenantWS, adminToken)
	subject := identity.QualifiedServiceAccount(providerCluster, "default", "provider")
	t.Logf("grant subject = %s", subject)

	grantName := "kedge:provider:edges:edgeproxy"
	clusterRoleGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	clusterRoleBindingGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}

	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata":   map[string]any{"name": grantName},
		"rules": []any{
			// "access" on "/" satisfies kcp's workspaceContentAuthorizer for
			// the foreign SA.
			map[string]any{"nonResourceURLs": []any{"/"}, "verbs": []any{"access"}},
			// The SAR the provider issues checks "proxy" on the edge kind.
			map[string]any{
				"apiGroups": []any{"edges.kedge.faros.sh"},
				"resources": []any{"kubernetesclusters", "linuxservers"},
				"verbs":     []any{"proxy", "get", "list", "watch"},
			},
			// The provider validates+authorizes the caller with its own
			// credential, so it must be able to create TokenReviews +
			// SubjectAccessReviews in the tenant workspace (mirrors the real
			// EnsureProviderEdgeProxyGrant).
			map[string]any{
				"apiGroups": []any{"authentication.k8s.io"},
				"resources": []any{"tokenreviews"},
				"verbs":     []any{"create"},
			},
			map[string]any{
				"apiGroups": []any{"authorization.k8s.io"},
				"resources": []any{"subjectaccessreviews"},
				"verbs":     []any{"create"},
			},
		},
	}}
	if _, err := tenantAdmin.Resource(clusterRoleGVR).Create(ctxWithTimeout(t, 10*time.Second), role, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create ClusterRole: %v", err)
	}
	crb := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   map[string]any{"name": grantName},
		"roleRef":    map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": grantName},
		"subjects":   []any{map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": subject}},
	}}
	if _, err := tenantAdmin.Resource(clusterRoleBindingGVR).Create(ctxWithTimeout(t, 10*time.Second), crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create ClusterRoleBinding: %v", err)
	}
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(clusterRoleBindingGVR).Delete(context.Background(), grantName, metav1.DeleteOptions{})
		_ = tenantAdmin.Resource(clusterRoleGVR).Delete(context.Background(), grantName, metav1.DeleteOptions{})
	})

	// 3. With the grant: authorization passes, tunnel lookup fails → 502.
	if !waitForCondition(t, 30*time.Second, func() (bool, string) {
		code := probe()
		return code == http.StatusBadGateway, fmt.Sprintf("status=%d (want 502)", code)
	}) {
		t.Fatal("edgeproxy never authorized the provider SA after grant")
	}

	// 4. Revoke → 403 again.
	if err := tenantAdmin.Resource(clusterRoleBindingGVR).Delete(ctxWithTimeout(t, 5*time.Second), grantName, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete ClusterRoleBinding: %v", err)
	}
	if !waitForCondition(t, 30*time.Second, func() (bool, string) {
		code := probe()
		return code == http.StatusForbidden, fmt.Sprintf("status=%d (want 403)", code)
	}) {
		t.Fatal("edgeproxy still authorizes the provider SA after revocation")
	}
}

// --- helpers ---

func httpGetJSON(t *testing.T, url, token string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
		Timeout:   10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("%s: status %d body=%s", url, resp.StatusCode, string(b))
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %s: %v body=%s", url, err, string(b))
	}
	return out
}

// loginStaticTokenAndGetCluster calls /auth/token-login with the static token
// and extracts the tenant workspace's logical cluster name from the kubeconfig.
func loginStaticTokenAndGetCluster(t *testing.T) string {
	t.Helper()
	var (
		b    []byte
		code int
	)
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		req, _ := http.NewRequest(http.MethodPost, hubURL+"/auth/token-login", nil)
		req.Header.Set("Authorization", "Bearer "+staticToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false, "token-login: " + err.Error()
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ = io.ReadAll(resp.Body)
		code = resp.StatusCode
		return code == 200, fmt.Sprintf("token-login: status %d body=%s", code, string(b))
	}) {
		t.Fatalf("token-login never succeeded: last status %d body=%s", code, string(b))
	}
	var out struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	kc, err := base64.StdEncoding.DecodeString(out.Kubeconfig)
	if err != nil {
		t.Fatalf("decode kubeconfig: %v", err)
	}
	for _, line := range strings.Split(string(kc), "\n") {
		if strings.Contains(line, "/clusters/") {
			i := strings.Index(line, "/clusters/") + len("/clusters/")
			rest := line[i:]
			for j, r := range rest {
				if r == ' ' || r == '\n' || r == '/' {
					return strings.TrimSpace(rest[:j])
				}
			}
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no /clusters/ in kubeconfig: %s", string(kc))
	return ""
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() (bool, string)) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastMsg string
	for time.Now().Before(deadline) {
		if ok, msg := cond(); ok {
			return true
		} else {
			lastMsg = msg
		}
		time.Sleep(time.Second)
	}
	t.Logf("wait timeout after %s; last status: %s", timeout, lastMsg)
	return false
}
