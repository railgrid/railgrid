// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package restapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

type lifecycleWorkspaceOps struct {
	WorkspaceOps
	check func()
}

func (o lifecycleWorkspaceOps) EnsureProviderAPIBinding(ctx context.Context, org, workspace, name, path, export string, claims []kcp.ProviderClaim) error {
	o.check()
	return o.WorkspaceOps.EnsureProviderAPIBinding(ctx, org, workspace, name, path, export, claims)
}
func (o lifecycleWorkspaceOps) DeleteProviderAPIBinding(ctx context.Context, org, workspace, name string) error {
	o.check()
	return o.WorkspaceOps.DeleteProviderAPIBinding(ctx, org, workspace, name)
}

type lifecycleOrgProviderOps struct {
	OrgProviderOps
	check func()
}

func (o lifecycleOrgProviderOps) EnsureOrgProviderWorkspace(ctx context.Context, org, name string) (string, error) {
	o.check()
	return o.OrgProviderOps.EnsureOrgProviderWorkspace(ctx, org, name)
}
func (o lifecycleOrgProviderOps) RecordProviderEdgeBinding(ctx context.Context, org, name, workspace, edge string) error {
	o.check()
	return o.OrgProviderOps.RecordProviderEdgeBinding(ctx, org, name, workspace, edge)
}
func (o lifecycleOrgProviderOps) GetOrgProviderWorkspace(ctx context.Context, org, name string) (*kcp.OrgProviderWorkspace, error) {
	o.check()
	return o.OrgProviderOps.GetOrgProviderWorkspace(ctx, org, name)
}
func (o lifecycleOrgProviderOps) DeleteOrgProviderWorkspace(ctx context.Context, org, name string) error {
	o.check()
	return o.OrgProviderOps.DeleteOrgProviderWorkspace(ctx, org, name)
}

type lifecycleCredentialMinter struct {
	ProviderCredentialMinter
	check func()
}

func (c lifecycleCredentialMinter) EnsureProviderSAAtPath(ctx context.Context, path string) error {
	c.check()
	return c.ProviderCredentialMinter.EnsureProviderSAAtPath(ctx, path)
}
func (c lifecycleCredentialMinter) MintProviderKubeconfigAtPath(ctx context.Context, path, url string) ([]byte, error) {
	c.check()
	return c.ProviderCredentialMinter.MintProviderKubeconfigAtPath(ctx, path, url)
}
func (c lifecycleCredentialMinter) RotateProviderCredentialAtPath(ctx context.Context, path, name, url string) (*providers.RotatedCredential, error) {
	c.check()
	return c.ProviderCredentialMinter.RotateProviderCredentialAtPath(ctx, path, name, url)
}

