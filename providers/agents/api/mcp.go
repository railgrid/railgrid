// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// MCP surface for the agents provider, mounted at /mcp. The hub's aggregate
// MCP endpoint probes every Ready provider's /mcp and federates its tools as
// "agents__<tool>", so this is what lets an MCP client — including an agent
// run reaching the aggregate — read and edit agent settings without the portal.
//
// The tools reuse the exact REST mutation path (applyAgentUpdate), so a field
// patched over MCP behaves identically to the same patch from the portal:
// absent fields are untouched, list fields replace wholesale, and the core
// family is always re-added to tool grants.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/llm"
)

// MCPHandler returns the streamable-HTTP MCP handler mounted at /mcp. A fresh
// server is built per request (Stateless) so each caller's identity is
// isolated; tools close over the request and resolve the tenant client lazily,
// so tools/list works even before identity headers are checked.
func (s *Server) MCPHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			srv := mcp.NewServer(&mcp.Implementation{
				Name:    "kedge-agents",
				Version: "0.1.0",
				Title:   "kedge agents provider",
			}, &mcp.ServerOptions{
				Instructions: "This MCP endpoint manages the AI agents hosted in your kedge " +
					"tenant workspace: their settings (system prompt, model credential, " +
					"autonomy, tool grants, channels, budgets, limits) plus the schedules, " +
					"connections, toolsets, and model credentials they reference. Use " +
					"list_agents / get_agent to inspect configuration and update_agent to " +
					"change it — only the fields you pass change, list fields replace " +
					"wholesale. Names for modelCredential, connections, and toolsets must " +
					"come from the corresponding list_* tool. Tenant identity is taken from " +
					"your bearer token — never ask the user for a tenant path. Creating " +
					"agents and pasting credentials stay in the portal.",
			})
			s.registerMCPTools(srv, r)
			return srv
		},
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
}

// mcpClient resolves the caller's tenant client from the identity the hub (or
// its federation client) put on the MCP request. Unlike requireClient it does
// not demand a parseable tenant path: federation forwards the cluster ID as
// both X-Kedge-Tenant and X-Kedge-Cluster, and the GraphQL client only needs
// the cluster ID plus the caller's token.
func (s *Server) mcpClient(r *http.Request) (*agentsclient.Client, error) {
	if s.gql == nil {
		return nil, errors.New("tenant access not configured — provider has no hub URL (set KEDGE_HUB_URL)")
	}
	clusterID := strings.TrimSpace(r.Header.Get("X-Kedge-Cluster"))
	if clusterID == "" {
		return nil, errors.New("no workspace cluster on this request (X-Kedge-Cluster missing) — cannot address the tenant workspace")
	}
	if bearerToken(r) == "" {
		return nil, errors.New("no bearer token on this request — the MCP request must carry the caller's credentials")
	}
	scope, err := s.gql.For(clusterID, bearerToken(r))
	if err != nil {
		return nil, err
	}
	return agentsclient.NewFromGraphQL(scope), nil
}

// agentSummary is one row of list_agents.
type agentSummary struct {
	Name            string `json:"name"`
	DisplayName     string `json:"displayName,omitempty"`
	Description     string `json:"description,omitempty"`
	Autonomy        string `json:"autonomy,omitempty"`
	ChatModel       string `json:"chatModel,omitempty"`
	Phase           string `json:"phase,omitempty"`
	SuspendedReason string `json:"suspendedReason,omitempty"`
}

type listAgentsOutput struct {
	Agents []agentSummary `json:"agents"`
}

// agentSettings is the full editable view get_agent and update_agent return.
type agentSettings struct {
	Name            string                         `json:"name"`
	DisplayName     string                         `json:"displayName,omitempty"`
	Description     string                         `json:"description,omitempty"`
	SystemPrompt    string                         `json:"systemPrompt,omitempty"`
	Autonomy        string                         `json:"autonomy,omitempty"`
	Models          map[string]string              `json:"models,omitempty"`
	ModelFallbacks  []string                       `json:"modelFallbacks,omitempty"`
	Delegates       []string                       `json:"delegates,omitempty"`
	Channels        []channelInput                 `json:"channels,omitempty"`
	Tools           agentsv1alpha1.AgentToolPolicy `json:"tools,omitzero"`
	Limits          agentsv1alpha1.AgentLimits     `json:"limits,omitzero"`
	Budget          *agentsv1alpha1.AgentBudget    `json:"budget,omitempty"`
	Phase           string                         `json:"phase,omitempty"`
	SuspendedReason string                         `json:"suspendedReason,omitempty"`
}

