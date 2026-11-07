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
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/store"
)

// liveProgress is a bounded presentation overlay on the durable transcript.
// Completed rows use the SAME IDs in both, so a reader can reconcile snapshots
// without replaying deltas or duplicating a completed command. It is separate
// from model history and opaque execution checkpoints. Each continuation starts
// a fresh overlay; earlier segments are already in the transcript.
type liveProgress struct {
	mu               sync.Mutex
	s                *Server
	ctx              context.Context
	scope            store.Scope
	runID, sessionID string
	startedAt        time.Time
	lastWrite        time.Time
	rows             []store.Message
	assistantID      string
	toolIDs          map[string]string
}

func (s *Server) newLiveProgress(ctx context.Context, run taskRun, sessionID string, startedAt time.Time) *liveProgress {
	p := &liveProgress{s: s, ctx: ctx, scope: run.Scope, runID: run.RunID, sessionID: sessionID,
		startedAt: startedAt, toolIDs: map[string]string{}}
	p.flush(true)
	return p
}

func (p *liveProgress) row(id, role, content string, metadata map[string]any) store.Message {
	return store.Message{ID: id, AgentName: p.scope.AgentName, RunID: p.runID, SessionID: p.sessionID,
		Role: role, Content: content, Metadata: metadata, CreatedAt: time.Now().UTC()}
}

func (p *liveProgress) put(row store.Message) {
	for i := range p.rows {
		if p.rows[i].ID == row.ID {
			p.rows[i] = row
			return
		}
	}
	p.rows = append(p.rows, row)
}

func (p *liveProgress) delta(text string) {
	if text == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	firstDelta := p.assistantID == ""
	if firstDelta {
		p.assistantID = uuid.NewString()
		p.put(p.row(p.assistantID, "assistant", "", turnMetadata("streaming", "running", p.startedAt, 0, 0, "")))
	}
	for i := range p.rows {
		if p.rows[i].ID == p.assistantID {
			p.rows[i].Content = safeTruncate(p.rows[i].Content+text, maxStoredOutput)
			break
		}
	}
	// Publish a new response immediately, even if no subsequent token or
	// command arrives after the previous boundary reset the write throttle.
	p.flush(firstDelta)
}

func (p *liveProgress) assistant(message backend.AssistantMessage) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.assistantID
	if id == "" {
		id = uuid.NewString()
	}
	if message.Complete {
		phase := "final"
		if message.IsCommentary() {
			phase = "commentary"
		}
		p.put(p.row(id, "assistant", safeTruncate(message.Content, maxStoredOutput),
			turnMetadata(phase, "running", p.startedAt, 0, message.Duration.Milliseconds(), "")))
		p.assistantID = ""
	}
	p.flush(true)
	return id
}

func (p *liveProgress) toolStart(callID, name, args string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.toolIDs[callID] == "" {
		p.toolIDs[callID] = uuid.NewString()
	}
	p.put(p.row(p.toolIDs[callID], "tool", "", map[string]any{
		"tool": name, "toolCallID": callID, "args": safeTruncate(redactArgs(args), maxStoredResult), "pending": true,
	}))
	p.flush(true)
}

func (p *liveProgress) toolID(callID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.toolIDs[callID]
	if id == "" {
		id = uuid.NewString()
	}
	delete(p.toolIDs, callID)
	return id
}

func (p *liveProgress) completed(row store.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	row.Content = safeTruncate(row.Content, maxStoredResult)
	p.put(row)
	p.flush(true)
}

func (p *liveProgress) flushPending() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flush(true)
}

// Called with mu held (or before publication). No timer goroutine owns state;
// token bursts coalesce, command boundaries and the end of a segment flush.
func (p *liveProgress) flush(force bool) {
	if !force && time.Since(p.lastWrite) < 250*time.Millisecond {
		return
	}
	// Older completed rows remain in the transcript. Keep both the current
	// partial response and the newest boundary (including a pending command).
	for len(p.rows) > 0 {
		raw, err := json.Marshal(p.rows)
		if err != nil || (len(p.rows) <= 32 && len(raw) <= 1024*1024) {
			break
		}
		remove := -1
		for i, row := range p.rows[:len(p.rows)-1] {
			if row.ID != p.assistantID && row.Metadata["pending"] != true {
				remove = i
				break
			}
		}
		if remove >= 0 {
			p.rows = append(p.rows[:remove], p.rows[remove+1:]...)
			continue
		}
		// If every retained row is active, keep the snapshot bounded by
		// dropping the oldest command. Completed rows were exhausted first.
		if len(p.rows) > 32 {
			for i, row := range p.rows[:len(p.rows)-1] {
				if row.ID != p.assistantID {
					p.rows = append(p.rows[:i], p.rows[i+1:]...)
					break
				}
			}
			continue
		}
		// Only protected rows remain. JSON escaping can expand their text
		// beyond the byte budget even when the raw content was bounded.
		// Trim the largest content against the size of the entire snapshot.
		largest := 0
		for i := range p.rows {
			if len(p.rows[i].Content) > len(p.rows[largest].Content) {
				largest = i
			}
		}
		original := p.rows[largest].Content
		if original == "" {
			// Defensive fallback for metadata that alone exceeds the budget.
			p.rows = p.rows[1:]
			continue
		}
		low, high := 0, len(original)-1
		for low < high {
			middle := low + (high-low+1)/2
			p.rows[largest].Content = safeTruncate(original, middle)
			candidate, candidateErr := json.Marshal(p.rows)
			if candidateErr == nil && len(candidate) <= 1024*1024 {
				low = middle
			} else {
				high = middle - 1
			}
		}
		p.rows[largest].Content = safeTruncate(original, low)
	}
	p.lastWrite = time.Now()
	ctx, cancel := boundedPersistContext(p.ctx)
	defer cancel()
	if err := p.s.store.PutRunProgress(ctx, p.scope, p.runID, p.rows); err != nil {
		log.Printf("agents: persisting live progress for run %s: %v", p.runID, err)
	}
}
