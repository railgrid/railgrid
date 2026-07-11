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

package infraprovider

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

var (
	templatesGVR  = schema.GroupVersionResource{Group: "infrastructure.kedge.faros.sh", Version: "v1alpha1", Resource: "templates"}
	crdGVR        = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	apiExportGVR  = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	apiBindingGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
)

const infraAPIExportName = "infrastructure.providers.kedge.faros.sh"

// seedTemplateNames is the current seed catalog. Deliberately explicit — a
// template appearing or vanishing from the seeds should fail this suite until
// the list is updated, mirroring what tenants actually see.
var seedTemplateNames = []string{
	"application", "cron-job", "database", "redis-cache", "simple-webapp", "worker",
}

func kcpDynamic(t *testing.T, clusterPath, token string) dynamic.Interface {
	t.Helper()
	cfg := &rest.Config{
		Host:            kcpServer + "/clusters/" + clusterPath,
		BearerToken:     token,
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	}
	c, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("dynamic client for %s: %v", clusterPath, err)
	}
	return c
}

func providerWSClient(t *testing.T) dynamic.Interface {
	return kcpDynamic(t, workspacePath, adminToken)
}

// applyProviderManifests applies provider.yaml (kind Provider) + manifest.yaml
// (kind CatalogEntry) into root:kedge:system:providers, mirroring
// `make install-provider-infrastructure`. Called from TestMain.
func applyProviderManifests() error {
	cfg := &rest.Config{
		Host:            kcpServer + "/clusters/root:kedge:system:providers",
		BearerToken:     adminToken,
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	}
	cl, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	gvrByKind := map[string]schema.GroupVersionResource{
		"Provider":     {Group: "admin.kedge.faros.sh", Version: "v1alpha1", Resource: "providers"},
		"CatalogEntry": {Group: "providers.kedge.faros.sh", Version: "v1alpha1", Resource: "catalogentries"},
	}
	for _, file := range []string{"provider.yaml", "manifest.yaml"} {
		raw, err := os.ReadFile(filepath.Join(repoRoot, "providers", "infrastructure", file))
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
				// The committed manifest targets the dev-loop port (:8082);
				// this suite runs the binary on its own port.
				overrideURL := "http://localhost:" + providerPort
				if err := unstructured.SetNestedField(obj.Object, overrideURL, "spec", "ui", "url"); err != nil {
					return fmt.Errorf("%s: override spec.ui.url: %w", file, err)
				}
				if err := unstructured.SetNestedField(obj.Object, overrideURL, "spec", "backend", "url"); err != nil {
					return fmt.Errorf("%s: override spec.backend.url: %w", file, err)
				}
			}
			// The hub reports /readyz before the admin/catalog APIs in
			// root:kedge:system:providers are fully servable, so the first
			// Create on a slow runner can 404 ("could not find the requested
			// resource"). Retry until the API is up.
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

// waitWorkspace waits until the provider workspace (created by the hub's
// Provider controller) answers API requests. Called from TestMain.
func waitWorkspace(timeout time.Duration) error {
	cfg := &rest.Config{
		Host:            kcpServer + "/clusters/" + workspacePath,
		BearerToken:     adminToken,
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	}
	cl, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, lastErr = cl.Resource(nsGVR).List(ctx, metav1.ListOptions{Limit: 1})
		cancel()
		if lastErr == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("workspace %s never became reachable: %v", workspacePath, lastErr)
}

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

// TestABootstrapSeedsCatalog asserts what `init` is supposed to leave behind:
// every current seed Template present in the provider workspace, and the
// platform APIExport in place.
func TestABootstrapSeedsCatalog(t *testing.T) {
	cl := providerWSClient(t)

	ok := waitForCondition(t, 60*time.Second, func() (bool, string) {
		list, err := cl.Resource(templatesGVR).List(ctxWithTimeout(t, 5*time.Second), metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		have := map[string]bool{}
		for _, item := range list.Items {
			have[item.GetName()] = true
		}
		for _, want := range seedTemplateNames {
			if !have[want] {
				return false, fmt.Sprintf("missing seed template %q (have %v)", want, mapKeys(have))
			}
		}
		return true, ""
	})
	if !ok {
		t.Fatal("seed templates never fully appeared in the provider workspace")
	}

	if _, err := cl.Resource(apiExportGVR).Get(ctxWithTimeout(t, 5*time.Second), infraAPIExportName, metav1.GetOptions{}); err != nil {
		t.Fatalf("platform APIExport %s missing: %v", infraAPIExportName, err)
	}
}

// TestBStubTemplateFullReconcile drives the Template controller's whole
// chain end-to-end against real kcp, using the stub backend (registered
// unconditionally) so no kro runtime is needed: Ready=True, per-template CRD
// established in the workspace, APIExport.spec.resources entry added — and
// all of it torn back down on delete.
func TestBStubTemplateFullReconcile(t *testing.T) {
	cl := providerWSClient(t)
	const name = "e2e-stub-widget"

	tmpl := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
		"kind":       "Template",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"displayName": "E2E stub widget",
			"description": "suite-created template driving the controller via the stub backend",
			"version":     "0.0.1",
			"backend":     "stub",
			"instanceCRD": map[string]any{
				"group":    "infrastructure.kedge.faros.sh",
				"version":  "v1alpha1",
				"resource": "e2estubwidgets",
				"kind":     "E2EStubWidget",
			},
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
			},
		},
	}}
	if _, err := cl.Resource(templatesGVR).Create(ctxWithTimeout(t, 10*time.Second), tmpl, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create stub template: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = cl.Resource(templatesGVR).Delete(ctx, name, metav1.DeleteOptions{})
	})

	// Ready=True end to end.
	ok := waitForCondition(t, 90*time.Second, func() (bool, string) {
		got, err := cl.Resource(templatesGVR).Get(ctxWithTimeout(t, 5*time.Second), name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
		for _, c := range conds {
			m, _ := c.(map[string]any)
			if m["type"] == "Ready" {
				return m["status"] == "True", fmt.Sprintf("Ready=%v reason=%v msg=%v", m["status"], m["reason"], m["message"])
			}
		}
		return false, "no Ready condition yet"
	})
	if !ok {
		t.Fatal("stub template never reached Ready=True (is the controller manager running?)")
	}

	// Per-template CRD established in the provider workspace.
	crdName := "e2estubwidgets.infrastructure.kedge.faros.sh"
	if _, err := cl.Resource(crdGVR).Get(ctxWithTimeout(t, 5*time.Second), crdName, metav1.GetOptions{}); err != nil {
		t.Fatalf("per-template CRD %s missing: %v", crdName, err)
	}

	// APIExport.spec.resources carries the instance resource.
	if !apiExportHasResource(t, cl, "e2estubwidgets") {
		t.Fatal("APIExport.spec.resources has no e2estubwidgets entry")
	}

	// Deletion runs the finalize chain: CRD + APIExport entry removed.
	if err := cl.Resource(templatesGVR).Delete(ctxWithTimeout(t, 10*time.Second), name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete stub template: %v", err)
	}
	ok = waitForCondition(t, 60*time.Second, func() (bool, string) {
		if _, err := cl.Resource(crdGVR).Get(ctxWithTimeout(t, 5*time.Second), crdName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			return false, "per-template CRD still present"
		}
		if apiExportHasResource(t, cl, "e2estubwidgets") {
			return false, "APIExport entry still present"
		}
		return true, ""
	})
	if !ok {
		t.Fatal("finalize chain never cleaned up the CRD/APIExport entry")
	}
}

