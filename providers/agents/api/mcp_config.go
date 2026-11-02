// Copyright 2026 The Railgrid Authors.
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
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

// mcpIdentity reconstructs the caller identity for tools that touch
// store-scoped data or mint cluster-scoped URLs. The hub identifies the
// tenant by cluster ID only (X-Railgrid-Tenant and X-Railgrid-Cluster carry the
// same value), so the org/workspace UUIDs come from kcp — the workspace's
// LogicalCluster read as the caller — with the cluster→tenant mapping the
// portal records on every REST call (the same mapping background execution
// uses) as the fallback when that lookup is unavailable.
func (s *Server) mcpIdentity(ctx context.Context, r *http.Request) identity {
	id := identity{
		tenant:    strings.TrimSpace(r.Header.Get("X-Railgrid-Tenant")),
		clusterID: strings.TrimSpace(r.Header.Get("X-Railgrid-Cluster")),
		user:      strings.TrimSpace(r.Header.Get("X-Railgrid-User")),
		token:     bearerToken(r),
	}
	if id.clusterID == "" {
		id.clusterID = id.tenant
	}
	s.resolveWorkspace(ctx, &id)
	if id.orgUUID == "" || id.workspaceUUID == "" {
		if ref, ok, err := s.tenantRef(ctx, id.clusterID); err == nil && ok {
			id.orgUUID, id.workspaceUUID = ref.OrgUUID, ref.WorkspaceUUID
		} else if err != nil {
			id.orgUUID, id.workspaceUUID = unmappedOrg, id.clusterID
			log.Printf("agents: migrating tenant scope for cluster %s: %v", id.clusterID, err)
		}
		return id
	}
	// This is the one class that still resolves the workspace as a caller, so
	// record what it learned: data-plane verbs and background execution have
	// no caller to read the LogicalCluster as and key their rows on this
	// mapping (resolveClusterScope, background.scopeFor).
	if id.workspacePath != "" && s.store != nil {
		ref := store.TenantRef{
			OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID, UpdatedAt: time.Now().UTC(),
		}
		if err := s.store.SaveTenantRef(ctx, id.clusterID, ref); err != nil {
			// Keep this request on the exact fallback scope where existing data
			// still lives. The next resolution retries the atomic migration; using
			// the newly learned scope here would make a failed migration look like
			// empty history and could defeat idempotency.
			log.Printf("agents: migrating tenant scope for cluster %s: %v", id.clusterID, err)
			id.orgUUID, id.workspaceUUID, id.workspacePath = unmappedOrg, id.clusterID, ""
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
	MaxToolTurns    int32          `json:"maxToolTurns,omitempty" jsonschema:"Cap on tool-call iterations per run; 0 uses the provider default"`
	TimeoutSeconds  int32          `json:"timeoutSeconds,omitempty" jsonschema:"Wall-clock budget for one run in seconds; 0 uses the provider default"`
	Channels        []channelInput `json:"channels,omitempty" jsonschema:"Messaging channel bindings (name + connectionRef + primary)"`
	// Templates: see agent_templates.go.
	Template   string            `json:"template,omitempty" jsonschema:"Built-in template to start from (see list_agent_templates). Its prompt, autonomy, grants and schedules are applied first; the other fields here override them"`
	Parameters map[string]string `json:"parameters,omitempty" jsonschema:"Template parameter values by key; a list parameter is one item per line"`
	Wiring     map[string]string `json:"wiring,omitempty" jsonschema:"Template wiring by slot key: model credential, connection and channel names, or <Kind>/<name> for an edge. modelCredential and channels above fill their slots when left out here"`
}

type nameInput struct {
	Name string `json:"name" jsonschema:"Resource name"`
}

type deletedOutput struct {
	Deleted string `json:"deleted"`
}

// --- triggers ---

type triggerSummary struct {
	Name     string `json:"name"`
	AgentRef string `json:"agentRef"`
	// WebhookRef is the Webhook the trigger routes from; its inbound URL is
	// on the webhook (list_webhooks).
	WebhookRef string            `json:"webhookRef"`
	Filter     map[string]string `json:"filter,omitempty"`
	Task       string            `json:"task,omitempty"`
	ChannelRef string            `json:"channelRef,omitempty"`
	Suspend    bool              `json:"suspend"`
	LastFired  string            `json:"lastFired,omitempty"`
}

type listTriggersOutput struct {
	Triggers []triggerSummary `json:"triggers"`
}

type createTriggerInput struct {
	Name       string            `json:"name" jsonschema:"Lowercase trigger name, e.g. deploy-failed"`
	AgentRef   string            `json:"agentRef" jsonschema:"Name of the agent this trigger runs (see list_agents)"`
	WebhookRef string            `json:"webhookRef" jsonschema:"Name of the webhook this trigger routes from (see list_webhooks; create one with create_webhook). Many triggers may share one webhook"`
	Filter     map[string]string `json:"filter,omitempty" jsonschema:"Conditions every delivery must pass: eventType (event header or body type/action), action (body action), repository (body repository.full_name owner/repo), match (payload substring), header.<name>. eventType/action/repository take a comma-separated any-of list, e.g. {\"eventType\":\"pull_request\",\"action\":\"opened,synchronize\"}. Any other key is rejected"`
	Task       string            `json:"task,omitempty" jsonschema:"The prompt the agent runs when the trigger fires; the event payload is appended"`
	Suspend    bool              `json:"suspend,omitempty" jsonschema:"Create the trigger paused"`
	ChannelRef string            `json:"channelRef,omitempty" jsonschema:"Agent channel this trigger's output goes to; empty means the primary channel"`
}

type updateTriggerInput struct {
	Name       string             `json:"name" jsonschema:"Name of the trigger to update (see list_triggers)"`
	Task       *string            `json:"task,omitempty" jsonschema:"New prompt to run when the trigger fires"`
	WebhookRef *string            `json:"webhookRef,omitempty" jsonschema:"Move the trigger to another webhook (see list_webhooks)"`
	Filter     *map[string]string `json:"filter,omitempty" jsonschema:"Replacement filter map (same keys as create_trigger); replaces the whole map"`
	Suspend    *bool              `json:"suspend,omitempty" jsonschema:"true pauses the trigger without deleting it, false resumes it"`
	ChannelRef *string            `json:"channelRef,omitempty" jsonschema:"Agent channel for this trigger's output; empty string means the primary channel"`
}

func triggerView(t *agentsv1alpha1.Trigger) triggerSummary {
	row := triggerSummary{
		Name:       t.Name,
		AgentRef:   t.Spec.AgentRef,
		WebhookRef: t.Spec.WebhookRef,
		Filter:     t.Spec.Filter,
		Task:       t.Spec.Task,
		ChannelRef: t.Spec.ChannelRef,
		Suspend:    t.Spec.Suspend,
	}
	if t.Status.LastFired != nil {
		row.LastFired = t.Status.LastFired.UTC().Format("2006-01-02T15:04:05Z")
	}
	return row
}

// --- webhooks ---

type webhookSummary struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	DisplayName string `json:"displayName,omitempty"`
	// DeliveryRetention is the webhook's own cap; Retention is what applies.
	DeliveryRetention *int32 `json:"deliveryRetention,omitempty"`
	Retention         int    `json:"retention"`
	// WebhookPath is the hub-relative inbound URL to register with the
	// sender. It embeds the webhook's secret, so treat it as a credential.
	WebhookPath string `json:"webhookPath,omitempty"`
	// Routes names the triggers routing from this webhook.
	Routes       []string `json:"routes"`
	LastReceived string   `json:"lastReceived,omitempty"`
	LastOutcome  string   `json:"lastOutcome,omitempty"`
}

type listWebhooksOutput struct {
	Webhooks []webhookSummary `json:"webhooks"`
}

type createWebhookInput struct {
	Name              string `json:"name" jsonschema:"Lowercase webhook name, e.g. github-railgrid"`
	Source            string `json:"source" jsonschema:"webhook (any HTTP POST) or github (GitHub webhook deliveries; the portal offers GitHub's event catalog)"`
	DisplayName       string `json:"displayName,omitempty" jsonschema:"Human-readable label"`
	DeliveryRetention *int32 `json:"deliveryRetention,omitempty" jsonschema:"How many recent deliveries to keep (0 to 1000); unset means the provider default, 0 keeps none"`
	RecordBodies      *bool  `json:"recordBodies,omitempty" jsonschema:"Whether the sink keeps each delivery's payload (default true). false keeps headers, event coordinates and every route's verdict but no body: for a sender whose payloads carry secrets or personal data, since anyone who can read the webhook can read its sink"`
}

type updateWebhookInput struct {
	Name              string  `json:"name" jsonschema:"Name of the webhook to update (see list_webhooks)"`
	DisplayName       *string `json:"displayName,omitempty" jsonschema:"New human-readable label"`
	DeliveryRetention *int32  `json:"deliveryRetention,omitempty" jsonschema:"New delivery cap (0 to 1000); 0 keeps none"`
	RecordBodies      *bool   `json:"recordBodies,omitempty" jsonschema:"Whether the sink keeps each delivery's payload from now on; false keeps headers and verdicts only"`
}

type listWebhookDeliveriesInput struct {
	Name    string `json:"name" jsonschema:"Name of the webhook (see list_webhooks)"`
	Outcome string `json:"outcome,omitempty" jsonschema:"Keep only deliveries with this rollup outcome: fired, filtered, unrouted or failed. Empty keeps all"`
	Trigger string `json:"trigger,omitempty" jsonschema:"Keep only deliveries routed to this trigger"`
	Limit   int    `json:"limit,omitempty" jsonschema:"How many recent deliveries to return, newest first (default 20, max 200)"`
}

// webhookDeliverySummary is one sink entry as the MCP tool reports it: the
// metadata, the routes and a body preview, never the recorded headers (they
// may name a redacted credential header, which a model has no use for).
type webhookDeliverySummary struct {
	ID         string               `json:"id"`
	ReceivedAt string               `json:"receivedAt"`
	Outcome    string               `json:"outcome"`
	Reason     string               `json:"reason,omitempty"`
	DeliveryID string               `json:"deliveryID,omitempty"`
	EventType  string               `json:"eventType,omitempty"`
	Action     string               `json:"action,omitempty"`
	Repository string               `json:"repository,omitempty"`
	ReplayOf   string               `json:"replayOf,omitempty"`
	Routes     []store.WebhookRoute `json:"routes"`
	// BodyPreview is the opening of the payload; BodySize is its full length.
	BodyPreview string `json:"bodyPreview,omitempty"`
	BodySize    int    `json:"bodySize"`
}

type listWebhookDeliveriesOutput struct {
	Webhook string `json:"webhook"`
	// Retention is how many deliveries the provider keeps for this webhook.
	Retention  int                      `json:"retention"`
	Deliveries []webhookDeliverySummary `json:"deliveries"`
}

func (s *Server) webhookView(w *agentsv1alpha1.Webhook) webhookSummary {
	row := webhookSummary{
		Name:              w.Name,
		Source:            w.Spec.Source,
		DisplayName:       w.Spec.DisplayName,
		DeliveryRetention: w.Spec.DeliveryRetention,
		Retention:         s.webhookRetention(w),
		WebhookPath:       w.Status.WebhookPath,
		Routes:            make([]string, 0, len(w.Status.Routes)),
	}
	for _, r := range w.Status.Routes {
		row.Routes = append(row.Routes, r.Name)
	}
	if w.Status.LastReceived != nil {
		row.LastReceived = w.Status.LastReceived.UTC().Format("2006-01-02T15:04:05Z")
	}
	if w.Status.LastDelivery != nil {
		row.LastOutcome = w.Status.LastDelivery.Outcome
	}
	return row
}

// checkWebhookRetention refuses a retention outside what the schema allows,
// with a message that names the bounds instead of the schema's.
func checkWebhookRetention(n *int32) error {
	if n != nil && (*n < 0 || *n > MaxTriggerDeliveryRetention) {
		return errBadRequest(fmt.Sprintf("deliveryRetention must be between 0 and %d", MaxTriggerDeliveryRetention))
	}
	return nil
}

// --- toolsets / connections / credentials ---

type createToolsetInput struct {
	Name            string   `json:"name" jsonschema:"Lowercase toolset name, e.g. ops-tools"`
	DisplayName     string   `json:"displayName,omitempty" jsonschema:"Human-readable name"`
	Description     string   `json:"description,omitempty" jsonschema:"What this bundle is for"`
	Families        []string `json:"families,omitempty" jsonschema:"Built-in tool families: core, web, github, mcp, files, edges, spawn, visualization"`
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
	// SigningSecret verifies inbound Slack events; Telegram secret tokens are generated.
	SigningSecret string `json:"signingSecret,omitempty" jsonschema:"Slack only: the app signing secret (Basic Information → Signing Secret) used to verify inbound events. Write-only"`
}

type updateConnectionInput struct {
	Name        string             `json:"name" jsonschema:"Name of the connection to update (see list_connections)"`
	DisplayName *string            `json:"displayName,omitempty" jsonschema:"New human-readable name"`
	BaseURL     *string            `json:"baseURL,omitempty" jsonschema:"New endpoint URL"`
	Channel     *string            `json:"channel,omitempty" jsonschema:"New delivery target"`
	Config      *map[string]string `json:"config,omitempty" jsonschema:"Replacement config map; replaces the whole map"`
	Secret      *string            `json:"secret,omitempty" jsonschema:"Rotate the credential. Write-only; omit to keep the current one"`
	// SigningSecret sets or rotates the Slack app signing secret.
	SigningSecret *string `json:"signingSecret,omitempty" jsonschema:"Slack only: set or rotate the app signing secret used to verify inbound events. Write-only"`
}

type saveCredentialInput struct {
	AnthropicWorkspaceID string `json:"anthropicWorkspaceID,omitempty" jsonschema:"Anthropic workspace ID (wrkspc_...), required for keys not scoped to a workspace; sent only to api.anthropic.com"`
	Name                 string `json:"name" jsonschema:"Credential name agents reference, e.g. anthropic-main"`
	Model                string `json:"model" jsonschema:"Model id served by this endpoint, e.g. claude-sonnet-5"`
	Provider             string `json:"provider,omitempty" jsonschema:"Provider id; defaults to the OpenAI-compatible driver"`
	BaseURL              string `json:"baseURL,omitempty" jsonschema:"API base URL; empty means the provider default"`
	APIKey               string `json:"apiKey,omitempty" jsonschema:"The API key. Write-only: stored in a Secret and never returned. Omit to keep the existing key when updating"`
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
	// Providers are the railgrid providers reachable through the hub's aggregate
	// tool endpoint, which every interactive run gets for free.
	Providers []string `json:"providers,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// toolFamilyDocs describes each grantable family in the terms a caller needs to
// choose between them.
var toolFamilyDocs = []toolFamilyInfo{
	{"visualization", "visualize_data: render bar, line, area, scatter, or pie charts from supplied data inline in Agents chat, with a data table and SVG export. No connection required; opt-in per interactive/background grant."},
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
		Description: "Create a new agent. The well-configured way is to start from a template (template, from list_agent_templates) and wire in parameters and wiring; the agent then arrives with its prompt, autonomy, tool grants and schedules set. " +
			"Otherwise assign its model with modelCredential (from list_model_credentials), bind messaging channels with channels, and bound runs with maxToolTurns / timeoutSeconds. " +
			"Every explicit field overrides the template's value. Tool grants and prompts can be changed afterwards with update_agent.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createAgentInput) (*mcp.CallToolResult, agentSettings, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, agentSettings{}, err
		}
		req := createAgentRequest{
			Name: in.Name, DisplayName: in.DisplayName, Description: in.Description,
			SystemPrompt: in.SystemPrompt, Autonomy: in.Autonomy,
			ModelCredential: in.ModelCredential, ModelFallbacks: in.ModelFallbacks,
			BudgetTokens: in.BudgetTokens, BudgetUSD: in.BudgetUSD, Channels: in.Channels,
			MaxToolTurns: in.MaxToolTurns, TimeoutSeconds: in.TimeoutSeconds,
			Template: in.Template, Parameters: in.Parameters, Wiring: in.Wiring,
		}
		a, report, err := s.createAgentFromRequest(ctx, c, &req)
		if err != nil {
			return nil, agentSettings{}, err
		}
		view := settingsView(a)
		view.CreatedSchedules = report.Schedules
		view.Warnings = report.Warnings
		return nil, view, nil
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
		Description: "List the event triggers that run agents on deliveries to a webhook, with the webhook each routes from and its filter.",
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
		Description: "Create an event trigger that runs an agent on deliveries to a webhook (webhookRef, from list_webhooks or create_webhook). Several triggers can route from one webhook, each with its own filter: " +
			"for a github webhook filter on eventType such as pull_request or issues, plus action and repository. Unknown filter keys are rejected.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createTriggerInput) (*mcp.CallToolResult, triggerSummary, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, triggerSummary{}, err
		}
		// Field-for-field mirror of createTriggerRequest (only the jsonschema
		// tags differ), so the conversion keeps the two in lockstep.
		req := createTriggerRequest(in)
		t, err := s.applyTriggerCreate(ctx, c, &req)
		if err != nil {
			return nil, triggerSummary{}, err
		}
		return nil, triggerView(t), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_trigger",
		Title:       "Update a trigger",
		Description: "Edit an existing trigger in place: rewrite its task, change its filter, move it to another webhook, or pause/resume it with suspend. Only the fields you pass change.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateTriggerInput) (*mcp.CallToolResult, triggerSummary, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, triggerSummary{}, err
		}
		req := updateTriggerRequest{
			Task: in.Task, WebhookRef: in.WebhookRef, Filter: in.Filter, Suspend: in.Suspend, ChannelRef: in.ChannelRef,
		}
		t, err := s.applyTriggerUpdate(ctx, c, in.Name, &req)
		if err != nil {
			return nil, triggerSummary{}, err
		}
		return nil, triggerView(t), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_trigger",
		Title:       "Delete a trigger",
		Description: "Permanently delete a trigger. Its webhook keeps its URL and its other triggers. To stop it temporarily, prefer update_trigger with suspend=true.",
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

	// --- webhooks ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_webhooks",
		Title:       "List webhooks",
		Description: "List the inbound webhooks: each one URL (webhookPath — it embeds a secret, so share it carefully) that triggers route from, with the triggers routing from it and its most recent delivery.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listWebhooksOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, listWebhooksOutput{}, err
		}
		list, err := c.Webhooks().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, listWebhooksOutput{}, err
		}
		out := listWebhooksOutput{Webhooks: make([]webhookSummary, 0, len(list.Items))}
		for i := range list.Items {
			out.Webhooks = append(out.Webhooks, s.webhookView(&list.Items[i]))
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "create_webhook",
		Title: "Create a webhook",
		Description: "Create an inbound webhook: one URL to register with a sender (source \"webhook\" for any HTTP POST, \"github\" for GitHub). Triggers then route from it by webhookRef. " +
			"The provider mints the URL a moment after creation; read it back with list_webhooks (webhookPath).",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createWebhookInput) (*mcp.CallToolResult, webhookSummary, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, webhookSummary{}, err
		}
		name, source := strings.TrimSpace(in.Name), strings.TrimSpace(in.Source)
		if name == "" || source == "" {
			return nil, webhookSummary{}, errBadRequest("name and source are required")
		}
		if source != agentsv1alpha1.WebhookSourceWebhook && source != agentsv1alpha1.WebhookSourceGitHub {
			return nil, webhookSummary{}, errBadRequest("unsupported source " + source + " (use webhook or github)")
		}
		if err := checkWebhookRetention(in.DeliveryRetention); err != nil {
			return nil, webhookSummary{}, err
		}
		wh, err := c.Webhooks().Create(ctx, &agentsv1alpha1.Webhook{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: agentsv1alpha1.WebhookSpec{
				Source: source, DisplayName: strings.TrimSpace(in.DisplayName), DeliveryRetention: in.DeliveryRetention,
				RecordBodies: in.RecordBodies,
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return nil, webhookSummary{}, err
		}
		return nil, s.webhookView(wh), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_webhook",
		Title:       "Update a webhook",
		Description: "Edit a webhook's label, delivery retention or whether payloads are recorded. Only the fields you pass change; the URL and the source stay as they are.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateWebhookInput) (*mcp.CallToolResult, webhookSummary, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, webhookSummary{}, err
		}
		if err := checkWebhookRetention(in.DeliveryRetention); err != nil {
			return nil, webhookSummary{}, err
		}
		wh, err := c.Webhooks().Get(ctx, strings.TrimSpace(in.Name), metav1.GetOptions{})
		if err != nil {
			return nil, webhookSummary{}, err
		}
		if in.DisplayName != nil {
			wh.Spec.DisplayName = strings.TrimSpace(*in.DisplayName)
		}
		if in.DeliveryRetention != nil {
			wh.Spec.DeliveryRetention = in.DeliveryRetention
		}
		if in.RecordBodies != nil {
			wh.Spec.RecordBodies = in.RecordBodies
		}
		out, err := c.Webhooks().Update(ctx, wh, metav1.UpdateOptions{})
		if err != nil {
			return nil, webhookSummary{}, err
		}
		return nil, s.webhookView(out), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_webhook",
		Title:       "Delete a webhook",
		Description: "Permanently delete a webhook: its URL stops answering and its recorded deliveries are discarded. Triggers routing from it stay, but receive nothing until pointed at another webhook.",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, deletedOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, deletedOutput{}, err
		}
		name := strings.TrimSpace(in.Name)
		if err := c.Webhooks().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return nil, deletedOutput{}, err
		}
		return nil, deletedOutput{Deleted: name}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "list_webhook_deliveries",
		Title: "List a webhook's recent deliveries",
		Description: "The webhook's inbound delivery sink, newest first: every authenticated POST to its URL and what each routed trigger made of it — fired (with the run it started), filtered (with the filter key that rejected it), or refused while paused/disabled. " +
			"This is how to tell \"the filter drops everything\" from \"nobody calls the URL\". Narrow to one trigger with trigger. Bodies are previewed; the portal shows them whole.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listWebhookDeliveriesInput) (*mcp.CallToolResult, listWebhookDeliveriesOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, listWebhookDeliveriesOutput{}, err
		}
		wh, err := c.Webhooks().Get(ctx, strings.TrimSpace(in.Name), metav1.GetOptions{})
		if err != nil {
			return nil, listWebhookDeliveriesOutput{}, err
		}
		id := s.mcpIdentity(ctx, r)
		if id.workspaceUUID == "" {
			return nil, listWebhookDeliveriesOutput{}, errBadRequest("this workspace is not mapped yet — open the agents UI once, then retry")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		rows, err := s.store.ListWebhookDeliveries(ctx, id.scope(""), string(wh.UID), store.WebhookDeliveryQuery{
			Outcome: strings.TrimSpace(in.Outcome), Trigger: strings.TrimSpace(in.Trigger), Limit: min(limit, maxDeliveriesPage),
		})
		if err != nil {
			return nil, listWebhookDeliveriesOutput{}, err
		}
		out := listWebhookDeliveriesOutput{Webhook: wh.Name, Retention: s.webhookRetention(wh), Deliveries: make([]webhookDeliverySummary, 0, len(rows))}
		for _, d := range rows {
			out.Deliveries = append(out.Deliveries, webhookDeliverySummary{
				ID: d.ID, ReceivedAt: d.ReceivedAt.UTC().Format("2006-01-02T15:04:05Z"), Outcome: d.Outcome, Reason: d.Reason,
				DeliveryID: d.DeliveryID, EventType: d.EventType, Action: d.Action, Repository: d.Repository,
				ReplayOf: d.ReplayOf, Routes: d.Routes, BodyPreview: d.Body, BodySize: d.BodySize,
			})
		}
		return nil, out, nil
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
		// Field-for-field mirror of toolsetRequest (only the jsonschema tags
		// differ), so the conversion keeps the two in lockstep.
		req := toolsetRequest(in)
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
			SigningSecret: in.SigningSecret,
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
			Config: in.Config, Secret: in.Secret, SigningSecret: in.SigningSecret,
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
			"Assign the result to an agent with update_agent's modelCredential. For a model using a saved provider connection, pass name and model only; edit the provider connection to change its endpoint or key.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in saveCredentialInput) (*mcp.CallToolResult, credentialSummary, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, credentialSummary{}, err
		}
		req := modelCredential{Name: in.Name, Provider: in.Provider, BaseURL: in.BaseURL, Model: in.Model, APIKey: in.APIKey, AnthropicWorkspaceID: in.AnthropicWorkspaceID}
		cred, err := applyCredentialUpsert(ctx, c, &req)
		if err != nil {
			return nil, credentialSummary{}, err
		}
		// Ready is deliberately absent here: the reconciler has not seen the
		// object yet, and claiming readiness a moment before anything verified
		// it is the mistake hasAPIKey used to make.
		return nil, credentialSummary{Name: cred.Name, Provider: cred.Provider, BaseURL: cred.BaseURL, Model: cred.Model, AnthropicWorkspaceID: cred.AnthropicWorkspaceID}, nil
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
		if err := deleteCredential(ctx, c, name); err != nil {
			return nil, deletedOutput{}, err
		}
		return nil, deletedOutput{Deleted: name}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "test_model_credential",
		Title: "Test a model credential",
		Description: "Health-check a model credential by listing the models its endpoint serves. Returns latency and curated agent model candidates. " +
			"Discovery filters non-chat modalities and known unsupported specialized model families. " +
			"Automatic Responses routing applies only to supported models and reasoning configurations on the official OpenAI endpoint; custom gateways default to Chat Completions.",
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
		models, latency, perr := llm.DiscoverModels(ctx, profile)
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
		// Best-effort: the workspace's MCPServer already publishes which railgrid
		// providers its aggregate endpoint reaches, so this is one cached read
		// of a bound object rather than a live MCP probe inside a discovery
		// call. An empty list means "could not tell", not "nothing enabled".
		if providers := s.federatedProviders(ctx, s.mcpIdentity(ctx, r)); len(providers) > 0 {
			out.Providers = providers
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
			SessionID: triggerSessionID(trig.Name, ""), Task: trig.Spec.Task,
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
	admission, err := s.startDetachedRun(r, c, id, agent, tr)
	if err != nil {
		return nil, runNowOutput{}, err
	}
	return nil, runNowOutput{
		RunID:  admission.ID,
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
