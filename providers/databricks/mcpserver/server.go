// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package mcpserver exposes Databricks table discovery tools to App Studio.
package mcpserver

import (
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/klog/v2"

	"github.com/faroshq/provider-databricks/queryapi"
)

type Deps struct {
	Tables                        map[string]queryapi.TableRef
	TableResolver                 queryapi.TableResolver
	ResolverFromRequest           func(*http.Request) queryapi.TableResolver
	DisableLocalhostMCPProtection bool
}

func NewHandler(deps Deps) http.Handler {
	return mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			return newPerRequestServer(deps, r)
		},
		&mcp.StreamableHTTPOptions{
			Stateless:                  true,
			DisableLocalhostProtection: deps.DisableLocalhostMCPProtection,
		},
	)
}

func newPerRequestServer(deps Deps, r *http.Request) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "kedge-databricks",
		Version: "0.1.0",
		Title:   "kedge Databricks provider",
	}, &mcp.ServerOptions{
		Instructions: "Use these tools only with Databricks tables already imported " +
			"as kedge Table resources. Do not import tables from App Studio. " +
			"Use tableRef only for design-time metadata and schema inspection. " +
			"Do not generate application code that calls provider-databricks, " +
			"and do not embed Databricks credentials or direct warehouse auth config.",
	})
	registerTools(srv, resolverForRequest(deps, r))
	return srv
}

func resolverForRequest(deps Deps, r *http.Request) queryapi.TableResolver {
	if deps.ResolverFromRequest != nil {
		if resolver := deps.ResolverFromRequest(r); resolver != nil {
			return resolver
		}
	}
	if deps.TableResolver != nil {
		return deps.TableResolver
	}
	return queryapi.StaticTableResolver(deps.Tables)
}

func safeRegister(name string, register func()) {
	defer func() {
		if r := recover(); r != nil {
			klog.Background().Error(fmt.Errorf("%v", r), "databricks MCP: tool registration panicked; tool skipped", "tool", name)
		}
	}()
	register()
}