func settingsView(a *agentsv1alpha1.Agent) agentSettings {
	chans := make([]channelInput, 0, len(a.Spec.Channels))
	for _, ch := range a.Spec.Channels {
		chans = append(chans, channelInput{Name: ch.Name, ConnectionRef: ch.ConnectionRef, Primary: ch.Primary})
	}
	return agentSettings{
		Name:            a.Name,
		DisplayName:     a.Spec.DisplayName,
		Description:     a.Spec.Description,
		SystemPrompt:    a.Spec.SystemPrompt,
		Autonomy:        a.Spec.Autonomy,
		Models:          a.Spec.Models,
		ModelFallbacks:  a.Spec.ModelFallbacks,
		Delegates:       a.Spec.Delegates,
		Channels:        chans,
		Tools:           a.Spec.Tools,
		Limits:          a.Spec.Limits,
		Budget:          a.Spec.Budget,
		Phase:           a.Status.Phase,
		SuspendedReason: a.Status.SuspendedReason,
	}
}

type getAgentInput struct {
	Name string `json:"name" jsonschema:"Name of the agent"`
}

// updateAgentInput mirrors updateAgentRequest: only fields present in the call
// are applied.
type updateAgentInput struct {
	Name                   string          `json:"name" jsonschema:"Name of the agent to update"`
	DisplayName            *string         `json:"displayName,omitempty" jsonschema:"Human-readable agent name"`
	Description            *string         `json:"description,omitempty" jsonschema:"Short summary of what this agent is for"`
	SystemPrompt           *string         `json:"systemPrompt,omitempty" jsonschema:"The agent's persona and standing instructions, injected at the head of every run"`
	Autonomy               *string         `json:"autonomy,omitempty" jsonschema:"Action posture: suggest (drafts only), ask (approval-gated), or auto"`
	ModelCredential        *string         `json:"modelCredential,omitempty" jsonschema:"Named model credential for chat (see list_model_credentials); empty string unassigns"`
	ModelFallbacks         *[]string       `json:"modelFallbacks,omitempty" jsonschema:"Ordered credential names tried when the primary model fails; replaces the whole list"`
	BudgetTokens           *int64          `json:"budgetTokens,omitempty" jsonschema:"Token cap per rolling month; 0 removes the token cap"`
	BudgetUSD              *string         `json:"budgetUSD,omitempty" jsonschema:"USD spend cap per rolling month as a decimal string; empty removes the cost cap"`
	MaxToolTurns           *int32          `json:"maxToolTurns,omitempty" jsonschema:"Cap on tool-call iterations per run; 0 uses the provider default"`
	TimeoutSeconds         *int32          `json:"timeoutSeconds,omitempty" jsonschema:"Wall-clock budget for one run in seconds; 0 uses the provider default"`
	Delegates              *[]string       `json:"delegates,omitempty" jsonschema:"Names of other agents this agent may spawn as sub-agents; replaces the whole list"`
	Channels               *[]channelInput `json:"channels,omitempty" jsonschema:"Messaging channel bindings (name + connectionRef + primary); replaces the whole list"`
	InteractiveFamilies    *[]string       `json:"interactiveFamilies,omitempty" jsonschema:"Built-in tool families for interactive runs (core, web, github, mcp, files, edges); replaces the list, core is always kept"`
	BackgroundFamilies     *[]string       `json:"backgroundFamilies,omitempty" jsonschema:"Built-in tool families for background runs; replaces the list, core is always kept"`
	InteractiveToolsets    *[]string       `json:"interactiveToolsets,omitempty" jsonschema:"Shared Toolset names linked for interactive runs; replaces the whole list"`
	BackgroundToolsets     *[]string       `json:"backgroundToolsets,omitempty" jsonschema:"Shared Toolset names linked for background runs; replaces the whole list"`
	InteractiveConnections *[]string       `json:"interactiveConnections,omitempty" jsonschema:"Connection names granted directly for interactive runs; replaces the whole list"`
	BackgroundConnections  *[]string       `json:"backgroundConnections,omitempty" jsonschema:"Connection names granted directly for background runs; replaces the whole list"`
}

