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
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
// An API-invoked run is deliberately NOT interactive even though its caller
// supplies a user token: nobody is present to answer an approval gate, so it is
// held to the same narrower grant as a schedule. A caller that needs more widens
// spec.tools.background.
func isInteractive(trigger string) bool {
	switch trigger {
	case agentsv1alpha1.RunTriggerChat, agentsv1alpha1.RunTriggerChannel:
		return true
	}
	return false
}

// defaultFamilies per trigger class when the agent spec grants nothing
// explicitly. Background gets read-only-ish families (core self-management +
// web reading); connection-backed families (github/mcp — potentially
// write-capable) and edges (acts as the calling user) must be granted
// explicitly for background runs.
func defaultFamilies(_ bool) []string {
	// Tools are wired in explicitly (as Tool objects / Toolsets), so an agent
	// with nothing wired gets only core (memory/notify) — never a broad default.
	return []string{"core"}
}

// buildToolset assembles the agent's tools for one run: family policy per
// trigger class, MCP/GitHub connection sessions, the edges family, sub-agent
// delegation, approval gating, and audit logging. The returned closer
// releases MCP sessions after the run.
// buildToolset returns the granted tools, the aggregated MCP server instructions
// (each connected server's `initialize` guidance, e.g. an edges Service's
// spec.instructions) for folding into the system context, and a closer.
func (s *Server) buildToolset(ctx context.Context, deps tools.Deps, run taskRun) ([]engine.Tool, string, func()) {
	trigger := run.Trigger
	interactive := isInteractive(trigger)
	// A worker inherits its parent's approval class rather than its own trigger's:
	// spawn is unattended, but a worker of an interactive chat run should be gated
	// by the same rules the human's own run was.
	if run.Worker != nil {
		interactive = isInteractive(run.Worker.ClassTrigger)
	}

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
				// The child acts as the same caller, so it inherits the data
				// plane. Edges is deliberately NOT inherited (no endpoint), so
				// this widens nothing: it only lets a delegated sub-agent use
				// the same instance-backed tools its parent could.
				ClusterID: parentDeps.DataPlane.ClusterID,
				HubToken:  parentDeps.DataPlane.Token,
			})
			if err != nil {
				return "", err
			}
			// Budget rollup: the child's spend (tokens + cost) also counts
			// against the parent.
			_, _ = s.store.AddUsage(dctx, parentDeps.Scope, parentDeps.Agent.Name,
				res.Usage.InputTokens, res.Usage.OutputTokens, res.Usage.USDMicros, time.Now().UTC(), 30*24*time.Hour)
			// The child hit its own approval gate. Say so plainly rather than
			// handing back a partial answer the parent would treat as final —
			// the child's run resumes on its own once the user decides.
			if res.Pending != nil {
				return fmt.Sprintf(
					"%s\n\n[delegation paused: %s needs approval to run %s. The user was asked; this sub-task will finish on its own once approved. Do not retry it.]",
					res.Content, target, res.Pending.Tool), nil
			}
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
	// Autonomy overrides the approval patterns: "suggest" gates every
	// consequential tool, "auto" acts freely, "ask" (default) uses the grant's
	// requireApproval list.
	switch deps.Agent.Spec.Autonomy {
	case agentsv1alpha1.AutonomySuggest:
		grant.RequireApproval = []string{"*"}
	case agentsv1alpha1.AutonomyAuto:
		grant.RequireApproval = nil
	}
	families := grant.Families
	if len(families) == 0 {
		families = defaultFamilies(interactive)
	}
	// A worker's grant was decided by its parent (already intersected with the
	// parent's own families), so it replaces the agent's spec-level grant rather
	// than adding to it.
	if run.Worker != nil {
		families = run.Worker.Families
	}

	// Fan-out to scoped workers. Wired only when granted and the tree is not
	// already at its depth limit — a depth-2 worker gets no spawn tool, the same
	// way a delegated run gets no delegate tool.
	var spawner *spawnCoordinator
	if slices.Contains(families, "spawn") && spawnDepth(run) < maxSpawnDepth {
		grantable := grantableWorkerFamilies(families)
		spawner = &spawnCoordinator{
			exec: s.executeTask, runCtx: ctx, parent: run, depth: spawnDepth(run), families: grantable,
			tasks: map[string]*spawnTask{},
		}
		policy := spawnPolicyFor(deps.Agent, grantable)
		spawner.maxPerRun, spawner.maxConcurrent = policy.MaxPerRun, policy.MaxConcurrent
		spawner.sem = make(chan struct{}, policy.MaxConcurrent)
		deps.Spawn, deps.Join, deps.SpawnPolicy = spawner.spawn, spawner.join, policy
	}

	var out []engine.Tool
	if slices.Contains(families, "core") {
		out = append(out, tools.Core(deps)...)
	}
	if slices.Contains(families, "web") {
		out = append(out, tools.Web(deps)...)
	}
	out = append(out, tools.Spawn(deps)...)

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

	// Edges: the hub's aggregate MCP endpoint (kube clusters + SSH servers)
	// dialed as the calling user. This is a base-layer capability provided by
	// the hub, not a wired-in provider tool — it is always enabled, never opt-in.
	// Interactive-only, and now checked rather than assumed: it acts as the
	// calling user, which is only meaningful while a human is present to see what
	// it does. That used to fall out of background runs carrying no token, but an
	// API-invoked run carries the caller's token (it needs one for the data plane)
	// while being unattended — so the class is the gate.
	if interactive && run.EdgesEndpoint != "" && run.HubToken != "" {
		sess, err := tools.ConnectMCPEndpoint(ctx, run.EdgesEndpoint, run.HubToken, "edges", run.EdgesInsecure)
		if err != nil {
			log.Printf("toolset: edges MCP unavailable: %v", err)
		} else {
			sessions = append(sessions, sess)
			out = append(out, sess.Tools...)
		}
	}

	// Drop the tools a worker never gets: self-management and human-facing ones
	// (see workerExcludedTools). Done here rather than in the tools package so
	// the whole worker policy reads in one place.
	if run.Worker != nil {
		out = slices.DeleteFunc(out, func(t engine.Tool) bool { return workerExcludedTools[t.Name] })
	}

	// Approval gating + audit wrap every tool.
	for i := range out {
		out[i] = s.wrapTool(out[i], deps, run, trigger, grant.RequireApproval)
	}

	var mcpInstr []string
	for _, sess := range sessions {
		if instr := strings.TrimSpace(sess.Instructions); instr != "" {
			mcpInstr = append(mcpInstr, instr)
		}
	}

	closer := func() {
		// Wait for spawned workers before tearing down: they run on this run's
		// context and write into its tree, so the run is not over while one is
		// still going. Workers the model never joined are bounded by the run's own
		// timeout, which is already the deadline for everything here.
		if spawner != nil {
			spawner.wait()
		}
		for _, sess := range sessions {
			sess.Close()
		}
	}
	return out, strings.Join(mcpInstr, "\n\n"), closer
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

