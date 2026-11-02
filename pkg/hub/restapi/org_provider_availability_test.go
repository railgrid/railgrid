// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package restapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

func TestOrgProviderAvailabilityDeniesAuthorityButPreservesReadsAndCleanup(t *testing.T) {
	for _, gate := range []string{"unconfigured", "not ready"} {
		for _, operation := range []struct {
			method, suffix string
			status         int
		}{
			{http.MethodPost, "", http.StatusServiceUnavailable},
			{http.MethodGet, "/vault/kubeconfig", http.StatusServiceUnavailable},
			{http.MethodPost, "/vault/credentials/rotate", http.StatusServiceUnavailable},
			{http.MethodGet, "", http.StatusOK},
			{http.MethodDelete, "/vault", http.StatusNoContent},
		} {
			t.Run(gate+"/"+operation.method+operation.suffix, func(t *testing.T) {
				mgr, ops, _ := newTestManager(t)
				if err := ops.EnsureOrgMembership(t.Context(), "org-a", "alice", "admin"); err != nil {
					t.Fatal(err)
				}
				orgOps := &fakeOrgProviderOps{workspaces: []kcp.OrgProviderWorkspace{{Name: "vault", Cluster: "vault-cluster"}}}
				creds := &fakeCredMinter{}
				mgr.WithOrgProviders(orgOps, creds)
				mgr.WithOrgProviderLifecycle(nil, func(context.Context, string, string, string) error { return nil })
				checks := 0
				if gate == "not ready" {
					mgr.WithOrgProviderAvailability(func(context.Context) error { checks++; return fmt.Errorf("native policy is not ready") })
				}
				server := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
				defer server.Close()
				req, err := http.NewRequest(operation.method, server.URL+"/api/orgs/org-a/providers"+operation.suffix, strings.NewReader(`{"name":"vault","edge":{"workspace":"ws-1","name":"prod"}}`))
				if err != nil {
					t.Fatal(err)
				}
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = response.Body.Close() }()
				if response.StatusCode != operation.status {
					t.Fatalf("status=%d want %d", response.StatusCode, operation.status)
				}
				if len(orgOps.registered) != 0 || len(orgOps.recordedEdges) != 0 || creds.ensured != 0 || creds.minted != 0 || len(creds.rotated) != 0 {
					t.Fatalf("unavailable containment allowed authority: registrations=%v edges=%v creds=%+v", orgOps.registered, orgOps.recordedEdges, creds)
				}
				wantChecks := 0
				if gate == "not ready" && (operation.status == http.StatusServiceUnavailable || (operation.method == http.MethodGet && operation.suffix == "")) {
					wantChecks = 1
				}
				if checks != wantChecks {
					t.Fatalf("availability checks=%d want %d", checks, wantChecks)
				}
				if operation.method == http.MethodDelete && len(orgOps.deleted) != 1 {
					t.Fatal("cleanup did not delete the provider")
				}
			})
		}
	}
}

func TestOrgProviderListPreservesItemsAndReportsGenericEnrollmentStatus(t *testing.T) {
	for _, gate := range []string{"unconfigured", "unavailable", "available"} {
		t.Run(gate, func(t *testing.T) {
			mgr, _, _ := newTestManager(t)
			mgr.WithOrgProviders(&fakeOrgProviderOps{
				workspaces: []kcp.OrgProviderWorkspace{{Name: "vault", Cluster: "vault-cluster"}},
			}, &fakeCredMinter{})
			if gate != "unconfigured" {
				mgr.WithOrgProviderAvailability(func(context.Context) error {
					if gate == "unavailable" {
						return fmt.Errorf("sensitive upstream diagnostic")
					}
					return nil
				})
			}
			server := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
			defer server.Close()
			response, err := http.Get(server.URL + "/api/orgs/org-a/providers")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d want 200", response.StatusCode)
			}
			var got ListOrgProvidersResponse
			if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if len(got.Items) != 1 || got.Items[0].Name != "vault" {
				t.Fatalf("existing providers missing from read response: %+v", got.Items)
			}
			wantAvailable := gate == "available"
			if got.Enrollment.Available != wantAvailable {
				t.Fatalf("enrollment.available=%v want %v", got.Enrollment.Available, wantAvailable)
			}
			if wantAvailable {
				if got.Enrollment.Reason != "" {
					t.Fatalf("available response has reason %q", got.Enrollment.Reason)
				}
			} else if got.Enrollment.Reason != orgProviderEnrollmentUnavailableReason || strings.Contains(got.Enrollment.Reason, "sensitive") {
				t.Fatalf("unavailable response leaked or changed generic reason: %q", got.Enrollment.Reason)
			}
		})
	}
}