// TestCRetiredTemplateIsSwept asserts retirement enforcement end to end
// (controller/template/retired.go): a retired platform template applied into
// a live provider workspace — the exact state of a deployment seeded before
// the retirement — is deleted by the controller without operator action.
func TestCRetiredTemplateIsSwept(t *testing.T) {
	cl := providerWSClient(t)
	const name = "sandbox-runner"

	tmpl := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
		"kind":       "Template",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"displayName": "Sandbox runner (retired)",
			"description": "left-behind template a pre-retirement deployment would carry",
			"version":     "0.0.1",
			"backend":     "stub",
			"instanceCRD": map[string]any{
				"group":    "infrastructure.kedge.faros.sh",
				"version":  "v1alpha1",
				"resource": "sandboxrunners",
				"kind":     "SandboxRunner",
			},
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
			},
		},
	}}
	if _, err := cl.Resource(templatesGVR).Create(ctxWithTimeout(t, 10*time.Second), tmpl, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create retired template: %v", err)
	}

	ok := waitForCondition(t, 90*time.Second, func() (bool, string) {
		_, err := cl.Resource(templatesGVR).Get(ctxWithTimeout(t, 5*time.Second), name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, "retired template still present"
	})
	if !ok {
		t.Fatal("controller never swept the retired sandbox-runner template")
	}
}

