// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/channels"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/llm"
	"github.com/faroshq/provider-agents/store"
	"github.com/faroshq/provider-agents/tools"
)

// Trigger classes: interactive runs have a human watching; background runs do
// not and get a smaller default tool surface (design rule 5).
func isInteractive(trigger string) bool {
	switch trigger {
	case agentsv1alpha1.RunTriggerChat, agentsv1alpha1.RunTriggerAPI, agentsv1alpha1.RunTriggerChannel:
		return true
	}
	return false
}

// defaultFamilies per trigger class when the agent spec grants nothing
// explicitly. Background gets read-only-ish families (core self-management +
// web reading); connection-backed families (github/mcp — potentially
// write-capable) and edges (acts as the calling user) must be granted
// explicitly for background runs.
func defaultFamilies(interactive bool) []string {
	if interactive {
		return []string{"core", "web", "github", "mcp", "edges"}
	}
	return []string{"core", "web"}
}

// buildToolset assembles the agent's tools for one run: family policy per
// trigger class, MCP/GitHub connection sessions, the edges family, sub-agent
// delegation, approval gating, and audit logging. The returned closer
// releases MCP sessions after the run.
func (s *Server) buildToolset(ctx context.Context, deps tools.Deps, run taskRun) ([]engine.Tool, func()) {
	trigger := run.Trigger
	interactive := isInteractive(trigger)

	// Sub-agent delegation: only for top-level runs (depth 1 — a delegated
	// run cannot delegate further) on agents with an allow-list. The closure
	// executes the child through the same shared path, records lineage via
	// ParentRunID, and rolls the child's usage into the parent's budget.
	if trigger != agentsv1alpha1.RunTriggerDelegation && len(deps.Agent.Spec.Delegates) > 0 {
		parentDeps := deps
		delegations := 0
		deps.Delegate = func(dctx context.Context, target, task string) (string, error) {
			if !slices.Contains(parentDeps.Agent.Spec.Delegates, target) {
				return "", fmt.Errorf("agent %q is not in this agent's delegates list", target)
			}
			if delegations >= 3 {
				return "", fmt.Errorf("delegation fan-out limit (3 per run) reached")
			}
			delegations++
			child, err := parentDeps.CR.GetAgent(dctx, target)
			if err != nil {
				return "", fmt.Errorf("loading agent %q: %w", target, err)
			}
			res, err := s.executeTask(dctx, taskRun{
				Creds: parentDeps.Secrets, CR: parentDeps.CR,
				Scope:       store.Scope{OrgUUID: parentDeps.Scope.OrgUUID, WorkspaceUUID: parentDeps.Scope.WorkspaceUUID, AgentName: target},
				Agent:       child,
				SessionID:   "delegate:" + parentDeps.Agent.Name + ":" + target,
				Task:        task,
				Trigger:     agentsv1alpha1.RunTriggerDelegation,
				SourceName:  parentDeps.Agent.Name,
				ParentRunID: parentDeps.RunID,
			})
			if err != nil {
				return "", err
			}
			// Budget rollup: the child's spend also counts against the parent.
			_, _ = s.store.AddUsage(dctx, parentDeps.Scope, parentDeps.Agent.Name,
				res.Usage.InputTokens, res.Usage.OutputTokens, 0, time.Now().UTC(), 30*24*time.Hour)
			return res.Content, nil
		}
	}
	grant := deps.Agent.Spec.Tools.Background
	if interactive {
		grant = deps.Agent.Spec.Tools.Interactive
	}
	// Merge any linked Toolsets (shared bundles) into this grant so their
	// families/connections/approval apply as if written inline.
	grant = s.expandToolsets(ctx, deps, grant)
	families := grant.Families
	if len(families) == 0 {
		families = defaultFamilies(interactive)
	}

	var out []engine.Tool
	if slices.Contains(families, "core") {
		out = append(out, tools.Core(deps)...)
	}
	if slices.Contains(families, "web") {
		out = append(out, tools.Web(deps)...)
	}

	// Connection-backed families: dial each granted mcp/github connection and
	// expose its discovered tools. Failures degrade (logged, family absent)
	// rather than failing the run.
	var sessions []*tools.MCPSession
	if slices.Contains(families, "mcp") || slices.Contains(families, "github") {
		conns, err := deps.CR.ListConnections(ctx)
		if err != nil {
			log.Printf("toolset: listing connections: %v", err)
		}
		for i := range conns {
			conn := &conns[i]
			isMCP := conn.Spec.Type == agentsv1alpha1.ConnectionTypeMCP && slices.Contains(families, "mcp")
			isGH := conn.Spec.Type == agentsv1alpha1.ConnectionTypeGitHub && slices.Contains(families, "github")
			if !isMCP && !isGH {
				continue
			}
			if len(grant.Connections) > 0 && !slices.Contains(grant.Connections, conn.Name) {
				continue
			}
			sess, err := tools.ConnectMCP(ctx, deps, conn)
			if err != nil {
				log.Printf("toolset: connection %q unavailable: %v", conn.Name, err)
				continue
			}
			sessions = append(sessions, sess)
			out = append(out, sess.Tools...)
		}
	}

	// Edges family: the hub's aggregate MCP endpoint (kube clusters + SSH
	// servers) dialed as the calling user. Interactive runs only — background
	// runs have no user token.
	if slices.Contains(families, "edges") && run.EdgesEndpoint != "" && run.EdgesToken != "" {
		sess, err := tools.ConnectMCPEndpoint(ctx, run.EdgesEndpoint, run.EdgesToken, "edges", run.EdgesInsecure)
		if err != nil {
			log.Printf("toolset: edges MCP unavailable: %v", err)
		} else {
			sessions = append(sessions, sess)
			out = append(out, sess.Tools...)
		}
	}

	// Approval gating + audit wrap every tool.
	for i := range out {
		out[i] = s.wrapTool(out[i], deps, trigger, grant.RequireApproval)
	}

	closer := func() {
		for _, sess := range sessions {
			sess.Close()
		}
	}
	return out, closer
}

