// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package restapi

import (
	"context"
	"net/http"
	"testing"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

func TestEnableOrgProviderRequiresReviewedConsentSnapshot(t *testing.T) {
	for _, scenario := range []string{"missing", "changed verbs", "changed selector", "changed hub access", "matching", "platform legacy"} {
		t.Run(scenario, func(t *testing.T) {
			mgr, ops, _ := newTestManager(t)
			mgr.WithOrgProviderAvailability(func(context.Context) error { return nil })
			withTestOrgProviderLifecycle(t, mgr)
			mgr.WithOrgProviderConsumerPolicy(func(context.Context, string, string, providers.Provider) error {
				if scenario != "matching" {
					t.Error("consumer preparation ran before consent or for a platform provider")
				}
				return nil
			})
			p := providers.Provider{Name: "app-studio", OrgUUID: "org-a", APIExportPath: "root:railgrid:tenants:org-a:providers:app-studio", APIExportName: "app-studio", Requires: []providersv1alpha1.ProviderRequirement{{Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "configmaps", Verbs: []providersv1alpha1.ProviderRequiredVerb{"get"}}}}}}
			req := EnableProviderRequest{ConsentDigest: providers.ConsentDigest(p), AcceptedClaims: []AcceptedClaim{{Resource: "configmaps"}}}
			switch scenario {
			case "missing":
				req.ConsentDigest = ""
			case "changed verbs":
				p.Requires[0].Resources[0].Verbs = []providersv1alpha1.ProviderRequiredVerb{"get", "delete"}
			case "changed selector":
				p.Requires[0].Resources[0].Selector = &providersv1alpha1.ProviderLabelSelector{MatchLabels: map[string]string{"owner": "changed"}}
			case "changed hub access":
				p.HubAccess = []providersv1alpha1.ProviderHubAccess{{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeOrg}}
			case "platform legacy":
				p.OrgUUID, req.ConsentDigest = "", ""
			}
			registry := providers.NewRegistry()
			registry.Upsert(p)
			mgr.WithProviderRegistry(registry)
			mgr.WithHubAccessGrants(hubaccess.NewStore(mgr.client), false)
			server := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
			defer server.Close()
			status, payload := postJSON(t, server.URL+enableURL, req)
			allowed := scenario == "matching" || scenario == "platform legacy"
			if allowed {
				if status != http.StatusOK {
					t.Fatalf("enable: %d %s", status, payload)
				}
				return
			}
			if status != http.StatusPreconditionFailed {
				t.Fatalf("stale consent: %d %s", status, payload)
			}
			if ops.providerBindCalls[wsKey{"org-a", "ws-1"}] != 0 {
				t.Fatal("stale consent wrote a binding")
			}
			grant, err := mgr.hubAccess.Get(t.Context(), hubaccess.GrantKey{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Provider: p.Name, ProviderOrgUUID: p.OrgUUID})
			if err != nil || grant != nil {
				t.Fatalf("stale consent wrote grant: %v %v", grant, err)
			}
		})
	}
}
