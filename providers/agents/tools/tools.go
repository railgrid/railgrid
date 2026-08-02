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
	"net/url"
	"strconv"
	"strings"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/llm"
	"github.com/faroshq/provider-agents/store"
)

// CRAccess is the minimal tenant-resource surface tools need. Implemented by
// the api package over the tenant client and over the virtual workspace.
type CRAccess interface {
	GetAgent(ctx context.Context, name string) (*agentsv1alpha1.Agent, error)
	CreateSchedule(ctx context.Context, s *agentsv1alpha1.Schedule) error
	GetSchedule(ctx context.Context, name string) (*agentsv1alpha1.Schedule, error)
	UpdateSchedule(ctx context.Context, s *agentsv1alpha1.Schedule) error
	DeleteSchedule(ctx context.Context, name string) error
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
	// DataPlane addresses tenant workloads through the infrastructure
	// provider's data plane. Zero value disables instance-backed tools.
	DataPlane DataPlane
	// Delegate runs a scoped task on another agent and returns its answer.
	// Injected by the api layer (it owns run execution); nil disables the
	// delegate tool.
	Delegate func(ctx context.Context, targetAgent, task string) (string, error)
}

// DataPlane is what a tool needs to reach a tenant workload provisioned by the
// infrastructure provider, over the platform-internal path rather than a public
// hostname. See docs/platform-internal-networking.md.
//
// Token is the CALLER's bearer token: the data plane authorizes by re-reading
// the instance as the caller. An interactive run supplies the human's token; a
// background run supplies the AGENT's own ServiceAccount token, minted into the
// tenant workspace (see api/agentidentity.go). Empty means neither was
// available — the identity could not be provisioned — and instance-backed tools
// report that rather than composing a call that 401s two hops away.
type DataPlane struct {
	HubBase   string
	ClusterID string
	Token     string
	// Insecure skips TLS verification against the hub (dev self-signed certs).
	Insecure bool
}

// Available reports whether a data-plane call can be made at all.
func (d DataPlane) Available() bool {
	return d.HubBase != "" && d.ClusterID != "" && d.Token != ""
}

// ProxyURL composes the URL of an instance's `proxy` verb:
//
//	{hub}/services/providers/infrastructure/dataplane/clusters/{cluster}/{resource}/{name}/proxy
//
// The caller appends its own path, if the template's endpoint does not pin one.
// It returns an error rather than a URL when the data plane is unusable, so
// every caller reports the same precise reason instead of a bare 401 from two
// hops away.
func (d DataPlane) ProxyURL(kind, connName, resource, instance string) (string, error) {
	if d.HubBase == "" || d.ClusterID == "" {
		return "", fmt.Errorf("%s connection %q names instance %q, but this provider has no hub/workspace context to reach it", kind, connName, instance)
	}
	if d.Token == "" {
		return "", fmt.Errorf("%s connection %q reaches instance %q over the platform data plane, which authorizes per caller, but this run has no identity — an interactive run uses yours, a background run uses the agent's own ServiceAccount, and provisioning that failed (check the provider log for \"identity unavailable\")", kind, connName, instance)
	}
	return strings.TrimRight(d.HubBase, "/") +
		fmt.Sprintf("/services/providers/infrastructure/dataplane/clusters/%s/%s/%s/proxy",
			url.PathEscape(d.ClusterID), url.PathEscape(resource), url.PathEscape(instance)), nil
}

// instanceRef reads the instance a Connection is bound to, with the resource
// name the template's instance CRD uses. Empty instance means the connection is
// not instance-backed.
func instanceRef(conn *agentsv1alpha1.Connection, defaultResource string) (instance, resource string) {
	instance = strings.TrimSpace(conn.Spec.Config["instance"])
	resource = strings.TrimSpace(conn.Spec.Config["instanceResource"])
	if resource == "" {
		resource = defaultResource
	}
	return instance, resource
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

// argInt reads an integer argument, tolerating the float64 that JSON numbers
// decode to and a numeric string. Returns 0 when absent or unparseable.
func argInt(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	return 0
}

// argBool reads a boolean argument, tolerating the "true"/"false" strings some
// models emit instead of a JSON boolean. Returns false when absent.
func argBool(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(v))
		return b
	}
	return false
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
