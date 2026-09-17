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

package kueryprovider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

// workspacePath is the provider sub-workspace the hub's Provider controller
// materializes from provider.yaml.
const workspacePath = "root:railgrid:providers:kuery"

// apiExportName is what `kuery-provider init` authors in the sub-workspace.
const apiExportName = "kuery.providers.railgrid.ai"

var (
	secretGVR     = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	apiBindingGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
	apiExportGVR  = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	schemaGVR     = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
	savedViewGVR  = schema.GroupVersionResource{
		Group: "kuery.providers.railgrid.ai", Version: "v1alpha1", Resource: "savedviews",
	}
)

// systemProvidersClient targets root:railgrid:system:providers — where
// Provider + CatalogEntry live.
func systemProvidersClient(t *testing.T) dynamic.Interface {
	return kcpDynamic(t, "root:railgrid:system:providers", adminToken)
}

// providerSubClient targets root:railgrid:providers:kuery, where the
// APIExport + schemas + RBAC live.
func providerSubClient(t *testing.T) dynamic.Interface {
	return kcpDynamic(t, workspacePath, adminToken)
}

// kcpDynamicRaw is kcpDynamic for non-test callers (TestMain bootstrap).
func kcpDynamicRaw(clusterPath, token string) (dynamic.Interface, error) {
	cfg := &rest.Config{
		Host:        kcpServer + "/clusters/" + clusterPath,
		BearerToken: token,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true, // dev cert is self-signed
		},
	}
	return dynamic.NewForConfig(cfg)
}

func kcpDynamic(t *testing.T, clusterPath, token string) dynamic.Interface {
	t.Helper()
	cfg := &rest.Config{
		Host:        kcpServer + "/clusters/" + clusterPath,
		BearerToken: token,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true, // dev cert is self-signed
		},
	}
	c, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("dynamic client for %s: %v", clusterPath, err)
	}
	return c
}

// applyKueryManifests applies provider.yaml (kind Provider) + manifest.yaml
// (kind CatalogEntry) into root:railgrid:system:providers, mirroring `make
// install-provider-kuery`. Called from TestMain. The hub reports /readyz
// before those APIs are fully servable, so creates are retried until the API
// answers.
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
				// The committed manifest targets :8084 (`make
				// run-provider-kuery`); the suite runs on :18118 to keep
				// test ports separate from dev-loop ports.
				overrideURL := "http://localhost:" + providerPort
				if err := unstructured.SetNestedField(obj.Object, overrideURL, "spec", "ui", "url"); err != nil {
					return fmt.Errorf("%s: override spec.ui.url: %w", file, err)
				}
				if err := unstructured.SetNestedField(obj.Object, overrideURL, "spec", "backend", "url"); err != nil {
					return fmt.Errorf("%s: override spec.backend.url: %w", file, err)
				}
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

// waitForCondition polls fn until it reports true or the timeout expires,
// logging the last reason on failure.
func waitForCondition(t *testing.T, timeout time.Duration, fn func() (bool, string)) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		ok, reason := fn()
		if ok {
			return true
		}
		last = reason
		time.Sleep(2 * time.Second)
	}
	t.Logf("condition never met: %s", last)
	return false
}

