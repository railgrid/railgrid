/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package infrastructure

// Second round of isolation cases, kept in a separate file so the
// "happy-path scoping" tests in isolation_test.go stay readable.
// These exercise: input-validation edge cases on the tenant header,
// the difference between tenant-scoped and global resources
// (templates aren't tenant-scoped — both tenants MUST see them),
// the X-Kedge-User header's role (attribution only — must NOT
// partition the listing), many-tenants stress, and a deterministic
// check that distinct workspace paths can't hash-collide into a
// shared namespace (which would silently merge two tenants' state).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestFEmptyTenantHeaderRejected — defends against a subtle
// regression: tenantFromRequest checking `header != ""` vs `len > 0`
// vs `Get(...) != nil`. An EMPTY header value should land in exactly
// the same bucket as a MISSING header: 400 TenantMissing, no fallback
// to "the empty-string tenant" (which would silently pool all
// not-quite-authenticated calls into one shared namespace).
func TestFEmptyTenantHeaderRejected(t *testing.T) {
	req, _ := http.NewRequestWithContext(ctxWithTimeout(t, 5*time.Second), http.MethodGet, providerURL+"/api/instances", nil)
	req.Header.Set("X-Kedge-Tenant", "")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty X-Kedge-Tenant must be 400 (same as missing); got %d", resp.StatusCode)
	}
}

// TestGTemplatesAreGlobalAcrossTenants is the inverse of the
// instance-isolation tests: TEMPLATES are deliberately NOT
// tenant-scoped (they're the catalog the platform offers to every
// tenant), so both tenants must see the same set. A regression that
// accidentally bucketed templates per tenant would manifest as
// "tenant A can browse the catalog but tenant B sees nothing" —
// painful to debug, easy to assert here.
func TestGTemplatesAreGlobalAcrossTenants(t *testing.T) {
	listA := listTemplates(t, tenantA)
	listB := listTemplates(t, tenantB)
	if len(listA) == 0 {
		t.Fatalf("no templates visible to tenant A — stub returned nothing?")
	}
	setA := nameSet(listA)
	setB := nameSet(listB)
	if !sameStringSet(setA, setB) {
		t.Fatalf("templates diverged across tenants — A=%v B=%v", setA, setB)
	}
}

// TestHUserHeaderDoesNotPartitionList — the X-Kedge-User header is
// attribution-only (it goes into the instance CR's metadata.labels
// for audit, NOT into the list selector). Two users in the SAME
// tenant must see EACH OTHER's instances. A regression that
// accidentally added user to the LIST selector would partition the
// view by user and break the "workspace is the security boundary"
// invariant.
func TestHUserHeaderDoesNotPartitionList(t *testing.T) {
	t.Cleanup(func() { deleteIfPresent(t, tenantA, "by-alice") })

	// alice provisions; the bucket is the tenant, the user goes on
	// the CR's labels for attribution.
	provisionAs(t, tenantA, "alice@example.com", "by-alice")

	// bob in the SAME tenant must see by-alice in his list.
	bs := listAs(t, tenantA, "bob@example.com")
	if !listResponseHasName(t, bs, "by-alice") {
		t.Fatalf("bob in tenant A could not see alice's instance — user header is wrongly partitioning the list")
	}
}

// TestIManyTenantsAreAllIsolated stress-tests N tenants creating one
// instance each with the SAME name. Every tenant's list must contain
// exactly its own instance and never any of the other (N-1). Catches
// regressions where the bucket map was keyed on something accidentally
// shared (e.g. a constant, the empty string, or a stale reference).
func TestIManyTenantsAreAllIsolated(t *testing.T) {
	const tenantCount = 8
	tenants := make([]string, tenantCount)
	for i := range tenants {
		tenants[i] = fmt.Sprintf("root:kedge:orgs:tenant-%02d:ws-%02d", i, i)
	}
	t.Cleanup(func() {
		for _, ten := range tenants {
			deleteIfPresent(t, ten, "shared-name")
		}
	})

	for _, ten := range tenants {
		provisionInstance(t, ten, "shared-name")
	}

	for i, ten := range tenants {
		names := listInstanceNames(t, ten)
		if len(names) != 1 {
			t.Fatalf("tenant %d expected exactly 1 instance; got %d: %v", i, len(names), names)
		}
		if names[0] != "shared-name" {
			t.Fatalf("tenant %d expected name shared-name; got %q", i, names[0])
		}
	}
}

