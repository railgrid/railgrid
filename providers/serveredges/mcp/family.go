/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package mcp registers the server-edges provider's linux MCP tool
// family into the aggregator. Pulls in the linuxmcp toolset side-
// effect imports so the registry is populated by the time the
// aggregator first asks for tools.
//
// Loaded via blank import from the parent serveredges manifest.
package mcp

import (
	"context"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"k8s.io/klog/v2"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	aggregatemcp "github.com/faroshq/faros-kedge/providers/mcp/aggregate"
	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp"

	// Side-effect imports register the in-tree linux toolsets (core,
	// diag, net, pkg, systemd) into linuxmcp's registry. Without these,
	// the family installs zero tools.
	_ "github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp/toolsets/core"
	_ "github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp/toolsets/diag"
	_ "github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp/toolsets/net"
	_ "github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp/toolsets/pkg"
	_ "github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp/toolsets/systemd"
)

func init() {
	aggregatemcp.RegisterToolFamily(aggregatemcp.ToolFamily{
		Name:     "linux",
		EdgeType: "server",
		Register: registerLinuxTools,
	})
}

// registerLinuxTools wires the linuxmcp toolset onto srv for one
// MCPServer request. Was the body of aggregatemcp.registerLinuxTools
// before the family registry split.
//
// Reads `commandTimeoutSeconds` and `maxOutputBytes` from
// FamilyContext.Extras (the aggregator passes through unknown CR
// fields) so server-edges keeps owning its policy knobs without the
// aggregator needing typed slots for every family.
func registerLinuxTools(srv *mcp.Server, fctx aggregatemcp.FamilyContext) {
	var cmdTimeout time.Duration
	if v, ok := fctx.Extras["commandTimeoutSeconds"].(int64); ok && v > 0 {
		cmdTimeout = time.Duration(v) * time.Second
	}
	var maxOut int
	if v, ok := fctx.Extras["maxOutputBytes"].(int64); ok && v > 0 {
		maxOut = int(v)
	}

	provider := linuxmcp.NewProvider(linuxmcp.Config{
		Cluster:        fctx.Cluster,
		EdgeNames:      fctx.EdgeNames,
		OpenSession:    openSessionFor(fctx),
		CommandTimeout: cmdTimeout,
		MaxOutputBytes: maxOut,
		ReadOnly:       fctx.ReadOnly,
	})
	linuxmcp.RegisterTo(srv, provider, fctx.Toolsets)
}

// openSessionFor returns a linuxmcp.OpenSessionFunc bound to a single
// request — Deps.OpenSSHSession owns the credential fetch + tunnel
// open dance, so this layer is just an adapter that supplies the
// caller-identity bound at request build time.
func openSessionFor(fctx aggregatemcp.FamilyContext) linuxmcp.OpenSessionFunc {
	return func(ctx context.Context, edgeName string) (*gossh.Client, error) {
		logger := klog.FromContext(ctx).WithName("linux-mcp-open-session").
			WithValues("cluster", fctx.Cluster, "edge", edgeName)
		return fctx.Deps.OpenSSHSession(ctx, fctx.Cluster, edgeName, fctx.CallerIdentity, logger)
	}
}