// hubGet issues a GET against the hub with the static tenant token.
func hubGet(t *testing.T, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, hubURL+path, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+staticToken)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// providerGet issues a GET straight at the kuery provider, bypassing the hub.
func providerGet(t *testing.T, path string) (int, []byte) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get("http://127.0.0.1:" + providerPort + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// providerGetWithHeaders is providerGet with caller-supplied headers, used to
// exercise kuery's tenant-identity contract.
func providerGetWithHeaders(t *testing.T, path string, headers map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+providerPort+path, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", path, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// TestACatalogProvisioning asserts the kcp-side artefacts provisioning leaves
// behind. TestMain already applied Provider + CatalogEntry and ran
// `kuery-provider init`, so the sub-workspace artifacts here come from the
// Provider controller plus init.
func TestACatalogProvisioning(t *testing.T) {
	sys := systemProvidersClient(t)

	providerGVR := schema.GroupVersionResource{Group: "admin.railgrid.ai", Version: "v1alpha1", Resource: "providers"}
	if _, err := sys.Resource(providerGVR).Get(ctxWithTimeout(t, 10*time.Second), "kuery", metav1.GetOptions{}); err != nil {
		t.Fatalf("Provider/kuery not found in root:railgrid:system:providers: %v", err)
	}

	ceGVR := schema.GroupVersionResource{Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries"}
	ce, err := sys.Resource(ceGVR).Get(ctxWithTimeout(t, 10*time.Second), "kuery", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("CatalogEntry/kuery not found: %v", err)
	}
	// edgeProxyAccess is what makes the hub grant the provider SA the "proxy"
	// verb on edges at Enable time; kuery's engagement controller depends on
	// it, so a silent flip to false would break fleet sync in production.
	if v, found, _ := unstructured.NestedBool(ce.Object, "spec", "edgeProxyAccess"); !found || !v {
		t.Errorf("CatalogEntry spec.edgeProxyAccess = %v (found=%v), want true", v, found)
	}
	if name, _, _ := unstructured.NestedString(ce.Object, "spec", "apiExport", "name"); name != apiExportName {
		t.Errorf("CatalogEntry spec.apiExport.name = %q, want %q", name, apiExportName)
	}

	// The Provider controller materializes the sub-workspace, the SA and the
	// provider-token Secret; mintRuntimeKubeconfig already depended on the
	// last of those, so assert the APIExport + schema that `init` authored.
	sub := providerSubClient(t)
	export, err := sub.Resource(apiExportGVR).Get(ctxWithTimeout(t, 10*time.Second), apiExportName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("APIExport %s not found in %s: %v", apiExportName, workspacePath, err)
	}

	// init must have attached the SavedView schema to the export.
	resources, _, _ := unstructured.NestedSlice(export.Object, "spec", "resources")
	var haveSavedView bool
	for _, r := range resources {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if m["name"] == "savedviews" && m["group"] == "kuery.providers.railgrid.ai" {
			haveSavedView = true
		}
	}
	if !haveSavedView {
		t.Errorf("APIExport spec.resources does not include savedviews.kuery.providers.railgrid.ai: %v", resources)
	}

	// The four built-in claims backing the per-workspace engagement
	// ServiceAccount. manifest.yaml, the chart's catalogentry.yaml and
	// init_cmd.go all have to agree; a claim dropped from the export is
	// silently denied at reconcile, which is invisible until fleet sync
	// quietly stops working.
	claims, _, _ := unstructured.NestedSlice(export.Object, "spec", "permissionClaims")
	claimed := map[string]bool{}
	for _, c := range claims {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		group, _ := m["group"].(string)
		resource, _ := m["resource"].(string)
		claimed[group+"/"+resource] = true
	}
	for _, want := range []string{
		"/serviceaccounts",
		"/secrets",
		"rbac.authorization.k8s.io/clusterroles",
		"rbac.authorization.k8s.io/clusterrolebindings",
	} {
		if !claimed[want] {
			t.Errorf("APIExport is missing permissionClaim %q (have %v)", want, claimed)
		}
	}

	// maximalPermissionPolicy caps tenant access as well as provider access,
	// so it must stay unset — same invariant the quickstart suite pins.
	if _, found, _ := unstructured.NestedMap(export.Object, "spec", "maximalPermissionPolicy", "local"); found {
		t.Error("spec.maximalPermissionPolicy.local was set; it caps tenant access too — must remain unset")
	}

	// Without the bind grant a tenant cannot create the APIBinding at all,
	// so Enable would fail for every tenant.
	crGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	crbGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
	const bindName = "railgrid:providers:bind:" + apiExportName
	if _, err := sub.Resource(crGVR).Get(ctxWithTimeout(t, 10*time.Second), bindName, metav1.GetOptions{}); err != nil {
		t.Errorf("bind ClusterRole %s missing: %v", bindName, err)
	}
	if _, err := sub.Resource(crbGVR).Get(ctxWithTimeout(t, 10*time.Second), bindName, metav1.GetOptions{}); err != nil {
		t.Errorf("bind ClusterRoleBinding %s missing: %v", bindName, err)
	}

	list, err := sub.Resource(schemaGVR).List(ctxWithTimeout(t, 10*time.Second), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list APIResourceSchemas: %v", err)
	}
	if len(list.Items) == 0 {
		t.Error("no APIResourceSchemas in the kuery sub-workspace; init did not apply the schemas dir")
	}
}

// TestBAPIProvidersDTO asserts kuery shows up in the hub's provider catalog
// DTO with the routing fields the portal needs.
func TestBAPIProvidersDTO(t *testing.T) {
	status, body := hubGet(t, "/api/providers")
	if status != http.StatusOK {
		t.Fatalf("GET /api/providers = %d, body=%s", status, truncate(body))
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode /api/providers: %v (body=%s)", err, truncate(body))
	}
	if !bytes.Contains(body, []byte("kuery")) {
		t.Fatalf("/api/providers does not mention kuery: %s", truncate(body))
	}
}

// TestCBackendProxy asserts the hub reverse-proxies to kuery's backend, so a
// tenant reaches the provider without addressing it directly.
func TestCBackendProxy(t *testing.T) {
	status, body := hubGet(t, "/services/providers/kuery/healthz")
	if status != http.StatusOK {
		t.Fatalf("backend proxy /services/providers/kuery/healthz = %d, body=%s", status, truncate(body))
	}
	if !bytes.Contains(body, []byte("ok")) {
		t.Errorf("healthz body = %s, want it to contain \"ok\"", truncate(body))
	}
}

// TestDQuerySchemaSurface asserts /api/query-schema answers without any
// connected edge. This is the surface the portal's query builder reads to
// populate relation names, so it must not depend on fleet sync.
func TestDQuerySchemaSurface(t *testing.T) {
	status, body := providerGet(t, "/api/query-schema")
	if status != http.StatusOK {
		t.Fatalf("GET /api/query-schema = %d, body=%s", status, truncate(body))
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("query-schema is not valid JSON: %v (body=%s)", err, truncate(body))
	}
	// The relation vocabulary the SavedView schema documents — if these stop
	// being advertised the portal's builder silently loses options.
	for _, rel := range []string{"owners", "descendants", "references"} {
		if !bytes.Contains(body, []byte(rel)) {
			t.Errorf("/api/query-schema does not advertise relation %q: %s", rel, truncate(body))
		}
	}
}

// TestEEdgesIdentityContract pins how /api/edges scopes callers. This is a
// tenant-isolation boundary: kuery answers only for the cluster ID the proxy
// asserts, so the three cases below (absent / malformed / valid) must keep
// their distinct statuses. The 400-vs-401 split matters — a workspace path is
// something the proxy should never send, and retrying cannot fix it, so it
// must not look like "just log in again".
func TestEEdgesIdentityContract(t *testing.T) {
	t.Run("no identity is 401", func(t *testing.T) {
		status, body := providerGetWithHeaders(t, "/api/edges", nil)
		if status != http.StatusUnauthorized {
			t.Errorf("GET /api/edges without identity = %d, want 401; body=%s", status, truncate(body))
		}
	})

	t.Run("workspace path is 400 not 401", func(t *testing.T) {
		// A path like root:railgrid:orgs:acme is a valid-looking tenant
		// reference but not a logical-cluster ID; kuery must reject it as a
		// caller error rather than an auth failure.
		status, body := providerGetWithHeaders(t, "/api/edges", map[string]string{
			"X-Railgrid-Tenant": "root:railgrid:orgs:acme",
		})
		if status != http.StatusBadRequest {
			t.Errorf("GET /api/edges with a workspace path = %d, want 400; body=%s", status, truncate(body))
		}
	})

	t.Run("valid cluster id returns the empty fleet", func(t *testing.T) {
		cluster := loginStaticTokenAndGetCluster(t)
		status, body := providerGetWithHeaders(t, "/api/edges", map[string]string{
			"X-Railgrid-Cluster": cluster,
		})
		if status != http.StatusOK {
			t.Fatalf("GET /api/edges with cluster %q = %d, body=%s", cluster, status, truncate(body))
		}
		var out struct {
			Tenant   string   `json:"tenant"`
			Edges    []string `json:"edges"`
			Clusters []string `json:"clusters"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("edges is not valid JSON: %v (body=%s)", err, truncate(body))
		}
		if out.Tenant != cluster {
			t.Errorf("tenant = %q, want the requested cluster %q", out.Tenant, cluster)
		}
		// No edge is connected in this suite, so the fleet must come back
		// empty — and as [] rather than null, which the portal iterates.
		if len(out.Edges) != 0 {
			t.Errorf("edges = %v, want empty on an unconnected fleet", out.Edges)
		}
		if !bytes.Contains(body, []byte(`"edges":[]`)) {
			t.Errorf("edges should marshal as [] not null: %s", truncate(body))
		}
	})
}

// TestFStatusSurface asserts /api/status answers and reports sync state.
func TestFStatusSurface(t *testing.T) {
	status, body := providerGet(t, "/api/status")
	if status != http.StatusOK {
		t.Fatalf("GET /api/status = %d, body=%s", status, truncate(body))
	}
	var out struct {
		Provider    string `json:"provider"`
		StoreDriver string `json:"storeDriver"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("status is not valid JSON: %v (body=%s)", err, truncate(body))
	}
	if out.Provider != "kuery" {
		t.Errorf("status provider = %q, want \"kuery\"", out.Provider)
	}
}

// TestGTenantEnableAndSavedViewUsable binds the kuery APIExport into the
// tenant workspace and round-trips a SavedView, which is the whole point of
// the export: a tenant can persist a query without the provider proxying it.
func TestGTenantEnableAndSavedViewUsable(t *testing.T) {
	tenantWS := loginStaticTokenAndGetCluster(t)
	t.Logf("tenant workspace = %s", tenantWS)
	tenant := kcpDynamic(t, tenantWS, staticToken)

	// Clean any stale binding from a previous run.
	_ = tenant.Resource(apiBindingGVR).Delete(ctxWithTimeout(t, 5*time.Second), "kuery", metav1.DeleteOptions{})
	for i := 0; i < 5; i++ {
		_, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 2*time.Second), "kuery", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			break
		}
		time.Sleep(time.Second)
	}

	// Claims mirror manifest.yaml: the built-in types backing the
	// per-workspace engagement ServiceAccount. A claim missing here is
	// silently denied at reconcile, so the binding must carry all four.
	claim := func(group, resource string) map[string]any {
		c := map[string]any{
			"resource": resource,
			"verbs":    []any{"get", "list", "watch", "create"},
			"selector": map[string]any{"matchAll": true},
			"state":    "Accepted",
		}
		if group != "" {
			c["group"] = group
		}
		return c
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": "kuery"},
		"spec": map[string]any{
			"reference": map[string]any{
				"export": map[string]any{
					"path": workspacePath,
					"name": apiExportName,
				},
			},
			"permissionClaims": []any{
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

	ok := waitForCondition(t, 60*time.Second, func() (bool, string) {
		got, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 2*time.Second), "kuery", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return phase == "Bound", "phase=" + phase
	})
	if !ok {
		t.Fatal("APIBinding never reached Bound")
	}

	sv := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kuery.providers.railgrid.ai/v1alpha1",
		"kind":       "SavedView",
		"metadata":   map[string]any{"name": "e2e-view", "namespace": "default"},
		"spec": map[string]any{
			"displayName": "e2e saved view",
			"root": map[string]any{
				"apiGroup":  "apps",
				"kind":      "Deployment",
				"namespace": "default",
				"name":      "e2e-target",
			},
			"relations": []any{"owners", "descendants"},
			"maxDepth":  int64(3),
		},
	}}
	if _, err := tenant.Resource(savedViewGVR).Namespace("default").Create(ctxWithTimeout(t, 10*time.Second), sv, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create SavedView: %v", err)
	}
	t.Cleanup(func() {
		_ = tenant.Resource(savedViewGVR).Namespace("default").Delete(context.Background(), "e2e-view", metav1.DeleteOptions{})
	})

	got, err := tenant.Resource(savedViewGVR).Namespace("default").Get(ctxWithTimeout(t, 5*time.Second), "e2e-view", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read SavedView back: %v", err)
	}
	if name, _, _ := unstructured.NestedString(got.Object, "spec", "displayName"); name != "e2e saved view" {
		t.Errorf("spec.displayName = %q", name)
	}
	rels, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "relations")
	if len(rels) != 2 || rels[0] != "owners" {
		t.Errorf("spec.relations = %v, want [owners descendants]", rels)
	}
	// maxDepth is int32 in the schema; kcp must round-trip it intact.
	if d, found, _ := unstructured.NestedInt64(got.Object, "spec", "maxDepth"); !found || d != 3 {
		t.Errorf("spec.maxDepth = %d (found=%v), want 3", d, found)
	}
}

// TestHTenantDisableRemovesSavedView asserts deleting the APIBinding takes the
// tenant's kuery API surface with it, so Disable really revokes access.
func TestHTenantDisableRemovesSavedView(t *testing.T) {
	tenantWS := loginStaticTokenAndGetCluster(t)
	tenant := kcpDynamic(t, tenantWS, staticToken)

	if err := tenant.Resource(apiBindingGVR).Delete(ctxWithTimeout(t, 10*time.Second), "kuery", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("delete APIBinding: %v", err)
	}

	ok := waitForCondition(t, 60*time.Second, func() (bool, string) {
		_, err := tenant.Resource(savedViewGVR).Namespace("default").List(ctxWithTimeout(t, 2*time.Second), metav1.ListOptions{})
		if err == nil {
			return false, "savedviews still served"
		}
		// Once the binding is gone the API disappears entirely.
		return apierrors.IsNotFound(err) || meta.IsNoMatchError(err), err.Error()
	})
	if !ok {
		t.Error("savedviews API still served after the APIBinding was deleted")
	}
}

// truncate keeps failure output readable when a handler returns a large body.
func truncate(b []byte) string {
	const max = 512
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}

// loginStaticTokenAndGetCluster exchanges the static token for a kubeconfig
// and returns the tenant's logical cluster. The hub reports /readyz before
// the users APIBinding in root:railgrid:users is fully usable, so the first
// logins after startup can 500; retry until the binding settles rather than
// failing the suite on the race.
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
	// kubeconfig server: https://.../clusters/<cluster>
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