func TestOrgProviderLifecycleCoversAllAuthorityWrites(t *testing.T) {
	for _, operation := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"register", http.MethodPost, "/api/orgs/org-a/providers", `{"name":"vault","edge":{"workspace":"ws-1","name":"prod"}}`, http.StatusCreated},
		{"credential", http.MethodGet, "/api/orgs/org-a/providers/vault/kubeconfig", "", http.StatusOK},
		{"rotate", http.MethodPost, "/api/orgs/org-a/providers/vault/credentials/rotate", "", http.StatusOK},
		{"enable", http.MethodPost, "/api/orgs/org-a/workspaces/ws-1/providers/vault/enable", "", http.StatusOK},
		{"disable", http.MethodPost, "/api/orgs/org-a/workspaces/ws-1/providers/vault/disable", "", http.StatusNoContent},
		{"delete", http.MethodDelete, "/api/orgs/org-a/providers/vault", "", http.StatusNoContent},
	} {
		t.Run(operation.name, func(t *testing.T) {
			mgr, ops, _ := newTestManager(t)
			if err := ops.EnsureOrgMembership(t.Context(), "org-a", "alice", "admin"); err != nil {
				t.Fatal(err)
			}
			if err := ops.EnsureChildWorkspace(t.Context(), "org-a", "ws-1"); err != nil {
				t.Fatal(err)
			}
			locked, locks, checks := false, 0, 0
			check := func() {
				checks++
				if !locked {
					t.Error("authority operation ran outside lifecycle lock")
				}
			}
			mgr.bootstrapper = lifecycleWorkspaceOps{WorkspaceOps: ops, check: check}
			orgOps := &fakeOrgProviderOps{targets: []kcp.EdgeInstallTarget{connectedEdge("ws-1", "prod")}, workspaces: []kcp.OrgProviderWorkspace{{Name: "vault", Cluster: "vault-cluster"}}}
			mgr.WithOrgProviders(lifecycleOrgProviderOps{OrgProviderOps: orgOps, check: check}, lifecycleCredentialMinter{ProviderCredentialMinter: &fakeCredMinter{}, check: check})
			mgr.WithOrgProviderAvailability(func(context.Context) error { return nil }).WithOrgProviderConsumerPolicy(func(context.Context, string, string, providers.Provider) error { check(); return nil })
			mgr.WithOrgProviderLifecycle(func(ctx context.Context, org string) (func(), error) {
				if org != "org-a" {
					t.Error("wrong lock scope")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("missing lifecycle timeout")
				}
				if locked {
					t.Error("nested lock")
				}
				locks++
				locked = true
				return func() { locked = false }, nil
			}, func(ctx context.Context, org, name, export string) error {
				check()
				if org != "org-a" || name != "vault" || export != "" {
					t.Error("binding audit did not cover the entire provider workspace")
				}
				return nil
			})
			registry := providers.NewRegistry()
			provider := providers.Provider{Name: "vault", OrgUUID: "org-a", APIExportPath: "root:railgrid:tenants:org-a:providers:vault", APIExportName: "vault"}
			registry.Upsert(provider)
			mgr.WithProviderRegistry(registry)
			body := operation.body
			if operation.name == "enable" {
				body = `{"consentDigest":"` + providers.ConsentDigest(provider) + `"}`
			}
			server := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
			defer server.Close()
			req, err := http.NewRequest(operation.method, server.URL+operation.path, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != operation.status {
				t.Fatalf("status=%d want %d", response.StatusCode, operation.status)
			}
			if locked || locks != 1 || checks == 0 {
				t.Fatalf("lifecycle held=%t locks=%d authority checks=%d", locked, locks, checks)
			}
		})
	}
}

func TestOrgProviderFreshRegistrationAndDeletionRefuseRemainingReferences(t *testing.T) {
	for _, scenario := range []string{"fresh registration", "existing registration retry", "delete while disabled", "audit unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			mgr, ops, _ := newTestManager(t)
			_ = ops.EnsureOrgMembership(t.Context(), "org-a", "alice", "admin")
			_ = ops.EnsureChildWorkspace(t.Context(), "org-a", "ws-1")
			orgOps := &fakeOrgProviderOps{targets: []kcp.EdgeInstallTarget{connectedEdge("ws-1", "prod")}}
			if scenario == "existing registration retry" {
				orgOps.workspaces = []kcp.OrgProviderWorkspace{{Name: "vault", Cluster: "vault-cluster"}}
			}
			creds := &fakeCredMinter{}
			mgr.WithOrgProviders(orgOps, creds)
			withTestOrgProviderLifecycle(t, mgr)
			audits := 0
			mgr.orgProviderCheckDelete = func(context.Context, string, string, string) error {
				audits++
				if scenario == "audit unavailable" {
					return fmt.Errorf("inventory unavailable")
				}
				return providers.ErrOrgProviderInUse
			}
			method, path, body := http.MethodPost, "/api/orgs/org-a/providers", `{"name":"vault","edge":{"workspace":"ws-1","name":"prod"}}`
			want := http.StatusConflict
			if scenario == "delete while disabled" {
				method, path, body = http.MethodDelete, path+"/vault", ""
				mgr.orgProviderLifecycleLock = nil
			} else {
				mgr.WithOrgProviderAvailability(func(context.Context) error { return nil })
			}
			if scenario == "existing registration retry" {
				want = http.StatusCreated
			}
			if scenario == "audit unavailable" {
				want = http.StatusServiceUnavailable
			}
			server := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
			defer server.Close()
			req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != want {
				t.Fatalf("status=%d want %d", response.StatusCode, want)
			}
			if scenario == "existing registration retry" {
				if audits != 0 || creds.minted != 1 {
					t.Fatalf("retry audit=%d minted=%d", audits, creds.minted)
				}
				return
			}
			if audits != 1 || len(orgOps.registered) != 0 || len(orgOps.deleted) != 0 || creds.ensured != 0 || creds.minted != 0 {
				t.Fatalf("refused audit made changes: audits=%d orgOps=%+v creds=%+v", audits, orgOps, creds)
			}
		})
	}
}