// TestJConcurrentProvisionAcrossTenantsNoLeak — race condition
// guard. Two tenants provision the SAME-NAMED instance in parallel;
// each should observe exactly one instance in their own list when
// the dust settles. Run multiple iterations to make races visible.
func TestJConcurrentProvisionAcrossTenantsNoLeak(t *testing.T) {
	const iterations = 5
	for iter := range iterations {
		name := fmt.Sprintf("concurrent-%d", iter)
		t.Run(name, func(t *testing.T) {
			t.Cleanup(func() {
				deleteIfPresent(t, tenantA, name)
				deleteIfPresent(t, tenantB, name)
			})

			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				provisionInstance(t, tenantA, name)
			}()
			go func() {
				defer wg.Done()
				provisionInstance(t, tenantB, name)
			}()
			wg.Wait()

			itemsA := listInstanceNames(t, tenantA)
			itemsB := listInstanceNames(t, tenantB)
			if !containsExactlyOne(itemsA, name) {
				t.Fatalf("tenant A should see exactly one %q; got %v", name, itemsA)
			}
			if !containsExactlyOne(itemsB, name) {
				t.Fatalf("tenant B should see exactly one %q; got %v", name, itemsB)
			}
		})
	}
}

// TestKNamespaceHashesDistinctAcrossTenants — write side: provision
// in two distinct tenants, GET each instance, verify the returned
// namespace strings are DIFFERENT. The provider derives the
// namespace from sha256(tenantPath)[:6]; two distinct paths must
// never collide into the same namespace, because the SAME namespace
// is what gives the kro CRs cross-tenant visibility.
//
// Probabilistically extremely unlikely (12 hex chars = 48 bits) but
// the regression we're really guarding against is "we changed
// tenantHash() to constant" or "the same instance object got reused"
// — bugs that would show up at zero cost in this assertion.
func TestKNamespaceHashesDistinctAcrossTenants(t *testing.T) {
	t.Cleanup(func() {
		deleteIfPresent(t, tenantA, "ns-probe")
		deleteIfPresent(t, tenantB, "ns-probe")
	})
	provisionInstance(t, tenantA, "ns-probe")
	provisionInstance(t, tenantB, "ns-probe")

	respA, bsA := httpDo(t, http.MethodGet, "/api/instances/ns-probe", tenantA, nil)
	respB, bsB := httpDo(t, http.MethodGet, "/api/instances/ns-probe", tenantB, nil)
	if respA.StatusCode != http.StatusOK || respB.StatusCode != http.StatusOK {
		t.Fatalf("expected both gets to be 200; A=%d B=%d", respA.StatusCode, respB.StatusCode)
	}
	nsA := extractNamespace(t, bsA)
	nsB := extractNamespace(t, bsB)
	if nsA == "" || nsB == "" {
		t.Fatalf("missing namespace fields (A=%q B=%q)", nsA, nsB)
	}
	if nsA == nsB {
		t.Fatalf("two distinct tenants resolved to the SAME namespace %q — hash collision or tenantHash regression", nsA)
	}
}

// TestLListAfterDeleteDoesNotLeak — delete tenant A's instance, then
// confirm tenant B (who never saw it) still doesn't see it AND that
// tenant A's list is now empty. Catches a regression where DELETE
// removed the index entry but left the underlying CR in a position
// readable cross-tenant.
func TestLListAfterDeleteDoesNotLeak(t *testing.T) {
	provisionInstance(t, tenantA, "ephemeral")

	resp, bs := httpDo(t, http.MethodDelete, "/api/instances/ephemeral", tenantA, nil)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("tenant A delete of own instance: status=%d body=%s", resp.StatusCode, string(bs))
	}

	if instanceListContains(t, tenantA, "ephemeral") {
		t.Fatalf("tenant A's own list still shows deleted instance — delete didn't unregister")
	}
	if instanceListContains(t, tenantB, "ephemeral") {
		t.Fatalf("tenant B's list leaked a deleted-from-A instance — cross-tenant index pollution")
	}
}