// expandToolsets merges every Toolset referenced by a grant into a copy of that
// grant, unioning families, connections, and approval rules. Missing/unreadable
// toolsets are logged and skipped so a bad reference degrades rather than fails.
func (s *Server) expandToolsets(ctx context.Context, deps tools.Deps, grant agentsv1alpha1.ToolGrant) agentsv1alpha1.ToolGrant {
	if len(grant.Toolsets) == 0 {
		return grant
	}
	fam := slices.Clone(grant.Families)
	conns := slices.Clone(grant.Connections)
	appr := slices.Clone(grant.RequireApproval)
	union := func(dst, add []string) []string {
		for _, v := range add {
			if !slices.Contains(dst, v) {
				dst = append(dst, v)
			}
		}
		return dst
	}
	for _, name := range grant.Toolsets {
		ts, err := deps.CR.GetToolset(ctx, name)
		if err != nil {
			log.Printf("toolset: linked toolset %q unavailable: %v", name, err)
			continue
		}
		fam = union(fam, ts.Spec.Families)
		conns = union(conns, ts.Spec.Connections)
		appr = union(appr, ts.Spec.RequireApproval)
	}
	grant.Families = fam
	grant.Connections = conns
	grant.RequireApproval = appr
	return grant
}

// wrapTool layers approval gating (when the tool matches the grant's
// requireApproval list) and audit logging around a tool's Exec.
func (s *Server) wrapTool(t engine.Tool, deps tools.Deps, trigger string, requireApproval []string) engine.Tool {
	needsApproval := toolNeedsApproval(t.Name, requireApproval)
	inner := t.Exec
	t.Exec = func(ctx context.Context, argsJSON string) (string, error) {
		if needsApproval {
			ok, msg := s.consumeApproval(ctx, deps, t.Name)
			if !ok {
				return msg, nil
			}
		}
		started := time.Now()
		outStr, err := inner(ctx, argsJSON)
		outcome := "ok"
		errText := ""
		if err != nil {
			outcome, errText = "error", err.Error()
		}
		_ = s.store.AppendToolCall(ctx, deps.Scope, store.ToolCall{
			ID: uuid.NewString(), AgentName: deps.Agent.Name, Trigger: trigger,
			Tool: t.Name, ArgsDigest: clipArgs(argsJSON), Outcome: outcome, Error: clipArgs(errText),
			DurationMS: time.Since(started).Milliseconds(), CreatedAt: time.Now().UTC(),
		})
		return outStr, err
	}
	return t
}

