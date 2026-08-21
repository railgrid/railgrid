// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// The rest of the MCP configuration surface: everything the portal's settings
// screens can do, minus the parts that need a browser (OAuth Connect) or that
// would let an agent widen its own permissions unattended (resolving approvals).
//
// Every tool delegates to the same apply* helper the REST handler uses, so a
// change made here behaves exactly like the same change made in the portal.
// Secrets are write-only throughout: tokens and API keys can be set or rotated,
// never read back.

import (
	"context"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/llm"
)

// mcpIdentity reconstructs the caller identity for tools that touch
// store-scoped data or mint cluster-scoped URLs. MCP federation forwards the
// cluster ID in both X-Faros-Tenant and X-Faros-Cluster, so the org/workspace
// UUIDs usually cannot be parsed from the header — they come from the
// cluster→tenant mapping the portal records on every REST call (the same
// mapping background execution uses).
func (s *Server) mcpIdentity(ctx context.Context, r *http.Request) identity {
	id := identity{
		tenantPath: strings.TrimSpace(r.Header.Get("X-Faros-Tenant")),
		clusterID:  strings.TrimSpace(r.Header.Get("X-Faros-Cluster")),
		user:       strings.TrimSpace(r.Header.Get("X-Faros-User")),
		token:      bearerToken(r),
	}
	id.orgUUID, id.workspaceUUID = parseTenantPath(id.tenantPath)
	if id.orgUUID == "" || id.workspaceUUID == "" {
		if ref, ok, _ := s.store.GetTenantRef(ctx, id.clusterID); ok {
			id.orgUUID, id.workspaceUUID = ref.OrgUUID, ref.WorkspaceUUID
		}
	}
	return id
}

// --- agents ---

type createAgentInput struct {
	Name            string         `json:"name" jsonschema:"Lowercase agent name, e.g. research-bot"`
	DisplayName     string         `json:"displayName,omitempty" jsonschema:"Human-readable name; defaults to name"`
	Description     string         `json:"description,omitempty" jsonschema:"Short summary of what this agent is for"`
	SystemPrompt    string         `json:"systemPrompt,omitempty" jsonschema:"The agent's persona and standing instructions"`
	Autonomy        string         `json:"autonomy,omitempty" jsonschema:"Action posture: suggest (drafts only), ask (approval-gated), or auto"`
	ModelCredential string         `json:"modelCredential,omitempty" jsonschema:"Named model credential for chat (see list_model_credentials)"`
	ModelFallbacks  []string       `json:"modelFallbacks,omitempty" jsonschema:"Ordered credential names tried when the primary model fails"`
	BudgetTokens    int64          `json:"budgetTokens,omitempty" jsonschema:"Token cap per rolling month; 0 means unlimited"`
	BudgetUSD       string         `json:"budgetUSD,omitempty" jsonschema:"USD spend cap per rolling month as a decimal string; empty means unlimited"`
	Channels        []channelInput `json:"channels,omitempty" jsonschema:"Messaging channel bindings (name + connectionRef + primary)"`
}

type nameInput struct {
	Name string `json:"name" jsonschema:"Resource name"`
}

type deletedOutput struct {
	Deleted string `json:"deleted"`
}

// --- triggers ---

type triggerSummary struct {
	Name          string            `json:"name"`
	AgentRef      string            `json:"agentRef"`
	Source        string            `json:"source"`
	ConnectionRef string            `json:"connectionRef,omitempty"`
	Filter        map[string]string `json:"filter,omitempty"`
	Task          string            `json:"task,omitempty"`
	ChannelRef    string            `json:"channelRef,omitempty"`
	Suspend       bool              `json:"suspend"`
	// WebhookPath is the inbound URL a webhook/github trigger listens on. It
	// embeds the trigger's shared secret, so treat it as a credential.
	WebhookPath string `json:"webhookPath,omitempty"`
	LastFired   string `json:"lastFired,omitempty"`
}

type listTriggersOutput struct {
	Triggers []triggerSummary `json:"triggers"`
}