func TestOrgProviderEnableRechecksCatalogAfterLifecycleWait(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	registry := providers.NewRegistry()
	provider := providers.Provider{Name: "app-studio", OrgUUID: "org-a", APIExportPath: "root:railgrid:tenants:org-a:providers:app-studio", APIExportName: "original"}
	registry.Upsert(provider)
	mgr.WithProviderRegistry(registry).WithOrgProviderAvailability(func(context.Context) error { return nil })
	mgr.WithOrgProviderLifecycle(func(context.Context, string) (func(), error) {
		changed := provider
		changed.APIExportName = "replacement"
		registry.Upsert(changed)
		return func() {}, nil
	}, nil)
	mgr.WithOrgProviderConsumerPolicy(func(context.Context, string, string, providers.Provider) error {
		t.Error("stale catalog reached preparation")
		return nil
	})
	server := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer server.Close()
	status, body := postJSON(t, server.URL+enableURL, EnableProviderRequest{ConsentDigest: providers.ConsentDigest(provider)})
	if status != http.StatusPreconditionFailed || ops.providerBindCalls[wsKey{"org-a", "ws-1"}] != 0 {
		t.Fatalf("status=%d body=%s bindings=%v", status, body, ops.providerBindCalls)
	}
}

func TestOrgProviderLifecycleFailureStopsNewAuthority(t *testing.T) {
	for _, scenario := range []string{"missing lock", "lock unavailable"} {
		for _, operation := range []struct{ method, suffix string }{
			{http.MethodPost, ""}, {http.MethodGet, "/vault/kubeconfig"}, {http.MethodPost, "/vault/credentials/rotate"},
		} {
			t.Run(scenario+"/"+operation.method+operation.suffix, func(t *testing.T) {
				mgr, ops, _ := newTestManager(t)
				_ = ops.EnsureOrgMembership(t.Context(), "org-a", "alice", "admin")
				_ = ops.EnsureChildWorkspace(t.Context(), "org-a", "ws-1")
				orgOps := &fakeOrgProviderOps{targets: []kcp.EdgeInstallTarget{connectedEdge("ws-1", "prod")}, workspaces: []kcp.OrgProviderWorkspace{{Name: "vault", Cluster: "vault-cluster"}}}
				creds := &fakeCredMinter{}
				mgr.WithOrgProviders(orgOps, creds).WithOrgProviderAvailability(func(context.Context) error { return nil })
				if scenario == "lock unavailable" {
					mgr.WithOrgProviderLifecycle(func(context.Context, string) (func(), error) { return nil, fmt.Errorf("busy") }, nil)
				}
				server := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
				defer server.Close()
				req, err := http.NewRequest(operation.method, server.URL+"/api/orgs/org-a/providers"+operation.suffix, strings.NewReader(`{"name":"vault","edge":{"workspace":"ws-1","name":"prod"}}`))
				if err != nil {
					t.Fatal(err)
				}
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusServiceUnavailable || len(orgOps.registered) != 0 || len(orgOps.recordedEdges) != 0 || creds.ensured != 0 || creds.minted != 0 || len(creds.rotated) != 0 {
					t.Fatalf("failed lifecycle admitted authority: status=%d orgOps=%+v creds=%+v", response.StatusCode, orgOps, creds)
				}
			})
		}
	}
}
