// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
	"k8s.io/client-go/rest"

	providersv1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

type relayAuthorizationRoundTripper func(*http.Request) (*http.Response, error)

func (f relayAuthorizationRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestOrgProviderRuntimeRequiresAuthenticatedControlTraffic(t *testing.T) {
	for _, test := range []struct {
		name, heartbeat, relay string
		embedded               bool
		available              bool
	}{
		{name: "legacy warn", heartbeat: "warn", embedded: true},
		{name: "embedded", heartbeat: "enforce", embedded: true, available: true},
		{name: "external without relay", heartbeat: "enforce"},
		{name: "external", heartbeat: "enforce", relay: "https://relay.example:9444", available: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Server{opts: &Options{EmbeddedKCP: test.embedded, ProviderHeartbeatAuth: test.heartbeat, ProviderRelayURL: test.relay}}
			if err := s.orgProviderRuntimeAvailable(context.Background()); (err == nil) != test.available {
				t.Fatalf("runtime available=%t, error=%v", test.available, err)
			}
		})
	}
}

func TestProviderRelayAuthorizationRejectsForeignOrUnboundConsumers(t *testing.T) {
	for _, scenario := range []string{"valid", "foreign organization", "org prefix lookalike", "organization workspace", "providers parent", "provider child", "wrong export path", "wrong export name", "stale export cluster", "binding not bound", "binding deleting", "binding invalid", "wrong bound resource", "undeclared verb"} {
		t.Run(scenario, func(t *testing.T) {
			p := providers.Provider{Name: "planner", OrgUUID: "org-a", CatalogEntryCluster: "catalog-a", APIExportPath: kcppaths.OrgProviderPath("org-a", "planner"), APIExportName: "planner.example", Export: &providersv1.ProviderExport{Resources: []providersv1.ProviderExportResource{{Name: "boards", APIVersion: "planner.example/v1", Verbs: []providersv1.ProviderVerb{{Name: "run"}}}}}}
			path := kcppaths.OrgPath("org-a") + ":workspace-a"
			exportPath, exportName, phase, boundResource := p.APIExportPath, p.APIExportName, "Bound", "boards"
			exportCluster := p.CatalogEntryCluster
			bindingDeleting := false
			var conditions []any
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
			case "stale export cluster":
				exportCluster = "replaced-catalog"
			case "binding not bound":
				phase = "Binding"
			case "binding deleting":
				bindingDeleting = true
			case "binding invalid":
				conditions = []any{map[string]any{"type": "APIExportValid", "status": "False"}}
			case "wrong bound resource":
				boundResource = "secrets"
			case "undeclared verb":
				route.Verb = "admin"
			}
			transport := relayAuthorizationRoundTripper(func(r *http.Request) (*http.Response, error) {
				var object any
				switch r.URL.Path {
				case "/clusters/consumer/apis/core.kcp.io/v1alpha1/logicalclusters/cluster":
					object = map[string]any{"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster", "metadata": map[string]any{"name": "cluster", "annotations": map[string]any{"kcp.io/path": path}}}
				case "/clusters/consumer/apis/apis.kcp.io/v1alpha2/apibindings":
					metadata := map[string]any{"name": "planner"}
					if bindingDeleting {
						metadata["deletionTimestamp"] = "2026-11-04T00:00:00Z"
					}
					status := map[string]any{"phase": phase, "apiExportClusterName": exportCluster, "boundResources": []any{map[string]any{"group": "planner.example", "resource": boundResource}}}
					if len(conditions) > 0 {
						status["conditions"] = conditions
					}
					object = map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBindingList", "items": []any{map[string]any{"metadata": metadata, "spec": map[string]any{"reference": map[string]any{"export": map[string]any{"path": exportPath, "name": exportName}}}, "status": status}}}
				default:
					t.Errorf("unexpected admin request %s", r.URL.Path)
					return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil)), Request: r}, nil
				}
				data, err := json.Marshal(object)
				if err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(data)), Request: r}, nil
			})
			err = providerRelayAuthorization(&rest.Config{Host: "https://kcp.invalid/clusters/root", Transport: transport})(context.Background(), p, route)
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("authorization error=%v", err)
			}
		})
	}
}
