// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
)

// An mcp connection naming an instance is addressed over the infrastructure
// provider's data plane, exactly like a self-hosted search connection — the
// user names the instance, never a URL carrying a cluster ID.
func TestConnectMCPInstanceAddressing(t *testing.T) {
	dp := DataPlane{HubBase: "https://hub.example.com/", ClusterID: "23qp2e0jwjeqwp2i", Token: "user-token"}

	t.Run("composes the verb root and appends nothing", func(t *testing.T) {
		// The browser template pins /mcp as the endpoint's upstreamPath, so the
		// verb root is the MCP endpoint. Appending /mcp here would double it.
		got, err := dp.ProxyURL("mcp", "browser", browserResource, "browser")
		if err != nil {
			t.Fatal(err)
		}
		want := "https://hub.example.com/services/providers/infrastructure/dataplane/clusters/23qp2e0jwjeqwp2i/browsers/browser/proxy"
		if got != want {
			t.Fatalf("endpoint = %s\nwant %s", got, want)
		}
	})

	t.Run("a background run is told why, not left with a 401", func(t *testing.T) {
		_, err := DataPlane{HubBase: dp.HubBase, ClusterID: dp.ClusterID}.ProxyURL("mcp", "browser", browserResource, "browser")
		if err == nil || !strings.Contains(err.Error(), "background") {
			t.Fatalf("want the background-run limitation named, got %v", err)
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
