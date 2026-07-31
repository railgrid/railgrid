// Copyright 2026 The Faros Authors.
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

	"github.com/google/uuid"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/channels"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/llm"
	"github.com/faroshq/provider-agents/store"
	"github.com/faroshq/provider-agents/tools"
)

// resumeDeps carries the tenant access a resume runs with. The portal path
// acts as the resolving user (tenant client + edges token); the channel path
// acts through the APIExport virtual workspace (no edges).
type resumeDeps struct {
	Creds         llm.SecretGetter
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	agentScope := store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, AgentName: item.AgentName}
	run, err := s.store.GetRun(ctx, agentScope, item.RunID)
	if err != nil {
		log.Printf("resume: run %s: %v", item.RunID, err)
		return
	}
	if run.Phase != store.RunPhasePendingApproval || len(run.Checkpoint) == 0 {
		log.Printf("resume: run %s is %s (not resumable)", run.ID, run.Phase)
		return
	}
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
	s.publishRunEvent(agentScope, run.ID, run.AgentName, run.Trigger, store.RunPhaseRunning)

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
	if approve {
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
	res, err := s.engine.ResumeTurnWithTools(ctx, model, ck.Engine, toolset, maxIters, approve, note, s.runCallbacks(ctx, tr, run.SessionID))
	end := time.Now().UTC()
	if err != nil {
		s.failResume(ctx, agentScope, run, err)
		return
	}
	// res.Usage is the run's cumulative total (the engine resumes from the
	// checkpoint's accumulator) so the run record stays truthful, but the
	// pre-pause portion was already billed to the rolling window when the run
	// paused — only the delta is added here.
	deltaIn := max(res.Usage.InputTokens-ck.Engine.Usage.InputTokens, 0)
	deltaOut := max(res.Usage.OutputTokens-ck.Engine.Usage.OutputTokens, 0)
	modelName := s.primaryModelName(ctx, rd.Creds, agent)
	costMicros := llm.CostMicros(modelName, res.Usage.InputTokens, res.Usage.OutputTokens)
	_, _ = s.store.AddUsage(ctx, agentScope, agent.Name, deltaIn, deltaOut,
		llm.CostMicros(modelName, deltaIn, deltaOut), end, 30*24*time.Hour)

	// The resumed loop may hit ANOTHER gated call — checkpoint again.
	if res.Interrupt != nil {
		next := runCheckpoint{
			Engine: res.Interrupt.Checkpoint, Tool: res.Interrupt.Tool, Args: res.Interrupt.Args,
			InboxID: res.Interrupt.RequestID, SourceName: ck.SourceName, NotifyChannel: ck.NotifyChannel,
		}
		ckJSON, _ := json.Marshal(next)
		if stored, gerr := s.store.GetRun(ctx, agentScope, run.ID); gerr == nil {
			stored.Phase = store.RunPhasePendingApproval
			stored.Checkpoint = ckJSON
			stored.UpdatedAt = end
			_ = s.store.SaveRun(ctx, agentScope, stored)
		}
		s.publishRunEvent(agentScope, run.ID, agent.Name, run.Trigger, store.RunPhasePendingApproval)
		return
	}

	_ = s.store.AppendMessage(ctx, agentScope, store.Message{
		ID: uuid.NewString(), AgentName: agent.Name, SessionID: run.SessionID, RunID: run.ID,
		Role: "assistant", Content: res.Content, CreatedAt: end,
	})
	s.finishRun(ctx, agentScope, run.ID, store.RunPhaseSucceeded, "", res.Usage, costMicros, end)
	s.publishRunEvent(agentScope, run.ID, agent.Name, run.Trigger, store.RunPhaseSucceeded)

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

func (s *Server) failResume(ctx context.Context, scope store.Scope, run store.Run, err error) {
	log.Printf("resume: run %s failed: %v", run.ID, err)
	s.finishRun(ctx, scope, run.ID, store.RunPhaseFailed, err.Error(), engine.Usage{}, 0, time.Now().UTC())
	s.publishRunEvent(scope, run.ID, run.AgentName, run.Trigger, store.RunPhaseFailed)
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
