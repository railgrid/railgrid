// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/railgrid/railgrid/pkg/runner"

	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tools"
)

// A harness-backed agent configures itself over MCP. The harness brings its own
// tools and ignores the provider's (see backend/harness), so the
// self-configuration family cannot be handed to it the way an in-process run
// gets it. Instead every launch of a harness turn for an agent with
// spec.selfConfiguration enabled is given one more MCP server: this provider's
// /mcp, reached through the hub, with a bearer minted for that launch alone.
//
// The bearer is NOT a kcp credential. It is an opaque random string whose
// SHA-256 the store binds to one (workspace, agent, run). Presented to /mcp it
// selects a server that serves exactly that agent's self-configuration and
// schedule tools, executed as this provider through its export virtual
// workspace — the same tools, scopes, ownership checks and approval setting an
// in-process run gets. RBAC could not express any of that (it cannot limit
// which fields of the Agent a token may write, nor name-scope a create), which
// is why the decision is made here and the token reaches nothing else. A
// harness runs arbitrary commands on its machine and can read its own MCP
// configuration, so the token must be worth no more than the tools are.

// harnessMCPTokenPrefix marks a harness session bearer, so /mcp can tell it
// from a hub-forwarded caller token without a store lookup for every request.
const harnessMCPTokenPrefix = "rgagt_"

// The purpose is authenticated by the stored hash of the whole bearer. A
// completed review session must never fall back to self-configuration tools.
const harnessMCPReviewTokenPrefix = "rgagt_review_"

type harnessMCPReviewSessionKey struct{}

// harnessMCPServerName is how the harness addresses the server: Claude Code
// shows its tools as mcp__agent__<tool>. The runner refuses "railgrid", which
// is its own permission server.
const harnessMCPServerName = "agent"

// harnessMCPDefaultTTL bounds a session when the agent sets no run timeout. A
// parked turn that resumes later gets a fresh token with the relaunch.
const harnessMCPDefaultTTL = 4 * time.Hour

// harnessScheduleTools are the core tools a harness session also gets: an
// in-process run always has them, and "add a job" is the most common thing an
// agent configures about itself.
var harnessScheduleTools = []string{"schedules_list", "schedule_create", "schedule_update", "schedule_delete"}

type harnessMCPSessionKey struct{}

func hashHarnessMCPToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// capabilityReader is the part of the runner client that reports what the
// runner supports. Optional on dispatch.Runner, so fakes need not implement it.
type capabilityReader interface {
	Capabilities(ctx context.Context) (runner.Capabilities, error)
}

