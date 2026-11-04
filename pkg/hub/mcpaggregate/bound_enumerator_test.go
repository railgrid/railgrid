/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpaggregate

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/go-logr/logr"

	"github.com/railgrid/railgrid/pkg/hub/providers"
)

type boundTestRouter struct{}

func (boundTestRouter) OrgProviderRoute(context.Context, providers.Provider, providers.DelegatedCaller) (providers.OrgProviderRoute, error) {
	return providers.OrgProviderRoute{BaseURL: "https://edge.invalid/provider", Transport: http.DefaultTransport}, nil
}

func TestBoundRegistryEnumeratorFollowsSelectedBindingAcrossShadow(t *testing.T) {
	reg := providers.NewRegistry()
	platformURL, _ := url.Parse("https://platform.invalid")
	orgURL, _ := url.Parse("https://org.invalid")
	reg.Upsert(providers.Provider{Name: "planner", BackendURL: platformURL, EndpointsValid: true,
		APIExportPath: "root:railgrid:providers:planner", APIExportName: "planner.providers.railgrid.ai", WorkspaceCluster: "platform-export"})
	reg.Upsert(providers.Provider{Name: "planner", OrgUUID: "org-a", BackendURL: orgURL, EndpointsValid: true,
		APIExportPath: "root:railgrid:orgs:org-a:providers:planner", APIExportName: "planner.providers.railgrid.ai", WorkspaceCluster: "org-export",
		EdgeRoute: &providers.EdgeRoute{WorkspaceUUID: "ws-a", Cluster: "tenant-cluster", EdgeName: "edge", ServiceName: "provider-planner"}})
	activePath, activeIssuer := "", ""
	resolveCopies := func(_ context.Context, _ string) (map[string]providers.Provider, error) {
		p, ok := reg.BoundCopy("org-a", "planner", activePath, "planner.providers.railgrid.ai", activeIssuer)
		if !ok {
			return nil, nil
		}
		return map[string]providers.Provider{"planner": p}, nil
	}
	enumerate := BoundRegistryEnumerator(reg, boundTestRouter{}, logr.Discard(), resolveCopies)

	for _, tc := range []struct {
		name, path, issuer, wantOrg string
		wantCount                   int
	}{
		{"platform binding wins over org shadow", "root:railgrid:providers:planner", "platform-export", "", 1},
		{"org binding selects org copy", "root:railgrid:orgs:org-a:providers:planner", "org-export", "org-a", 1},
		{"foreign issuer does not fall back", "root:railgrid:providers:planner", "foreign-cluster", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			activePath, activeIssuer = tc.path, tc.issuer
			targets := enumerate(context.Background(), Caller{OrgUUID: "org-a", WorkspaceUUID: "ws-a", User: "alice"})
			if len(targets) != tc.wantCount {
				t.Fatalf("targets = %#v, want %d selected providers", targets, tc.wantCount)
			}
			if tc.wantCount == 0 {
				return
			}
			if targets[0].OrgUUID != tc.wantOrg {
				t.Fatalf("target org = %q, want %q", targets[0].OrgUUID, tc.wantOrg)
			}
		})
	}
}
