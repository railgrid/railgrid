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
	"strings"
	"time"

	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/llm"
	"github.com/faroshq/provider-agents/store"
)

// Session compaction. A long-lived session — a channel conversation, or a
// schedule that replies turn after turn for months — eventually assembles a
// context larger than the model's window, and the run dies on a context-length
// error. Truncating the history instead would make the agent silently forget
// what it agreed to.
//
// So: when the assembled context approaches the window, summarize everything
// older than the last few messages with the agent's `compaction` model, persist
// the summary against the session, and replay it in place of those messages.
// Compacting again folds the previous summary into the new one, so the stored
// summary always stands for the whole prefix of the session.
//
// This is the cross-turn half of the context problem. The within-turn half — one
// turn whose tool observations are individually huge, e.g. a research parent
// joining ten workers — is handled by the engine's own turn budget
// (engine.TurnConfig.ContextBudgetTokens), because those observations never
// reach the transcript this operates on.
const (
	// compactThresholdPct is how full the context may get before compacting.
	// Well below 100 because the estimate is a heuristic and the model's reply
	// also has to fit.
	compactThresholdPct = 70
	// compactKeepMessages is how many of the newest messages stay verbatim. The
	// recent turns are what the next reply actually builds on; summarizing them
	// would blur the thing the user just said.
	compactKeepMessages = 10
	// compactMinFold is the fewest messages worth folding. Below this the
	// summarization call costs more than it saves.
	compactMinFold = 6
	// compactMaxSummaryChars bounds the summary itself, so a compacted session
	// cannot grow a new unbounded prefix.
	compactMaxSummaryChars = 6000
	// compactSourceBudget bounds how much transcript is fed to the summarizer in
	// one call — the compaction model has a window too.
	compactSourceBudget = 60000
)

// sessionContext is a session's replayable history: the compaction summary
// standing in for the older messages (nil when never compacted) plus the
// messages that came after it.
type sessionContext struct {
	Summary  *store.SessionSummary
	Messages []store.Message
}

// loadSessionContext reads the session's summary and the messages it does not
// already cover. Both replay (assembleTurnCtx) and the compaction decision go
// through here so they can never disagree about what the model will see.
func (s *Server) loadSessionContext(ctx context.Context, scope store.Scope, sessionID string, limit int) sessionContext {
	out := sessionContext{}
	if sum, ok, err := s.store.GetSessionSummary(ctx, scope, sessionID); err == nil && ok {
		out.Summary = &sum
	}
	msgs, err := s.store.LoadRecentMessages(ctx, scope, sessionID, limit)
	if err != nil {
		return out
	}
	if out.Summary == nil {
		out.Messages = msgs
		return out
	}
	// Drop what the summary already stands for. A message exactly at ThroughAt is
	// covered (ThroughAt is the newest folded message's timestamp).
	kept := make([]store.Message, 0, len(msgs))
	for _, m := range msgs {
		if !m.CreatedAt.After(out.Summary.ThroughAt) {
			continue
		}
		kept = append(kept, m)
	}
	out.Messages = kept
	return out
}

// summaryMessage renders a stored summary as the system message that replaces
// the folded transcript. It says plainly that this is a summary of earlier
// turns, so the model does not mistake it for something the user just wrote.
func summaryMessage(sum store.SessionSummary) engine.Message {
	return engine.Message{Role: engine.RoleSystem, Content: fmt.Sprintf(
		"Summary of this conversation's earlier %d messages (they are no longer replayed in full; treat this as an accurate record of what was said and decided):\n\n%s",
		sum.MessageCount, sum.Summary)}
}

// maybeCompactSession compacts the session when the context it would assemble is
// too close to the model's window. Called before the turn is assembled, because
// the point is to be under the limit when the request goes out.
//
// Best-effort throughout: a failure here logs and returns, leaving the run to
// proceed with the context it has. A run that would have overflowed still
// overflows, but a compaction outage never turns a working agent into a broken
// one.
func (s *Server) maybeCompactSession(ctx context.Context, run taskRun, sessionID, modelName string) {
	// A worker starts from a fresh session with no history, so there is nothing
	// to compact and no reason to pay for a check.
	if run.Worker != nil {
		return
	}
	scope, agent := run.Scope, run.Agent

	window := llm.ContextWindowFor(modelName)
	budget := window * compactThresholdPct / 100
	sc := s.loadSessionContext(ctx, scope, sessionID, chatHistoryLimit)
	if len(sc.Messages) <= compactKeepMessages {
		return // nothing foldable, whatever the size
	}

	used := s.estimateTurnTokens(ctx, run, sessionID, sc)
	if used < budget {
		return
	}

	fold := sc.Messages[:len(sc.Messages)-compactKeepMessages]
	if len(fold) < compactMinFold {
		// Too little to fold and still over budget: the recent turns alone are
		// oversized. Say so once — the operator's lever is a bigger model or a
		// lower maxToolTurns, and silently doing nothing here looks like a bug.
		log.Printf("compaction: agent %s session %s is over budget (~%d/%d tokens) but only %d older message(s) can be folded; leaving it alone",
			agent.Name, sessionID, used, budget, len(fold))
		return
	}

	summary, err := s.summarizeMessages(ctx, run, sc.Summary, fold)
	if err != nil {
		log.Printf("compaction: agent %s session %s: %v", agent.Name, sessionID, err)
		return
	}

	through := fold[len(fold)-1]
	now := time.Now().UTC()
	rec := store.SessionSummary{
		SessionID: sessionID,
		Summary:   safeTruncate(summary, compactMaxSummaryChars),
		ThroughAt: through.CreatedAt,
		CreatedAt: now,
		UpdatedAt: now,
	}
	rec.MessageCount = len(fold)
	if sc.Summary != nil {
		// The new summary subsumes the old one, so it stands for both spans.
		rec.MessageCount += sc.Summary.MessageCount
		rec.CreatedAt = sc.Summary.CreatedAt
	}
	if err := s.store.PutSessionSummary(ctx, scope, rec); err != nil {
		log.Printf("compaction: agent %s session %s: saving summary: %v", agent.Name, sessionID, err)
		return
	}
	log.Printf("compaction: agent %s session %s folded %d message(s) into a summary (was ~%d/%d tokens)",
		agent.Name, sessionID, len(fold), used, budget)
}

