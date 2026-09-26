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

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/util/identity"
)

var (
	kubernetesClusterGVR = schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "kubernetesclusters"}
	linuxServerGVR       = schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "linuxservers"}
	apiBindingGVR        = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
)

// TestACatalogProvisioning asserts the kcp-side artefacts the edges bootstrap
// leaves behind: the CatalogEntry becomes Ready, and the provider sub-workspace
// holds the APIResourceSchemas + APIExport (with the 5 permission claims) that
// `edges-provider init` authored.
func TestACatalogProvisioning(t *testing.T) {
	cl := kcpDynamic(t, "root:railgrid:system:providers", adminToken)
	gvr := schema.GroupVersionResource{Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries"}
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
				if strings.Contains(it.GetName(), "."+k+".edges.railgrid.ai") {
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

	t.Run("APIExport claims exactly what the provider still needs", func(t *testing.T) {
		gvr := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
		got, err := sub.Resource(gvr).Get(ctxWithTimeout(t, 5*time.Second), edgesAPIExportName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get APIExport %s: %v", edgesAPIExportName, err)
		}
		claims, _, _ := unstructured.NestedSlice(got.Object, "spec", "permissionClaims")
		// Four claims, and which four is the assertion.
		//
		// This used to be seven. The provider-contract remediation removed
		// serviceaccounts, clusterroles and clusterrolebindings: a provider
		// does not mint identities, it asks the hub's scoped-identity service
		// (docs/provider-connectivity-contract.md §"Scoped identities", review
		// finding M7). A claim on those three types IS the ability to mint a
		// standing credential in every consumer workspace, so their absence is
		// the invariant worth pinning — see the explicit check below.
		//
		// What remains: namespaces + secrets (the objects the provider owns in
		// a tenant workspace; secrets is narrowed by a defaultSelector) and the
		// two delegated-auth review APIs the provider uses to authenticate and
		// authorize data-plane callers against the consumer workspace
		// (kcp#4279/#4280).
		byCoordinate := map[string]bool{}
		for _, c := range claims {
			m, _ := c.(map[string]any)
			g, _ := m["group"].(string)
			res, _ := m["resource"].(string)
			byCoordinate[g+"/"+res] = true
		}
		for _, forbidden := range []string{"/serviceaccounts", "rbac.authorization.k8s.io/clusterroles", "rbac.authorization.k8s.io/clusterrolebindings"} {
			if byCoordinate[forbidden] {
				t.Errorf("APIExport claims %q; a provider does not mint identities — it asks the hub for a scoped identity", forbidden)
			}
		}
		if len(claims) != 4 {
			t.Fatalf("expected 4 permissionClaims (namespaces, secrets, tokenreviews, subjectaccessreviews), got %d: %v", len(claims), claims)
		}
		wantReviewClaims := map[string]bool{
			"authentication.k8s.io/tokenreviews":        false,
			"authorization.k8s.io/subjectaccessreviews": false,
			"/namespaces": false,
			"/secrets":    false,
		}
		for key := range wantReviewClaims {
			wantReviewClaims[key] = byCoordinate[key]
		}
		for key, found := range wantReviewClaims {
			if !found {
				t.Errorf("APIExport missing permissionClaim %q", key)
			}
		}
		resources, _, _ := unstructured.NestedSlice(got.Object, "spec", "resources")
		if len(resources) < 2 {
			t.Errorf("expected at least kubernetesclusters+linuxservers in APIExport resources, got %d", len(resources))
		}
	})
}

// TestBAPIProvidersDTO asserts the edges provider surfaces on the hub's
// /api/providers DTO with the Edges category and its export section — the
// APIExport name a tenant binds, the workspace path hosting it, and the API
// groups the hub read off the export itself.
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
	export, _ := e["export"].(map[string]any)
	if export == nil {
		t.Fatalf("DTO carries no export section; edges exports an API: %v", e)
	}
	if export["name"] != edgesAPIExportName {
		t.Errorf("export.name = %v, want %s", export["name"], edgesAPIExportName)
	}
	if export["path"] != edgesWorkspacePath {
		t.Errorf("export.path = %v, want %s", export["path"], edgesWorkspacePath)
	}
	// export.apiGroups is what the hub read off the APIExport itself, and it
	// differs from the export name: `edges.providers.railgrid.ai` serves
	// `edges.railgrid.ai`. Empty means the hub never managed to read it, which
	// is the fail-closed state the scoped-identity policy refuses on.
	groups, _ := export["apiGroups"].([]any)
	if len(groups) == 0 {
		t.Errorf("export.apiGroups is empty; the hub never resolved the groups edges serves: %v", export)
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
		{"kube-edge-1", "edges.railgrid.ai/v1alpha1", "KubernetesCluster", kubernetesClusterGVR},
		{"srv-edge-1", "edges.railgrid.ai/v1alpha1", "LinuxServer", linuxServerGVR},
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
// against real kcp: the provider SA token addressing a tenant edge's k8s verb
// — a kcp custom subresource on the edges APIExport, reached on the hub's kcp
// front door at /clusters/{id}/apis/edges.railgrid.ai/v1alpha1/... — is denied
// before the grant and 502 after (the gate runs before the tunnel lookup, so a
// missing tunnel means authorization PASSED).
func TestDEdgeProxyAuthBoundary(t *testing.T) {
	workspaceGVR := schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}

	// Create a DEDICATED child workspace for this test and enable edges in it.
	// Delegated auth resolves the tenant through the provider's APIExport virtual
	// workspace, which only engages consumer clusters that have bound edges — so
	// the test tenant must be a real, engaged consumer. A fresh child (never
	// bound before) also avoids the CRD bind/unbind/rebind churn that a shared
	// static-token workspace would suffer across subtests.
	// Parent must serve the tenancy API (i.e. allow child workspaces); a
	// token-login tenant is a leaf `workspace` type that does not. root:railgrid
	// already parents the provider/org workspaces, so create the test consumer
	// there with admin.
	parentAdmin := kcpDynamic(t, "root:railgrid", adminToken)
	const childName = "edges-authboundary"
	_ = parentAdmin.Resource(workspaceGVR).Delete(ctxWithTimeout(t, 5*time.Second), childName, metav1.DeleteOptions{})
	if _, err := parentAdmin.Resource(workspaceGVR).Create(ctxWithTimeout(t, 10*time.Second), &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": childName},
	}}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create child workspace: %v", err)
	}
	t.Cleanup(func() {
		_ = parentAdmin.Resource(workspaceGVR).Delete(context.Background(), childName, metav1.DeleteOptions{})
	})
	var tenantWS string
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		got, err := parentAdmin.Resource(workspaceGVR).Get(ctxWithTimeout(t, 2*time.Second), childName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		tenantWS, _, _ = unstructured.NestedString(got.Object, "spec", "cluster")
		return phase == "Ready" && tenantWS != "", "phase=" + phase
	}) {
		t.Fatal("child workspace never became Ready")
	}
	tenant := kcpDynamic(t, tenantWS, adminToken)

	// Enable edges: bind the APIExport accepting every claim (including the
	// tokenreviews + subjectaccessreviews review APIs the provider needs to run
	// delegated auth against this workspace through the VW).
	acceptClaim := func(group, resource string) map[string]any {
		return map[string]any{
			"group": group, "resource": resource,
			"verbs":    []any{"get", "list", "watch", "create", "update", "patch", "delete"},
			"selector": map[string]any{"matchAll": true},
			"state":    "Accepted",
		}
	}
	if _, err := tenant.Resource(apiBindingGVR).Create(ctxWithTimeout(t, 10*time.Second), &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": "edges"},
		"spec": map[string]any{
			"reference": map[string]any{"export": map[string]any{"path": edgesWorkspacePath, "name": edgesAPIExportName}},
			// Exactly the claims the export still declares. Accepting one it
			// does not (serviceaccounts, clusterroles, clusterrolebindings —
			// the identity-minting trio the remediation removed) binds anyway
			// but stamps PermissionClaimsValid=False on the APIBinding, so the
			// suite would be asserting "Bound" over a broken binding.
			"permissionClaims": []any{
				acceptClaim("", "namespaces"), acceptClaim("", "secrets"),
				acceptClaim("authentication.k8s.io", "tokenreviews"), acceptClaim("authorization.k8s.io", "subjectaccessreviews"),
			},
		},
	}}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create APIBinding: %v", err)
	}
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
	// A real edge makes the provider's multicluster manager engage this cluster
	// (so tenantConfigFor can resolve it), and the probe below targets THIS
	// edge.
	//
	// It used to target a deliberately non-existent one, on the premise that
	// "authorization runs before the tunnel lookup". That is no longer true:
	// kcp authorizes the verb, then provider-sdk/dataplane's gate reviews the
	// caller's visibility of the addressed object and reads it as the
	// provider, so a name that does not exist is denied at the gate whatever
	// the grant says — the probe would read 404 before and after the grant
	// and prove nothing. Pointing it at a real edge that no agent has
	// connected is what separates the two states: denied (404) without the
	// grant, past the gate and failing at the tunnel lookup (502) with it.
	if _, err := tenant.Resource(kubernetesClusterGVR).Create(ctxWithTimeout(t, 10*time.Second), &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "edges.railgrid.ai/v1alpha1",
		"kind":       "KubernetesCluster",
		"metadata":   map[string]any{"name": "e2e-engage"},
		"spec":       map[string]any{},
	}}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create edge to engage tenant: %v", err)
	}

	// Provider workspace cluster ID — the value kcp embeds in the SA token
	// claims and the grant subject must carry.
	providersWS := kcpDynamic(t, "root:railgrid:providers", adminToken)
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

	// The edge exists but has no agent, so a request that passes the gate
	// dies at the tunnel lookup — which is exactly the signal this test reads.
	// The verb is a kube path on the hub's kcp front door; the tail after
	// "k8s" is the Kubernetes API path the edge is asked for.
	proxyURL := apiurl.EdgeVerbURL(hubURL, "kubernetes", tenantWS, "e2e-engage", "k8s") + "/api"
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

	// 1. No grant → authorization must fail without disclosure.
	//
	// kcp refuses the verb with 403 (RBAC on kubernetesclusters/k8s) before
	// the request ever reaches the provider; a caller who holds the verb but
	// cannot see the edge is refused by the provider's gate with 404
	// (provider-sdk/dataplane answers ErrDenied with StatusNotFound so a
	// denial does not disclose whether the addressed edge exists). Both are
	// "denied"; neither is the 502 that only a request past the gate can
	// produce.
	if code := probe(); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("expected 403 or 404 before grant, got %d", code)
	}

	// 2. Materialize the grant in the tenant workspace exactly as the Enable
	// endpoint does (same qualified subject, proxy verb on both edge kinds).
	tenantAdmin := kcpDynamic(t, tenantWS, adminToken)
	subject := identity.QualifiedServiceAccount(providerCluster, "default", "provider")
	t.Logf("grant subject = %s", subject)

	grantName := "railgrid:provider:edges:edgeproxy"
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
			// The gate reviews the caller's `get` on the addressed object, so
			// `get` on the kind is what lets the request past it at all.
			map[string]any{
				"apiGroups": []any{"edges.railgrid.ai"},
				"resources": []any{"kubernetesclusters", "linuxservers"},
				"verbs":     []any{"get", "list", "watch"},
			},
			// kcp authorizes the verb itself: the HTTP method mapped onto the
			// RBAC verb on the {resource}/{verb} COORDINATE —
			// kubernetesclusters/k8s, not the old wildcard `proxy` on the
			// kind — so a grant on a verb coordinate is the wildcard verb,
			// exactly as the hub materializes one. kubectl's GET/LIST/WATCH
			// through k8s and an SSH upgrade are all covered by it; a grant
			// of `proxy` on the kind authorizes nothing.
			map[string]any{
				"apiGroups": []any{"edges.railgrid.ai"},
				"resources": []any{"kubernetesclusters/k8s", "kubernetesclusters/ssh", "linuxservers/k8s", "linuxservers/ssh"},
				"verbs":     []any{"*"},
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
		// A gate refusal is a 404 on the wire whether the caller was denied, the
		// addressed object could not be read, or the review never ran, so the
		// provider's own account of it is the only way to tell them apart.
		t.Logf("edges-provider log tail:\n%s", tailInitLog(providerLogPath, 40))
		t.Fatal("edgeproxy never authorized the provider SA after grant")
	}

	// 4. Revoke → denied again (403 from kcp, or 404 per the non-disclosure
	// rule above).
	if err := tenantAdmin.Resource(clusterRoleBindingGVR).Delete(ctxWithTimeout(t, 5*time.Second), grantName, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete ClusterRoleBinding: %v", err)
	}
	if !waitForCondition(t, 30*time.Second, func() (bool, string) {
		code := probe()
		return code == http.StatusForbidden || code == http.StatusNotFound, fmt.Sprintf("status=%d (want 403 or 404)", code)
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
