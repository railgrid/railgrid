// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Durable approval resume: a run that paused on a gated tool call (phase
// PendingApproval, checkpoint persisted) continues in place once the user
// approves or denies — from the portal inbox, the Activity view, or a channel
// /approve. The approved call executes with the EXACT arguments the model
// requested; a denial is fed back as an observation so the model can react.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/channels"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tools"
)

// resumeDeps carries the tenant access a resume runs with: the gate's
// provider client on the inbox-resolve verb, the virtual-workspace client on
// the channel path. Both act as the provider; neither carries edges.
type resumeDeps struct {
	Creds         llm.CredentialResolver
	CR            tools.CRAccess
	EdgesEndpoint string
	HubToken      string
	EdgesInsecure bool
	ClusterID     string
}

// resumeApprovedRun continues a checkpointed run after the user's decision.
// Detached from the resolving request: runs on its own context with the run's
// own timeout. Errors are recorded on the run, not returned to the resolver.
func (s *Server) resumeApprovedRun(scope store.Scope, item store.InboxItem, rd resumeDeps, approve bool, note string) {
	agentScope := store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, AgentName: item.AgentName}
	s.resumeRun(context.Background(), agentScope, item.RunID, rd, resumeIntent{
		Approval: true, Approve: approve, Note: note,
		FromPhase: store.RunPhasePendingApproval,
	})
}

// resumeIntent distinguishes the two reasons a checkpointed run continues.
//
// An approval resume answers one specific gated call, so it must find the run in
// PendingApproval and it carries the user's verdict. A recovery resume picks up a
// run its replica dropped: the checkpoint holds no pending call (see
// engine.Callbacks.OnCheckpoint), so there is no verdict to apply and the loop
// simply re-asks the model.
type resumeIntent struct {
	Approval  bool
	Approve   bool
	Note      string
	FromPhase store.RunPhase
}