type credentialSummary struct {
	Name      string `json:"name"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	HasAPIKey bool   `json:"hasAPIKey"`
}

type listCredentialsOutput struct {
	Credentials []credentialSummary `json:"credentials"`
}

type agentConnectionSummary struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	DisplayName    string `json:"displayName,omitempty"`
	Phase          string `json:"phase,omitempty"`
	OAuthConnected bool   `json:"oauthConnected,omitempty"`
}

type listAgentConnectionsOutput struct {
	Connections []agentConnectionSummary `json:"connections"`
}

type toolsetSummary struct {
	Name            string   `json:"name"`
	DisplayName     string   `json:"displayName,omitempty"`
	Families        []string `json:"families,omitempty"`
	Connections     []string `json:"connections,omitempty"`
	RequireApproval []string `json:"requireApproval,omitempty"`
}

type listToolsetsOutput struct {
	Toolsets []toolsetSummary `json:"toolsets"`
}

type scheduleSummary struct {
	Name     string `json:"name"`
	AgentRef string `json:"agentRef"`
	Type     string `json:"type"`
	Schedule string `json:"schedule,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
	Task     string `json:"task,omitempty"`
	Suspend  bool   `json:"suspend,omitempty"`
	NextRun  string `json:"nextRun,omitempty"`
	LastRun  string `json:"lastRun,omitempty"`
}

type listSchedulesOutput struct {
	Schedules []scheduleSummary `json:"schedules"`
}