// estimateTurnTokens approximates what this turn will send: the same assembly
// assembleTurnCtx performs, measured rather than built, so the two stay in step.
func (s *Server) estimateTurnTokens(ctx context.Context, run taskRun, sessionID string, sc sessionContext) int {
	total := llm.EstimateTokens(run.Agent.Spec.SystemPrompt) + llm.EstimateTokens(run.Task)
	if sc.Summary != nil {
		total += llm.EstimateTokens(sc.Summary.Summary)
	}
	for _, m := range sc.Messages {
		total += llm.EstimateTokens(m.Content)
		// Tool steps replay as a compact record, not the raw row.
		if m.Role == "tool" {
			total += replayToolTokens
		}
	}
	// Memory notes are injected into every non-worker run; count them, since on a
	// long-lived agent they are not small.
	if run.Agent.Spec.Memory.Enabled == nil || *run.Agent.Spec.Memory.Enabled {
		if notes, err := s.store.ListMemories(ctx, run.Scope, memoryNoteLimit(run.Agent)); err == nil {
			for _, n := range notes {
				total += llm.EstimateTokens(n.Title) + llm.EstimateTokens(safeTruncate(n.Body, memoryNoteClip))
			}
		}
	}
	return total
}

// replayToolTokens is the per-tool-step overhead of the "[record of an earlier
// tool call]" wrapper (labels + clipped args), on top of the message content.
const replayToolTokens = 160

// summarizeMessages asks the compaction model to fold a span of transcript (and
// any previous summary) into one durable summary.
func (s *Server) summarizeMessages(ctx context.Context, run taskRun, prev *store.SessionSummary, fold []store.Message) (string, error) {
	model, err := s.buildModelForPurpose(ctx, run.Creds, run.Agent, llm.PurposeCompaction)
	if err != nil {
		return "", fmt.Errorf("compaction model: %w", err)
	}

	var b strings.Builder
	if prev != nil {
		b.WriteString("Summary of the conversation before this excerpt:\n")
		b.WriteString(prev.Summary)
		b.WriteString("\n\n")
	}
	b.WriteString("Transcript excerpt to fold in:\n")
	for _, m := range fold {
		role := m.Role
		content := m.Content
		if role == "tool" {
			toolName, _ := m.Metadata["tool"].(string)
			content = fmt.Sprintf("[tool %s] %s", toolName, content)
			role = "assistant-tool"
		}
		fmt.Fprintf(&b, "%s: %s\n", role, content)
	}
	source := safeTruncate(b.String(), compactSourceBudget)

	msgs := []engine.Message{
		{Role: engine.RoleSystem, Content: compactionSystemPrompt},
		{Role: engine.RoleUser, Content: source},
	}
	res, err := s.engine.StreamTurn(ctx, model, msgs, nil)
	if err != nil {
		return "", fmt.Errorf("summarizing: %w", err)
	}
	out := strings.TrimSpace(res.Content)
	if out == "" {
		return "", fmt.Errorf("summarizing: the compaction model returned nothing")
	}

	// Compaction is real spend on the agent's budget, so account for it. Cost is
	// attributed to the compaction model when it resolves in the catalog.
	end := time.Now().UTC()
	costMicros := llm.CostMicros(s.modelNameForPurpose(ctx, run.Creds, run.Agent, llm.PurposeCompaction),
		res.Usage.InputTokens, res.Usage.OutputTokens)
	_, _ = s.store.AddUsage(ctx, run.Scope, run.Agent.Name,
		res.Usage.InputTokens, res.Usage.OutputTokens, costMicros, end, 30*24*time.Hour)
	return out, nil
}

// compactionSystemPrompt tells the summarizer to write the record the agent will
// have to act on later — decisions and commitments, not prose about the chat.
const compactionSystemPrompt = `You compact an assistant's conversation history so it can keep working after the older turns are dropped.

Write a dense factual record of the excerpt below. Preserve, in this order of priority:
1. Decisions made and commitments given, with whatever was agreed verbatim enough to honor.
2. Facts established: names, identifiers, versions, numbers, URLs, file paths, configuration values.
3. What the user wants and how they want it — stated preferences, constraints, and corrections they made.
4. Work state: what is done, what is in progress, what was deliberately abandoned and why.
5. Open threads: questions unanswered, things promised but not yet delivered.

Rules: write in the third person about "the user" and "the assistant". Keep specifics over summary words — "set the replica count to 3" not "discussed scaling". Do not invent anything that is not in the excerpt. Do not editorialize, and do not add a preamble or a closing remark. If a previous summary is given, merge it in and return ONE combined record, not two sections.`
