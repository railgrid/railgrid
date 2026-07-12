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
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/llm"
	"github.com/faroshq/provider-agents/store"
)

// ErrBudgetExceeded is returned when an agent has spent its budget for the
// current window. Interactive callers surface it; background callers suspend.
var ErrBudgetExceeded = errors.New("budget exceeded")

// budgetWindow maps an AgentBudget window to a duration (default 30d/month).
func budgetWindow(b *agentsv1alpha1.AgentBudget) time.Duration {
	if b != nil && b.Window == "day" {
		return 24 * time.Hour
	}
	return 30 * 24 * time.Hour
}

// checkBudget reports an ErrBudgetExceeded (wrapped with detail) when the
// agent's rolling-window usage has reached its token or USD cap. A nil budget,
// or zero limits, never blocks.
func (s *Server) checkBudget(ctx context.Context, scope store.Scope, agent *agentsv1alpha1.Agent, now time.Time) error {
	b := agent.Spec.Budget
	if b == nil || (b.TokenLimit == 0 && strings.TrimSpace(b.USDLimit) == "") {
		return nil
	}
	u, err := s.store.GetUsage(ctx, scope, agent.Name, now, budgetWindow(b))
	if err != nil {
		return nil // fail open on usage-read errors; don't wedge the agent
	}
	if b.TokenLimit > 0 && u.InputTokens+u.OutputTokens >= b.TokenLimit {
		return fmt.Errorf("%w: %d/%d tokens used this %s", ErrBudgetExceeded, u.InputTokens+u.OutputTokens, b.TokenLimit, budgetName(b))
	}
	if usd := strings.TrimSpace(b.USDLimit); usd != "" {
		if lim, perr := strconv.ParseFloat(usd, 64); perr == nil && lim > 0 {
			spent := float64(u.USDMicros) / 1e6
			if spent >= lim {
				return fmt.Errorf("%w: $%.2f/$%.2f used this %s", ErrBudgetExceeded, spent, lim, budgetName(b))
			}
		}
	}
	return nil
}

func budgetName(b *agentsv1alpha1.AgentBudget) string {
	if b != nil && b.Window == "day" {
		return "day"
	}
	return "month"
}

// runResult is the outcome of a non-streaming agent execution.
type runResult struct {
	RunID   string `json:"runID"`
	Content string `json:"content"`
	Usage   struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
	} `json:"usage"`
}

// executeTask runs one agent turn to completion against a task prompt,
// persisting the transcript and run record. It is the shared execution path
// for chat, run-now, background schedule fires, and webhook events. creds is
// only used to read the agent's model-credential Secret — per-request callers
// pass the tenant client (acting as the user), background callers pass a
// virtual-workspace-backed getter. onDelta is optional (nil = non-streaming).
func (s *Server) executeTask(
	ctx context.Context,
	creds llm.SecretGetter,
	scope store.Scope,
	agent *agentsv1alpha1.Agent,
	sessionID, task, trigger, scheduleRef string,
	onDelta func(string),
) (runResult, error) {
	now := time.Now().UTC()
	if err := s.checkBudget(ctx, scope, agent, now); err != nil {
		return runResult{}, err
	}

	model, err := s.buildChatModelCtx(ctx, creds, agent)
	if err != nil {
		return runResult{}, err
	}
	if sessionID == "" {
		sessionID = trigger // e.g. schedules share a per-trigger session
	}

	runID := uuid.NewString()
	_ = s.store.AppendMessage(ctx, scope, store.Message{
		ID: uuid.NewString(), AgentName: agent.Name, SessionID: sessionID, RunID: runID,
		Role: "user", Content: task, CreatedAt: now,
	})
	_ = s.store.SaveRun(ctx, scope, store.Run{
		ID: runID, AgentName: agent.Name, SessionID: sessionID, Trigger: trigger,
		Phase: store.RunPhaseRunning, Input: task, CreatedAt: now, UpdatedAt: now, StartedAt: &now,
	})

	msgs := s.assembleTurnCtx(ctx, scope, agent, sessionID, task)
	res, err := s.engine.StreamTurn(ctx, model, msgs, onDelta)
	end := time.Now().UTC()
	if err != nil {
		if run, gerr := s.store.GetRun(ctx, scope, runID); gerr == nil {
			run.Phase = store.RunPhaseFailed
			run.Message = err.Error()
			run.UpdatedAt = end
			run.FinishedAt = &end
			_ = s.store.SaveRun(ctx, scope, run)
		}
		return runResult{}, err
	}

	_ = s.store.AppendMessage(ctx, scope, store.Message{
		ID: uuid.NewString(), AgentName: agent.Name, SessionID: sessionID, RunID: runID,
		Role: "assistant", Content: res.Content, CreatedAt: end,
	})
	if run, gerr := s.store.GetRun(ctx, scope, runID); gerr == nil {
		run.Phase = store.RunPhaseSucceeded
		run.InputTokens = res.Usage.InputTokens
		run.OutputTokens = res.Usage.OutputTokens
		run.UpdatedAt = end
		run.FinishedAt = &end
		_ = s.store.SaveRun(ctx, scope, run)
	}
	_, _ = s.store.AddUsage(ctx, scope, agent.Name, res.Usage.InputTokens, res.Usage.OutputTokens, 0, end, 30*24*time.Hour)

	out := runResult{RunID: runID, Content: res.Content}
	out.Usage.InputTokens = res.Usage.InputTokens
	out.Usage.OutputTokens = res.Usage.OutputTokens
	return out, nil
}

// assembleTurnCtx builds the message list (system prompt + recent history +
// task) using a context rather than an *http.Request, so background callers
// (scheduler) can reuse it.
func (s *Server) assembleTurnCtx(ctx context.Context, scope store.Scope, agent *agentsv1alpha1.Agent, sessionID, task string) []engine.Message {
	var msgs []engine.Message
	if sp := agent.Spec.SystemPrompt; sp != "" {
		msgs = append(msgs, engine.Message{Role: engine.RoleSystem, Content: sp})
	}
	history, _ := s.store.LoadRecentMessages(ctx, scope, sessionID, chatHistoryLimit)
	for _, m := range history {
		role := engine.RoleUser
		if m.Role == "assistant" {
			role = engine.RoleAssistant
		}
		msgs = append(msgs, engine.Message{Role: role, Content: m.Content})
	}
	msgs = append(msgs, engine.Message{Role: engine.RoleUser, Content: task})
	return msgs
}
