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

package provider

import (
	"bytes"
	"context"
	"crypto/tls"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

// workspacePath is the provider sub-workspace the hub's Provider controller
// materializes from provider.yaml.
const workspacePath = "root:kedge:providers:quickstart"

var secretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// providersWorkspaceClient returns a dynamic client targeting
// systemProvidersClient returns a dynamic client targeting
// root:kedge:system:providers — where Provider + CatalogEntry live since the
// provider bootstrap refactor.
func systemProvidersClient(t *testing.T) dynamic.Interface {
	return kcpDynamic(t, "root:kedge:system:providers", adminToken)
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

// providerSubClient returns a dynamic client targeting
// root:kedge:providers:{name} (where the per-provider APIExport + schemas
// + RBAC live).
func providerSubClient(t *testing.T, name string) dynamic.Interface {
	return kcpDynamic(t, "root:kedge:providers:"+name, adminToken)
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

// applyQuickstartManifests applies provider.yaml (kind Provider) +
// manifest.yaml (kind CatalogEntry) into root:kedge:system:providers,
// mirroring `make install-provider-quickstart`. Called from TestMain. The
// hub reports /readyz before those APIs are fully servable, so creates are
// retried until the API answers.
func applyQuickstartManifests() error {
	cl, err := kcpDynamicRaw("root:kedge:system:providers", adminToken)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	gvrByKind := map[string]schema.GroupVersionResource{
		"Provider":     {Group: "admin.kedge.faros.sh", Version: "v1alpha1", Resource: "providers"},
		"CatalogEntry": {Group: "providers.kedge.faros.sh", Version: "v1alpha1", Resource: "catalogentries"},
	}
	for _, file := range []string{"provider.yaml", "manifest.yaml"} {
		raw, err := os.ReadFile(filepath.Join(repoRoot, "providers", "quickstart", file))
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
				// The committed manifest targets :8081 (`make
				// run-provider-quickstart`); the suite runs on :18081 to
				// keep test ports separate from dev-loop ports.
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

// TestACatalogProvisioning asserts every kcp-side artefact provisioning is
// supposed to leave behind. TestMain already applied Provider + CatalogEntry
// (into root:kedge:system:providers) and ran `quickstart-provider init`, so
// the sub-workspace artifacts here come from the Provider controller + init.
func TestACatalogProvisioning(t *testing.T) {
	// Wait for status.conditions[Ready] == True on the CatalogEntry.
	cl := systemProvidersClient(t)
	gvr := schema.GroupVersionResource{
		Group: "providers.kedge.faros.sh", Version: "v1alpha1", Resource: "catalogentries",
	}
	ready := waitForCondition(t, 90*time.Second, func() (bool, string) {
		got, err := cl.Resource(gvr).Get(ctxWithTimeout(t, 5*time.Second), "quickstart", metav1.GetOptions{})
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
		t.Fatal("CatalogEntry never became Ready")
	}

	sub := providerSubClient(t, "quickstart")

	t.Run("sub-workspace APIResourceSchema present", func(t *testing.T) {
		arsGVR := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
		list, err := sub.Resource(arsGVR).List(ctxWithTimeout(t, 5*time.Second), metav1.ListOptions{})
		if err != nil {
			t.Fatalf("list APIResourceSchemas: %v", err)
		}
		if len(list.Items) == 0 {
			t.Fatal("no APIResourceSchemas in sub-workspace")
		}
		found := false
		for _, it := range list.Items {
			if strings.HasSuffix(it.GetName(), ".greetings.quickstart.providers.kedge.faros.sh") {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected greetings APIResourceSchema, got %d items", len(list.Items))
		}
	})

	t.Run("APIExport present with resources and permissionClaims", func(t *testing.T) {
		gvr := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
		got, err := sub.Resource(gvr).Get(ctxWithTimeout(t, 5*time.Second), "quickstart.providers.kedge.faros.sh", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get APIExport: %v", err)
		}
		resources, _, _ := unstructured.NestedSlice(got.Object, "spec", "resources")
		if len(resources) != 1 {
			t.Fatalf("expected 1 resource in APIExport spec, got %d", len(resources))
		}
		claims, _, _ := unstructured.NestedSlice(got.Object, "spec", "permissionClaims")
		if len(claims) != 1 {
			t.Fatalf("expected 1 permissionClaim, got %d", len(claims))
		}
		// MaximalPermissionPolicy must NOT be set — see the comment in
		// provision.go:ApplyAPIExport explaining why.
		if _, found, _ := unstructured.NestedMap(got.Object, "spec", "maximalPermissionPolicy", "local"); found {
			t.Fatal("spec.maximalPermissionPolicy.local was set; it caps tenant access too — must remain unset")
		}
	})

	t.Run("bind grant ClusterRole + ClusterRoleBinding for system:authenticated", func(t *testing.T) {
		crGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
		crbGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
		const name = "kedge:providers:bind:quickstart.providers.kedge.faros.sh"

		cr, err := sub.Resource(crGVR).Get(ctxWithTimeout(t, 5*time.Second), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get ClusterRole %s: %v", name, err)
		}
		rules, _, _ := unstructured.NestedSlice(cr.Object, "rules")
		if len(rules) == 0 {
			t.Fatal("ClusterRole has no rules")
		}
		rule := rules[0].(map[string]any)
		verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
		if len(verbs) != 1 || verbs[0] != "bind" {
			t.Fatalf("expected verbs=[bind], got %v", verbs)
		}

		crb, err := sub.Resource(crbGVR).Get(ctxWithTimeout(t, 5*time.Second), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get ClusterRoleBinding %s: %v", name, err)
		}
		subjects, _, _ := unstructured.NestedSlice(crb.Object, "subjects")
		if len(subjects) != 1 {
			t.Fatalf("expected 1 subject, got %d", len(subjects))
		}
		s := subjects[0].(map[string]any)
		if s["kind"] != "Group" || s["name"] != "system:authenticated" {
			t.Fatalf("expected Group system:authenticated, got %v/%v", s["kind"], s["name"])
		}
	})

	t.Run("provider ServiceAccount + cluster-admin binding in sub-workspace", func(t *testing.T) {
		saGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}
		_, err := sub.Resource(saGVR).Namespace("default").Get(ctxWithTimeout(t, 5*time.Second), "provider", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("ServiceAccount default/provider not found: %v", err)
		}
		crbGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
		crb, err := sub.Resource(crbGVR).Get(ctxWithTimeout(t, 5*time.Second), "kedge:providers:sa:provider", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("ClusterRoleBinding kedge:providers:sa:provider not found: %v", err)
		}
		role, _, _ := unstructured.NestedString(crb.Object, "roleRef", "name")
		if role != "cluster-admin" {
			t.Fatalf("expected cluster-admin role, got %q", role)
		}
	})
}

func TestBAPIProvidersDTO(t *testing.T) {
	body := httpGetJSON(t, hubURL+"/api/providers", staticToken)
	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("expected at least one provider")
	}

	byName := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byName[m["name"].(string)] = m
	}

	t.Run("quickstart third-party provider shape", func(t *testing.T) {
		qs := byName["quickstart"]
		if qs == nil {
			t.Fatalf("quickstart not in /api/providers: keys=%v", keysOf(byName))
		}
		for _, k := range []string{"displayName", "ready", "hasUI", "hasBackend", "apiExportPath", "apiExportName", "permissionClaims"} {
			if _, ok := qs[k]; !ok {
				t.Errorf("expected key %q in DTO, got: %v", k, qs)
			}
		}
		if qs["ready"] != true {
			t.Errorf("expected ready=true, got %v", qs["ready"])
		}
		if qs["apiExportPath"] != "root:kedge:providers:quickstart" {
			t.Errorf("apiExportPath = %v", qs["apiExportPath"])
		}
		// Third-party provider should NOT carry a builtinRoute.
		if br, ok := qs["builtinRoute"]; ok && br != "" {
			t.Errorf("third-party provider should not have builtinRoute, got %v", br)
		}
	})

	// NOTE: the mcp / kubernetes-edges / server-edges first-party builtins
	// (and their embedded LocalUIAssets + /ui/providers/mcp micro-frontend)
	// were extracted into standalone out-of-process providers, so they no
	// longer appear as hub builtins in /api/providers. Their DTO + UI
	// behavior is covered by the edges provider's own suite; this suite now
	// exercises only the generic third-party (quickstart) provider path.

	t.Run("categories registry surfaced in response", func(t *testing.T) {
		cats, _ := body["categories"].([]any)
		if len(cats) == 0 {
			t.Fatal("expected categories block in /api/providers response")
		}
		seen := map[string]map[string]any{}
		for _, c := range cats {
			m := c.(map[string]any)
			seen[m["name"].(string)] = m
		}
		for _, want := range []struct{ name, icon string }{
			{"Edges", "Server"},
			{"AI", "Sparkles"},
		} {
			c := seen[want.name]
			if c == nil {
				t.Errorf("missing category %s; saw %v", want.name, seen)
				continue
			}
			if c["icon"] != want.icon {
				t.Errorf("category %s: icon = %v, want %s", want.name, c["icon"], want.icon)
			}
		}
	})
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --providers flag-mechanics tests live in test/e2e/suites/providerflags
// because they must spawn their own hub on port 2380 (embedded etcd's
// hard-coded port) and so cannot coexist with this suite's shared hub.

func TestCBackendProxy(t *testing.T) {
	body := httpGetJSON(t, hubURL+"/services/providers/quickstart/api/hello", staticToken)
	if body["provider"] != "quickstart" {
		t.Errorf("expected provider=quickstart, got %v", body["provider"])
	}
	// tokenLength != 0 proves Authorization header was forwarded.
	if n, _ := body["tokenLength"].(float64); n == 0 {
		t.Error("Authorization header was not forwarded to provider")
	}
}

func TestDUIProxyMainJS(t *testing.T) {
	resp, err := http.Get(hubURL + "/ui/providers/quickstart/main.js")
	if err != nil {
		t.Fatalf("GET main.js: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "javascript") {
		t.Errorf("expected javascript content-type, got %q", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte("customElements.define")) {
		t.Error("main.js did not register a custom element")
	}
}

func TestEUIProxyIcon(t *testing.T) {
	resp, err := http.Get(hubURL + "/ui/providers/quickstart/icon.svg")
	if err != nil {
		t.Fatalf("GET icon.svg: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "svg") {
		t.Errorf("expected svg content-type, got %q", ct)
	}
}

func TestFTenantEnableAndCRUsable(t *testing.T) {
	tenantWS := loginStaticTokenAndGetCluster(t)
	t.Logf("tenant workspace = %s", tenantWS)
	tenant := kcpDynamic(t, tenantWS, staticToken)

	apiBindingGVR := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}

	// Clean any stale binding from a previous run.
	_ = tenant.Resource(apiBindingGVR).Delete(ctxWithTimeout(t, 5*time.Second), "quickstart", metav1.DeleteOptions{})
	// Wait briefly for delete to settle.
	for i := 0; i < 5; i++ {
		_, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 2*time.Second), "quickstart", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			break
		}
		time.Sleep(time.Second)
	}

	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": "quickstart"},
		"spec": map[string]any{
			"reference": map[string]any{
				"export": map[string]any{
					"path": "root:kedge:providers:quickstart",
					"name": "quickstart.providers.kedge.faros.sh",
				},
			},
			"permissionClaims": []any{
				map[string]any{
					"resource": "configmaps",
					"verbs":    []any{"get", "list", "watch"},
					"selector": map[string]any{"matchAll": true},
					"state":    "Accepted",
				},
			},
		},
	}}
	if _, err := tenant.Resource(apiBindingGVR).Create(ctxWithTimeout(t, 10*time.Second), binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create APIBinding: %v", err)
	}

	// Wait for Bound.
	ok := waitForCondition(t, 30*time.Second, func() (bool, string) {
		got, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 2*time.Second), "quickstart", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return phase == "Bound", "phase=" + phase
	})
	if !ok {
		t.Fatal("APIBinding never reached Bound")
	}

	// CR must now be creatable in the tenant workspace.
	greetingGVR := schema.GroupVersionResource{
		Group: "quickstart.providers.kedge.faros.sh", Version: "v1alpha1", Resource: "greetings",
	}
	g := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "quickstart.providers.kedge.faros.sh/v1alpha1",
		"kind":       "Greeting",
		"metadata":   map[string]any{"name": "e2e-hello", "namespace": "default"},
		"spec":       map[string]any{"message": "hello from e2e"},
	}}
	if _, err := tenant.Resource(greetingGVR).Namespace("default").Create(ctxWithTimeout(t, 10*time.Second), g, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Greeting: %v", err)
	}
	t.Cleanup(func() {
		_ = tenant.Resource(greetingGVR).Namespace("default").Delete(context.Background(), "e2e-hello", metav1.DeleteOptions{})
	})

	got, err := tenant.Resource(greetingGVR).Namespace("default").Get(ctxWithTimeout(t, 5*time.Second), "e2e-hello", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read Greeting back: %v", err)
	}
	if msg, _, _ := unstructured.NestedString(got.Object, "spec", "message"); msg != "hello from e2e" {
		t.Errorf("spec.message = %q", msg)
	}
}

