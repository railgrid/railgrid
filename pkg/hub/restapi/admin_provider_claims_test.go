/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package restapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"

	hubproviders "github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// newAdminTestServer mounts only RegisterAdmin, with no middleware: in
// production the /api/admin subrouter is already gated by pkg/hub/admin's
// Middleware, so reaching the handler IS the authorization. Modelling that as
// "no gate in the test router" keeps the test about the migration rather than
// about a gate this package does not own.
func newAdminTestServer(t *testing.T, mgr *Manager) *httptest.Server {
	t.Helper()
	r := mux.NewRouter()
	NewHandler(mgr).RegisterAdmin(r.PathPrefix("/api/admin").Subrouter())
	return httptest.NewServer(r)
}

// agentsProvider is the registry record the migration reads: the claim set
// that motivated the endpoint (agents adding tokenreviews/subjectaccessreviews
// for its service-to-service path), plus one claim that is NOT tenant-scoped
// so the filter has something to drop.
func agentsProvider() hubproviders.Provider {
	return hubproviders.Provider{
		Name:          "agents",
		APIExportPath: kcppaths.ProviderPath("agents"),
		APIExportName: "agents",
		PermissionClaims: []hubproviders.PermissionClaim{
			{Resource: "secrets", Verbs: []string{"get", "list", "watch", "create", "update", "delete"}, TenantScoped: true},
			{Group: "authentication.k8s.io", Resource: "tokenreviews", Verbs: []string{"create"}, TenantScoped: true},
			{Group: "authorization.k8s.io", Resource: "subjectaccessreviews", Verbs: []string{"create"}, TenantScoped: true},
			{Group: "apis.kcp.io", Resource: "apiexports", Verbs: []string{"get"}},
		},
	}
}

func postReaccept(t *testing.T, srv *httptest.Server, name string) (int, ReacceptClaimsResponse) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/admin/providers/"+name+"/claims/reaccept", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out ReacceptClaimsResponse
	// A non-2xx body is the error envelope, not this shape; decoding failure
	// there is expected and the caller asserts on the status.
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// seedEnabled records an `agents` binding in each of the given workspaces,
// which is what the fake's fleet walk reads.
func seedEnabled(ops *fakeOps, keys ...wsKey) {
	for _, key := range keys {
		if ops.providerBindings[key] == nil {
			ops.providerBindings[key] = map[string]string{}
		}
		ops.providerBindings[key]["agents"] = "agents"
	}
}

func TestReacceptProviderClaims_MigratesEveryBindingThenIsIdempotent(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	seedEnabled(ops, wsKey{"org-a", "ws-1"}, wsKey{"org-a", "ws-2"}, wsKey{"org-b", "ws-1"})
	reg := hubproviders.NewRegistry()
	reg.Upsert(agentsProvider())
	mgr.WithProviderRegistry(reg)
	srv := newAdminTestServer(t, mgr)
	defer srv.Close()

	status, got := postReaccept(t, srv, "agents")
	if status != http.StatusOK {
		t.Fatalf("status: got %d, want 200", status)
	}
	if got.Updated != 3 || got.Unchanged != 0 || len(got.Failed) != 0 {
		t.Fatalf("counts: got updated=%d unchanged=%d failed=%v, want 3/0/none", got.Updated, got.Unchanged, got.Failed)
	}
	// Only the tenant-scoped claims are applied: a claim a tenant's binding
	// does not grant has no business being written onto it.
	wantClaims := []ReacceptedClaim{
		{Resource: "secrets", Verbs: []string{"get", "list", "watch", "create", "update", "delete"}},
		{Group: "authentication.k8s.io", Resource: "tokenreviews", Verbs: []string{"create"}},
		{Group: "authorization.k8s.io", Resource: "subjectaccessreviews", Verbs: []string{"create"}},
	}
	if fmt.Sprint(got.Claims) != fmt.Sprint(wantClaims) {
		t.Fatalf("claims: got %v, want %v", got.Claims, wantClaims)
	}

	// Re-running is the retry, so a fleet that is already correct must report
	// no writes rather than churning every tenant's binding.
	status, again := postReaccept(t, srv, "agents")
	if status != http.StatusOK {
		t.Fatalf("second status: got %d, want 200", status)
	}
	if again.Updated != 0 || again.Unchanged != 3 {
		t.Fatalf("second run: got updated=%d unchanged=%d, want 0/3", again.Updated, again.Unchanged)
	}
	if n := ops.reacceptCalls[wsKey{"org-a", "ws-1"}]; n != 2 {
		t.Fatalf("re-accept calls for org-a/ws-1 = %d, want 2", n)
	}
}

