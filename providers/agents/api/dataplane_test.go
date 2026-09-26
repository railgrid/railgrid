// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/tools"
)

// fakeVerbCaller stands in for *dataplane.Callers: it renders the export
// virtual-workspace URL the real one would, against a fixed endpoint.
type fakeVerbCaller struct{ endpoint string }

func (f fakeVerbCaller) ExportVerbURL(_ context.Context, gvr schema.GroupVersionResource, r dataplane.Request) (string, error) {
	return dataplane.SubresourceURL(f.endpoint, gvr.Group, gvr.Version, r)
}

func (f fakeVerbCaller) ProviderHTTPClient() (*http.Client, error) { return &http.Client{}, nil }

// Instance-backed tools (self-hosted search, a browser instance) reach the
// infrastructure provider's instances/proxy verb through THIS provider's own
// export virtual workspace, as itself. Every execution path builds its own
// taskRun, and a path that forgets the cluster degrades silently: the tool
// still loads, the model still calls it, and the user gets an error that reads
// like the instance is broken. This asserts the wiring per path instead.
func TestDataPlaneForRunPaths(t *testing.T) {
	s := &Server{verbCallers: fakeVerbCaller{endpoint: "https://hub.example.com/services/apiexport/prov/agents.railgrid.ai"}}

	t.Run("a run in a workspace is usable, whoever started it", func(t *testing.T) {
		for _, run := range []taskRun{
			{ClusterID: "23qp2e0jwjeqwp2i", Trigger: agentsv1alpha1.RunTriggerChat},
			// A background run has no user behind it and reaches the instance
			// exactly the same way: the call is made as the provider.
			{ClusterID: "23qp2e0jwjeqwp2i", Trigger: agentsv1alpha1.RunTriggerSchedule},
		} {
			dp := s.dataPlaneFor(run)
			if !dp.Available() {
				t.Fatalf("data plane unusable for %s run: %+v", run.Trigger, dp)
			}
			url, err := dp.ProxyURL(t.Context(), "mcp", "browser", "instances", "b1", "")
			if err != nil {
				t.Fatal(err)
			}
			// The path comes from dataplane.SubresourcePath through the export
			// virtual workspace — no provider name and no hub grammar spelled
			// here.
			want := "https://hub.example.com/services/apiexport/prov/agents.railgrid.ai/clusters/23qp2e0jwjeqwp2i/apis/infrastructure.railgrid.ai/v1alpha1/instances/b1/proxy"
			if url != want {
				t.Errorf("proxy URL = %q, want %q", url, want)
			}
		}
	})

	t.Run("a provider with no provider-scoped config is unusable, by design", func(t *testing.T) {
		// No kubeconfig, no export virtual workspace to call through. The tool
		// must say so rather than compose a URL that fails two hops away.
		cold := &Server{}
		dp := cold.dataPlaneFor(taskRun{ClusterID: "23qp2e0jwjeqwp2i"})
		if dp.Available() {
			t.Fatal("a provider with no verb callers must not be usable")
		}
		if _, err := dp.ProxyURL(t.Context(), "mcp", "browser", "instances", "b1", ""); err == nil {
			t.Fatal("ProxyURL must refuse without a provider client")
		}
	})

	t.Run("a run without a cluster is unusable", func(t *testing.T) {
		// The regression that motivated this test: the interactive chat path
		// once set the token but not ClusterID, so search failed in chat — the
		// one place it was supposed to work.
		if s.dataPlaneFor(taskRun{}).Available() {
			t.Fatal("a run with no cluster ID must not be usable")
		}
	})
}

// The DataPlane a run gets is the tools' contract; keep the compile-time
// assertion that the real caller factory satisfies it.
var _ tools.VerbCaller = (*dataplane.Callers)(nil)