// resumeRun rehydrates a checkpointed run and continues its loop. Shared by the
// approval path and the recovery sweep.
func (s *Server) resumeRun(parent context.Context, agentScope store.Scope, runID string, rd resumeDeps, intent resumeIntent) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Hour)
	defer cancel()

	run, err := s.store.GetRun(ctx, agentScope, runID)
	if err != nil {
		log.Printf("resume: run %s: %v", runID, err)
		return
	}
	if run.Phase != intent.FromPhase || len(run.Checkpoint) == 0 {
		log.Printf("resume: run %s is %s (not resumable)", run.ID, run.Phase)
		return
	}
	startedAt := run.CreatedAt
	if run.StartedAt != nil {
		startedAt = *run.StartedAt
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	approve, note := intent.Approve, intent.Note
	var ck runCheckpoint
	if err := json.Unmarshal(run.Checkpoint, &ck); err != nil {
		log.Printf("resume: run %s checkpoint corrupt: %v", run.ID, err)
		return
	}
	// Claim so only one resolver resumes (double /approve, portal + channel).
	if _, err := s.store.ClaimRun(ctx, agentScope, run.ID, uuid.NewString(), time.Now().UTC()); err != nil {
		log.Printf("resume: run %s: %v", run.ID, err)
		return
	}
	s.upgradeLegacyImageCheckpoint(ctx, agentScope, run, &ck.Engine)
	s.publishRunEvent(agentScope, runEvent{ID: run.ID, Agent: run.AgentName, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: store.RunPhaseRunning})

	agent, err := rd.CR.GetAgent(ctx, run.AgentName)
	if err != nil {
		s.failResume(ctx, agentScope, run, fmt.Errorf("loading agent: %w", err))
		return
	}
	model, err := s.buildChatModelCtx(ctx, rd.Creds, agent)
	if err != nil {
		s.failResume(ctx, agentScope, run, err)
		return
	}

	used := false
	tr := taskRun{
		Creds: rd.Creds, CR: rd.CR, Scope: agentScope, Agent: agent,
		RunID: run.ID, SessionID: run.SessionID, Trigger: run.Trigger,
		SourceName: ck.SourceName, NotifyChannel: ck.NotifyChannel,
		EdgesEndpoint: rd.EdgesEndpoint, HubToken: rd.HubToken, EdgesInsecure: rd.EdgesInsecure,
		ClusterID: rd.ClusterID,
	}
	// Only an approval resume pre-authorizes a call. A recovery checkpoint has no
	// pending call at all, so there is nothing to grant.
	if intent.Approval && approve {
		tr.ApproveTool, tr.ApproveArgs, tr.approveUsed = ck.Tool, ck.Args, &used
	}
	s.liveRuns.register(run.ID, cancel)
	defer s.liveRuns.unregister(run.ID)

	toolset, _, closeTools := s.buildToolset(ctx, tools.Deps{
		Store: s.store, Scope: agentScope, Agent: agent, CR: rd.CR,
		Secrets: rd.Creds, ConnSecretName: connectionSecretName, RunID: run.ID,
		DataPlane: s.dataPlaneFor(tr),
	}, tr)
	defer closeTools()

	maxIters := 16
	if v := int(agent.Spec.Limits.MaxToolTurns); v > 0 {
		maxIters = min(v, 32)
	}
	modelName := s.primaryModelName(ctx, rd.Creds, agent)
	workedMS, workedKnown := checkpointWorkedDuration(run.Checkpoint, ck)
	if !workedKnown && run.WorkedDurationMS != nil {
		workedMS, workedKnown = *run.WorkedDurationMS, true
	}
	tracker := newTurnProgressTrackerState(workedMS, workedKnown)
	tr.transcriptWrites = &transcriptWriteState{}
	cb := s.runCallbacks(ctx, tr, run.SessionID, startedAt, tracker)
	// A resumed run keeps checkpointing, so a replica that dies again picks up
	// from where the resume got to rather than from the original snapshot.
	cb.OnCheckpoint = s.checkpointRecorder(ctx, tr, run.SessionID, func() int64 { return tracker.durationMS() })
	cancelCheck := s.cancelCheck(agentScope, run.ID)
	callbackCheck := cb.CheckAbort
	cb.CheckAbort = func(checkCtx context.Context) error {
		if err := cancelCheck(checkCtx); err != nil {
			return err
		}
		return callbackCheck(checkCtx)
	}
	res, err := s.engine.ResumeTurnWithTools(ctx, model, ck.Engine, toolset, engine.TurnConfig{
		MaxIters:            maxIters,
		ContextBudgetTokens: turnContextBudget(modelName),
		ContextCompactor:    s.contextCompactor(tr, run.SessionID, modelName),
		CheckpointEvery:     checkpointEveryIterations,
	}, approve, note, cb)
	end := time.Now().UTC()
	// Same as a fresh run: a refusal about the model id names the credential
	// to fix rather than reading like the resume broke.
	if err = llm.ExplainChatCompletionsRefusal(err, credentialNameForPurpose(agent, llm.PurposeChat)); err != nil {
		s.failResume(ctx, agentScope, run, err, tracker)
		return
	}
	// res.Usage is the run's cumulative total (the engine resumes from the
	// checkpoint's accumulator) so the run record stays truthful, but the
	// pre-pause portion was already billed to the rolling window when the run
	// paused — only the delta is added here.
	deltaIn := max(res.Usage.InputTokens-ck.Engine.Usage.InputTokens, 0)
	deltaOut := max(res.Usage.OutputTokens-ck.Engine.Usage.OutputTokens, 0)
	costMicros := llm.CostMicros(modelName, res.Usage.InputTokens, res.Usage.OutputTokens)
	window, _ := s.store.AddUsage(ctx, agentScope, agent.Name, deltaIn, deltaOut,
		llm.CostMicros(modelName, deltaIn, deltaOut), end, 30*24*time.Hour)

	// The resumed loop may hit ANOTHER gated call — checkpoint again.
	if res.Interrupt != nil {
		next := runCheckpoint{
			Engine: res.Interrupt.Checkpoint, Tool: res.Interrupt.Tool, Args: res.Interrupt.Args,
			InboxID: res.Interrupt.RequestID, SourceName: ck.SourceName, NotifyChannel: ck.NotifyChannel,
			WorkedDurationMS: tracker.durationMS(),
		}
		ckJSON, _ := json.Marshal(next)
		if stored, gerr := s.store.GetRun(ctx, agentScope, run.ID); gerr == nil {
			stored.Phase = store.RunPhasePendingApproval
			stored.Checkpoint = ckJSON
			stored.WorkedDurationMS = tracker.workedDurationMS()
			stored.UpdatedAt = end
			_ = s.saveRun(ctx, agentScope, stored)
		}
		s.appendTurnTerminal(ctx, agentScope, tr, run.SessionID, startedAt, end, tracker, turnStatusForRunPhase(store.RunPhasePendingApproval), "", "")
		s.publishRunEvent(agentScope, runEvent{ID: run.ID, Agent: agent.Name, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: store.RunPhasePendingApproval})
		return
	}

	finalContent := tracker.finalText(res.FinalContent)
	if err := s.appendTurnFinal(ctx, agentScope, tr, run.SessionID, startedAt, end, tracker, finalContent); err != nil {
		s.failResume(ctx, agentScope, run, fmt.Errorf("persist final assistant message: %w", err), tracker)
		return
	}
	body, sources := splitSources(res.Content)
	persistCtx, cancelPersist := boundedPersistContext(ctx)
	s.finishRun(persistCtx, agentScope, run.ID, runOutcome{
		Phase: store.RunPhaseSucceeded, Usage: res.Usage, CostMicros: costMicros,
		Output: body, Sources: sources, WorkedDurationMS: tracker.workedDurationMS(),
	}, end)
	s.recordAgentRun(persistCtx, rd.CR, agent, end, &window)
	cancelPersist()
	s.publishRunEvent(agentScope, runEvent{ID: run.ID, Agent: agent.Name, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: store.RunPhaseSucceeded})

	// Deliver the continuation where the run's output was headed: channel runs
	// reply on their source connection, background runs notify their channel
	// role. Interactive chat needs no delivery — the transcript update + run
	// event reach the portal.
	if out := strings.TrimSpace(res.Content); out != "" {
		switch run.Trigger {
		case agentsv1alpha1.RunTriggerChannel:
			s.sendToConnection(ctx, rd, ck.SourceName, out)
		case agentsv1alpha1.RunTriggerSchedule, agentsv1alpha1.RunTriggerHeartbeat,
			agentsv1alpha1.RunTriggerWakeup, agentsv1alpha1.RunTriggerEvent:
			if connName, ok := agent.Spec.ResolveChannelConnection(ck.NotifyChannel); ok {
				s.sendToConnection(ctx, rd, connName, fmt.Sprintf("[%s] %s", ck.SourceName, out))
			}
		}
	}
}

