/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/railgrid/provider-sdk/dataplane"
)

// A verb has no caller bearer: kcp authenticates the user and stamps
// requestheader identity, so the gate is a SubjectAccessReview the PROVIDER
// runs on the caller's behalf and every handler acts as the provider.
// UseProviderCallers is what gives the server that standing; without it every
// verb fails closed.
func TestUseProviderCallersMakesTheServerActAsTheProvider(t *testing.T) {
	const cluster = "aaaaaaaaaaaaaaaa"

	s := &Server{hubBase: "https://hub.example:9443"}
	if s.callers != nil || s.tenant != nil || s.tenantWorkspaces != nil {
		t.Fatal("a server with no provider credential must start with no callers, tenant client or workspace lookup")
	}
	if _, err := s.clientFor(identity{clusterID: cluster}); err == nil {
		t.Fatal("clientFor succeeded without a provider credential; verbs must fail closed")
	}

	// The config main already holds for the controllers — nothing new minted.
	// AsProvider acts through the export's virtual workspace, which it reads
	// from the APIExportEndpointSlice in the provider workspace.
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clusters/root:railgrid:providers:app-studio/apis/apis.kcp.io/v1alpha1/apiexportendpointslices/ai.railgrid.ai" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"apis.kcp.io/v1alpha1","kind":"APIExportEndpointSlice","metadata":{"name":"ai.railgrid.ai"},"status":{"endpoints":[{"url":"https://kcp.example:6443/services/apiexport/abc123/ai.railgrid.ai"}]}}`))
	}))
	defer kcp.Close()
	cfg := &rest.Config{Host: kcp.URL + "/clusters/root:railgrid:providers:app-studio", BearerToken: "provider-token"}
	callers, err := dataplane.NewCallerFactory(cfg, dataplane.WithProviderConfig(cfg, "ai.railgrid.ai"))
	if err != nil {
		t.Fatal(err)
	}
	s.UseProviderCallers(callers)

	if _, err := s.callers.AsProvider(cluster); err != nil {
		t.Fatalf("AsProvider after UseProviderCallers: %v", err)
	}
	if s.tenant == nil || s.tenantWorkspaces == nil || s.tenantProviders == nil {
		t.Fatal("UseProviderCallers must wire the tenant client, the workspace lookup and the dependency check off the same factory")
	}
	if _, err := s.clientFor(identity{clusterID: cluster}); err != nil {
		t.Fatalf("clientFor after UseProviderCallers: %v", err)
	}
	// A cross-provider verb is addressed through the export virtual workspace.
	got, err := s.callers.ExportVerbURL(t.Context(), infrastructureGroupVersion.WithResource("instances"), dataplane.Request{ClusterID: cluster, Resource: "instances", Name: "shop-dev", Verb: "log"})
	if err != nil {
		t.Fatalf("ExportVerbURL: %v", err)
	}
	if want := "https://kcp.example:6443/services/apiexport/abc123/ai.railgrid.ai/clusters/" + cluster + "/apis/infrastructure.railgrid.ai/v1alpha1/instances/shop-dev/log"; got != want {
		t.Fatalf("ExportVerbURL = %q, want %q", got, want)
	}

	// A nil factory leaves the server alone rather than dropping it.
	before := s.callers
	s.UseProviderCallers(nil)
	if s.callers != before {
		t.Error("UseProviderCallers(nil) replaced the caller factory")
	}
}
