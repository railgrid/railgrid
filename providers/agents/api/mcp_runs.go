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
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/store"
)

// Run tools. Until these, the MCP surface was configuration-only: it could
// re-fire a pre-created Schedule or Trigger and nothing else, so an MCP client —
// including another agent reaching this provider through the hub's aggregate
// endpoint — could not hand an agent a task or read back what it produced.
//
// The hub federates these as agents__run_agent / agents__get_run /
// agents__list_runs, so delegation between agents needs no further plumbing: it
// rides the calling user's token like every other federated tool.

type runAgentInput struct {
	Agent string `json:"agent" jsonschema:"Name of the agent to run (see list_agents)"`
	Task  string `json:"task" jsonschema:"The self-contained task for the agent. Include every fact it needs — it does not see this conversation."`
	// SessionID is optional; omitting it keeps unrelated invocations from piling
	// into one context.
	SessionID string `json:"sessionId,omitempty" jsonschema:"Continue an existing session; omit for a fresh one"`
	Wait      int    `json:"wait,omitempty" jsonschema:"Seconds to wait for the answer inline, max 120. Omit or 0 to return immediately with a run id to poll via get_run."`
}

type runAgentOutput struct {
	RunID string `json:"runId"`
	Phase string `json:"phase"`
	// Output is the agent's answer when the run settled inside the wait.
	Output  string   `json:"output,omitempty"`
	Sources []string `json:"sources,omitempty"`
	// Message carries the failure reason for a Failed/Aborted run.
	Message string `json:"message,omitempty"`
	// Status tells the caller what to do next in words, since a phase alone does
	// not say whether waiting longer would help.
	Status string `json:"status"`
}

type getRunInput struct {
	RunID string `json:"runId" jsonschema:"Run id returned by run_agent, run_schedule or run_trigger"`
	Wait  int    `json:"wait,omitempty" jsonschema:"Seconds to wait for the run to finish before answering, max 300. Omit to read its current state."`
}

type runStepView struct {
	Tool       string `json:"tool"`
	Outcome    string `json:"outcome"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"durationMS,omitempty"`
}

type getRunOutput struct {
	RunID   string   `json:"runId"`
	Agent   string   `json:"agent"`
	Trigger string   `json:"trigger"`
	Phase   string   `json:"phase"`
	Input   string   `json:"input,omitempty"`
	Output  string   `json:"output,omitempty"`
	Sources []string `json:"sources,omitempty"`
	Message string   `json:"message,omitempty"`
	// Steps is the tool trace, trimmed to what a caller can act on: names,
	// outcomes and timings, not full payloads (those are in the portal).
	Steps []runStepView `json:"steps,omitempty"`
	// Children are sub-agent runs (spawned workers, delegations).
	Children []runListView `json:"children,omitempty"`
	Usage    runUsageView  `json:"usage"`
}

type runUsageView struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	USDMicros    int64 `json:"usdMicros"`
}

type runListView struct {
	RunID   string `json:"runId"`
	Agent   string `json:"agent"`
	Trigger string `json:"trigger"`
	Phase   string `json:"phase"`
	Input   string `json:"input,omitempty"`
	// HasOutput says an answer is available from get_run without shipping it here.
	HasOutput bool   `json:"hasOutput,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
}

type listRunsInput struct {
	Agent   string `json:"agent,omitempty" jsonschema:"Only this agent's runs"`
	Phase   string `json:"phase,omitempty" jsonschema:"Pending, Running, PendingApproval, Succeeded, Failed or Aborted"`
	Trigger string `json:"trigger,omitempty" jsonschema:"chat, api, schedule, heartbeat, wakeup, event, channel, delegation or spawn"`
	Session string `json:"session,omitempty" jsonschema:"Only runs in this session"`
	Parent  string `json:"parent,omitempty" jsonschema:"Only sub-agent runs of this parent run id"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Maximum runs to return (default 20, max 100)"`
}

type listRunsOutput struct {
	Runs []runListView `json:"runs"`
}

