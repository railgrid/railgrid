/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kromulticluster

// Tenant isolation guarantees the kro-multicluster provider MUST keep
// on its REST surface. Each test is asymmetric on the X-Kedge-Tenant
// header (the only piece of identity the provider trusts; the hub
// backend proxy is what populates it in production) and checks the
// negative case as well as the positive: tenant A and tenant B never
// see each other's instances, even when they share an instance NAME.
//
// We don't go through the hub here — the hub's tenant-resolver chain
// has its own e2e under test/e2e/suites/provider. This suite isolates
// the PROVIDER's scoping behavior so a regression in either layer
// surfaces in the right suite, not as a noisy joint failure.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// tenantA and tenantB are kcp workspace paths in the canonical
// root:kedge:orgs:<org>:<ws> form the hub injects. Two distinct
// orgs (not just two workspaces under the same org) so we cover
// org-level isolation as well as workspace-level.
const (
	tenantA = "root:kedge:orgs:11111111-1111-1111-1111-111111111111:aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	tenantB = "root:kedge:orgs:22222222-2222-2222-2222-222222222222:bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

// stubTemplate is one of the templates the in-memory kro stub serves.
// "app" exists across every stub fixture and has minimal required
// inputs, so it works for both create + list tests.
const stubTemplate = "app"

// httpDo is a thin client with sensible defaults — we do enough
// per-test calls that letting the default client's keepalive fight
// our cleanup leaks goroutines in -race.
func httpDo(t *testing.T, method, path, tenant string, body any) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctxWithTimeout(t, 10*time.Second), method, providerURL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tenant != "" {
		req.Header.Set("X-Kedge-Tenant", tenant)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)
	return resp, bs
}

// provisionInstance creates an instance in the given tenant. Returns
// the canonical instance name (matches what was sent). Fatals on any
// non-201 — the suite assumes provisioning works; the isolation
// behavior is what we're actually testing.
func provisionInstance(t *testing.T, tenant, name string) {
	t.Helper()
	body := map[string]any{
		"templateName": stubTemplate,
		"name":         name,
		"values":       map[string]any{"name": name},
	}
	resp, bs := httpDo(t, http.MethodPost, "/api/instances", tenant, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("provision %s in tenant %s: status=%d body=%s", name, shortTenant(tenant), resp.StatusCode, string(bs))
	}
}

// deleteIfPresent best-effort cleans up an instance — 404 is fine
// (the test may have asserted DELETE wasn't allowed and the instance
// is already gone or never existed).
func deleteIfPresent(t *testing.T, tenant, name string) {
	t.Helper()
	resp, _ := httpDo(t, http.MethodDelete, "/api/instances/"+name, tenant, nil)
	_ = resp
}

// shortTenant prints just the trailing UUID so test logs stay
// readable. We don't need the full path to identify which tenant a
// failure happened in.
func shortTenant(p string) string {
	if i := lastColon(p); i > 0 && i < len(p)-1 {
		return p[i+1:]
	}
	return p
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

// TestATenantMissingReturns400 is the baseline: without the
// X-Kedge-Tenant header, the provider MUST refuse to serve any
// tenant-scoped endpoint. Defends against a regression where header
// validation accidentally falls through to a default tenant (which
// would silently leak data across all callers).
func TestATenantMissingReturns400(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"list", http.MethodGet, "/api/instances", nil},
		{"get", http.MethodGet, "/api/instances/foo", nil},
		{"create", http.MethodPost, "/api/instances", map[string]any{"templateName": stubTemplate, "name": "foo"}},
		{"delete", http.MethodDelete, "/api/instances/foo", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, bs := httpDo(t, tc.method, tc.path, "", tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 (TenantMissing); got %d: %s", resp.StatusCode, string(bs))
			}
			if !bytesContains(bs, "X-Kedge-Tenant") {
				t.Fatalf("expected error mentioning X-Kedge-Tenant; got %s", string(bs))
			}
		})
	}
}

// TestBListIsTenantScoped — A creates "alpha", B's list never shows
// it. The reverse asserts the bucket isn't keyed on something
// accidentally shared (e.g. the empty string).
func TestBListIsTenantScoped(t *testing.T) {
	t.Cleanup(func() {
		deleteIfPresent(t, tenantA, "alpha")
		deleteIfPresent(t, tenantB, "beta")
	})

	provisionInstance(t, tenantA, "alpha")
	provisionInstance(t, tenantB, "beta")

	if !instanceListContains(t, tenantA, "alpha") {
		t.Fatalf("tenant A's list should contain alpha")
	}
	if instanceListContains(t, tenantA, "beta") {
		t.Fatalf("tenant A's list MUST NOT contain beta (cross-tenant leak)")
	}
	if !instanceListContains(t, tenantB, "beta") {
		t.Fatalf("tenant B's list should contain beta")
	}
	if instanceListContains(t, tenantB, "alpha") {
		t.Fatalf("tenant B's list MUST NOT contain alpha (cross-tenant leak)")
	}
}