func TestGTenantDisableRemovesCR(t *testing.T) {
	tenantWS := loginStaticTokenAndGetCluster(t)
	tenant := kcpDynamic(t, tenantWS, staticToken)

	apiBindingGVR := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
	greetingGVR := schema.GroupVersionResource{
		Group: "quickstart.providers.kedge.faros.sh", Version: "v1alpha1", Resource: "greetings",
	}

	// Sanity: binding exists from the previous test.
	if _, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 5*time.Second), "quickstart", metav1.GetOptions{}); err != nil {
		t.Skipf("no quickstart APIBinding to disable (skipping): %v", err)
	}
	if err := tenant.Resource(apiBindingGVR).Delete(ctxWithTimeout(t, 5*time.Second), "quickstart", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete APIBinding: %v", err)
	}

	// After delete, the CR group should disappear from the tenant workspace.
	ok := waitForCondition(t, 30*time.Second, func() (bool, string) {
		_, err := tenant.Resource(greetingGVR).Namespace("default").List(ctxWithTimeout(t, 2*time.Second), metav1.ListOptions{})
		if err == nil {
			return false, "Greeting list still succeeds"
		}
		// Either a NotFound from the missing resource or a "no matches" discovery error is acceptable.
		msg := err.Error()
		return strings.Contains(msg, "could not find the requested resource") ||
			strings.Contains(msg, "no matches for kind") ||
			apierrors.IsNotFound(err), msg
	})
	if !ok {
		t.Fatal("Greeting CR still discoverable after disable")
	}
}