func (s *Server) registerMCPTools(srv *mcp.Server, r *http.Request) {
	yes := true
	no := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &yes}
	mutating := &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &yes}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_agents",
		Title:       "List agents",
		Description: "List the AI agents configured in your workspace with their phase, autonomy posture, and assigned chat model credential.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listAgentsOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, listAgentsOutput{}, err
		}
		list, err := c.Agents().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, listAgentsOutput{}, err
		}
		out := listAgentsOutput{Agents: make([]agentSummary, 0, len(list.Items))}
		for i := range list.Items {
			a := &list.Items[i]
			out.Agents = append(out.Agents, agentSummary{
				Name:            a.Name,
				DisplayName:     a.Spec.DisplayName,
				Description:     a.Spec.Description,
				Autonomy:        a.Spec.Autonomy,
				ChatModel:       a.Spec.Models["chat"],
				Phase:           a.Status.Phase,
				SuspendedReason: a.Status.SuspendedReason,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_agent",
		Title:       "Get agent settings",
		Description: "Read one agent's full settings: system prompt, model assignments, autonomy, tool grants, channels, limits, and budget.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getAgentInput) (*mcp.CallToolResult, agentSettings, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, agentSettings{}, err
		}
		a, err := c.Agents().Get(ctx, strings.TrimSpace(in.Name), metav1.GetOptions{})
		if err != nil {
			return nil, agentSettings{}, err
		}
		return nil, settingsView(a), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "update_agent",
		Title: "Update agent settings",
		Description: "Patch an agent's settings. Only the fields you pass change; list fields (fallbacks, delegates, channels, families, toolsets, connections) replace the stored list wholesale, so read the agent first when appending. " +
			"Semantics are identical to the portal's save.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateAgentInput) (*mcp.CallToolResult, agentSettings, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, agentSettings{}, err
		}
		req := &updateAgentRequest{
			ModelCredential:        in.ModelCredential,
			ModelFallbacks:         in.ModelFallbacks,
			SystemPrompt:           in.SystemPrompt,
			Description:            in.Description,
			Autonomy:               in.Autonomy,
			BudgetTokens:           in.BudgetTokens,
			BudgetUSD:              in.BudgetUSD,
			MaxToolTurns:           in.MaxToolTurns,
			TimeoutSeconds:         in.TimeoutSeconds,
			Channels:               in.Channels,
			Delegates:              in.Delegates,
			DisplayName:            in.DisplayName,
			InteractiveFamilies:    in.InteractiveFamilies,
			BackgroundFamilies:     in.BackgroundFamilies,
			InteractiveToolsets:    in.InteractiveToolsets,
			BackgroundToolsets:     in.BackgroundToolsets,
			InteractiveConnections: in.InteractiveConnections,
			BackgroundConnections:  in.BackgroundConnections,
		}
		a, err := s.applyAgentUpdate(ctx, c, strings.TrimSpace(in.Name), req)
		if err != nil {
			return nil, agentSettings{}, err
		}
		return nil, settingsView(a), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_model_credentials",
		Title:       "List model credentials",
		Description: "List the named model credentials agents can be assigned (keys are never returned). Use these names for update_agent's modelCredential and modelFallbacks.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listCredentialsOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, listCredentialsOutput{}, err
		}
		secrets, err := c.ListSecrets(ctx, llm.SecretNamespace)
		if err != nil {
			return nil, listCredentialsOutput{}, err
		}
		out := listCredentialsOutput{Credentials: []credentialSummary{}}
		for i := range secrets {
			sec := &secrets[i]
			if !strings.HasPrefix(sec.Name, llm.ModelCredentialPrefix) {
				continue
			}
			get := func(k string) string { return strings.TrimSpace(string(sec.Data[k])) }
			out.Credentials = append(out.Credentials, credentialSummary{
				Name:      strings.TrimPrefix(sec.Name, llm.ModelCredentialPrefix),
				Provider:  get("provider"),
				Model:     get("model"),
				HasAPIKey: get("apiKey") != "",
			})
		}
		sort.Slice(out.Credentials, func(i, j int) bool { return out.Credentials[i].Name < out.Credentials[j].Name })
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_connections",
		Title:       "List connections",
		Description: "List the external connections (messaging, github, mcp, http, …) agents can be granted or use as channels.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listAgentConnectionsOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, listAgentConnectionsOutput{}, err
		}
		list, err := c.Connections().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, listAgentConnectionsOutput{}, err
		}
		out := listAgentConnectionsOutput{Connections: make([]agentConnectionSummary, 0, len(list.Items))}
		for i := range list.Items {
			cn := &list.Items[i]
			out.Connections = append(out.Connections, agentConnectionSummary{
				Name:           cn.Name,
				Type:           cn.Spec.Type,
				DisplayName:    cn.Spec.DisplayName,
				Phase:          cn.Status.Phase,
				OAuthConnected: cn.Status.OAuthConnected,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_toolsets",
		Title:       "List toolsets",
		Description: "List the shared toolsets (reusable bundles of tool families, connections, and approval rules) agents can link.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listToolsetsOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, listToolsetsOutput{}, err
		}
		list, err := c.Toolsets().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, listToolsetsOutput{}, err
		}
		out := listToolsetsOutput{Toolsets: make([]toolsetSummary, 0, len(list.Items))}
		for i := range list.Items {
			t := &list.Items[i]
			out.Toolsets = append(out.Toolsets, toolsetSummary{
				Name:            t.Name,
				DisplayName:     t.Spec.DisplayName,
				Families:        t.Spec.Families,
				Connections:     t.Spec.Connections,
				RequireApproval: t.Spec.RequireApproval,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_schedules",
		Title:       "List schedules",
		Description: "List the schedules (cron / one-shot / heartbeat) that run agents autonomously, with their next and last run times.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listSchedulesOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, listSchedulesOutput{}, err
		}
		list, err := c.Schedules().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, listSchedulesOutput{}, err
		}
		out := listSchedulesOutput{Schedules: make([]scheduleSummary, 0, len(list.Items))}
		for i := range list.Items {
			sch := &list.Items[i]
			row := scheduleSummary{
				Name:     sch.Name,
				AgentRef: sch.Spec.AgentRef,
				Type:     sch.Spec.Type,
				Schedule: sch.Spec.Schedule,
				TimeZone: sch.Spec.TimeZone,
				Task:     sch.Spec.Task,
				Suspend:  sch.Spec.Suspend,
			}
			if sch.Status.NextRun != nil {
				row.NextRun = sch.Status.NextRun.UTC().Format("2006-01-02T15:04:05Z")
			}
			if sch.Status.LastRun != nil {
				row.LastRun = sch.Status.LastRun.UTC().Format("2006-01-02T15:04:05Z")
			}
			out.Schedules = append(out.Schedules, row)
		}
		return nil, out, nil
	})
}