// TestCGetAcrossTenantsIs404 — tenant B attempting to GET tenant A's
// instance must NOT succeed. We accept 404 (collapsed-not-found) over
// 403 because surfacing the existence of the instance is itself
// information disclosure.
func TestCGetAcrossTenantsIs404(t *testing.T) {
	t.Cleanup(func() { deleteIfPresent(t, tenantA, "gamma") })
	provisionInstance(t, tenantA, "gamma")

	// Sanity: A can GET its own.
	resp, _ := httpDo(t, http.MethodGet, "/api/instances/gamma", tenantA, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tenant A should be able to GET its own instance; got %d", resp.StatusCode)
	}

	// The negative: B sees 404.
	resp, bs := httpDo(t, http.MethodGet, "/api/instances/gamma", tenantB, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("tenant B GET of tenant A's instance must be 404; got %d: %s", resp.StatusCode, string(bs))
	}
}

// TestDDeleteAcrossTenantsIs404 — tenant B trying to delete tenant A's
// instance must fail AND tenant A's instance must still exist
// afterwards. Catches a regression where the handler validated tenant
// scope on read paths but not on the delete path.
func TestDDeleteAcrossTenantsIs404(t *testing.T) {
	t.Cleanup(func() { deleteIfPresent(t, tenantA, "delta") })
	provisionInstance(t, tenantA, "delta")

	resp, bs := httpDo(t, http.MethodDelete, "/api/instances/delta", tenantB, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("tenant B DELETE of tenant A's instance must be 404; got %d: %s", resp.StatusCode, string(bs))
	}

	// Tenant A still sees its instance after the cross-tenant
	// delete attempt.
	if !instanceListContains(t, tenantA, "delta") {
		t.Fatalf("tenant A's instance disappeared after a cross-tenant DELETE attempt — isolation broken")
	}
}

// TestESameNameAcrossTenantsDoesNotConflict — instance names are
// scoped per tenant, NOT globally. Two tenants creating "shared" must
// both succeed AND each tenant's instance is independently
// reachable. Defends against the namespace name (which is derived
// from tenant hash) accidentally being reused across tenants.
func TestESameNameAcrossTenantsDoesNotConflict(t *testing.T) {
	t.Cleanup(func() {
		deleteIfPresent(t, tenantA, "shared")
		deleteIfPresent(t, tenantB, "shared")
	})

	provisionInstance(t, tenantA, "shared")
	provisionInstance(t, tenantB, "shared") // must NOT 409

	respA, bsA := httpDo(t, http.MethodGet, "/api/instances/shared", tenantA, nil)
	respB, bsB := httpDo(t, http.MethodGet, "/api/instances/shared", tenantB, nil)
	if respA.StatusCode != http.StatusOK || respB.StatusCode != http.StatusOK {
		t.Fatalf("expected both tenants to GET their own 'shared'; got A=%d B=%d (A=%s B=%s)",
			respA.StatusCode, respB.StatusCode, string(bsA), string(bsB))
	}

	// Each tenant's instance must report ITS OWN namespace (different
	// hashes → different namespaces). Catches a regression where the
	// namespace map collapsed across tenants.
	nsA := extractNamespace(t, bsA)
	nsB := extractNamespace(t, bsB)
	if nsA == "" || nsB == "" {
		t.Fatalf("missing namespace on instance response (A=%q B=%q)", nsA, nsB)
	}
	if nsA == nsB {
		t.Fatalf("tenants share a namespace (%s) — tenantHash collision or stub leak", nsA)
	}
}

// instanceListContains issues GET /api/instances and reports whether
// `name` appears in the items array. Fatals on transport / status
// errors — those aren't isolation regressions, they're suite bugs.
func instanceListContains(t *testing.T, tenant, name string) bool {
	t.Helper()
	resp, bs := httpDo(t, http.MethodGet, "/api/instances", tenant, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list (tenant=%s): status=%d body=%s", shortTenant(tenant), resp.StatusCode, string(bs))
	}
	var out struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(bs, &out); err != nil {
		t.Fatalf("decode list: %v (body=%s)", err, string(bs))
	}
	for _, it := range out.Items {
		if it.Name == name {
			return true
		}
	}
	return false
}

// extractNamespace pulls "namespace" off an Instance JSON. Returns
// the empty string on parse failure so the caller can render a
// meaningful diagnostic.
func extractNamespace(t *testing.T, bs []byte) string {
	t.Helper()
	var inst struct {
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(bs, &inst); err != nil {
		return ""
	}
	return inst.Namespace
}

func bytesContains(haystack []byte, needle string) bool {
	return len(haystack) > 0 && bytesIndex(haystack, needle) >= 0
}

func bytesIndex(haystack []byte, needle string) int {
	n := len(needle)
	if n == 0 {
		return 0
	}
	for i := 0; i+n <= len(haystack); i++ {
		if string(haystack[i:i+n]) == needle {
			return i
		}
	}
	return -1
}

// silence "imported and not used" if a future refactor drops ctx.
var _ = context.Background
var _ = fmt.Sprintf
