// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
	"k8s.io/client-go/rest"

	providersv1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

func TestProviderRelayAuthorizationRejectsForeignOrUnboundConsumers(t *testing.T) {
	for _, scenario := range []string{"valid", "foreign organization", "org prefix lookalike", "organization workspace", "providers parent", "provider child", "wrong export path", "wrong export name", "binding not bound", "wrong bound resource", "undeclared verb"} {
		t.Run(scenario, func(t *testing.T) {
			p := providers.Provider{Name: "planner", OrgUUID: "org-a", CatalogEntryCluster: "catalog-a", APIExportPath: kcppaths.OrgProviderPath("org-a", "planner"), APIExportName: "planner.example", Export: &providersv1.ProviderExport{Resources: []providersv1.ProviderExportResource{{Name: "boards", APIVersion: "planner.example/v1", Verbs: []providersv1.ProviderVerb{{Name: "run"}}}}}}
			path := kcppaths.OrgPath("org-a") + ":workspace-a"
			exportPath, exportName, phase, boundResource := p.APIExportPath, p.APIExportName, "Bound", "boards"
			route, err := dataplane.ParseSubresourcePath("/clusters/consumer/apis/planner.example/v1/boards/demo/run")
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "foreign organization":
				path = kcppaths.OrgPath("org-b") + ":workspace-b"
			case "org prefix lookalike":
				path = kcppaths.OrgPath("org-a-evil") + ":workspace-a"
			case "organization workspace":
				path = kcppaths.OrgPath("org-a")
			case "providers parent":
				path = kcppaths.OrgProvidersParent("org-a")
			case "provider child":
				path = kcppaths.OrgProviderPath("org-a", "planner")
			case "wrong export path":
				exportPath = kcppaths.OrgProviderPath("org-b", "planner")
			case "wrong export name":
				exportName = "other.example"
			case "binding not bound":
				phase = "Binding"
			case "wrong bound resource":
				boundResource = "secrets"
			case "undeclared verb":
				route.Verb = "admin"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var object any
				switch r.URL.Path {
				case "/clusters/consumer/apis/core.kcp.io/v1alpha1/logicalclusters/cluster":
					object = map[string]any{"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster", "metadata": map[string]any{"name": "cluster", "annotations": map[string]any{"kcp.io/path": path}}}
				case "/clusters/consumer/apis/apis.kcp.io/v1alpha2/apibindings":
					object = map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBindingList", "items": []any{map[string]any{"metadata": map[string]any{"name": "planner"}, "spec": map[string]any{"reference": map[string]any{"export": map[string]any{"path": exportPath, "name": exportName}}}, "status": map[string]any{"phase": phase, "boundResources": []any{map[string]any{"group": "planner.example", "resource": boundResource}}}}}}
				default:
					t.Errorf("unexpected admin request %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(object); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			err = providerRelayAuthorization(&rest.Config{Host: srv.URL + "/clusters/root"})(context.Background(), p, route)
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("authorization error=%v", err)
			}
		})
	}
}