// toolNeedsApproval matches a tool name against the grant's requireApproval
// entries ("toolname", "conn__*" wildcards, or "*").
func toolNeedsApproval(name string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == "*" || p == name {
			return true
		}
		if strings.HasSuffix(p, "*") && strings.HasPrefix(name, strings.TrimSuffix(p, "*")) {
			return true
		}
	}
	return false
}

// consumeApproval checks the inbox for an un-consumed approval for this
// agent+tool. Present → consume it and allow the call. Absent → post an
// approval request (portal inbox + channel via the notify path is the
// caller's concern) and tell the model to wait.
func (s *Server) consumeApproval(ctx context.Context, deps tools.Deps, toolName string) (bool, string) {
	items, err := s.store.ListInbox(ctx, store.Scope{OrgUUID: deps.Scope.OrgUUID, WorkspaceUUID: deps.Scope.WorkspaceUUID}, store.InboxStateApproved)
	if err == nil {
		for _, it := range items {
			if it.AgentName == deps.Agent.Name && it.Kind == store.InboxKindApproval && it.Payload["tool"] == toolName {
				// Consume: flip to answered so one approval authorizes one call.
				_, _ = s.store.ResolveInboxItem(ctx, store.Scope{OrgUUID: deps.Scope.OrgUUID, WorkspaceUUID: deps.Scope.WorkspaceUUID},
					it.ID, store.InboxStateAnswered, "consumed by "+toolName, time.Now().UTC())
				return true, ""
			}
		}
	}
	now := time.Now().UTC()
	_ = s.store.AddInboxItem(ctx, store.Scope{OrgUUID: deps.Scope.OrgUUID, WorkspaceUUID: deps.Scope.WorkspaceUUID}, store.InboxItem{
		ID: uuid.NewString(), AgentName: deps.Agent.Name, Kind: store.InboxKindApproval,
		State: store.InboxStatePending, Prompt: "Allow " + deps.Agent.Name + " to run " + toolName + "?",
		Payload: map[string]any{"tool": toolName}, CreatedAt: now, UpdatedAt: now,
	})
	// Push the request to the user's channel so it can be answered where they
	// live: reply /inbox to list, /approve N to allow.
	if connName := strings.TrimSpace(deps.Agent.Spec.DefaultNotifyConnection); connName != "" {
		if conn, err := deps.CR.GetConnection(ctx, connName); err == nil {
			token := ""
			if sec, serr := deps.Secrets.GetSecret(ctx, llm.SecretNamespace, connectionSecretName(connName)); serr == nil {
				if v, ok := sec.Data["token"]; ok {
					token = string(v)
				}
			}
			_ = channels.Send(ctx, channels.Message{
				Type: conn.Spec.Type, Token: token, Target: conn.Spec.Channel, Config: conn.Spec.Config,
				Text: fmt.Sprintf("⏳ %s wants to run %s. Reply /inbox to review, /approve 1 to allow.", deps.Agent.Name, toolName),
			})
		}
	}
	return false, "this tool requires user approval — an approval request was posted to the user's inbox and channel; tell the user to approve it and try again"
}

func clipArgs(s string) string {
	if len(s) <= 300 {
		return s
	}
	return s[:300] + "…"
}
