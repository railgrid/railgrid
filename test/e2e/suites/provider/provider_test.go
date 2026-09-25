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
const workspacePath = "root:railgrid:providers:quickstart"

var secretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// greetingGVR is the provider's one kind. Cluster-scoped — see the comment in
// providers/quickstart/apis/v1alpha1/types_greeting.go.
var greetingGVR = schema.GroupVersionResource{
	Group: "quickstart.providers.railgrid.ai", Version: "v1alpha1", Resource: "greetings",
}

var apiBindingGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}

// providersWorkspaceClient returns a dynamic client targeting
// systemProvidersClient returns a dynamic client targeting
// root:railgrid:system:providers — where Provider + CatalogEntry live since the
// provider bootstrap refactor.
func systemProvidersClient(t *testing.T) dynamic.Interface {
	return kcpDynamic(t, "root:railgrid:system:providers", adminToken)
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
// root:railgrid:providers:{name} (where the per-provider APIExport + schemas
// + RBAC live).
func providerSubClient(t *testing.T, name string) dynamic.Interface {
	return kcpDynamic(t, "root:railgrid:providers:"+name, adminToken)
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
// manifest.yaml (kind CatalogEntry) into root:railgrid:system:providers,
// mirroring `make install-provider-quickstart`. Called from TestMain. The
// hub reports /readyz before those APIs are fully servable, so creates are
// retried until the API answers.
func applyQuickstartManifests() error {
	cl, err := kcpDynamicRaw("root:railgrid:system:providers", adminToken)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	gvrByKind := map[string]schema.GroupVersionResource{
		"Provider":     {Group: "admin.railgrid.ai", Version: "v1alpha1", Resource: "providers"},
		"CatalogEntry": {Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries"},
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
				if err := unstructured.SetNestedField(obj.Object, overrideURL, "spec", "serving", "ui", "url"); err != nil {
					return fmt.Errorf("%s: override spec.serving.ui.url: %w", file, err)
				}
				if err := unstructured.SetNestedField(obj.Object, overrideURL, "spec", "serving", "backend", "url"); err != nil {
					return fmt.Errorf("%s: override spec.serving.backend.url: %w", file, err)
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
// (into root:railgrid:system:providers) and ran `quickstart-provider init`, so
// the sub-workspace artifacts here come from the Provider controller + init.
func TestACatalogProvisioning(t *testing.T) {
	// Wait for status.conditions[Ready] == True on the CatalogEntry.
	cl := systemProvidersClient(t)
	gvr := schema.GroupVersionResource{
		Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries",
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
			if strings.HasSuffix(it.GetName(), ".greetings.quickstart.providers.railgrid.ai") {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected greetings APIResourceSchema, got %d items", len(list.Items))
		}
	})

	t.Run("APIExport present with resources and permissionClaims", func(t *testing.T) {
		gvr := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
		got, err := sub.Resource(gvr).Get(ctxWithTimeout(t, 5*time.Second), "quickstart.providers.railgrid.ai", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get APIExport: %v", err)
		}
		// The export carries the one parent resource plus one custom subresource
		// entry per verb declared under spec.export.resources[].verbs, named
		// "<resource>/<verb>" in RBAC style (provider-sdk/apiexportgen). Count
		// them apart: the parent set is what apigen produced, the subresource
		// set is what the manifest declares.
		resources, _, _ := unstructured.NestedSlice(got.Object, "spec", "resources")
		var parents, subresources []string
		for _, entry := range resources {
			name, _, _ := unstructured.NestedString(entry.(map[string]any), "name")
			if strings.Contains(name, "/") {
				subresources = append(subresources, name)
			} else {
				parents = append(parents, name)
			}
		}
		if len(parents) != 1 {
			t.Fatalf("expected 1 parent resource in APIExport spec, got %d (%v)", len(parents), parents)
		}
		if len(subresources) == 0 {
			t.Fatal("expected the declared verb to appear as a custom subresource entry, got none")
		}
		for _, name := range subresources {
			if !strings.HasPrefix(name, parents[0]+"/") {
				t.Fatalf("subresource %q does not hang off the exported resource %q", name, parents[0])
			}
		}
		// Exactly one permission claim, and it is not about data: the provider
		// reconciles only the Greetings its own APIExport serves, but its
		// greet verb is a kcp custom subresource, and the proxied gate runs a
		// SubjectAccessReview through the export virtual workspace, which kcp
		// serves only for an export that claims it. manifest.yaml and the
		// chart CatalogEntry must agree (hack/verify-provider-contract.mjs).
		claims, _, _ := unstructured.NestedSlice(got.Object, "spec", "permissionClaims")
		if len(claims) != 1 {
			t.Fatalf("expected exactly the subjectaccessreviews permissionClaim, got %d: %v", len(claims), claims)
		}
		if c, _ := claims[0].(map[string]any); c["group"] != "authorization.k8s.io" || c["resource"] != "subjectaccessreviews" {
			t.Fatalf("expected the subjectaccessreviews claim, got %v", claims[0])
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
		const name = "railgrid:providers:bind:quickstart.providers.railgrid.ai"

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
		crb, err := sub.Resource(crbGVR).Get(ctxWithTimeout(t, 5*time.Second), "railgrid:providers:sa:provider", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("ClusterRoleBinding railgrid:providers:sa:provider not found: %v", err)
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
		// The DTO mirrors CatalogEntry.spec one section for one: export,
		// requires, serving, hub — with the same JSON names, so the portal and
		// `kubectl get catalogentry -o yaml` see the same shape. `requires` is
		// asserted separately below; `hub` is absent because quickstart asks
		// nothing of the hub itself.
		for _, k := range []string{"displayName", "ready", "export", "serving"} {
			if _, ok := qs[k]; !ok {
				t.Errorf("expected key %q in DTO, got: %v", k, qs)
			}
		}
		if qs["ready"] != true {
			t.Errorf("expected ready=true, got %v", qs["ready"])
		}
		if _, ok := qs["hub"]; ok {
			t.Errorf("quickstart asks nothing of the hub; DTO carries a hub section: %v", qs["hub"])
		}

		export, _ := qs["export"].(map[string]any)
		if export == nil {
			t.Fatalf("DTO carries no export section: %v", qs)
		}
		if export["name"] != "quickstart.providers.railgrid.ai" {
			t.Errorf("export.name = %v", export["name"])
		}
		if export["path"] != "root:railgrid:providers:quickstart" {
			t.Errorf("export.path = %v", export["path"])
		}
		// The greet verb hangs off the resource it is served on, and carries
		// the apiVersion/kind a consumer needs to address the coordinate
		// without knowing quickstart's group.
		resources, _ := export["resources"].([]any)
		if len(resources) != 1 {
			t.Fatalf("export.resources = %d entries, want the one greetings entry: %v", len(resources), resources)
		}
		greetings, _ := resources[0].(map[string]any)
		if greetings["name"] != "greetings" || greetings["kind"] != "Greeting" {
			t.Errorf("export.resources[0] = %v, want greetings/Greeting", greetings)
		}
		if greetings["apiVersion"] != "quickstart.providers.railgrid.ai/v1alpha1" {
			t.Errorf("export.resources[0].apiVersion = %v", greetings["apiVersion"])
		}
		verbs, _ := greetings["verbs"].([]any)
		if len(verbs) != 1 {
			t.Fatalf("greetings carries %d verbs, want the one greet verb: %v", len(verbs), verbs)
		}
		if verb, _ := verbs[0].(map[string]any); verb["name"] != "greet" || verb["readOnly"] != true {
			t.Errorf("greetings.verbs[0] = %v, want the read-only greet verb", verbs[0])
		}

		// The one requirement quickstart declares — the subjectaccessreviews
		// review API its subresource gate runs through — reaches the Enable
		// dialog. It names no provider: authorization.k8s.io is a platform
		// builtin, so it is a claim and not a dependency edge.
		requires, _ := qs["requires"].([]any)
		if len(requires) != 1 {
			t.Fatalf("quickstart declares one requirement; DTO carries %d: %v", len(requires), requires)
		}
		requirement, _ := requires[0].(map[string]any)
		if requirement["group"] != "authorization.k8s.io" {
			t.Errorf("requires[0].group = %v, want authorization.k8s.io", requirement["group"])
		}
		if p, ok := requirement["provider"]; ok && p != "" {
			t.Errorf("requires[0].provider = %v; a platform builtin is nobody's dependency", p)
		}
		required, _ := requirement["resources"].([]any)
		if len(required) != 1 {
			t.Fatalf("requires[0].resources = %d entries: %v", len(required), required)
		}
		if res, _ := required[0].(map[string]any); res["name"] != "subjectaccessreviews" {
			t.Errorf("requires[0].resources[0] = %v, want subjectaccessreviews", required[0])
		}

		// serving.ui and serving.backend are present-or-absent, as in the spec,
		// and neither publishes the declared URL: it names an in-cluster address
		// the browser cannot reach and must not learn.
		serving, _ := qs["serving"].(map[string]any)
		if serving == nil {
			t.Fatalf("DTO carries no serving section: %v", qs)
		}
		ui, hasUI := serving["ui"].(map[string]any)
		if !hasUI {
			t.Errorf("serving.ui absent; quickstart declares a micro-frontend: %v", serving)
		}
		if _, hasBackend := serving["backend"]; !hasBackend {
			t.Errorf("serving.backend absent; quickstart declares a backend: %v", serving)
		}
		if raw, _ := json.Marshal(serving); strings.Contains(string(raw), providerPort) {
			t.Errorf("serving leaks the provider's in-cluster address: %s", raw)
		}
		// Third-party provider should NOT carry a builtinRoute.
		if br, ok := ui["builtinRoute"]; ok && br != "" {
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

// TestCBackendProxy exercises the only non-verb routes the provider is allowed
// to serve (Pillar 2 class (c)), through the hub's backend proxy. /readyz is
// what the CatalogEntry's spec.serving.backend.healthPath points at, so a 200 here is
// what keeps the hub's BackendHealthy green.
func TestCBackendProxy(t *testing.T) {
	for _, path := range []string{"/healthz", "/readyz"} {
		url := hubURL + "/services/providers/quickstart" + path
		ok := waitForCondition(t, 90*time.Second, func() (bool, string) {
			code, body := httpGet(t, url, staticToken)
			return code == 200 && strings.Contains(body, `"ok"`), fmt.Sprintf("status %d body=%s", code, body)
		})
		if !ok {
			t.Errorf("%s never answered 200 ok", url)
		}
	}
}

// TestC2NoAdhocRESTSurface pins the closed route list: the demo /api/* routes
// the reference provider used to teach are gone, and a provider that grows one
// back is a deviation even if it authorizes correctly.
func TestC2NoAdhocRESTSurface(t *testing.T) {
	for _, path := range []string{"/api/hello", "/api/stream"} {
		code, body := httpGet(t, hubURL+"/services/providers/quickstart"+path, staticToken)
		// The provider falls through to its portal index for unknown paths, so
		// what must not happen is a JSON payload of its own.
		if strings.Contains(body, `"provider"`) || strings.Contains(body, "chunk 1") {
			t.Errorf("GET %s answered with an ad-hoc REST payload (status %d): %s", path, code, body)
		}
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

	if _, err := tenant.Resource(apiBindingGVR).Create(ctxWithTimeout(t, 10*time.Second), quickstartBinding(), metav1.CreateOptions{}); err != nil {
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

	// CR must now be creatable in the tenant workspace. Greeting is
	// cluster-scoped: the data-plane grammar addresses an object by name alone,
	// so a kind a verb hangs off cannot be namespaced.
	g := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "quickstart.providers.railgrid.ai/v1alpha1",
		"kind":       "Greeting",
		"metadata":   map[string]any{"name": "e2e-hello"},
		"spec":       map[string]any{"message": "hello from e2e"},
	}}
	if _, err := tenant.Resource(greetingGVR).Create(ctxWithTimeout(t, 10*time.Second), g, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Greeting: %v", err)
	}
	t.Cleanup(func() {
		_ = tenant.Resource(greetingGVR).Delete(context.Background(), "e2e-hello", metav1.DeleteOptions{})
	})

	got, err := tenant.Resource(greetingGVR).Get(ctxWithTimeout(t, 5*time.Second), "e2e-hello", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read Greeting back: %v", err)
	}
	if msg, _, _ := unstructured.NestedString(got.Object, "spec", "message"); msg != "hello from e2e" {
		t.Errorf("spec.message = %q", msg)
	}
}

// TestF2GreetVerbAcrossTwoWorkspaces is the suite's contract test for the
// provider's Pillar 1 and Pillar 2 halves at once.
//
// Two static tokens mean two users and therefore two tenant workspaces, each of
// which enables the provider independently. That is what makes the two
// assertions here meaningful:
//
//   - ONE reconciler stamps status.observedAt in BOTH workspaces. It is a
//     multicluster reconciler on the provider's APIExportEndpointSlice, so a
//     provider that quietly engaged only the first workspace fails here.
//   - Workspace A's bearer greets A's Greeting through the HUB's kcp front
//     door — hubURL + /clusters/{id}/apis/…/greetings/{name}/greet, the path
//     the portal and a hub-issued kubeconfig use — and is refused on B's. A
//     verb is a kcp custom subresource: the hub forwards /clusters/ straight
//     to kcp, kcp authorizes `create` on greetings/greet with RBAC and
//     forwards to the provider with the caller stamped, and the provider's
//     gate decides visibility with a SubjectAccessReview as that caller. A
//     holds no RBAC in B, so the refusal comes from kcp before the provider is
//     asked, and nothing about B leaks either way.
//   - The provider's own port is NOT a way in: a bearer presented there is
//     not a caller, and the request is refused with 401 rather than served
//     anonymously or as the provider.
func TestF2GreetVerbAcrossTwoWorkspaces(t *testing.T) {
	workspaceA := loginAndGetCluster(t, staticToken)
	workspaceB := loginAndGetCluster(t, secondToken)
	t.Logf("workspace A = %s, workspace B = %s", workspaceA, workspaceB)
	if workspaceA == workspaceB {
		t.Fatalf("both static tokens resolved to the same workspace %s; the isolation assertion would be vacuous", workspaceA)
	}

	enableQuickstart(t, workspaceA, staticToken)
	enableQuickstart(t, workspaceB, secondToken)

	createGreeting(t, workspaceA, staticToken, "greet-a", "Hello from A")
	createGreeting(t, workspaceB, secondToken, "greet-b", "Hello from B")

	// Pillar 1: one reconciler, every bound workspace. status.observedAt is
	// absent until a controller has actually seen the object.
	for _, ws := range []struct{ cluster, token, name string }{
		{workspaceA, staticToken, "greet-a"},
		{workspaceB, secondToken, "greet-b"},
	} {
		if !waitForCondition(t, 120*time.Second, func() (bool, string) {
			got, err := kcpDynamic(t, ws.cluster, ws.token).Resource(greetingGVR).
				Get(ctxWithTimeout(t, 5*time.Second), ws.name, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			observed, _, _ := unstructured.NestedString(got.Object, "status", "observedAt")
			ready := "unset"
			conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
			for _, c := range conds {
				if m, _ := c.(map[string]any); m["type"] == "Ready" {
					ready = fmt.Sprintf("%v/%v", m["status"], m["reason"])
				}
			}
			return observed != "" && ready == "True/GreetingReady", "observedAt=" + observed + " Ready=" + ready
		}) {
			t.Fatalf("Greeting %s in %s never got status.observedAt with Ready=True", ws.name, ws.cluster)
		}
	}

	// Pillar 2, the happy path: the caller can see the object and holds
	// `create` on greetings/greet in their own workspace, and reaches the verb
	// through the hub exactly as the portal does.
	code, body := greetViaHub(t, workspaceA, staticToken, "greet-a")
	if code != http.StatusOK {
		t.Fatalf("greet own Greeting through the hub: got %d, want 200 (body %s)", code, body)
	}
	var envelope struct {
		Result struct {
			Greeting string `json:"greeting"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("greet response is not an actionwire envelope: %v (body %s)", err, body)
	}
	if !strings.HasPrefix(envelope.Result.Greeting, "Hello from A,") {
		t.Errorf("greeting = %q, want it to start with the spec.message stored in workspace A", envelope.Result.Greeting)
	}

	// The isolation assertion: workspace A's token has no standing in
	// workspace B, so kcp refuses at its door. Whatever the status, nothing
	// about B may leak.
	code, body = greetViaHub(t, workspaceB, staticToken, "greet-b")
	if code == http.StatusOK {
		t.Fatalf("workspace A's token greeted workspace B's Greeting through the hub: got 200 (body %s)", body)
	}
	if strings.Contains(body, "Hello from B") {
		t.Errorf("cross-workspace refusal leaked workspace B's greeting: %s", body)
	}
	t.Logf("cross-workspace greet through the hub refused with %d", code)

	// The provider's own port: the same kube path, the same valid bearer, no
	// kcp in front to authenticate it and stamp a caller. The provider must
	// refuse — a bearer is not a caller on a verb, and anonymous is never a
	// fallback — and must not answer with the greeting.
	code, body = greetAtProviderPort(t, workspaceA, staticToken, "greet-a")
	if code != http.StatusUnauthorized {
		t.Fatalf("bearer straight at the provider's port: got %d, want 401 (body %s)", code, body)
	}
	if strings.Contains(body, "Hello from A") {
		t.Errorf("the provider served a verb to a bearer with no stamped caller: %s", body)
	}
}

// enableQuickstart creates the APIBinding a tenant's Enable flow would create,
// as the tenant, and waits for it to bind. Idempotent.
// TestF3GreetVerbThroughKcpSubresource drives the verb straight at kcp, with
// no hub in front: the generated APIExport declares "greetings/greet" with
// virtual storage pointing at the provider's DataPlaneEndpointSlice, so a kcp
// shard authorizes the caller, forwards the request to the provider
// (X-Remote-User stamped, bearer gone) and the provider's gate runs a
// SubjectAccessReview as that user before serving. That is the path a tenant
// hits with a plain kcp kubeconfig, and it must answer exactly what the hub's
// front door does in F2 — and refuse across workspaces at kcp's door.
func TestF3GreetVerbThroughKcpSubresource(t *testing.T) {
	workspaceA := loginAndGetCluster(t, staticToken)
	workspaceB := loginAndGetCluster(t, secondToken)

	// F2 left greet-a / greet-b behind; make the test self-sufficient anyway.
	enableQuickstart(t, workspaceA, staticToken)
	enableQuickstart(t, workspaceB, secondToken)
	createGreeting(t, workspaceA, staticToken, "greet-a", "Hello from A")
	createGreeting(t, workspaceB, secondToken, "greet-b", "Hello from B")

	code, body := greetViaKcp(t, workspaceA, staticToken, "greet-a")
	if code != http.StatusOK {
		t.Fatalf("greet own Greeting through kcp: got %d, want 200 (body %s)", code, body)
	}
	var envelope struct {
		Result struct {
			Greeting string `json:"greeting"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("greet response through kcp is not an actionwire envelope: %v (body %s)", err, body)
	}
	if !strings.HasPrefix(envelope.Result.Greeting, "Hello from A") {
		t.Errorf("greeting = %q, want it to start with the spec.message stored in workspace A", envelope.Result.Greeting)
	}

	// Workspace A's token has no standing in workspace B: kcp refuses before
	// the provider is ever asked, and nothing about B leaks either way.
	code, body = greetViaKcp(t, workspaceB, staticToken, "greet-b")
	if code == http.StatusOK {
		t.Fatalf("workspace A's token greeted workspace B's Greeting through kcp: got 200 (body %s)", body)
	}
	if strings.Contains(body, "Hello from B") {
		t.Fatalf("cross-workspace refusal leaked workspace B's greeting: %s", body)
	}
	t.Logf("cross-workspace greet through kcp refused with %d", code)
}

// TestF4CrossClaimThroughClaimerVW is the cross-provider hop: a second
// APIExport ("the claimer", standing in for another provider) claims quickstart's
// greetings AND greetings/greet, a tenant accepts both, and the claimer calls
// the verb through ITS OWN virtual workspace — never touching the tenant's
// /clusters/ path, which is where a claimer holds no RBAC. kcp-dev/kcp#4388
// (ba9e0a6a4) advertises the claimed subresource on the claimer's VW and
// forwards it to the shard, which resolves quickstart's entry and proxies to the
// provider. This test proves the whole path against our endpoint and records
// what the endpoint sees, which is the open design question: the identity is
// the CLAIMER's (here the provider ServiceAccount), not a tenant user.
func TestF4CrossClaimThroughClaimerVW(t *testing.T) {
	const (
		claimerGroup  = "claimer.example.com"
		claimerExport = "claimer.example.com"
		schemaName    = "v1.widgets.claimer.example.com"
	)
	ctx := ctxWithTimeout(t, 3*time.Minute)
	workspaceA := loginAndGetCluster(t, staticToken)
	enableQuickstart(t, workspaceA, staticToken)
	createGreeting(t, workspaceA, staticToken, "greet-a", "Hello from A")

	providerToken, err := extractToken(runtimeKubeconfig)
	if err != nil {
		t.Fatalf("provider token: %v", err)
	}
	admin := kcpDynamic(t, workspacePath, adminToken)
	asProvider := kcpDynamic(t, workspacePath, providerToken)

	// 1. A PermissionClaimPolicy lets an export of claimerGroup claim quickstart's
	//    group without an identityHash. It reserves both groups to the provider
	//    subject, so the claimer export below is created AS that subject.
	//    Written through the admin virtual workspace, the only place kcp accepts
	//    it (pkg/hub/bootstrap does the same for the platform policy).
	policyGVR := schema.GroupVersionResource{Group: "admin.kcp.io", Version: "v1alpha1", Resource: "permissionclaimpolicies"}
	adminVW, err := dynamic.NewForConfig(&rest.Config{Host: kcpServer + "/services/admin/clusters/root", BearerToken: adminToken, TLSClientConfig: rest.TLSClientConfig{Insecure: true}})
	if err != nil {
		t.Fatalf("admin VW client: %v", err)
	}
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admin.kcp.io/v1alpha1", "kind": "PermissionClaimPolicy",
		"metadata": map[string]any{"name": "e2e-claimer"},
		"spec": map[string]any{
			"claims":    []any{map[string]any{"claimer": claimerGroup, "groups": []any{greetingGVR.Group}}},
			"providers": []any{map[string]any{"kind": "User", "name": "system:serviceaccount:default:provider"}},
		},
	}}
	if _, err := adminVW.Resource(policyGVR).Create(ctx, policy, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create PermissionClaimPolicy: %v", err)
	}
	t.Cleanup(func() {
		_ = adminVW.Resource(policyGVR).Delete(context.Background(), "e2e-claimer", metav1.DeleteOptions{})
	})

	// 2. The claimer export, with one resource of its own (an export with no
	//    resources cannot hold an identity-agnostic claim) and the two claims.
	schemaGVR := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
	widgets := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1", "kind": "APIResourceSchema",
		"metadata": map[string]any{"name": schemaName},
		"spec": map[string]any{
			"group": claimerGroup, "scope": "Cluster",
			"names":    map[string]any{"plural": "widgets", "singular": "widget", "kind": "Widget", "listKind": "WidgetList"},
			"versions": []any{map[string]any{"name": "v1alpha1", "served": true, "storage": true, "schema": map[string]any{"type": "object"}}},
		},
	}}
	if _, err := admin.Resource(schemaGVR).Create(ctx, widgets, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create claimer APIResourceSchema: %v", err)
	}
	exportGVR := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	export := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIExport",
		"metadata": map[string]any{"name": claimerExport},
		"spec": map[string]any{
			"resources": []any{map[string]any{"group": claimerGroup, "name": "widgets", "schema": schemaName, "storage": map[string]any{"crd": map[string]any{}}}},
			"permissionClaims": []any{
				map[string]any{"group": greetingGVR.Group, "resource": "greetings", "verbs": []any{"get"}},
				map[string]any{"group": greetingGVR.Group, "resource": "greetings/greet", "verbs": []any{"create"}},
			},
		},
	}}
	if _, err := asProvider.Resource(exportGVR).Create(ctx, export, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create claimer APIExport (identity-agnostic claims on %s): %v", greetingGVR.Group, err)
	}
	// The bind grant every export needs before a tenant may reference it: the
	// same ClusterRole + ClusterRoleBinding quickstart's init writes for its own
	// export (TestACatalogProvisioning), for the claimer.
	crGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	crbGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
	bindRole := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": "e2e-claimer-bind"},
		"rules": []any{map[string]any{
			"apiGroups": []any{"apis.kcp.io"}, "resources": []any{"apiexports"},
			"resourceNames": []any{claimerExport}, "verbs": []any{"bind"},
		}},
	}}
	if _, err := admin.Resource(crGVR).Create(ctx, bindRole, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create claimer bind ClusterRole: %v", err)
	}
	bindGrant := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": "e2e-claimer-bind"},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "e2e-claimer-bind"},
		"subjects": []any{map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Group", "name": "system:authenticated"}},
	}}
	if _, err := admin.Resource(crbGVR).Create(ctx, bindGrant, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create claimer bind ClusterRoleBinding: %v", err)
	}

	// 3. The tenant binds the claimer and accepts both claims — the consent
	//    half; the hub's Enable flow writes exactly this.
	tenant := kcpDynamic(t, workspaceA, staticToken)
	accepted := func(resource string, verbs ...any) map[string]any {
		return map[string]any{"group": greetingGVR.Group, "resource": resource, "verbs": verbs, "selector": map[string]any{"matchAll": true}, "state": "Accepted"}
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding",
		"metadata": map[string]any{"name": "claimer"},
		"spec": map[string]any{
			"reference":        map[string]any{"export": map[string]any{"path": workspacePath, "name": claimerExport}},
			"permissionClaims": []any{accepted("greetings", "get"), accepted("greetings/greet", "create")},
		},
	}}
	if _, err := tenant.Resource(apiBindingGVR).Create(ctx, binding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("bind claimer in workspace A: %v", err)
	}
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		got, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 5*time.Second), "claimer", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
		valid := false
		for _, c := range conds {
			if m, _ := c.(map[string]any); m["type"] == "PermissionClaimsValid" && m["status"] == "True" {
				valid = true
			}
		}
		return phase == "Bound" && valid, fmt.Sprintf("phase=%s claimsValid=%v", phase, valid)
	}) {
		t.Fatal("claimer APIBinding never became Bound with valid claims")
	}

	sliceGVR := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices"}
	slice := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1", "kind": "APIExportEndpointSlice",
		"metadata": map[string]any{"name": claimerExport},
		"spec":     map[string]any{"export": map[string]any{"path": workspacePath, "name": claimerExport}},
	}}
	if _, err := admin.Resource(sliceGVR).Create(ctx, slice, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create claimer APIExportEndpointSlice: %v", err)
	}
	var vwURL string
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		got, err := admin.Resource(sliceGVR).Get(ctxWithTimeout(t, 5*time.Second), claimerExport, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		endpoints, _, _ := unstructured.NestedSlice(got.Object, "status", "endpoints")
		for _, e := range endpoints {
			if m, ok := e.(map[string]any); ok {
				if u, _ := m["url"].(string); u != "" {
					vwURL = strings.TrimRight(u, "/")
					return true, u
				}
			}
		}
		conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
		exp, err := admin.Resource(exportGVR).Get(ctxWithTimeout(t, 5*time.Second), claimerExport, metav1.GetOptions{})
		var expStatus any
		if err == nil {
			expStatus = exp.Object["status"]
		} else {
			expStatus = err.Error()
		}
		return false, fmt.Sprintf("no endpoint yet; slice conditions=%v export status=%v", conds, expStatus)
	}) {
		t.Fatal("claimer APIExportEndpointSlice never published a virtual workspace URL")
	}

	// 4. The hop itself: the claimed subresource, through the claimer's VW, as
	//    the provider ServiceAccount (the identity a real provider would use).
	//    A 404 here would be the gap the first POC had. Whatever else comes
	//    back is decided by OUR gate, and the provider log says as whom.
	call := func(token string) (int, string) {
		url := fmt.Sprintf("%s/clusters/%s/apis/%s/%s/greetings/greet-a/greet", vwURL, workspaceA, greetingGVR.Group, greetingGVR.Version)
		req, err := http.NewRequestWithContext(ctxWithTimeout(t, 30*time.Second), http.MethodPost, url, strings.NewReader(`{"input":{}}`))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := insecureClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", url, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(body))
	}
	// Control first: the claimed PARENT through the claimer's VW, as the
	// provider SA. This is ordinary claimed-resource forwarding, so it isolates
	// the subresource hop from everything the claim shares with it.
	getParent := func(token string) (int, string) {
		url := fmt.Sprintf("%s/clusters/%s/apis/%s/%s/greetings/greet-a", vwURL, workspaceA, greetingGVR.Group, greetingGVR.Version)
		req, err := http.NewRequestWithContext(ctxWithTimeout(t, 30*time.Second), http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := insecureClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(body))
	}
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		// The VW installs the claimed API asynchronously.
		code, body := getParent(providerToken)
		return code == http.StatusOK, fmt.Sprintf("%d %.300s", code, body)
	}) {
		t.Fatal("the claimed parent (greetings/greet-a) never became readable through the claimer's VW as the provider SA")
	}
	t.Logf("control: claimed parent readable through the claimer VW as the provider SA")

	// The request must reach OUR endpoint: the claimer's VW builds the API,
	// forwards to the shard, the shard resolves quickstart's greetings/greet
	// entry to its DataPlaneEndpointSlice and proxies there. The VW installs
	// the claimed API asynchronously, so poll until the provider log shows it.
	// Only log written from here on counts: earlier tests forwarded greet-a
	// too (F3, as a tenant user), and those lines must not be mistaken for
	// this hop.
	logStart := int64(0)
	if info, err := os.Stat(providerLogPath); err == nil {
		logStart = info.Size()
	}
	var code int
	var body string
	var sawLine, gateLine string
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		code, body = call(providerToken)
		logBytes, err := os.ReadFile(providerLogPath)
		if err != nil {
			return false, err.Error()
		}
		if int64(len(logBytes)) > logStart {
			logBytes = logBytes[logStart:]
		} else {
			logBytes = nil
		}
		for _, line := range strings.Split(string(logBytes), "\n") {
			if strings.Contains(line, "verb request") && strings.Contains(line, workspaceA) && strings.Contains(line, "greet-a") {
				sawLine = line
			}
			if strings.Contains(line, "greet refused") && strings.Contains(line, "greet-a") {
				gateLine = line
			}
		}
		return sawLine != "", fmt.Sprintf("%d %.200s", code, body)
	}) {
		t.Fatalf("the provider never received the forwarded request; the claimer's VW or the shard answered %d itself: %s", code, body)
	}
	t.Logf("endpoint saw: %s", sawLine)

	// WHO the endpoint sees is the claimer's identity, not a tenant user: the
	// impersonated provider ServiceAccount, with kcp's warrant forwarded in
	// X-Remote-Extra-*. This is the fact the cross-provider design turns on.
	if !strings.Contains(sawLine, `user="system:serviceaccount:default:provider"`) {
		t.Errorf("expected the endpoint to see the claimer's ServiceAccount, got: %s", sawLine)
	}
	if !strings.Contains(sawLine, "authorization.kcp.io/warrant") {
		t.Errorf("expected kcp's warrant among the forwarded extras, got: %s", sawLine)
	}

	// The gate trusts the claim for a foreign provider: the provider SA holds no
	// RBAC in the tenant workspace (a SubjectAccessReview would refuse it, and
	// kcp does not honour the forwarded warrant inside one), but kcp built this
	// subresource on the claimer's VW only because the tenant accepted the
	// claim, and authorized the call against it before forwarding. So the
	// answer is the verb's own.
	if gateLine != "" {
		t.Errorf("the gate refused a foreign provider whose claim kcp already enforced: %s", gateLine)
	}
	if code != http.StatusOK {
		t.Fatalf("as the provider SA through the claimer VW: got %d, want 200 (body %.300s)", code, body)
	}
	t.Logf("as the provider SA through the claimer VW: %d (actionwire envelope)", code)

	// Control: the same call as kcp admin, who holds RBAC everywhere — this
	// isolates routing from the gate's authorization.
	adminCode, adminBody := call(adminToken)
	t.Logf("as kcp admin through the claimer VW: %d %.200s", adminCode, adminBody)
	if adminCode != http.StatusOK {
		t.Fatalf("routing check failed: admin through the claimer VW got %d (body %s), want 200", adminCode, adminBody)
	}
}