type createTriggerInput struct {
	Name          string            `json:"name" jsonschema:"Lowercase trigger name, e.g. deploy-failed"`
	AgentRef      string            `json:"agentRef" jsonschema:"Name of the agent this trigger runs (see list_agents)"`
	Source        string            `json:"source" jsonschema:"webhook (any HTTP POST) or github (GitHub webhook deliveries)"`
	ConnectionRef string            `json:"connectionRef,omitempty" jsonschema:"Connection this trigger is wired to, for sources that need one"`
	Filter        map[string]string `json:"filter,omitempty" jsonschema:"Key/value conditions the event payload must match, e.g. {\"action\":\"opened\"}"`
	Task          string            `json:"task,omitempty" jsonschema:"The prompt the agent runs when the trigger fires; the event payload is appended"`
	Suspend       bool              `json:"suspend,omitempty" jsonschema:"Create the trigger paused"`
	ChannelRef    string            `json:"channelRef,omitempty" jsonschema:"Agent channel this trigger's output goes to; empty means the primary channel"`
}

type updateTriggerInput struct {
	Name          string             `json:"name" jsonschema:"Name of the trigger to update (see list_triggers)"`
	Task          *string            `json:"task,omitempty" jsonschema:"New prompt to run when the trigger fires"`
	Source        *string            `json:"source,omitempty" jsonschema:"New source: webhook or github"`
	ConnectionRef *string            `json:"connectionRef,omitempty" jsonschema:"Rewire the trigger to another connection; empty string unwires it"`
	Filter        *map[string]string `json:"filter,omitempty" jsonschema:"Replacement filter map; replaces the whole map"`
	Suspend       *bool              `json:"suspend,omitempty" jsonschema:"true pauses the trigger without deleting it, false resumes it"`
	ChannelRef    *string            `json:"channelRef,omitempty" jsonschema:"Agent channel for this trigger's output; empty string means the primary channel"`
}

func triggerView(t *agentsv1alpha1.Trigger) triggerSummary {
	row := triggerSummary{
		Name:          t.Name,
		AgentRef:      t.Spec.AgentRef,
		Source:        t.Spec.Source,
		ConnectionRef: t.Spec.ConnectionRef,
		Filter:        t.Spec.Filter,
		Task:          t.Spec.Task,
		ChannelRef:    t.Spec.ChannelRef,
		Suspend:       t.Spec.Suspend,
		WebhookPath:   t.Status.WebhookPath,
	}
	if t.Status.LastFired != nil {
		row.LastFired = t.Status.LastFired.UTC().Format("2006-01-02T15:04:05Z")
	}
	return row
}

// --- toolsets / connections / credentials ---

type createToolsetInput struct {
	Name            string   `json:"name" jsonschema:"Lowercase toolset name, e.g. ops-tools"`
	DisplayName     string   `json:"displayName,omitempty" jsonschema:"Human-readable name"`
	Description     string   `json:"description,omitempty" jsonschema:"What this bundle is for"`
	Families        []string `json:"families,omitempty" jsonschema:"Built-in tool families: core, web, github, mcp, files, edges, spawn"`
	Connections     []string `json:"connections,omitempty" jsonschema:"Connection names whose tools this bundle grants"`
	RequireApproval []string `json:"requireApproval,omitempty" jsonschema:"Tool-name patterns that must be approved before running; \"*\" gates everything"`
}

type updateToolsetInput struct {
	Name            string    `json:"name" jsonschema:"Name of the toolset to update (see list_toolsets)"`
	DisplayName     *string   `json:"displayName,omitempty" jsonschema:"New human-readable name"`
	Description     *string   `json:"description,omitempty" jsonschema:"New description"`
	Families        *[]string `json:"families,omitempty" jsonschema:"Replacement family list; replaces the whole list"`
	Connections     *[]string `json:"connections,omitempty" jsonschema:"Replacement connection list; replaces the whole list"`
	RequireApproval *[]string `json:"requireApproval,omitempty" jsonschema:"Replacement approval patterns; replaces the whole list"`
}