// TestDProvidersDTO asserts the hub lists the infrastructure provider for a
// logged-in user once the CatalogEntry is reconciled.
func TestDProvidersDTO(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}
	ok := waitForCondition(t, 90*time.Second, func() (bool, string) {
		req, _ := http.NewRequest(http.MethodGet, hubURL+"/api/providers", nil)
		req.Header.Set("Authorization", "Bearer "+staticToken)
		resp, err := client.Do(req)
		if err != nil {
			return false, err.Error()
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return false, fmt.Sprintf("status %d: %s", resp.StatusCode, string(body))
		}
		if !bytes.Contains(body, []byte(`"infrastructure"`)) {
			return false, "no infrastructure entry yet: " + string(body)
		}
		return true, ""
	})
	if !ok {
		t.Fatal("/api/providers never listed the infrastructure provider")
	}
}

// TestETenantSeesTemplatesCatalog is the tenant vertical: bind the provider's
// APIExport in the static user's workspace and list Templates through the
// binding — the same read App Studio's template picker and the MCP
// list_templates tool perform.
func TestETenantSeesTemplatesCatalog(t *testing.T) {
	tenantWS := loginStaticTokenAndGetCluster(t)
	t.Logf("tenant workspace = %s", tenantWS)
	tenant := kcpDynamic(t, tenantWS, staticToken)

	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": "infrastructure"},
		"spec": map[string]any{
			"reference": map[string]any{
				"export": map[string]any{
					"path": workspacePath,
					"name": infraAPIExportName,
				},
			},
		},
	}}
	if _, err := tenant.Resource(apiBindingGVR).Create(ctxWithTimeout(t, 10*time.Second), binding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create APIBinding: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = tenant.Resource(apiBindingGVR).Delete(ctx, "infrastructure", metav1.DeleteOptions{})
	})

	ok := waitForCondition(t, 60*time.Second, func() (bool, string) {
		got, err := tenant.Resource(apiBindingGVR).Get(ctxWithTimeout(t, 5*time.Second), "infrastructure", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return phase == "Bound", "phase=" + phase
	})
	if !ok {
		t.Fatal("APIBinding never reached Bound")
	}

	// The Templates catalog must be readable through the binding.
	ok = waitForCondition(t, 60*time.Second, func() (bool, string) {
		list, err := tenant.Resource(templatesGVR).List(ctxWithTimeout(t, 5*time.Second), metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		have := map[string]bool{}
		for _, item := range list.Items {
			have[item.GetName()] = true
		}
		for _, want := range seedTemplateNames {
			if !have[want] {
				return false, fmt.Sprintf("missing %q via binding (have %v)", want, mapKeys(have))
			}
		}
		return true, ""
	})
	if !ok {
		t.Fatal("tenant never saw the seeded templates through the APIBinding")
	}
}

// --- helpers ---------------------------------------------------------------

func apiExportHasResource(t *testing.T, cl dynamic.Interface, resource string) bool {
	t.Helper()
	export, err := cl.Resource(apiExportGVR).Get(ctxWithTimeout(t, 5*time.Second), infraAPIExportName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get APIExport: %v", err)
	}
	entries, _, _ := unstructured.NestedSlice(export.Object, "spec", "resources")
	for _, e := range entries {
		m, _ := e.(map[string]any)
		if m["name"] == resource {
			return true
		}
	}
	return false
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// loginStaticTokenAndGetCluster logs the static-token user in over the hub
// REST API and returns their personal workspace cluster path — same helper
// (and same startup-race retry) as suites/provider.
func loginStaticTokenAndGetCluster(t *testing.T) string {
	t.Helper()
	var (
		b    []byte
		code int
	)
	client := &http.Client{Timeout: 10 * time.Second}
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		req, _ := http.NewRequest(http.MethodPost, hubURL+"/auth/token-login", nil)
		req.Header.Set("Authorization", "Bearer "+staticToken)
		resp, err := client.Do(req)
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