// greetViaKcp POSTs the greet verb as a kcp custom subresource, straight at
// kcp: /clusters/{id}/apis/{group}/{version}/greetings/{name}/greet.
func greetViaKcp(t *testing.T, cluster, token, name string) (int, string) {
	t.Helper()
	url := fmt.Sprintf("%s/clusters/%s/apis/%s/%s/%s/%s/greet", kcpServer, cluster, greetingGVR.Group, greetingGVR.Version, greetingGVR.Resource, name)
	return postGreet(t, insecureClient, url, token)
}

// insecureClient talks to the embedded kcp's self-signed dev certificate.
var insecureClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // dev cert

// quickstartBinding is the APIBinding the hub's Enable flow writes for
// quickstart in a tenant workspace: the export reference plus its one
// tenantScoped claim, accepted — the review API the provider's proxied
// subresource gate runs through its export virtual workspace. Every place the
// suite binds a tenant uses this, because a binding created without the
// accepted claim makes kcp refuse that SubjectAccessReview and turns the
// shard-forwarded verb into a 500.
func quickstartBinding() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": "quickstart"},
		"spec": map[string]any{
			"reference": map[string]any{
				"export": map[string]any{
					"path": workspacePath,
					"name": "quickstart.providers.railgrid.ai",
				},
			},
			"permissionClaims": []any{map[string]any{
				"group":    "authorization.k8s.io",
				"resource": "subjectaccessreviews",
				"verbs":    []any{"create"},
				"selector": map[string]any{"matchAll": true},
				"state":    "Accepted",
			}},
		},
	}}
}