// registerRunMCPTools adds the invoke + read tools.
func (s *Server) registerRunMCPTools(srv *mcp.Server, r *http.Request) {
	yes := true
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &yes}
	mutating := &mcp.ToolAnnotations{IdempotentHint: false, OpenWorldHint: &yes}

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "run_agent",
		Title: "Run an agent on a task",
		Description: "Give an agent a task and get its answer. The run executes in the background: with `wait` you get the answer inline when it finishes in time, otherwise you get a run id to read with get_run. " +
			"Use this to delegate work — research, a review, a summary — to an agent configured for it. The agent runs with its own tools, model and budget, and unattended runs use its background tool grant, so a task needing an approval-gated tool will come back as PendingApproval rather than acting.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in runAgentInput) (*mcp.CallToolResult, runAgentOutput, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, runAgentOutput{}, err
		}
		task := strings.TrimSpace(in.Task)
		if task == "" {
			return nil, runAgentOutput{}, errBadRequest("task is required")
		}
		id := s.mcpIdentity(ctx, r)
		if id.workspaceUUID == "" {
			return nil, runAgentOutput{}, errBadRequest("this workspace is not mapped yet — open the agents UI once, then retry")
		}
		agent, err := c.Agents().Get(ctx, strings.TrimSpace(in.Agent), metav1.GetOptions{})
		if err != nil {
			return nil, runAgentOutput{}, err
		}
		scope := id.scope(agent.Name)
		runID := s.startDetachedRun(r, c, id, agent, taskRun{
			SessionID: strings.TrimSpace(in.SessionID), Task: task,
			Trigger:    agentsv1alpha1.RunTriggerAPI,
			SourceName: apiRunSource(id),
		})

		out := runAgentOutput{RunID: runID, Phase: string(store.RunPhasePending),
			Status: "started — read it with get_run(runId)"}
		if in.Wait > 0 {
			wait := min(time.Duration(in.Wait)*time.Second, invokeMaxWait)
			if run, settled := s.waitForRun(ctx, scope, runID, wait); settled {
				return nil, runAnswer(run), nil
			}
			out.Phase = string(store.RunPhaseRunning)
			out.Status = "still running — it did not finish within the wait; read it with get_run(runId)"
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "get_run",
		Title: "Get a run's result",
		Description: "Read a run: its phase, its answer once finished, the sources it cited, its tool steps, and any sub-agent runs it started. " +
			"Pass `wait` to block until it finishes instead of polling.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getRunInput) (*mcp.CallToolResult, getRunOutput, error) {
		id := s.mcpIdentity(ctx, r)
		if id.workspaceUUID == "" {
			return nil, getRunOutput{}, errBadRequest("this workspace is not mapped yet — open the agents UI once, then retry")
		}
		scope := id.scope("")
		runID := strings.TrimSpace(in.RunID)
		if in.Wait > 0 {
			s.waitForRun(ctx, scope, runID, min(time.Duration(in.Wait)*time.Second, waitMaxTimeout))
		}
		detail, err := s.runDetailFor(ctx, scope, runID)
		if err != nil {
			return nil, getRunOutput{}, err
		}
		out := getRunOutput{
			RunID: detail.ID, Agent: detail.Agent, Trigger: detail.Trigger, Phase: detail.Phase,
			Input: detail.Input, Output: detail.Output, Sources: detail.Sources, Message: detail.Message,
			Usage: runUsageView{InputTokens: detail.InputTokens, OutputTokens: detail.OutputTokens, USDMicros: detail.USDMicros},
		}
		for _, st := range detail.Steps {
			out.Steps = append(out.Steps, runStepView{Tool: st.Tool, Outcome: st.Outcome, Error: st.Error, DurationMS: st.DurationMS})
		}
		for _, ch := range detail.Children {
			out.Children = append(out.Children, runView(ch))
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_runs",
		Title:       "List runs",
		Description: "List agent runs newest-first, optionally filtered by agent, phase, trigger, session or parent run. Use it to find work in flight, or the sub-agent runs a research pass started.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listRunsInput) (*mcp.CallToolResult, listRunsOutput, error) {
		id := s.mcpIdentity(ctx, r)
		if id.workspaceUUID == "" {
			return nil, listRunsOutput{}, errBadRequest("this workspace is not mapped yet — open the agents UI once, then retry")
		}
		limit := 20
		if in.Limit > 0 {
			limit = min(in.Limit, 100)
		}
		page, err := s.store.QueryRuns(ctx, id.scope(strings.TrimSpace(in.Agent)), store.RunQuery{
			Phase:       store.RunPhase(strings.TrimSpace(in.Phase)),
			Trigger:     strings.TrimSpace(in.Trigger),
			SessionID:   strings.TrimSpace(in.Session),
			ParentRunID: strings.TrimSpace(in.Parent),
			Limit:       limit,
		})
		if err != nil {
			return nil, listRunsOutput{}, err
		}
		out := listRunsOutput{Runs: make([]runListView, 0, len(page.Items))}
		for _, run := range page.Items {
			out.Runs = append(out.Runs, runView(summarize(run)))
		}
		return nil, out, nil
	})
}

// runAnswer renders a settled run for run_agent's inline reply, naming in words
// what the phase means for the caller.
func runAnswer(run store.Run) runAgentOutput {
	out := runAgentOutput{
		RunID: run.ID, Phase: string(run.Phase),
		Output: run.Output, Sources: run.Sources, Message: run.Message,
	}
	switch run.Phase {
	case store.RunPhaseSucceeded:
		out.Status = "finished"
	case store.RunPhasePendingApproval:
		out.Status = "paused — the agent needs a human to approve a tool call; it cannot be resolved from here (use the portal or a channel)"
	default:
		out.Status = "did not finish — see message"
	}
	return out
}

func runView(rs runSummary) runListView {
	v := runListView{
		RunID: rs.ID, Agent: rs.Agent, Trigger: rs.Trigger, Phase: rs.Phase,
		Input: rs.InputPreview, HasOutput: rs.HasOutput,
	}
	if rs.StartedAt != nil {
		v.StartedAt = rs.StartedAt.UTC().Format(time.RFC3339)
	}
	return v
}
