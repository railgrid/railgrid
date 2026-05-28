/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package mcp registers the kubernetes-edges provider's MCP tool
// family into the aggregator. Loaded by the parent kubernetesedges
// package via a blank import in manifest.go so that as soon as the
// provider is enabled, its kube tools appear on every MCPServer
// endpoint without further wiring.
//
// All the kubernetes-mcp-server side-effect imports (toolsets/config,
// toolsets/core, toolsets/helm, …) used to live in the now-deleted
// per-kind virtual builder; they now anchor here so the kube toolset
// registry is populated by the time the aggregator first asks for it.
package mcp

import (
	"context"
	"fmt"
	"slices"

	mcpapi "github.com/containers/kubernetes-mcp-server/pkg/api"
	mcpconfig "github.com/containers/kubernetes-mcp-server/pkg/config"
	mcpkubernetes "github.com/containers/kubernetes-mcp-server/pkg/kubernetes"
	kubemcp "github.com/containers/kubernetes-mcp-server/pkg/mcp"

	// Side-effect imports populate kubernetes-mcp-server's toolset
	// registry. Without these, upstream.Toolsets() returns nil and the
	// aggregator surfaces zero kube tools.
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/config"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/core"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/helm"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kcp"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kiali"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kubevirt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/pkg/virtual/builder"
	aggregatemcp "github.com/faroshq/faros-kedge/providers/mcp/aggregate"
)

func init() {
	aggregatemcp.RegisterToolFamily(aggregatemcp.ToolFamily{
		Name:     "kubernetes",
		EdgeType: "kubernetes",
		Register: registerKubeTools,
	})
}

// registerKubeTools wires every applicable upstream kubernetes-mcp-server
// tool onto srv. Was the body of aggregatemcp.registerKubeTools before
// the family registry split; lives here so the kube toolset choices
// (which upstream toolsets to load, ReadOnly policy, etc.) are owned
// by the kubernetes-edges provider rather than a generic aggregator.
func registerKubeTools(srv *mcp.Server, fctx aggregatemcp.FamilyContext) {
	// Build the per-request KedgeEdgeProvider (multi-edge form). When
	// EdgeNames is empty the upstream Server still initializes but its
	// tools return errors at invocation time — that's preferable to
	// silently dropping the kube toolset from tools/list.
	kubeProvider := builder.NewMultiEdgeKedgeEdgeProvider(
		fctx.Cluster, fctx.EdgeNames, fctx.Deps.EdgeConnManager, fctx.HubBaseURL, fctx.BearerToken,
	)

	staticCfg := mcpconfig.Default()
	staticCfg.Stateless = true
	staticCfg.ReadOnly = fctx.ReadOnly
	if len(fctx.Toolsets) > 0 {
		staticCfg.Toolsets = fctx.Toolsets
	}
	upstreamCfg := kubemcp.Configuration{StaticConfig: staticCfg}

	// We build the upstream Server purely as a tool factory —
	// ServerToolToGoSdkTool needs a *Server reference for its handler
	// closure. The upstream Server's own toolset-registration on its
	// internal mcp.Server is harmless and unused.
	kubeSrv, err := kubemcp.NewServer(upstreamCfg, kubeProvider)
	if err != nil {
		// Surface as a single error-returning tool so tools/list still
		// renders other families + list_targets instead of failing the
		// whole aggregate server.
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "kube_tools_unavailable",
			Description: "Kubernetes toolset failed to initialize. See server logs.",
		}, func(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			return nil, nil, fmt.Errorf("kubernetes-mcp-server initialization failed: %w", err)
		})
		return
	}

	for _, toolset := range upstreamCfg.Toolsets() {
		if toolset == nil {
			continue
		}
		for _, tool := range toolset.GetTools(kubeProvider) {
			if !isKubeToolApplicable(staticCfg, tool) {
				continue
			}
			gsTool, gsHandler, err := kubemcp.ServerToolToGoSdkTool(kubeSrv, tool)
			if err != nil {
				// Skip just this tool — leaving the rest intact is
				// preferable to dropping the whole tools/list.
				continue
			}
			srv.AddTool(gsTool, gsHandler)
		}
	}

	// Keep mcpkubernetes import live (it's the Provider interface
	// kubeProvider satisfies via the concrete type from builder).
	var _ mcpkubernetes.Provider = kubeProvider
}

// isKubeToolApplicable is a local copy of the upstream's unexported
// Configuration.isToolApplicable. Mirrors ReadOnly / DisableDestructive
// / EnabledTools / DisabledTools so the family surfaces the same tool
// set the upstream Server's own ServeHTTP would have.
func isKubeToolApplicable(cfg *mcpconfig.StaticConfig, tool mcpapi.ServerTool) bool {
	if cfg.ReadOnly {
		// Tools without ReadOnlyHint are assumed mutating.
		if tool.Tool.Annotations.ReadOnlyHint == nil || !*tool.Tool.Annotations.ReadOnlyHint {
			return false
		}
	}
	if cfg.DisableDestructive {
		if tool.Tool.Annotations.DestructiveHint != nil && *tool.Tool.Annotations.DestructiveHint {
			return false
		}
	}
	if len(cfg.EnabledTools) > 0 && !slices.Contains(cfg.EnabledTools, tool.Tool.Name) {
		return false
	}
	if len(cfg.DisabledTools) > 0 && slices.Contains(cfg.DisabledTools, tool.Tool.Name) {
		return false
	}
	return true
}