func TestOrgProviderAvailabilityDoesNotMaskAuthorization(t *testing.T) {
	for _, caller := range []string{"unauthenticated", "member"} {
		t.Run(caller, func(t *testing.T) {
			mgr, _, _ := newTestManager(t)
			mgr.WithOrgProviders(&fakeOrgProviderOps{}, &fakeCredMinter{}).
				WithOrgProviderAvailability(func(context.Context) error {
					t.Error("availability checked before authorization")
					return fmt.Errorf("unavailable")
				})
			if caller == "unauthenticated" {
				// The shared test server always installs a TenantContext, so
				// exercise a genuinely unscoped request directly here.
				response := httptest.NewRecorder()
				NewHandler(mgr).registerOrgProvider(response, httptest.NewRequest(http.MethodPost, "/api/orgs/org-a/providers", nil))
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("status=%d want 401", response.Code)
				}
				return
			}
			server := newTestServer(t, mgr, memberTC("alice", "org-a", ""))
			defer server.Close()
			status, _ := postJSON(t, server.URL+"/api/orgs/org-a/providers", RegisterOrgProviderRequest{Name: "vault"})
			if status != http.StatusForbidden {
				t.Fatalf("status=%d want 403", status)
			}
		})
	}
}

func TestOrgProviderEnableAvailabilityPrecedesGrantsAndBindings(t *testing.T) {
	for _, scenario := range []string{"unconfigured", "not ready", "platform"} {
		t.Run(scenario, func(t *testing.T) {
			mgr, ops, _ := newTestManager(t)
			provider := providers.Provider{Name: "app-studio", OrgUUID: "org-a", APIExportPath: "root:railgrid:tenants:org-a:providers:app-studio", APIExportName: "app-studio"}
			if scenario == "platform" {
				provider.OrgUUID = ""
			}
			if scenario == "not ready" {
				mgr.WithOrgProviderAvailability(func(context.Context) error { return fmt.Errorf("not ready") })
			}
			registry := providers.NewRegistry()
			registry.Upsert(provider)
			mgr.WithProviderRegistry(registry).WithHubAccessGrants(hubaccess.NewStore(mgr.client), false)
			server := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
			defer server.Close()
			status, body := postJSON(t, server.URL+enableURL, EnableProviderRequest{ConsentDigest: providers.ConsentDigest(provider)})
			if scenario == "platform" {
				if status != http.StatusOK {
					t.Fatalf("platform enable=%d %s", status, body)
				}
				return
			}
			if status != http.StatusServiceUnavailable || ops.providerBindCalls[wsKey{"org-a", "ws-1"}] != 0 {
				t.Fatalf("enable=%d %s bindings=%v", status, body, ops.providerBindCalls)
			}
			grant, err := mgr.hubAccess.Get(t.Context(), hubaccess.GrantKey{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Provider: provider.Name, ProviderOrgUUID: provider.OrgUUID})
			if err != nil || grant != nil {
				t.Fatalf("unavailable containment wrote grant: %v %v", grant, err)
			}
		})
	}
}