func enableQuickstart(t *testing.T, cluster, token string) {
	t.Helper()
	tenant := kcpDynamic(t, cluster, token)
	_, err := tenant.Resource(apiBindingGVR).Create(ctxWithTimeout(t, 10*time.Second), quickstartBinding(), metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create APIBinding in %s: %v", cluster, err)
	}
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		got, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 5*time.Second), "quickstart", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return phase == "Bound", "phase=" + phase
	}) {
		t.Fatalf("APIBinding in %s never reached Bound", cluster)
	}
}

// createGreeting writes a Greeting as the tenant, the way the portal's kube
// client does. Cluster-scoped: the data-plane grammar addresses by name alone.
func createGreeting(t *testing.T, cluster, token, name, message string) {
	t.Helper()
	tenant := kcpDynamic(t, cluster, token)
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "quickstart.providers.railgrid.ai/v1alpha1",
		"kind":       "Greeting",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"message": message},
	}}
	// The binding lands before the API is servable in the workspace, so retry.
	if !waitForCondition(t, 60*time.Second, func() (bool, string) {
		_, err := tenant.Resource(greetingGVR).Create(ctxWithTimeout(t, 10*time.Second), object, metav1.CreateOptions{})
		if err == nil || apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return false, err.Error()
	}) {
		t.Fatalf("create Greeting %s in %s never succeeded", name, cluster)
	}
	t.Cleanup(func() {
		_ = tenant.Resource(greetingGVR).Delete(context.Background(), name, metav1.DeleteOptions{})
	})
}