type createConnectionInput struct {
	Name        string            `json:"name" jsonschema:"Lowercase connection name, e.g. team-telegram"`
	Type        string            `json:"type" jsonschema:"github, mcp, websearch, edges, http, telegram, slack, smtp, or discord"`
	DisplayName string            `json:"displayName,omitempty" jsonschema:"Human-readable name"`
	BaseURL     string            `json:"baseURL,omitempty" jsonschema:"Endpoint URL for mcp/http/websearch connections"`
	Channel     string            `json:"channel,omitempty" jsonschema:"Delivery target for messaging connections (chat id, channel, address)"`
	Config      map[string]string `json:"config,omitempty" jsonschema:"Type-specific settings"`
	Secret      string            `json:"secret,omitempty" jsonschema:"The credential (bot token, PAT, API key). Write-only: stored in a Secret and never returned"`
}

type updateConnectionInput struct {
	Name        string             `json:"name" jsonschema:"Name of the connection to update (see list_connections)"`
	DisplayName *string            `json:"displayName,omitempty" jsonschema:"New human-readable name"`
	BaseURL     *string            `json:"baseURL,omitempty" jsonschema:"New endpoint URL"`
	Channel     *string            `json:"channel,omitempty" jsonschema:"New delivery target"`
	Config      *map[string]string `json:"config,omitempty" jsonschema:"Replacement config map; replaces the whole map"`
	Secret      *string            `json:"secret,omitempty" jsonschema:"Rotate the credential. Write-only; omit to keep the current one"`
}

type saveCredentialInput struct {
	Name     string `json:"name" jsonschema:"Credential name agents reference, e.g. anthropic-main"`
	Model    string `json:"model" jsonschema:"Model id served by this endpoint, e.g. claude-sonnet-5"`
	Provider string `json:"provider,omitempty" jsonschema:"Provider id; defaults to the OpenAI-compatible driver"`
	BaseURL  string `json:"baseURL,omitempty" jsonschema:"API base URL; empty means the provider default"`
	APIKey   string `json:"apiKey,omitempty" jsonschema:"The API key. Write-only: stored in a Secret and never returned. Omit to keep the existing key when updating"`
}

type testResultOutput struct {
	OK        bool     `json:"ok"`
	LatencyMS int64    `json:"latencyMS,omitempty"`
	Error     string   `json:"error,omitempty"`
	Models    []string `json:"models,omitempty"`
}

// --- discovery ---

type toolFamilyInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type listToolFamiliesOutput struct {
	Families []toolFamilyInfo `json:"families"`
	// Providers are the faros providers reachable through the hub's aggregate
	// tool endpoint, which every interactive run gets for free.
	Providers []string `json:"providers,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// toolFamilyDocs describes each grantable family in the terms a caller needs to
// choose between them.
var toolFamilyDocs = []toolFamilyInfo{
	{"core", "Always on: memory, self-scheduling (schedule_create/update/delete), notify, ask, delegate."},
	{"web", "web_fetch (SSRF-guarded) and web_search; needs a websearch Connection for search."},
	{"github", "GitHub tools via a github Connection (issues, PRs, code)."},
	{"mcp", "Tools from any mcp Connection the agent is granted."},
	{"files", "Read/write files in the agent's workspace."},
	{"edges", "Kubernetes clusters and SSH servers connected as edges. Interactive runs only."},
	{"spawn", "spawn/join: fan out to scoped workers (this agent on sub-tasks, fresh context, a subset of its other granted families) and collect their answers. The basis of a research pass; costs count against the agent's own budget."},
}

// --- run now ---

type runNowOutput struct {
	RunID string `json:"runID"`
	// Status explains what to do next: runs are asynchronous.
	Status string `json:"status"`
}

// registerConfigMCPTools adds the trigger, toolset, connection, credential,
// agent-lifecycle, and discovery tools to the MCP server.
func (s *Server) registerConfigMCPTools(srv *mcp.Server, r *http.Request) {
	yes := true
	no := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &yes}
	mutating := &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &yes}
	destructive := &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: &yes, OpenWorldHint: &yes}

	// --- agents ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "create_agent",
		Title: "Create an agent",
		Description: "Create a new agent. Assign its model with modelCredential (from list_model_credentials) and bind messaging channels with channels. " +
			"Tool grants, limits, and prompts can be set afterwards with update_agent.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createAgentInput) (*mcp.CallToolResult, agentSettings, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, agentSettings{}, err
		}
		id := s.mcpIdentity(ctx, r)
		req := createAgentRequest{
			Name: in.Name, DisplayName: in.DisplayName, Description: in.Description,
			SystemPrompt: in.SystemPrompt, Autonomy: in.Autonomy,
			ModelCredential: in.ModelCredential, ModelFallbacks: in.ModelFallbacks,
			BudgetTokens: in.BudgetTokens, BudgetUSD: in.BudgetUSD, Channels: in.Channels,
		}
		a, created, err := s.applyAgentCreate(ctx, c, id, &req)
		if err != nil {
			return nil, agentSettings{}, err
		}
		if created {
			s.trackAgentCreated(ctx, id, a.Name)
		}
		return nil, settingsView(a), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_agent",
		Title:       "Delete an agent",
		Description: "Permanently delete an agent and its stored conversations, memories, and run history. Schedules and triggers pointing at it stop firing.",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, deletedOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, deletedOutput{}, err
		}
		name := strings.TrimSpace(in.Name)
		if err := c.Agents().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return nil, deletedOutput{}, err
		}
		// Best-effort teardown of the agent's store data, exactly as the REST
		// handler does. Skipped when the workspace scope is unknown, so we never
		// delete another workspace's rows.
		if id := s.mcpIdentity(ctx, r); id.workspaceUUID != "" {
			_ = s.store.DeleteAgentData(ctx, id.scope(name), name)
		}
		return nil, deletedOutput{Deleted: name}, nil
	})

	// --- triggers ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_triggers",
		Title:       "List triggers",
		Description: "List the event triggers that run agents on inbound webhooks or GitHub deliveries, with their filters and inbound URLs.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listTriggersOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, listTriggersOutput{}, err
		}
		list, err := c.Triggers().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, listTriggersOutput{}, err
		}
		out := listTriggersOutput{Triggers: make([]triggerSummary, 0, len(list.Items))}
		for i := range list.Items {
			out.Triggers = append(out.Triggers, triggerView(&list.Items[i]))
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "create_trigger",
		Title: "Create a trigger",
		Description: "Create an event trigger that runs an agent when something arrives: source \"webhook\" for any HTTP POST, \"github\" for GitHub webhook deliveries. " +
			"The response carries the inbound webhookPath to register with the sender — it embeds a secret, so share it carefully.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createTriggerInput) (*mcp.CallToolResult, triggerSummary, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, triggerSummary{}, err
		}
		req := createTriggerRequest{
			Name: in.Name, AgentRef: in.AgentRef, Source: in.Source,
			ConnectionRef: in.ConnectionRef, Filter: in.Filter, Task: in.Task,
			Suspend: in.Suspend, ChannelRef: in.ChannelRef,
		}
		t, err := s.applyTriggerCreate(ctx, c, s.mcpIdentity(ctx, r).clusterID, &req)
		if err != nil {
			return nil, triggerSummary{}, err
		}
		return nil, triggerView(t), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_trigger",
		Title:       "Update a trigger",
		Description: "Edit an existing trigger in place: rewrite its task, change its filter, rewire its connection, or pause/resume it with suspend. Only the fields you pass change.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateTriggerInput) (*mcp.CallToolResult, triggerSummary, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, triggerSummary{}, err
		}
		req := updateTriggerRequest{
			Task: in.Task, Source: in.Source, ConnectionRef: in.ConnectionRef,
			Filter: in.Filter, Suspend: in.Suspend, ChannelRef: in.ChannelRef,
		}
		t, err := s.applyTriggerUpdate(ctx, c, s.mcpIdentity(ctx, r).clusterID, in.Name, &req)
		if err != nil {
			return nil, triggerSummary{}, err
		}
		return nil, triggerView(t), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_trigger",
		Title:       "Delete a trigger",
		Description: "Permanently delete a trigger and invalidate its inbound URL. To stop it temporarily, prefer update_trigger with suspend=true.",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, deletedOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, deletedOutput{}, err
		}
		name := strings.TrimSpace(in.Name)
		if err := c.Triggers().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return nil, deletedOutput{}, err
		}
		return nil, deletedOutput{Deleted: name}, nil
	})

	// --- toolsets ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "create_toolset",
		Title: "Create a toolset",
		Description: "Create a shared toolset: a reusable bundle of tool families, connections, and approval rules that agents link with update_agent's " +
			"interactiveToolsets / backgroundToolsets. Family names come from list_tool_families.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createToolsetInput) (*mcp.CallToolResult, toolsetSummary, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, toolsetSummary{}, err
		}
		req := toolsetRequest{
			Name: in.Name, DisplayName: in.DisplayName, Description: in.Description,
			Families: in.Families, Connections: in.Connections, RequireApproval: in.RequireApproval,
		}
		ts, err := applyToolsetCreate(ctx, c, &req)
		if err != nil {
			return nil, toolsetSummary{}, err
		}
		return nil, toolsetView(ts), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_toolset",
		Title:       "Update a toolset",
		Description: "Edit a shared toolset. Only the fields you pass change; list fields (families, connections, requireApproval) replace the stored list wholesale, so read it first when appending.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateToolsetInput) (*mcp.CallToolResult, toolsetSummary, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, toolsetSummary{}, err
		}
		req := updateToolsetRequest{
			DisplayName: in.DisplayName, Description: in.Description, Families: in.Families,
			Connections: in.Connections, RequireApproval: in.RequireApproval,
		}
		ts, err := applyToolsetUpdate(ctx, c, in.Name, &req)
		if err != nil {
			return nil, toolsetSummary{}, err
		}
		return nil, toolsetView(ts), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_toolset",
		Title:       "Delete a toolset",
		Description: "Delete a shared toolset. Agents linking it silently lose those grants, so check list_agents first.",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, deletedOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, deletedOutput{}, err
		}
		name := strings.TrimSpace(in.Name)
		if err := c.Toolsets().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return nil, deletedOutput{}, err
		}
		return nil, deletedOutput{Deleted: name}, nil
	})

	// --- connections ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "create_connection",
		Title: "Create a connection",
		Description: "Create an external connection: a messaging channel (telegram, slack, smtp, discord), a tool source (github, mcp, http, websearch, edges). " +
			"The credential goes in secret and is stored write-only. OAuth-based connections must be created in the portal — the Connect flow needs a browser.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createConnectionInput) (*mcp.CallToolResult, agentConnectionSummary, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, agentConnectionSummary{}, err
		}
		req := createConnectionRequest{
			Name: in.Name, Type: in.Type, DisplayName: in.DisplayName,
			BaseURL: in.BaseURL, Channel: in.Channel, Config: in.Config, Secret: in.Secret,
		}
		conn, err := s.applyConnectionCreate(ctx, c, &req)
		if err != nil {
			return nil, agentConnectionSummary{}, err
		}
		return nil, connectionView(conn), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_connection",
		Title:       "Update a connection",
		Description: "Edit a connection: rename it, repoint its URL or delivery target, or rotate its credential by passing secret. Only the fields you pass change.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateConnectionInput) (*mcp.CallToolResult, agentConnectionSummary, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, agentConnectionSummary{}, err
		}
		req := updateConnectionRequest{
			DisplayName: in.DisplayName, BaseURL: in.BaseURL, Channel: in.Channel,
			Config: in.Config, Secret: in.Secret,
		}
		conn, err := applyConnectionUpdate(ctx, c, in.Name, &req)
		if err != nil {
			return nil, agentConnectionSummary{}, err
		}
		return nil, connectionView(conn), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_connection",
		Title:       "Delete a connection",
		Description: "Delete a connection and its stored credential. Agents and triggers referencing it lose that capability, so check list_agents first.",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, deletedOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, deletedOutput{}, err
		}
		name := strings.TrimSpace(in.Name)
		if err := deleteConnectionAndSecret(ctx, c, name); err != nil {
			return nil, deletedOutput{}, err
		}
		return nil, deletedOutput{Deleted: name}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "test_connection",
		Title:       "Test a messaging connection",
		Description: "Send a test message through a messaging connection (telegram, slack, smtp, discord) to verify the credential and target work. Delivers a real message.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, testResultOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, testResultOutput{}, err
		}
		if err := sendConnectionTest(ctx, c, in.Name); err != nil {
			return nil, testResultOutput{}, err
		}
		return nil, testResultOutput{OK: true}, nil
	})

	// --- model credentials ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "save_model_credential",
		Title: "Create or update a model credential",
		Description: "Create or update a named model credential agents can be assigned. The apiKey is write-only — omit it when updating to keep the stored key. " +
			"Assign the result to an agent with update_agent's modelCredential.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in saveCredentialInput) (*mcp.CallToolResult, credentialSummary, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, credentialSummary{}, err
		}
		req := modelCredential{Name: in.Name, Provider: in.Provider, BaseURL: in.BaseURL, Model: in.Model, APIKey: in.APIKey}
		cred, err := applyCredentialUpsert(ctx, c, &req)
		if err != nil {
			return nil, credentialSummary{}, err
		}
		return nil, credentialSummary{Name: cred.Name, Provider: cred.Provider, Model: cred.Model, HasAPIKey: cred.HasAPIKey}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_model_credential",
		Title:       "Delete a model credential",
		Description: "Delete a named model credential. Agents assigned it stop running until they are pointed at another one, so check list_agents first.",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, deletedOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, deletedOutput{}, err
		}
		name := strings.TrimSpace(in.Name)
		if err := c.DeleteSecret(ctx, llm.SecretNamespace, llm.CredentialSecretName(name)); err != nil {
			return nil, deletedOutput{}, err
		}
		return nil, deletedOutput{Deleted: name}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "test_model_credential",
		Title:       "Test a model credential",
		Description: "Health-check a model credential by listing the models its endpoint serves. Returns latency and, on success, the available model ids.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, testResultOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, testResultOutput{}, err
		}
		profile, err := llm.LoadCredential(ctx, c, strings.TrimSpace(in.Name))
		if err != nil {
			return nil, testResultOutput{OK: false, Error: "credential not configured: " + err.Error()}, nil
		}
		models, latency, perr := probeOpenAIModels(ctx, profile.BaseURL, profile.APIKey)
		if perr != nil {
			return nil, testResultOutput{OK: false, LatencyMS: latency.Milliseconds(), Error: perr.Error()}, nil
		}
		return nil, testResultOutput{OK: true, LatencyMS: latency.Milliseconds(), Models: models}, nil
	})

	// --- discovery ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "list_tool_families",
		Title: "List tool families",
		Description: "List the built-in tool families that can be granted to an agent or a toolset, with what each one provides. " +
			"Use these names for update_agent's interactiveFamilies / backgroundFamilies and create_toolset's families.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listToolFamiliesOutput, error) {
		out := listToolFamiliesOutput{
			Families: toolFamilyDocs,
			Note:     "core is always granted and cannot be removed.",
		}
		// Best-effort, cache-only: the aggregate endpoint knows which faros
		// providers an interactive run reaches without any grant. Probing it
		// live would put a network round-trip inside a discovery call, so an
		// empty list here means "not probed recently", not "nothing enabled".
		if cached, hit := s.capabilities.get(s.mcpIdentity(ctx, r).clusterID); hit {
			out.Providers = cached.Providers
			out.Note += " The listed providers are reachable in interactive runs via the hub's aggregate endpoint, with no tool grant needed."
		}
		return nil, out, nil
	})

	// --- run now ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "run_schedule",
		Title: "Run a schedule now",
		Description: "Fire a schedule's task immediately without waiting for its next cron tick — the way to test a schedule after editing it. " +
			"The run is asynchronous and its output goes to the schedule's channel, exactly like a real firing.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, runNowOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, runNowOutput{}, err
		}
		sched, err := c.Schedules().Get(ctx, strings.TrimSpace(in.Name), metav1.GetOptions{})
		if err != nil {
			return nil, runNowOutput{}, err
		}
		task := sched.Spec.Task
		trigger := agentsv1alpha1.RunTriggerSchedule
		if sched.Spec.Type == agentsv1alpha1.ScheduleTypeHeartbeat {
			trigger = agentsv1alpha1.RunTriggerHeartbeat
			task = "Review this standing checklist and report only if something is actionable:\n\n" + sched.Spec.Checklist
		}
		return s.mcpRunNow(ctx, r, c, sched.Spec.AgentRef, taskRun{
			SessionID: "schedule:" + sched.Name, Task: task, Trigger: trigger,
			SourceName: sched.Name, NotifyChannel: sched.Spec.ChannelRef,
		})
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "run_trigger",
		Title: "Run a trigger now",
		Description: "Fire a trigger's task immediately, as if its event had arrived — the way to test a trigger after editing it. " +
			"The run is asynchronous and its output goes to the trigger's channel.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, runNowOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, runNowOutput{}, err
		}
		trig, err := c.Triggers().Get(ctx, strings.TrimSpace(in.Name), metav1.GetOptions{})
		if err != nil {
			return nil, runNowOutput{}, err
		}
		return s.mcpRunNow(ctx, r, c, trig.Spec.AgentRef, taskRun{
			SessionID: "trigger:" + trig.Name, Task: trig.Spec.Task,
			Trigger: agentsv1alpha1.RunTriggerEvent, SourceName: trig.Name,
			NotifyChannel: trig.Spec.ChannelRef,
		})
	})
}

// mcpRunNow starts a detached run for a schedule/trigger "run now" tool. It
// mirrors the REST handlers: resolve the agent, refuse an empty task, and
// return the run id for follow-up.
func (s *Server) mcpRunNow(ctx context.Context, r *http.Request, c *agentsclient.Client, agentRef string, tr taskRun) (*mcp.CallToolResult, runNowOutput, error) {
	agent, err := c.Agents().Get(ctx, agentRef, metav1.GetOptions{})
	if err != nil {
		return nil, runNowOutput{}, err
	}
	if strings.TrimSpace(tr.Task) == "" {
		return nil, runNowOutput{}, errBadRequest("this schedule/trigger has no task to run")
	}
	id := s.mcpIdentity(ctx, r)
	if id.workspaceUUID == "" {
		// Without a workspace scope the run's transcript would land under a
		// fallback scope the portal never reads — better to say so than to
		// silently strand it.
		return nil, runNowOutput{}, errBadRequest("this workspace is not mapped yet — open the agents UI once, then retry")
	}
	runID := s.startDetachedRun(r, c, id, agent, tr)
	return nil, runNowOutput{
		RunID:  runID,
		Status: "started — the run is executing in the background; its output is delivered to the configured channel",
	}, nil
}

// toolsetView / connectionView are the shared row shapes the toolset and
// connection tools return.
func toolsetView(ts *agentsv1alpha1.Toolset) toolsetSummary {
	return toolsetSummary{
		Name:            ts.Name,
		DisplayName:     ts.Spec.DisplayName,
		Families:        ts.Spec.Families,
		Connections:     ts.Spec.Connections,
		RequireApproval: ts.Spec.RequireApproval,
	}
}

func connectionView(cn *agentsv1alpha1.Connection) agentConnectionSummary {
	return agentConnectionSummary{
		Name:           cn.Name,
		Type:           cn.Spec.Type,
		DisplayName:    cn.Spec.DisplayName,
		Phase:          cn.Status.Phase,
		OAuthConnected: cn.Status.OAuthConnected,
	}
}
