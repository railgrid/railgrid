// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package tools implements the built-in tool families agents can call during
// a run: core (memory, self-scheduling, notify, ask), web (SSRF-guarded fetch
// + search), and mcp (remote MCP servers, with a GitHub preset). Families are
// pure functions over narrow interfaces so both execution paths — a verb (the
// gate's provider client) and background (APIExport virtual workspace), both
// acting as the provider — reuse them unchanged.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
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
	// Secrets reads tenant Secrets (connection credentials) and resolves
	// ModelCredentials, which is what a delegated or spawned sub-run needs to
	// build its own model.
	Secrets llm.CredentialResolver
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

	// Spawn starts a scoped worker — this same agent on a sub-task, with a fresh
	// context and a narrowed toolset — and returns its task id without waiting.
	// Join collects the results. Both are injected by the api layer (it owns run
	// execution); nil on either disables both tools.
	Spawn func(ctx context.Context, req SpawnRequest) (taskID string, err error)
	Join  func(ctx context.Context, taskIDs []string, timeoutSeconds int) (string, error)

	// SpawnPolicy describes the limits and grantable tool families the spawn tool
	// advertises to the model, so its description matches what it will accept.
	SpawnPolicy SpawnPolicy
}

// SpawnRequest is one worker the model asked for.
type SpawnRequest struct {
	// Task is the self-contained sub-task the worker works on.
	Task string
	// Instructions is extra guidance folded into the worker's system context.
	Instructions string
	// Families names the tool families the worker should get. The api layer
	// intersects this with what the calling run itself holds — a worker can never
	// reach anything its parent could not.
	Families []string
	// MaxToolTurns bounds the worker's own tool-call loop.
	MaxToolTurns int
}

// SpawnPolicy is the advertised spawn envelope for one run.
type SpawnPolicy struct {
	// Families a worker may be granted (already narrowed to the parent's grant).
	Families []string
	// DefaultFamilies is what a worker gets when the model names none.
	DefaultFamilies []string
	// MaxPerRun caps spawns in this run; MaxConcurrent caps simultaneous workers.
	MaxPerRun     int
	MaxConcurrent int
	// DefaultToolTurns / MaxToolTurns bound a worker's own loop.
	DefaultToolTurns int
	MaxToolTurns     int
}

// VerbCaller addresses a verb ANOTHER provider serves — a custom subresource
// this provider has claimed under spec.dependencies[].composes[] — through
// this provider's own APIExport virtual workspace, with this provider's own
// credential. *dataplane.Callers implements it; tests substitute a fake.
type VerbCaller interface {
	ExportVerbURL(ctx context.Context, gvr schema.GroupVersionResource, r dataplane.Request) (string, error)
	ProviderHTTPClient() (*http.Client, error)
}

// DataPlane is what a tool needs to reach a tenant workload provisioned by the
// infrastructure provider, over the platform-internal path rather than a public
// hostname. See docs/platform-internal-networking.md.
//
// The call is made AS THIS PROVIDER: the infrastructure provider's
// instances/proxy verb is a custom subresource the agents APIExport claims
// (manifest.yaml spec.dependencies), so kcp serves it on this provider's own
// virtual workspace, authorizes it against the claim the tenant accepted, and
// forwards it to infrastructure impersonating this provider. No caller
// credential travels: an interactive run and an unattended one reach an
// instance the same way, and neither lends the agent a human's reach.
type DataPlane struct {
	// ClusterID is the tenant workspace's kcp logical-cluster ID.
	ClusterID string
	// Callers builds the URL and the HTTP client. Nil means this provider has
	// no provider-scoped config (no kubeconfig), so instance-backed tools
	// report that rather than composing a call that fails two hops away.
	Callers VerbCaller
}

// Available reports whether a data-plane call can be made at all.
func (d DataPlane) Available() bool {
	return d.ClusterID != "" && d.Callers != nil
}

// InstanceAPIGroup is the API group a Connection's instance reference lives in.
// This IS a legitimate constant: it is the API contract between the two
// providers, not a routing detail — an agent's Connection names an instance of
// this group, and the composition claim in manifest.yaml names the same group.
const InstanceAPIGroup = "infrastructure.railgrid.ai"

// InstanceAPIVersion is the version the instances resource is served at.
const InstanceAPIVersion = "v1alpha1"

// ProxyURL composes the URL of an instance's `proxy` verb, through this
// provider's export virtual workspace:
//
//	{vw}/clusters/{cluster}/apis/infrastructure.railgrid.ai/v1alpha1/{resource}/{name}/proxy[/{tail}]
//
// The path itself comes from dataplane.SubresourcePath (via ExportVerbURL), so
// the grammar has one implementation in the tree and a consumer cannot spell
// another provider's route slightly differently from the way that provider
// parses it. tail is the upstream path beneath the verb ("" for the root, which
// is where a template pinning its own upstream path is reached).
//
// It returns an error rather than a URL when the data plane is unusable, so
// every caller reports the same precise reason instead of a bare failure from
// two hops away.
func (d DataPlane) ProxyURL(ctx context.Context, kind, connName, resource, instance, tail string) (string, error) {
	if d.ClusterID == "" {
		return "", fmt.Errorf("%s connection %q names instance %q, but this run has no workspace context to reach it", kind, connName, instance)
	}
	if d.Callers == nil {
		return "", fmt.Errorf("%s connection %q reaches instance %q over the platform data plane as this provider, but the provider has no provider-scoped kubeconfig (RAILGRID_PROVIDER_KUBECONFIG), so instance-backed tools are unavailable", kind, connName, instance)
	}
	gvr := schema.GroupVersionResource{Group: InstanceAPIGroup, Version: InstanceAPIVersion, Resource: resource}
	u, err := d.Callers.ExportVerbURL(ctx, gvr, dataplane.Request{
		ClusterID: d.ClusterID, Resource: resource, Name: instance, Verb: "proxy", Tail: strings.Trim(tail, "/"),
	})
	if err != nil {
		return "", fmt.Errorf("%s connection %q addresses instance %q: %w", kind, connName, instance, err)
	}
	return u, nil
}

// HTTPClient returns the client that authenticates a ProxyURL call as this
// provider. It carries the provider kubeconfig's credential and TLS settings.
func (d DataPlane) HTTPClient() (*http.Client, error) {
	if d.Callers == nil {
		return nil, fmt.Errorf("no provider-scoped config: the platform data plane is unavailable")
	}
	return d.Callers.ProviderHTTPClient()
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

// argStringSlice reads a string-array argument, tolerating the two shapes
// models emit instead of a JSON array: a single bare string, and a
// comma-separated string. Blanks are dropped; absent yields nil.
func argStringSlice(args map[string]any, key string) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	switch v := args[key].(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	case []string:
		for _, s := range v {
			add(s)
		}
	case string:
		for _, s := range strings.Split(v, ",") {
			add(s)
		}
	}
	return out
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