// TestMTenantHeaderSpoofingViaUserHeader — defends against a class
// of bugs where the provider trusts whichever header has more
// information. Setting BOTH X-Kedge-Tenant and X-Kedge-User to
// look-like-tenant strings must NOT cause the listing to widen to
// include tenant B's instances. The user header is for attribution
// only — it must never be confused for a scope.
func TestMTenantHeaderSpoofingViaUserHeader(t *testing.T) {
	t.Cleanup(func() {
		deleteIfPresent(t, tenantA, "spoof-target")
		deleteIfPresent(t, tenantB, "spoof-bait")
	})
	provisionInstance(t, tenantA, "spoof-target")
	provisionInstance(t, tenantB, "spoof-bait")

	// Caller is tenant A but tries to smuggle tenant B's path
	// through X-Kedge-User. List should ONLY return spoof-target.
	bs := listAs(t, tenantA, tenantB /* attacker injects B's path as user */)
	if listResponseHasName(t, bs, "spoof-bait") {
		t.Fatalf("X-Kedge-User-as-tenant smuggling worked — list leaked tenant B's instance")
	}
	if !listResponseHasName(t, bs, "spoof-target") {
		t.Fatalf("expected tenant A's own instance in its list")
	}
}

// ===== helpers shared with isolation_test.go =====

// provisionAs is provisionInstance with an explicit X-Kedge-User on
// the request, so tests can assert per-user attribution semantics.
func provisionAs(t *testing.T, tenant, user, name string) {
	t.Helper()
	body := map[string]any{
		"templateName": stubTemplate,
		"name":         name,
		"values":       map[string]any{"name": name},
	}
	bs, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, _ := http.NewRequestWithContext(ctxWithTimeout(t, 10*time.Second), http.MethodPost, providerURL+"/api/instances", bytes.NewReader(bs))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kedge-Tenant", tenant)
	req.Header.Set("X-Kedge-User", user)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body2, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("provision %s as %s in %s: status=%d body=%s", name, user, shortTenant(tenant), resp.StatusCode, string(body2))
	}
}

// listAs returns the raw response body of GET /api/instances with
// X-Kedge-Tenant + X-Kedge-User set. Fatals on non-200 transport.
func listAs(t *testing.T, tenant, user string) []byte {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctxWithTimeout(t, 10*time.Second), http.MethodGet, providerURL+"/api/instances", nil)
	req.Header.Set("X-Kedge-Tenant", tenant)
	req.Header.Set("X-Kedge-User", user)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list as %s in %s: status=%d body=%s", user, shortTenant(tenant), resp.StatusCode, string(bs))
	}
	return bs
}

func listTemplates(t *testing.T, tenant string) []map[string]any {
	t.Helper()
	resp, bs := httpDo(t, http.MethodGet, "/api/templates", tenant, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list templates (tenant=%s): status=%d body=%s", shortTenant(tenant), resp.StatusCode, string(bs))
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(bs, &out); err != nil {
		t.Fatalf("decode templates: %v", err)
	}
	return out.Items
}

func nameSet(items []map[string]any) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, it := range items {
		if n, ok := it["name"].(string); ok {
			out[n] = struct{}{}
		}
	}
	return out
}

func sameStringSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func listInstanceNames(t *testing.T, tenant string) []string {
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
		t.Fatalf("decode list: %v", err)
	}
	names := make([]string, 0, len(out.Items))
	for _, it := range out.Items {
		names = append(names, it.Name)
	}
	return names
}

func containsExactlyOne(items []string, target string) bool {
	count := 0
	for _, it := range items {
		if it == target {
			count++
		}
	}
	return count == 1
}

func listResponseHasName(t *testing.T, bs []byte, name string) bool {
	t.Helper()
	var out struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(bs, &out); err != nil {
		t.Fatalf("decode list response: %v (body=%s)", err, string(bs))
	}
	for _, it := range out.Items {
		if it.Name == name {
			return true
		}
	}
	return false
}