const legacyResumeImagePlaceholder = "[multimodal content from tool calls omitted on resume]"

// upgradeLegacyImageCheckpoint recovers provenance lost by checkpoints written
// before CheckpointMessage recorded ephemeral image follow-ups. It only looks
// at this run's trusted checkpoint after authenticating the task and tool group
// against the durable transcript. Ambiguous or incomplete shapes are unchanged.
func (s *Server) upgradeLegacyImageCheckpoint(ctx context.Context, scope store.Scope, run store.Run, checkpoint *engine.Checkpoint) {
	if checkpoint == nil || run.SessionID == "" || strings.TrimSpace(run.Input) == "" || run.Input == legacyResumeImagePlaceholder ||
		!hasLegacyResumeImagePlaceholder(checkpoint.Messages) {
		return
	}
	rows, err := s.loadAllSessionMessages(ctx, scope, run.SessionID)
	if err != nil {
		log.Printf("resume: run %s could not verify legacy image checkpoint provenance: %v", run.ID, err)
		return
	}
	if upgraded := markLegacyResumeImagePlaceholders(checkpoint, run, rows); upgraded > 0 {
		log.Printf("resume: recovered ephemeral image provenance for run %s messages=%d", run.ID, upgraded)
	}
}

func hasLegacyResumeImagePlaceholder(messages []engine.CheckpointMessage) bool {
	for _, message := range messages {
		if message.Role == engine.RoleUser && message.Content == legacyResumeImagePlaceholder && !message.Ephemeral {
			return true
		}
	}
	return false
}