// harnessSelfMCPServers mints this launch's session and returns the server to
// attach, or nil when there is none to attach: self-configuration is off, the
// provider has no public hub URL to give a remote machine, or the runner is
// too old to accept the field (it would refuse the whole start otherwise).
// None of those fails the turn; the harness simply runs without the server.
func (s *Server) harnessSelfMCPServers(ctx context.Context, run taskRun, runID string, dispatcher any) []runner.MCPServer {
	agent := run.Agent
	reviewScope := tools.ParseCodeScope(run.Task)
	reviewEnabled := reviewScope != nil && reviewScope.ReviewOnly
	grant := agent.Spec.Tools.Background
	if isInteractive(run.Trigger) {
		grant = agent.Spec.Tools.Interactive
	}
	reviewEnabled = reviewEnabled && slices.Contains(grant.Families, "code")
	if sc := agent.Spec.SelfConfiguration; (sc == nil || !sc.Enabled) && !reviewEnabled {
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(s.cfg.HubPublicURL), "/")
	endpoint := strings.TrimSpace(s.cfg.HarnessMCPURL)
	if endpoint == "" && base != "" {
		endpoint = base + "/services/providers/agents/mcp"
	}
	if endpoint == "" {
		log.Printf("harness self-configuration: agent %s has it enabled, but RAILGRID_HUB_PUBLIC_URL is unset, so a remote harness has no address to reach it at", agent.Name)
		return nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		log.Printf("harness MCP endpoint is invalid; refusing session attachment")
		return nil
	}
	loopback := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
	secureTransport := parsed.Scheme == "https" || parsed.Scheme == "http" && loopback
	if parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !secureTransport {
		log.Printf("harness MCP endpoint is invalid; refusing session attachment")
		return nil
	}
	if caps, ok := dispatcher.(capabilityReader); ok {
		c, err := caps.Capabilities(ctx)
		if err != nil {
			log.Printf("harness self-configuration: reading runner capabilities for agent %s: %v", agent.Name, err)
			return nil
		}
		if reviewEnabled && !slices.Contains(c.Verification, runner.RemoteMCPApprovedToolsCapability) {
			log.Printf("harness review MCP requires runner capability %s", runner.RemoteMCPApprovedToolsCapability)
			return nil
		}
		if !slices.Contains(c.Verification, runner.RemoteMCPCapability) {
			log.Printf("harness self-configuration: the runner for agent %s does not support %s; upgrade it to let the harness configure the agent", agent.Name, runner.RemoteMCPCapability)
			return nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		log.Printf("harness self-configuration: minting a session token: %v", err)
		return nil
	}
	prefix := harnessMCPTokenPrefix
	if reviewEnabled {
		prefix = harnessMCPReviewTokenPrefix
	}
	token := prefix + base64.RawURLEncoding.EncodeToString(raw)
	ttl := harnessMCPDefaultTTL
	if t := time.Duration(agent.Spec.Limits.TimeoutSeconds) * time.Second; t > 0 {
		ttl = t + 10*time.Minute
	}
	now := time.Now().UTC()
	err = s.store.PutHarnessMCPSession(ctx, store.Scope{OrgUUID: run.Scope.OrgUUID, WorkspaceUUID: run.Scope.WorkspaceUUID}, store.HarnessMCPSession{
		TokenHash: hashHarnessMCPToken(token), ClusterID: run.ClusterID,
		AgentName: agent.Name, AgentUID: string(agent.UID), RunID: runID,
		Interactive: isInteractive(run.Trigger), ExpiresAt: now.Add(ttl), CreatedAt: now,
	})
	if err != nil {
		log.Printf("harness self-configuration: recording the session for agent %s: %v", agent.Name, err)
		return nil
	}
	server := runner.MCPServer{Name: harnessMCPServerName, URL: endpoint, Token: token}
	if reviewEnabled {
		server.ApprovedTools = []string{"code_pull_request"}
	}
	return []runner.MCPServer{server}
}

// harnessSessionMCP routes a request bearing a harness session token to that
// session's server and answers every other request with next. The lookup
// happens before the MCP layer so a stale or forged token is a plain 401, not
// a server with no tools.
func (s *Server) harnessSessionMCP(next http.Handler) http.Handler {
	self := mcp.NewStreamableHTTPHandler(s.harnessSelfServer, &mcp.StreamableHTTPOptions{Stateless: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if !strings.HasPrefix(token, harnessMCPTokenPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		sess, ok, err := s.store.GetHarnessMCPSession(r.Context(), hashHarnessMCPToken(token), time.Now().UTC())
		if err != nil {
			writeStatus(w, http.StatusInternalServerError, "InternalError", "looking up the harness session failed")
			return
		}
		if !ok {
			writeStatus(w, http.StatusUnauthorized, "Unauthorized", "this harness session has ended or its token is not valid")
			return
		}
		ctx := context.WithValue(r.Context(), harnessMCPSessionKey{}, sess)
		ctx = context.WithValue(ctx, harnessMCPReviewSessionKey{}, strings.HasPrefix(token, harnessMCPReviewTokenPrefix))
		self.ServeHTTP(w, r.WithContext(ctx))
	})
}

// harnessSelfServer builds the per-request server for one harness session: the
// session's agent, read fresh, and the tools its spec allows now — not when the
// turn started, so turning self-configuration off takes effect at once.
func (s *Server) harnessSelfServer(r *http.Request) *mcp.Server {
	sess, _ := r.Context().Value(harnessMCPSessionKey{}).(store.HarnessMCPSession)
	options := &mcp.ServerOptions{
		Instructions: "These tools change the railgrid agent you are running as: its system prompt and description, " +
			"its triggers and their filters, its memory settings, and its schedules (recurring jobs and one-shot wakeups). " +
			"Read self_config_get first. Changes take effect from your next turn. A change that needs the user's approval " +
			"is queued and applied when they approve it; do not retry it.",
	}
	if review, _ := r.Context().Value(harnessMCPReviewSessionKey{}).(bool); review {
		options.Instructions = "Use code_pull_request to observe this admitted PR and publish actionable findings as COMMENT reviews of the assigned commit. Zero findings produces no review. Factory manages the single PR status comment. No branch writes, status comments, self-configuration or further delegation are available."
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "railgrid-agent", Version: "0.1.0", Title: "scoped Agent tools"}, options)
	deps, err := s.harnessSessionDeps(r.Context(), sess)
	if err != nil {
		log.Printf("harness self-configuration: session for agent %s: %v", sess.AgentName, err)
		return srv
	}
	gated := selfConfigGated(deps.Agent, sess.Interactive)
	available := harnessSelfTools(deps)
	if deps.CodeScope != nil {
		available = nil
	}
	if deps.CodeScope != nil && deps.CodeScope.ReviewOnly {
		grant := deps.Agent.Spec.Tools.Background
		if sess.Interactive {
			grant = deps.Agent.Spec.Tools.Interactive
		}
		grant = s.expandToolsets(r.Context(), deps, grant)
		if slices.Contains(grant.Families, "code") || slices.Contains(grant.Families, "code-write") {
			available = append(available, tools.Code(deps, false)...)
		}
	}
	for _, t := range available {
		toolSchema := paramsSchema(t.Params)
		if t.JSONSchema != nil {
			toolSchema = t.JSONSchema
		}
		srv.AddTool(&mcp.Tool{Name: t.Name, Description: t.Desc, InputSchema: toolSchema},
			s.harnessToolHandler(sess, deps, t, gated && slices.Contains(tools.SelfConfigMutatingTools, t.Name)))
	}
	return srv
}

// harnessSessionDeps resolves the session's agent through this provider's
// export virtual workspace and refuses a session whose agent was recreated
// under the same name, or whose self-configuration has since been turned off.
func (s *Server) harnessSessionDeps(ctx context.Context, sess store.HarnessMCPSession) (tools.Deps, error) {
	if s.bg == nil {
		return tools.Deps{}, errors.New("this provider has no virtual-workspace access configured")
	}
	dyn, err := s.bg.scoped(ctx, sess.ClusterID)
	if err != nil {
		return tools.Deps{}, err
	}
	cr := vwCR{dyn}
	agent, err := cr.GetAgent(ctx, sess.AgentName)
	if err != nil {
		return tools.Deps{}, err
	}
	if string(agent.UID) != sess.AgentUID {
		return tools.Deps{}, fmt.Errorf("agent %s was recreated since this session began", sess.AgentName)
	}
	scope := store.Scope{OrgUUID: sess.Scope.OrgUUID, WorkspaceUUID: sess.Scope.WorkspaceUUID, AgentName: agent.Name}
	deps := tools.Deps{Store: s.store, Scope: scope, Agent: agent, CR: cr, Secrets: vwSecrets{dyn}, RunID: sess.RunID}
	if run, err := s.store.GetRun(ctx, scope, sess.RunID); err == nil && run.Phase == store.RunPhaseRunning && run.AgentUID == sess.AgentUID {
		if code := tools.ParseCodeScope(run.Input); code != nil && code.ReviewOnly && run.ParentRunID != "" {
			if run.Repository == nil {
				return tools.Deps{}, fmt.Errorf("review session has no pinned repository")
			}
			deps.CodeReviewHead = run.Repository.BaseCommit
			deps.CodeScope = code
			deps.InvocationKey = run.IdempotencyKey
			deps.DataPlane = s.dataPlaneFor(taskRun{ClusterID: sess.ClusterID})
		}
	}
	if review, _ := ctx.Value(harnessMCPReviewSessionKey{}).(bool); review && deps.CodeScope == nil {
		return tools.Deps{}, fmt.Errorf("review session no longer has an active Run capability")
	}
	if sc := agent.Spec.SelfConfiguration; (sc == nil || !sc.Enabled) && deps.CodeScope == nil {
		return tools.Deps{}, fmt.Errorf("agent session no longer has an active capability")
	}
	return deps, nil
}

// harnessSelfTools is what a harness session serves: the self-configuration
// family plus the core schedule tools.
func harnessSelfTools(deps tools.Deps) []backend.Tool {
	if sc := deps.Agent.Spec.SelfConfiguration; sc == nil || !sc.Enabled {
		return nil
	}
	out := tools.SelfConfig(deps)
	for _, t := range tools.Core(deps) {
		if slices.Contains(harnessScheduleTools, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) harnessToolHandler(sess store.HarnessMCPSession, deps tools.Deps, t backend.Tool, gated bool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := "{}"
		if req.Params != nil && len(req.Params.Arguments) > 0 {
			args = string(req.Params.Arguments)
		}
		if gated {
			id := s.postChangeRequest(ctx, sess, deps, t.Name, args)
			return textResult(fmt.Sprintf("Queued for the user's approval (inbox item %s). It will be applied when they approve it; do not retry it.", id), false), nil
		}
		out, err := s.runHarnessTool(ctx, deps, t, args)
		if err != nil {
			return textResult(err.Error(), true), nil
		}
		return textResult(out, false), nil
	}
}

// runHarnessTool executes one tool and writes the audit row, so a harness
// session's changes show up in the run's trace like an in-process run's do.
func (s *Server) runHarnessTool(ctx context.Context, deps tools.Deps, t backend.Tool, args string) (string, error) {
	started := time.Now()
	out, err := t.Exec(ctx, args)
	outcome, errText := "ok", ""
	if err != nil {
		outcome, errText = "error", err.Error()
	}
	_ = s.store.AppendToolCall(ctx, deps.Scope, store.ToolCall{
		ID: uuid.NewString(), AgentName: deps.Agent.Name, RunID: deps.RunID, Trigger: "harness",
		Tool: t.Name, Args: redactArgs(args), Result: safeTruncate(out, maxStoredResult),
		Outcome: outcome, Error: safeTruncate(errText, 4000),
		DurationMS: time.Since(started).Milliseconds(), CreatedAt: time.Now().UTC(),
	})
	return out, err
}

func textResult(text string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isError}
}

// paramsSchema renders a built-in tool's parameters as the JSON schema MCP
// advertises.
func paramsSchema(params map[string]backend.Param) map[string]any {
	props := map[string]any{}
	required := []string{}
	for name, p := range params {
		prop := map[string]any{"type": p.Type, "description": p.Desc}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		if p.Type == "array" {
			prop["items"] = map[string]any{"type": "string"}
		}
		props[name] = prop
		if p.Required {
			required = append(required, name)
		}
	}
	slices.Sort(required)
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// postChangeRequest records a held-back change in the inbox. Unlike an
// approval it parks nothing: the harness turn carries on, and approving the
// item is what applies the change (applyChangeRequest).
func (s *Server) postChangeRequest(ctx context.Context, sess store.HarnessMCPSession, deps tools.Deps, toolName, args string) string {
	now := time.Now().UTC()
	wsScope := store.Scope{OrgUUID: deps.Scope.OrgUUID, WorkspaceUUID: deps.Scope.WorkspaceUUID}
	id := uuid.NewString()
	err := s.store.AddInboxItem(ctx, wsScope, store.InboxItem{
		ID: id, AgentName: deps.Agent.Name, RunID: deps.RunID, Kind: store.InboxKindChange,
		State:  store.InboxStatePending,
		Prompt: "Allow " + deps.Agent.Name + " to change its own configuration with " + toolName + "?",
		Payload: map[string]any{
			"tool": toolName, "args": redactArgs(args), "arguments": args,
			"clusterID": sess.ClusterID, "agentUID": sess.AgentUID, "trigger": "harness",
		},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		log.Printf("harness self-configuration: queueing %s for agent %s: %v", toolName, deps.Agent.Name, err)
	}
	_ = s.store.AppendToolCall(ctx, deps.Scope, store.ToolCall{
		ID: uuid.NewString(), AgentName: deps.Agent.Name, RunID: deps.RunID, Trigger: "harness",
		Tool: toolName, Args: redactArgs(args), Outcome: "pending_approval", CreatedAt: now,
	})
	s.events.publish(wsScope, "inbox", map[string]any{
		"id": id, "state": "pending", "agent": deps.Agent.Name, "tool": toolName, "runID": deps.RunID,
	})
	pushInboxNotice(ctx, deps, fmt.Sprintf("⏳ %s wants to change its own configuration (%s). Reply /inbox to review, /approve to apply it.", deps.Agent.Name, toolName))
	return id
}

// applyChangeRequest applies an approved change item: the exact call the
// harness asked for, re-checked against the agent as it is now. The agent must
// be the one that asked (same UID), self-configuration must still be on, and
// the tool must still be one its scopes allow — an approval does not outlive
// the policy it was asked under.
func (s *Server) applyChangeRequest(ctx context.Context, scope store.Scope, item store.InboxItem) (string, error) {
	toolName, _ := item.Payload["tool"].(string)
	args, _ := item.Payload["arguments"].(string)
	clusterID, _ := item.Payload["clusterID"].(string)
	agentUID, _ := item.Payload["agentUID"].(string)
	if toolName == "" || clusterID == "" || !slices.Contains(tools.SelfConfigMutatingTools, toolName) {
		return "", errors.New("this change request is malformed; deny it")
	}
	deps, err := s.harnessSessionDeps(ctx, store.HarnessMCPSession{
		Scope: scope, ClusterID: clusterID, AgentName: item.AgentName, AgentUID: agentUID, RunID: item.RunID,
	})
	if err != nil {
		return "", err
	}
	for _, t := range harnessSelfTools(deps) {
		if t.Name == toolName {
			return s.runHarnessTool(ctx, deps, t, args)
		}
	}
	return "", fmt.Errorf("agent %s may no longer use %s: its self-configuration scopes changed", item.AgentName, toolName)
}