func TestOrgProviderConsumerPreparationPrecedesGrantsAndBindings(t *testing.T) {
	for _, scenario := range []string{"unconfigured", "unavailable", "ready", "platform", "allocated platform", "allocated platform unavailable", "allocated platform disabled"} {
		t.Run(scenario, func(t *testing.T) {
			mgr, ops, _ := newTestManager(t)
			withTestOrgProviderLifecycle(t, mgr)
			provider := providers.Provider{
				Name: "app-studio", OrgUUID: "org-a", APIExportPath: "root:railgrid:tenants:org-a:providers:app-studio", APIExportName: "app-studio",
				HubAccess: []providersv1alpha1.ProviderHubAccess{{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeOrg}},
			}
			if strings.Contains(scenario, "platform") {
				provider.OrgUUID = ""
			}
			mgr.WithOrgProviderAvailability(func(context.Context) error { return nil })
			if scenario == "allocated platform disabled" {
				mgr.orgProviderAvailability = nil
			}
			mgr.WithOrgProviderConsumerPreparationCheck(func(context.Context, string, string, providers.Provider) (bool, error) {
				return scenario != "platform", nil
			})
			checks := 0
			if scenario != "unconfigured" {
				mgr.WithOrgProviderConsumerPolicy(func(ctx context.Context, org, workspace string, actual providers.Provider) error {
					checks++
					if org != "org-a" || workspace != "ws-1" || actual.OrgUUID != provider.OrgUUID || actual.APIExportName != provider.APIExportName {
						t.Error("consumer preparation received the wrong scope or provider")
					}
					if scenario != "ready" && scenario != "allocated platform" {
						return fmt.Errorf("consumer quota unavailable")
					}
					return nil
				})
			}
			registry := providers.NewRegistry()
			registry.Upsert(provider)
			mgr.WithProviderRegistry(registry).WithHubAccessGrants(hubaccess.NewStore(mgr.client), false)
			server := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
			defer server.Close()
			status, body := postJSON(t, server.URL+enableURL, EnableProviderRequest{ConsentDigest: providers.ConsentDigest(provider), AcceptedHubAccess: []AcceptedHubAccess{{Capability: string(providersv1alpha1.HubCapabilityMembershipsRead), Scope: string(providersv1alpha1.HubAccessScopeOrg)}}})
			allowed := scenario == "ready" || scenario == "platform" || scenario == "allocated platform"
			want := http.StatusServiceUnavailable
			if allowed {
				want = http.StatusOK
			}
			if status != want {
				t.Fatalf("enable=%d want %d: %s", status, want, body)
			}
			if scenario == "platform" && checks != 0 {
				t.Fatal("platform Enable consulted org consumer policy")
			}
			if (scenario == "unavailable" || scenario == "ready" || scenario == "allocated platform" || scenario == "allocated platform unavailable") && checks != 1 {
				t.Fatalf("consumer preparation checks=%d", checks)
			}
			grant, err := mgr.hubAccess.Get(t.Context(), hubaccess.GrantKey{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Provider: provider.Name, ProviderOrgUUID: provider.OrgUUID})
			if err != nil {
				t.Fatal(err)
			}
			if !allowed && (grant != nil || ops.providerBindCalls[wsKey{"org-a", "ws-1"}] != 0) {
				t.Fatalf("refused consumer preparation wrote grant=%v or bindings=%v", grant, ops.providerBindCalls)
			}
			if allowed && (grant == nil || ops.providerBindCalls[wsKey{"org-a", "ws-1"}] != 1) {
				t.Fatalf("prepared provider missing grant or binding: grant=%v bindings=%v", grant, ops.providerBindCalls)
			}
		})
	}
}

func withTestOrgProviderLifecycle(t *testing.T, mgr *Manager) {
	t.Helper()
	mgr.WithOrgProviderLifecycle(func(ctx context.Context, org string) (func(), error) {
		if org == "" {
			t.Error("lifecycle lock missing organization scope")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("lifecycle lock has no bounded context")
		}
		return func() {}, nil
	}, func(context.Context, string, string, string) error { return nil })
}
