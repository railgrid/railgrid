// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package tools implements the built-in tool families agents can call during
// a run: core (memory, self-scheduling, notify, ask), web (SSRF-guarded fetch
// + search), and mcp (remote MCP servers, with a GitHub preset). Families are
// pure functions over narrow interfaces so both execution paths — per-request
// (tenant client, acting as the user) and background (APIExport virtual
// workspace) — reuse them unchanged.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/llm"
	"github.com/faroshq/provider-agents/store"
)

// CRAccess is the minimal tenant-resource surface tools need. Implemented by
// the api package over the tenant client and over the virtual workspace.
type CRAccess interface {
	GetAgent(ctx context.Context, name string) (*agentsv1alpha1.Agent, error)
	CreateSchedule(ctx context.Context, s *agentsv1alpha1.Schedule) error
	ListSchedules(ctx context.Context) ([]agentsv1alpha1.Schedule, error)
	ListConnections(ctx context.Context) ([]agentsv1alpha1.Connection, error)
	GetConnection(ctx context.Context, name string) (*agentsv1alpha1.Connection, error)
	GetToolset(ctx context.Context, name string) (*agentsv1alpha1.Toolset, error)
}

// Deps carries everything a tool family needs to build its tools for one run.
type Deps struct {
	Store store.Store
	Scope store.Scope
	Agent *agentsv1alpha1.Agent
	CR    CRAccess
	// Secrets reads tenant Secrets (connection credentials).
	Secrets llm.SecretGetter
	// ConnSecretName maps a Connection name to its Secret name.
	ConnSecretName func(name string) string
	// RunID is the executing run — recorded on sub-agent runs as the parent.
	RunID string
	// Delegate runs a scoped task on another agent and returns its answer.
	// Injected by the api layer (it owns run execution); nil disables the
	// delegate tool.
	Delegate func(ctx context.Context, targetAgent, task string) (string, error)
}

// connToken reads a connection's credential token ("" when absent).
func (d Deps) connToken(ctx context.Context, connName string) string {
	if d.Secrets == nil || d.ConnSecretName == nil {
		return ""
	}
	sec, err := d.Secrets.GetSecret(ctx, llm.SecretNamespace, d.ConnSecretName(connName))
	if err != nil {
		return ""
	}
	if v, ok := sec.Data["token"]; ok {
		return string(v)
	}
	return ""
}

// parseArgs unmarshals model-provided JSON arguments.
func parseArgs(argsJSON string) (map[string]any, error) {
	out := map[string]any{}
	if argsJSON == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(argsJSON), &out); err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	return out, nil
}

func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