func TestHHeartbeatEndpoint(t *testing.T) {
	// Known name → 200.
	req, _ := http.NewRequest(http.MethodPost, hubURL+"/api/providers/quickstart/heartbeat",
		strings.NewReader(`{"version":"0.1.0","status":"healthy"}`))
	req.Header.Set("Authorization", "Bearer "+staticToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("heartbeat POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Unknown name → 404.
	req2, _ := http.NewRequest(http.MethodPost, hubURL+"/api/providers/nope/heartbeat", strings.NewReader("{}"))
	req2.Header.Set("Authorization", "Bearer "+staticToken)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("heartbeat unknown POST: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != 404 {
		t.Fatalf("expected 404 for unknown provider, got %d", resp2.StatusCode)
	}
}

// httpGetJSON GETs url with bearer auth and decodes the JSON body.
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

// loginStaticTokenAndGetCluster calls /auth/token-login with the static
// token and extracts the tenant workspace's logical cluster name from the
// returned kubeconfig.
func loginStaticTokenAndGetCluster(t *testing.T) string {
	t.Helper()
	// The hub reports /readyz before the users APIBinding in
	// root:kedge:users is fully usable, so the first logins after startup
	// can 500 with "failed to create user" (the handler's user list hits
	// "the server could not find the requested resource"). Retry until the
	// binding settles rather than failing the suite on the race.
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

// waitForCondition polls every second until cond() returns true or the
// deadline expires. The string returned by cond() is logged on each tick
// so the failure mode is observable.
func waitForCondition(t *testing.T, timeout time.Duration, cond func() (bool, string)) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastMsg string
	for time.Now().Before(deadline) {
		ok, msg := cond()
		if ok {
			return true
		}
		lastMsg = msg
		time.Sleep(time.Second)
	}
	t.Logf("wait timeout after %s; last status: %s", timeout, lastMsg)
	return false
}