// One unreachable workspace must not hide the rest of the fleet: the operator
// needs the counts for everything that worked plus coordinates for what did not.
func TestReacceptProviderClaims_IsolatesPerBindingFailures(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	seedEnabled(ops, wsKey{"org-a", "ws-1"}, wsKey{"org-a", "ws-2"})
	ops.reacceptErr[wsKey{"org-a", "ws-1"}] = fmt.Errorf("binding is terminating")
	reg := hubproviders.NewRegistry()
	reg.Upsert(agentsProvider())
	mgr.WithProviderRegistry(reg)
	srv := newAdminTestServer(t, mgr)
	defer srv.Close()

	status, got := postReaccept(t, srv, "agents")
	if status != http.StatusOK {
		t.Fatalf("status: got %d, want 200", status)
	}
	if got.Updated != 1 || got.Unchanged != 0 {
		t.Fatalf("counts: got updated=%d unchanged=%d, want 1/0", got.Updated, got.Unchanged)
	}
	if len(got.Failed) != 1 {
		t.Fatalf("failed: got %v, want one entry", got.Failed)
	}
	f := got.Failed[0]
	if f.Org != "org-a" || f.Workspace != "ws-1" || f.Binding != "agents" || f.Error == "" {
		t.Fatalf("failure entry: got %+v, want org-a/ws-1/agents with a reason", f)
	}
	if n := ops.reacceptCalls[wsKey{"org-a", "ws-2"}]; n != 1 {
		t.Fatalf("healthy workspace not migrated: calls = %d, want 1", n)
	}
}

// A provider the hub has not observed a CatalogEntry for would produce an
// empty claim set, and writing that would STRIP every tenant's grants. Refuse
// before touching anything.
func TestReacceptProviderClaims_RefusesToClearClaims(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	seedEnabled(ops, wsKey{"org-a", "ws-1"})
	reg := hubproviders.NewRegistry()
	prov := agentsProvider()
	prov.PermissionClaims = []hubproviders.PermissionClaim{
		{Group: "apis.kcp.io", Resource: "apiexports", Verbs: []string{"get"}}, // not tenant-scoped
	}
	reg.Upsert(prov)
	mgr.WithProviderRegistry(reg)
	srv := newAdminTestServer(t, mgr)
	defer srv.Close()

	status, _ := postReaccept(t, srv, "agents")
	if status != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", status)
	}
	if n := ops.reacceptCalls[wsKey{"org-a", "ws-1"}]; n != 0 {
		t.Fatalf("bindings rewritten despite refusal: calls = %d", n)
	}
}

func TestReacceptProviderClaims_RejectsUnknownAndUnboundProviders(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	reg := hubproviders.NewRegistry()
	reg.Upsert(hubproviders.Provider{Name: "kubernetes-edges"}) // built-in: no APIExport
	mgr.WithProviderRegistry(reg)
	srv := newAdminTestServer(t, mgr)
	defer srv.Close()

	if status, _ := postReaccept(t, srv, "nope"); status != http.StatusNotFound {
		t.Errorf("unknown provider: got %d, want 404", status)
	}
	if status, _ := postReaccept(t, srv, "kubernetes-edges"); status != http.StatusBadRequest {
		t.Errorf("provider without APIExport: got %d, want 400", status)
	}
}

func TestReacceptProviderClaims_NoRegistryWired(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	srv := newAdminTestServer(t, mgr)
	defer srv.Close()

	if status, _ := postReaccept(t, srv, "agents"); status != http.StatusNotImplemented {
		t.Errorf("status: got %d, want 501", status)
	}
}

// A fleet walk that fails outright is a 500, not a partial success reported as
// "nothing to do" — the difference matters when the caller is about to roll
// out code that depends on the new claim.
func TestReacceptProviderClaims_ListFailureIs500(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	ops.listForExportErr = fmt.Errorf("kcp unavailable")
	reg := hubproviders.NewRegistry()
	reg.Upsert(agentsProvider())
	mgr.WithProviderRegistry(reg)
	srv := newAdminTestServer(t, mgr)
	defer srv.Close()

	if status, _ := postReaccept(t, srv, "agents"); status != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", status)
	}
}

// The fleet walk matches on the EXPORT, so an Org self-hosting a provider of
// the same name keeps its own claim set: a platform migration must not rewrite
// bindings made against somebody else's CatalogEntry.
func TestReacceptProviderClaims_SkipsOtherExports(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	key := wsKey{"org-a", "ws-1"}
	ops.providerBindings[key] = map[string]string{"code": "code"}
	reg := hubproviders.NewRegistry()
	reg.Upsert(agentsProvider())
	mgr.WithProviderRegistry(reg)
	srv := newAdminTestServer(t, mgr)
	defer srv.Close()

	status, got := postReaccept(t, srv, "agents")
	if status != http.StatusOK {
		t.Fatalf("status: got %d, want 200", status)
	}
	if got.Updated != 0 || got.Unchanged != 0 || len(got.Failed) != 0 {
		t.Fatalf("counts: got updated=%d unchanged=%d failed=%v, want an empty run", got.Updated, got.Unchanged, got.Failed)
	}
	if n := ops.reacceptCalls[key]; n != 0 {
		t.Fatalf("unrelated binding touched: calls = %d", n)
	}
}