// greetViaHub POSTs the greet verb through the HUB's kcp front door,
// hubURL + /clusters/{id}/apis/{group}/{version}/greetings/{name}/greet, with
// the tenant's bearer — the URL the portal's kube client builds
// (portalkit kubeVerbPath) and the one a hub-issued kubeconfig addresses. The
// hub forwards /clusters/ to kcp untouched; everything after that is
// greetViaKcp's path.
func greetViaHub(t *testing.T, cluster, token, name string) (int, string) {
	t.Helper()
	url := fmt.Sprintf("%s/clusters/%s/apis/%s/%s/%s/%s/greet", hubURL, cluster, greetingGVR.Group, greetingGVR.Version, greetingGVR.Resource, name)
	return postGreet(t, http.DefaultClient, url, token)
}

// greetAtProviderPort POSTs the greet verb's kube path DIRECTLY at the
// provider's own port with a bearer and nothing else. It exists to prove a
// negative: with no kcp shard in front to authenticate the caller and stamp
// X-Remote-User, the provider has nobody to authorize and must refuse.
func greetAtProviderPort(t *testing.T, cluster, token, name string) (int, string) {
	t.Helper()
	url := fmt.Sprintf("%s/clusters/%s/apis/%s/%s/%s/%s/greet", providerURL, cluster, greetingGVR.Group, greetingGVR.Version, greetingGVR.Resource, name)
	return postGreet(t, http.DefaultClient, url, token)
}

