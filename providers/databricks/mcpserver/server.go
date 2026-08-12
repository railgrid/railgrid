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

	"github.com/faroshq/provider-databricks/actions"
	"github.com/faroshq/provider-databricks/queryapi"
)

type Deps struct {
	Tables                        map[string]queryapi.TableRef
	TableResolver                 queryapi.TableResolver
	ResolverFromRequest           func(*http.Request) queryapi.TableResolver
	ActionExecutor                actions.QueryExecutor
	ActionExecutorFromRequest     func(*http.Request) actions.QueryExecutor
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
		Name:    "faros-databricks",
		Version: "0.1.0",
		Title:   "faros Databricks provider",
	}, &mcp.ServerOptions{
		Instructions: "Use these tools only with Databricks tables already imported " +
			"as faros Table resources. Do not import tables from App Studio. " +
			"Use list_tables first when you need a table name, and copy its exact tables[].name " +
			"into tableRef for metadata and the versioned query_table action (actionVersion v1). " +
			"An App Studio integration alias or other binding alias is never a tableRef. " +
			"query_table accepts only exact column names and a bounded limit; never send raw SQL. " +
			"Do not generate application code that calls provider-databricks, " +
			"and do not embed Databricks credentials or direct warehouse auth config.",
	})
	registerTools(srv, resolverForRequest(deps, r), actionExecutorForRequest(deps, r))
	return srv
}

func actionExecutorForRequest(deps Deps, r *http.Request) actions.QueryExecutor {
	if deps.ActionExecutorFromRequest != nil {
		if executor := deps.ActionExecutorFromRequest(r); executor != nil {
			return executor
		}
	}
	return deps.ActionExecutor
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