// approvalExempt lists tools that never require approval: they only talk to
// the user or the agent's own memory, so gating them (e.g. under
// autonomy=suggest's "*") would make the agent unable to even ask.
// The read-only schedules_list belongs here (it was misspelled "schedule_list"
// and so was never actually exempt); the mutating schedule_create/update/delete
// deliberately stay gated.
var approvalExempt = map[string]bool{
	"memory_save": true, "memory_list": true, "notify": true, "ask": true,
	"wait": true, "schedules_list": true,
}

// wrapTool normalizes every tool onto one audited execution path (ExecRich —
// the engine prefers it, so nothing can dodge the wrapper) and layers the
// approval gate over it. A gated call posts an approval request and pauses the
// run via an engine interrupt; the resume path pre-authorizes exactly one call
// (run.ApproveTool/ApproveArgs) which executes without re-gating.
func (s *Server) wrapTool(t engine.Tool, deps tools.Deps, run taskRun, trigger string, requireApproval []string) engine.Tool {
	needsApproval := toolNeedsApproval(t.Name, requireApproval) && !approvalExempt[t.Name]
	richInner, textInner := t.ExecRich, t.Exec
	inner := func(ctx context.Context, argsJSON string) (engine.Observation, error) {
		if richInner != nil {
			return richInner(ctx, argsJSON)
		}
		out, err := textInner(ctx, argsJSON)
		return engine.Observation{Text: out}, err
	}
	approved := run.approvalFor(t.Name)
	t.Exec = nil
	t.ExecRich = func(ctx context.Context, argsJSON string) (engine.Observation, error) {
		if needsApproval && !approved.consume(argsJSON) {
			reqID := s.postApprovalRequest(ctx, deps, trigger, t.Name, argsJSON)
			_ = s.store.AppendToolCall(ctx, deps.Scope, store.ToolCall{
				ID: uuid.NewString(), AgentName: deps.Agent.Name, RunID: deps.RunID, Trigger: trigger,
				Tool: t.Name, Args: redactArgs(argsJSON), Outcome: "pending_approval",
				CreatedAt: time.Now().UTC(),
			})
			return engine.Observation{}, &engine.InterruptError{Tool: t.Name, Args: argsJSON, RequestID: reqID}
		}
		started := time.Now()
		obs, err := inner(ctx, argsJSON)
		outcome, errText := "ok", ""
		if err != nil {
			outcome, errText = "error", err.Error()
		}
		_ = s.store.AppendToolCall(ctx, deps.Scope, store.ToolCall{
			ID: uuid.NewString(), AgentName: deps.Agent.Name, RunID: deps.RunID, Trigger: trigger,
			Tool: t.Name, Args: redactArgs(argsJSON), Result: safeTruncate(obs.Text, maxStoredResult),
			Outcome: outcome, Error: safeTruncate(errText, 4000),
			DurationMS: time.Since(started).Milliseconds(), CreatedAt: time.Now().UTC(),
		})
		return obs, err
	}
	return t
}