func postGreet(t *testing.T, client *http.Client, url, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctxWithTimeout(t, 30*time.Second), http.MethodPost, url, strings.NewReader(`{"input":{}}`))
	if err != nil {
		t.Fatalf("build greet request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

func TestGTenantDisableRemovesCR(t *testing.T) {
	tenantWS := loginStaticTokenAndGetCluster(t)
	tenant := kcpDynamic(t, tenantWS, staticToken)

	// Sanity: binding exists from the previous test.
	if _, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 5*time.Second), "quickstart", metav1.GetOptions{}); err != nil {
		t.Skipf("no quickstart APIBinding to disable (skipping): %v", err)
	}
	if err := tenant.Resource(apiBindingGVR).Delete(ctxWithTimeout(t, 5*time.Second), "quickstart", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete APIBinding: %v", err)
	}

	// After delete, the CR group should disappear from the tenant workspace.
	ok := waitForCondition(t, 30*time.Second, func() (bool, string) {
		_, err := tenant.Resource(greetingGVR).List(ctxWithTimeout(t, 2*time.Second), metav1.ListOptions{})
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

// httpGet GETs url with bearer auth and returns the status and raw body.
func httpGet(t *testing.T, url, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
		Timeout:   10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
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

// loginStaticTokenAndGetCluster is loginAndGetCluster for the default static
// user.
func loginStaticTokenAndGetCluster(t *testing.T) string {
	t.Helper()
	return loginAndGetCluster(t, staticToken)
}

// loginAndGetCluster calls /auth/token-login with token and extracts that
// user's tenant workspace logical cluster from the returned kubeconfig. Each
// static token is its own identity with its own workspace.
func loginAndGetCluster(t *testing.T, token string) string {
	t.Helper()
	// The hub reports /readyz before the users APIBinding in
	// root:railgrid:users is fully usable, so the first logins after startup
	// can 500 with "failed to create user" (the handler's user list hits
	// "the server could not find the requested resource"). Retry until the
	// binding settles rather than failing the suite on the race.
	var (
		b    []byte
		code int
	)
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		req, _ := http.NewRequest(http.MethodPost, hubURL+"/auth/token-login", nil)
		req.Header.Set("Authorization", "Bearer "+token)
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
