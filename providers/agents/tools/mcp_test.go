// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// fakeVerbCaller renders the export virtual-workspace URL *dataplane.Callers
// would, against a fixed endpoint, and hands out a marked HTTP client.
type fakeVerbCaller struct{ endpoint string }

func (f fakeVerbCaller) ExportVerbURL(_ context.Context, gvr schema.GroupVersionResource, r dataplane.Request) (string, error) {
	return dataplane.SubresourceURL(f.endpoint, gvr.Group, gvr.Version, r)
}

func (f fakeVerbCaller) ProviderHTTPClient() (*http.Client, error) { return &http.Client{}, nil }

const testVW = "https://hub.example.com/services/apiexport/prov/agents.railgrid.ai"

// An mcp connection naming an instance is addressed over the infrastructure
// provider's instances/proxy verb through THIS provider's export virtual
// workspace, exactly like a self-hosted search connection — the user names the
// instance, never a URL carrying a cluster ID, and no caller credential is
// involved.
func TestConnectMCPInstanceAddressing(t *testing.T) {
	dp := DataPlane{ClusterID: "23qp2e0jwjeqwp2i", Callers: fakeVerbCaller{endpoint: testVW + "/"}}

	t.Run("composes the verb root and appends nothing", func(t *testing.T) {
		// The browser template pins /mcp as the endpoint's upstreamPath, so the
		// verb root is the MCP endpoint. Appending /mcp here would double it.
		got, err := dp.ProxyURL(context.Background(), "mcp", "browser", browserResource, "browser", "")
		if err != nil {
			t.Fatal(err)
		}
		want := testVW + "/clusters/23qp2e0jwjeqwp2i/apis/infrastructure.railgrid.ai/v1alpha1/instances/browser/proxy"
		if got != want {
			t.Fatalf("endpoint = %s\nwant %s", got, want)
		}
	})

	t.Run("a provider with no provider-scoped config is told why", func(t *testing.T) {
		// The call is made as the provider; without its kubeconfig there is
		// nothing to make it with, and the message has to say so rather than
		// point at the instance.
		_, err := DataPlane{ClusterID: dp.ClusterID}.ProxyURL(context.Background(), "mcp", "browser", browserResource, "browser", "")
		if err == nil || !strings.Contains(err.Error(), "provider-scoped kubeconfig") {
			t.Fatalf("want the missing provider config named, got %v", err)
		}
	})

	t.Run("no instance and no baseURL names both options", func(t *testing.T) {
		conn := &agentsv1alpha1.Connection{}
		conn.Name = "browser"
		conn.Spec.Type = agentsv1alpha1.ConnectionTypeMCP
		_, err := ConnectMCP(context.Background(), Deps{DataPlane: dp}, conn)
		if err == nil || !strings.Contains(err.Error(), "neither an instance nor a baseURL") {
			t.Fatalf("want an error naming both options, got %v", err)
		}
	})
}