// markLegacyResumeImagePlaceholders mutates only structurally authenticated
// image notes. The current request must be a unique durable user row from this
// run, and its checkpoint occurrence must precede a complete, persisted tool
// call/result group followed by the unbound legacy note.
func markLegacyResumeImagePlaceholders(checkpoint *engine.Checkpoint, run store.Run, rows []store.Message) int {
	if checkpoint == nil || run.ID == "" || run.SessionID == "" || run.Input == "" || run.Input == legacyResumeImagePlaceholder {
		return 0
	}
	request, ok := uniqueRunInputRow(rows, run)
	if !ok {
		return 0
	}

	var runCalls, runResults []store.Message
	for _, row := range rows {
		if row.RunID != run.ID || row.SessionID != run.SessionID {
			continue
		}
		switch row.Role {
		case "assistant":
			if len(historyToolCalls(row)) > 0 {
				runCalls = append(runCalls, row)
			}
		case "tool":
			if messageToolCallID(row) != "" {
				runResults = append(runResults, row)
			}
		}
	}

	upgraded := 0
	messages := checkpoint.Messages
	for index := range messages {
		message := messages[index]
		if message.Role != engine.RoleUser || message.Content != legacyResumeImagePlaceholder || message.Ephemeral ||
			message.ID != "" || message.Sequence != 0 || message.Name != "" || message.ToolCallID != "" || len(message.ToolCalls) != 0 {
			continue
		}
		groupStart, ok := authenticatedCheckpointToolGroup(messages, index, runCalls, runResults)
		if !ok {
			continue
		}
		if _, ok := checkpointRunInputBefore(messages, request, run.Input, groupStart); !ok {
			continue
		}
		messages[index].Ephemeral = true
		upgraded++
	}
	checkpoint.Messages = messages
	return upgraded
}

func uniqueRunInputRow(rows []store.Message, run store.Run) (store.Message, bool) {
	var found store.Message
	count := 0
	for _, row := range rows {
		if row.RunID != run.ID || row.SessionID != run.SessionID || row.Role != "user" || row.Content != run.Input || strings.TrimSpace(row.ID) == "" {
			continue
		}
		found = row
		count++
	}
	return found, count == 1
}

func checkpointRunInputBefore(messages []engine.CheckpointMessage, request store.Message, input string, before int) (int, bool) {
	var explicit, unbound []int
	for index := 0; index < before; index++ {
		message := messages[index]
		if message.Role != engine.RoleUser || message.Content != input {
			continue
		}
		if message.ID == request.ID {
			if message.Sequence != 0 && request.Sequence != 0 && message.Sequence != request.Sequence {
				continue
			}
			explicit = append(explicit, index)
			continue
		}
		if message.ID == "" && (message.Sequence == 0 || request.Sequence == 0 || message.Sequence == request.Sequence) {
			unbound = append(unbound, index)
		}
	}
	if len(explicit) == 1 {
		return explicit[0], true
	}
	if len(explicit) > 1 || len(unbound) != 1 {
		return 0, false
	}
	return unbound[0], true
}