// postApprovalRequest records a pending approval (bound to this run and the
// exact requested call) and pushes it to the user's primary channel.
func (s *Server) postApprovalRequest(ctx context.Context, deps tools.Deps, trigger, toolName, argsJSON string) string {
	now := time.Now().UTC()
	wsScope := store.Scope{OrgUUID: deps.Scope.OrgUUID, WorkspaceUUID: deps.Scope.WorkspaceUUID}
	id := uuid.NewString()
	_ = s.store.AddInboxItem(ctx, wsScope, store.InboxItem{
		ID: id, AgentName: deps.Agent.Name, RunID: deps.RunID, Kind: store.InboxKindApproval,
		State:  store.InboxStatePending,
		Prompt: "Allow " + deps.Agent.Name + " to run " + toolName + "?",
		Payload: map[string]any{
			"tool": toolName, "args": redactArgs(argsJSON), "trigger": trigger,
		},
		CreatedAt: now, UpdatedAt: now,
	})
	s.events.publish(wsScope, "inbox", map[string]any{
		"id": id, "state": "pending", "agent": deps.Agent.Name, "tool": toolName, "runID": deps.RunID,
	})
	// Push the request to the user's channel so it can be answered where they
	// live: reply /inbox to list, /approve N to allow (which resumes the run).
	if connName, ok := deps.Agent.Spec.ResolveChannelConnection(""); ok {
		if conn, err := deps.CR.GetConnection(ctx, connName); err == nil {
			token := ""
			if sec, serr := deps.Secrets.GetSecret(ctx, llm.SecretNamespace, connectionSecretName(connName)); serr == nil {
				if v, ok := sec.Data["token"]; ok {
					token = string(v)
				}
			}
			_ = channels.Send(ctx, channels.Message{
				Type: conn.Spec.Type, Token: token, Target: conn.Spec.Channel, Config: conn.Spec.Config,
				Text: fmt.Sprintf("⏳ %s wants to run %s. Reply /inbox to review, /approve to allow — the run resumes automatically.", deps.Agent.Name, toolName),
			})
		}
	}
	return id
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

// Storage caps for audit payloads. Full-fidelity within reason: enough for a
// trace view, bounded so a pathological tool result can't bloat a row.
const (
	maxStoredArgs   = 16 * 1024
	maxStoredResult = 64 * 1024
	// maxStoredOutput bounds a run's persisted answer. Generous: this is the
	// deliverable of a research run, and it is read back by API callers.
	maxStoredOutput = 256 * 1024
)

// approvalGrant pre-authorizes exactly one call of one tool with exact
// arguments — set on the resume path after the user approved. consume reports
// whether the call matches and burns the grant.
type approvalGrant struct {
	args string
	used *bool
}

func (g approvalGrant) consume(argsJSON string) bool {
	if g.used == nil || *g.used || g.args != argsJSON {
		return false
	}
	*g.used = true
	return true
}

// approvalFor returns the pre-authorized grant for a tool ("no grant" for all
// tools except the approved resume call).
func (r taskRun) approvalFor(toolName string) approvalGrant {
	if r.ApproveTool == "" || r.ApproveTool != toolName || r.approveUsed == nil {
		return approvalGrant{}
	}
	return approvalGrant{args: r.ApproveArgs, used: r.approveUsed}
}

// sensitiveArgKeys flags JSON argument keys whose values are redacted before
// the audit log persists them.
var sensitiveArgKeys = []string{"token", "password", "secret", "authorization", "api_key", "apikey", "api-key", "bearer", "credential"}

// redactArgs redacts secret-looking values in a JSON arguments object and
// bounds its stored size. Non-JSON input is stored truncated as-is.
func redactArgs(argsJSON string) string {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return safeTruncate(trimmed, maxStoredArgs)
	}
	redactMap(m)
	b, err := json.Marshal(m)
	if err != nil {
		return safeTruncate(trimmed, maxStoredArgs)
	}
	return safeTruncate(string(b), maxStoredArgs)
}

func redactMap(m map[string]any) {
	for k, v := range m {
		lk := strings.ToLower(k)
		redact := false
		for _, s := range sensitiveArgKeys {
			if strings.Contains(lk, s) {
				redact = true
				break
			}
		}
		if redact {
			m[k] = "[redacted]"
			continue
		}
		if nested, ok := v.(map[string]any); ok {
			redactMap(nested)
		}
	}
}

// safeTruncate bounds s to at most n bytes without splitting a UTF-8 rune,
// appending an honest truncation marker.
func safeTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…[truncated]"
}