func authenticatedCheckpointToolGroup(messages []engine.CheckpointMessage, before int, runCalls, runResults []store.Message) (int, bool) {
	if before <= 0 || messages[before-1].Role != engine.RoleTool {
		return 0, false
	}
	start := before - 1
	for start >= 0 && messages[start].Role == engine.RoleTool {
		start--
	}
	if start < 0 || messages[start].Role != engine.RoleAssistant || len(messages[start].ToolCalls) == 0 {
		return 0, false
	}

	assistant := messages[start]
	calls := make([]schema.ToolCall, 0, len(assistant.ToolCalls))
	pending := make(map[string]bool, len(assistant.ToolCalls))
	for _, call := range assistant.ToolCalls {
		id := strings.TrimSpace(call.ID)
		name := strings.TrimSpace(call.Name)
		if id == "" || name == "" || pending[id] {
			return 0, false
		}
		pending[id] = false
		calls = append(calls, schema.ToolCall{
			ID: id, Type: "function",
			Function: schema.FunctionCall{Name: name, Arguments: call.Args},
		})
	}
	for index := start + 1; index < before; index++ {
		result := messages[index]
		if result.Role != engine.RoleTool || result.ToolCallID == "" {
			return 0, false
		}
		seen, exists := pending[result.ToolCallID]
		if !exists || seen {
			return 0, false
		}
		matches := matchingRunToolRows(runResults, result.ToolCallID)
		if len(matches) != 1 || matches[0].Content != result.Content ||
			(result.Name != "" && messageToolName(matches[0]) != result.Name) {
			return 0, false
		}
		pending[result.ToolCallID] = true
	}
	for _, complete := range pending {
		if !complete {
			return 0, false
		}
	}
	if matches := matchingRunAssistantCallRows(runCalls, engine.Message{
		Role: engine.RoleAssistant, Content: assistant.Content, ToolCalls: calls,
	}); len(matches) != 1 {
		return 0, false
	}
	return start, true
}

func (s *Server) failResume(ctx context.Context, scope store.Scope, run store.Run, err error, trackers ...*turnProgressTracker) {
	log.Printf("resume: run %s failed: %v", run.ID, err)
	end := time.Now().UTC()
	phase := store.RunPhaseFailed
	if ctx.Err() != nil {
		phase = store.RunPhaseAborted
	}
	startedAt := run.CreatedAt
	if run.StartedAt != nil {
		startedAt = *run.StartedAt
	}
	if startedAt.IsZero() {
		startedAt = end
	}
	tracker := trackerForStored(run)
	if len(trackers) > 0 && trackers[0] != nil {
		tracker = trackers[0]
	}
	tr := taskRunForStored(run)
	persistCtx, cancelPersist := boundedPersistContext(ctx)
	defer cancelPersist()
	s.appendTurnTerminal(persistCtx, scope, tr, run.SessionID, startedAt, end, tracker, turnStatusForRunPhase(phase), tracker.partialText(), err.Error())
	s.finishRun(persistCtx, scope, run.ID, runOutcome{Phase: phase, Message: err.Error(), WorkedDurationMS: tracker.workedDurationMS()}, end)
	s.publishRunEvent(scope, runEvent{ID: run.ID, Agent: run.AgentName, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: phase})
}

// sendToConnection delivers text through a named messaging connection using
// the resume path's CR access.
func (s *Server) sendToConnection(ctx context.Context, rd resumeDeps, connName, text string) {
	if strings.TrimSpace(connName) == "" || strings.TrimSpace(text) == "" {
		return
	}
	conn, err := rd.CR.GetConnection(ctx, connName)
	if err != nil {
		log.Printf("resume: channel connection %q: %v", connName, err)
		return
	}
	token := ""
	if sec, serr := rd.Creds.GetSecret(ctx, llm.SecretNamespace, connectionSecretName(connName)); serr == nil {
		if v, ok := sec.Data["token"]; ok {
			token = string(v)
		}
	}
	if err := channels.Send(ctx, channels.Message{
		Type: conn.Spec.Type, Token: token, Target: conn.Spec.Channel, Config: conn.Spec.Config,
		Text: safeTruncate(text, 3500),
	}); err != nil {
		log.Printf("resume: send via %q failed: %v", connName, err)
	}
}
