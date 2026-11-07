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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/store"
)

func TestLiveProgressBoundsJSONEscapedPartialText(t *testing.T) {
	for name, text := range map[string]string{
		"html":    strings.Repeat("<>&", maxStoredOutput/3+1),
		"control": strings.Repeat("\x00", maxStoredOutput+1),
		"unicode": strings.Repeat("🙂", maxStoredOutput/4+1),
	} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			st := store.NewMemoryStore()
			scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "escaped", AgentName: "coder"}
			now := time.Now().UTC()
			if err := st.SaveRun(ctx, scope, store.Run{ID: name, AgentName: "coder", Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			p := (&Server{store: st}).newLiveProgress(ctx, taskRun{Scope: scope, RunID: name}, "session", now)
			p.delta(text)
			p.flushPending()
			rows, err := st.GetRunProgress(ctx, scope, name)
			if err != nil || len(rows) != 1 || rows[0].ID != p.assistantID || rows[0].Content == "" {
				t.Fatalf("current partial response was lost: %+v, %v", rows, err)
			}
			raw, err := json.Marshal(rows)
			if err != nil || len(raw) > 1024*1024 {
				t.Fatalf("escaped snapshot exceeds byte limit: %d bytes, %v", len(raw), err)
			}
			if !utf8.ValidString(rows[0].Content) {
				t.Fatal("truncation split a UTF-8 code point")
			}
		})
	}
}

func TestLiveProgressPublishesFirstDeltaWithoutAnotherEvent(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "first", AgentName: "coder"}
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, scope, store.Run{ID: "first", AgentName: "coder", Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	p := (&Server{store: st}).newLiveProgress(ctx, taskRun{Scope: scope, RunID: "first"}, "session", now)
	for _, text := range []string{"Starting a slow operation", "Continuing after a boundary"} {
		p.delta(text)
		rows, err := st.GetRunProgress(ctx, scope, "first")
		if err != nil || len(rows) == 0 || rows[len(rows)-1].Content != text {
			t.Fatalf("first delta requires a later event to become visible: rows=%d, %v", len(rows), err)
		}
		p.assistant(backend.AssistantMessage{Content: text, Phase: "commentary", Complete: true})
	}
}

func TestLiveProgressRetainsNewestRowAtByteLimit(t *testing.T) {
	for _, boundary := range []string{"command", "commentary", "final"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := t.Context()
			st := store.NewMemoryStore()
			scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "bounds", AgentName: "coder"}
			now := time.Now().UTC()
			if err := st.SaveRun(ctx, scope, store.Run{ID: boundary, AgentName: "coder", Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			p := (&Server{store: st}).newLiveProgress(ctx, taskRun{Scope: scope, RunID: boundary}, "session", now)
			text := strings.Repeat("<>&", maxStoredOutput/3+1)
			p.delta(text)
			p.flushPending()
			partialID := p.assistantID
			newestID := partialID
			if boundary == "command" {
				p.toolStart("call", "shell", `{"command":"`+strings.Repeat("<", maxStoredResult)+`"}`)
				newestID = p.toolIDs["call"]
			} else {
				phase := ""
				if boundary == "commentary" {
					phase = "commentary"
				}
				if got := p.assistant(backend.AssistantMessage{Content: text, Phase: phase, Complete: true}); got != partialID {
					t.Fatal("completion changed identity")
				}
			}
			rows, err := st.GetRunProgress(ctx, scope, boundary)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(rows)
			if err != nil || len(raw) > 1024*1024 {
				t.Fatalf("snapshot exceeds byte limit: %d bytes, %v", len(raw), err)
			}
			foundPartial, foundNewest := false, false
			for _, row := range rows {
				if row.ID == partialID && row.Content != "" && utf8.ValidString(row.Content) {
					foundPartial = true
				}
				if row.ID == newestID {
					foundNewest = true
					if boundary == "command" && row.Metadata["pending"] != true {
						t.Fatal("newest command lost its pending state")
					}
				}
			}
			if !foundPartial || !foundNewest {
				t.Fatalf("snapshot lost current text or newest boundary: partial=%v newest=%v rows=%d", foundPartial, foundNewest, len(rows))
			}
		})
	}
}

func TestLiveProgressSurvivesObserverDisconnectAndApprovalResume(t *testing.T) {
	for _, backendType := range []string{agentsv1alpha1.AgentBackendHarness, agentsv1alpha1.AgentBackendModel} {
		t.Run(backendType, func(t *testing.T) {
			ctx := t.Context()
			st := store.NewMemoryStore()
			scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"}
			started := time.Now().UTC()
			record := store.Run{ID: "run", AgentName: "coder", SessionID: "session", Phase: store.RunPhaseRunning, CreatedAt: started, UpdatedAt: started}
			if err := st.SaveRun(ctx, scope, record); err != nil {
				t.Fatal(err)
			}
			agent := &agentsv1alpha1.Agent{}
			agent.Name = "coder"
			run := taskRun{Scope: scope, Agent: agent, RunID: "run", Trigger: "chat"}
			// There are deliberately no live request callbacks. This is also how
			// a continuation resumed by a different replica executes.
			s := &Server{store: st}
			sink := s.turnSink(ctx, run, "session", started, backendType, nil)
			sink.Delta("Inspecting the repository")
			sink.ToolStart("call", "shell", `{"command":"go test","token":"secret-value"}`)
			reader := &Server{store: st}
			detail, err := reader.runDetailFor(ctx, scope, "run")
			if err != nil {
				t.Fatal(err)
			}
			if len(detail.Progress) != 2 || detail.Progress[0].Content != "Inspecting the repository" || detail.Progress[1].Metadata["pending"] != true {
				t.Fatalf("disconnected progress = %+v", detail.Progress)
			}
			if args, _ := detail.Progress[1].Metadata["args"].(string); strings.Contains(args, "secret-value") {
				t.Fatalf("unredacted command arguments: %s", args)
			}
			toolID := detail.Progress[1].ID
			sink.ToolEnd(backend.ToolEvent{ID: "call", Name: "shell", Result: "PASS", Duration: time.Second})
			page, err := st.ListMessages(ctx, scope, "session", 10, "")
			if err != nil || len(page.Items) != 1 || page.Items[0].ID != toolID {
				t.Fatalf("tool completion changed presentation identity: %+v, %v", page, err)
			}
			detail, err = reader.runDetailFor(ctx, scope, "run")
			if err != nil || len(detail.Progress) != 2 || detail.Progress[1].Content != "PASS" || detail.Progress[1].Metadata["pending"] == true {
				t.Fatalf("completed progress = %+v, %v", detail.Progress, err)
			}
			// Parked and terminal runs use their committed transcript, never an
			// obsolete partial snapshot. A new sink resets only the overlay.
			record.Phase = store.RunPhasePendingApproval
			if err := st.SaveRun(ctx, scope, record); err != nil {
				t.Fatal(err)
			}
			detail, err = reader.runDetailFor(ctx, scope, "run")
			if err != nil || len(detail.Progress) != 0 {
				t.Fatalf("parked overlay: %+v, %v", detail.Progress, err)
			}
			record.Phase = store.RunPhaseRunning
			if err := st.SaveRun(ctx, scope, record); err != nil {
				t.Fatal(err)
			}
			resumed := reader.turnSink(ctx, run, "session", started, backendType, nil)
			resumed.Delta("Continuing after approval")
			resumed.ToolStart("next", "shell", `{"command":"git diff"}`)
			detail, err = s.runDetailFor(ctx, scope, "run")
			if err != nil || len(detail.Progress) != 2 || detail.Progress[0].Content != "Continuing after approval" {
				t.Fatalf("resumed progress = %+v, %v", detail.Progress, err)
			}
			page, err = st.ListMessages(ctx, scope, "session", 10, "")
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("resume damaged transcript: %+v, %v", page, err)
			}
		})
	}
}

func TestLiveProgressBoundsSnapshotsWithoutEvictingCurrentText(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"}
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, scope, store.Run{ID: "run", AgentName: "coder", Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	p := (&Server{store: st}).newLiveProgress(ctx, taskRun{Scope: scope, RunID: "run"}, "session", now)
	p.delta("Still working")
	for i := range 40 {
		id := fmt.Sprint(i)
		p.toolStart(id, "shell", "{}")
		p.completed(p.row(p.toolID(id), "tool", strings.Repeat("x", maxStoredResult), map[string]any{"tool": "shell"}))
	}
	p.delta(" after many commands")
	p.flushPending()
	rows, err := st.GetRunProgress(ctx, scope, "run")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(rows)
	if err != nil || len(raw) > 1024*1024 || len(rows) > 32 {
		t.Fatalf("unbounded progress snapshot: %d bytes, %d rows, %v", len(raw), len(rows), err)
	}
	if len(rows) == 0 || rows[0].Content != "Still working after many commands" {
		t.Fatalf("current partial text was evicted: %+v", rows)
	}
}

func TestHarnessCommentaryUsesSharedLiveAndSavedProgressPhases(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"}
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, scope, store.Run{ID: "run", AgentName: "coder", Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	agent := &agentsv1alpha1.Agent{}
	agent.Name = "coder"
	run := taskRun{RunID: "run", SessionID: "session", Scope: scope, Agent: agent}
	var frames []chatFrame
	bindChatStream(&run, func(event string, data map[string]any) { frames = append(frames, chatFrame{Event: event, Data: data}) })
	s := &Server{store: st}
	tracker := newTurnProgressTracker(0)
	sink := s.turnSink(ctx, run, "session", now, agentsv1alpha1.AgentBackendHarness, tracker)
	sink.Delta("Inspecting.\n\nChecking tests.")
	sink.Assistant(backend.AssistantMessage{Content: "Inspecting.\n\nChecking tests.", Phase: "commentary", PresentationOnly: true, Complete: true})
	if tracker.workedKnown || tracker.gotFinal || tracker.partialText() != "" {
		t.Fatalf("commentary invented timing or final state: %+v", tracker)
	}
	frame := frames[len(frames)-1]
	if frame.Event != "assistant_message" || frame.Data["phase"] != "commentary" {
		t.Fatalf("live boundary = %+v", frame)
	}
	if _, ok := frame.Data["segmentDurationMS"]; ok {
		t.Fatal("untimed commentary has a fabricated duration")
	}
	sink.ToolStart("cmd", "shell", "{}")
	sink.ToolEnd(backend.ToolEvent{ID: "cmd", Name: "shell", Result: "PASS", Duration: 50 * time.Millisecond})
	sink.Delta("Fixed.")
	sink.Assistant(backend.AssistantMessage{Content: "Fixed.", Complete: true, Duration: 200 * time.Millisecond})
	if tracker.finalText("") != "Fixed." || tracker.durationMS() != 250 {
		t.Fatalf("final/timing = %+v", tracker)
	}
	page, err := st.ListMessages(ctx, scope, "session", 10, "")
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("saved progress = %+v, %v", page, err)
	}
	var commentary store.Message
	for _, row := range page.Items {
		if row.Role == "assistant" {
			commentary = row
		}
	}
	if commentary.Content != "Inspecting.\n\nChecking tests." || commentary.Metadata["turnPhase"] != "commentary" {
		t.Fatalf("saved commentary = %+v", commentary)
	}
}

func TestLiveProgressEvictsCompletedRowsBeforePendingCommands(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "pending", AgentName: "coder"}
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, scope, store.Run{ID: "pending", AgentName: "coder", Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	p := (&Server{store: st}).newLiveProgress(ctx, taskRun{Scope: scope, RunID: "pending"}, "session", now)
	p.toolStart("long", "shell", `{}`)
	longID := p.toolIDs["long"]
	for i := range 40 {
		call := fmt.Sprintf("short-%d", i)
		p.toolStart(call, "shell", `{}`)
		p.completed(store.Message{ID: p.toolID(call), Role: "tool", Content: "done", Metadata: map[string]any{"pending": false}})
	}
	rows, err := st.GetRunProgress(ctx, scope, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) > 32 {
		t.Fatalf("snapshot exceeds row bound: %d", len(rows))
	}
	for _, row := range rows {
		if row.ID == longID && row.Metadata["pending"] == true {
			return
		}
	}
	t.Fatal("still-running command was evicted in favor of completed rows")
}

func TestLiveProgressSettlesToolAfterTranscriptWriteFailure(t *testing.T) {
	for _, backendType := range []string{agentsv1alpha1.AgentBackendHarness, agentsv1alpha1.AgentBackendModel} {
		t.Run(backendType, func(t *testing.T) {
			ctx := t.Context()
			base := store.NewMemoryStore()
			st := &failRoleAppendStore{Store: base, failRole: "tool"}
			scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"}
			now := time.Now().UTC()
			if err := st.SaveRun(ctx, scope, store.Run{ID: "run", AgentName: "coder", Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			agent := &agentsv1alpha1.Agent{}
			agent.Name = "coder"
			sink := (&Server{store: st}).turnSink(ctx, taskRun{Scope: scope, Agent: agent, RunID: "run"}, "session", now, backendType, nil)
			sink.ToolStart("call", "shell", "{}")
			before, err := base.GetRunProgress(ctx, scope, "run")
			if err != nil || len(before) != 1 || before[0].Metadata["pending"] != true {
				t.Fatalf("pending progress = %+v, %v", before, err)
			}
			sink.ToolEnd(backend.ToolEvent{ID: "call", Name: "shell", Result: "PASS"})
			if sink.run.transcriptWrites.failure() == nil {
				t.Fatal("transcript failure was not recorded")
			}
			page, err := base.ListMessages(ctx, scope, "session", 10, "")
			if err != nil || len(page.Items) != 0 {
				t.Fatalf("failed transcript unexpectedly persisted: %+v, %v", page, err)
			}
			after, err := base.GetRunProgress(ctx, scope, "run")
			if err != nil || len(after) != 1 || after[0].ID != before[0].ID || after[0].Content != "PASS" || after[0].Metadata["pending"] == true {
				t.Fatalf("completed command remained pending after transcript failure: %+v, %v", after, err)
			}
		})
	}
}

type failingProgressReadStore struct {
	store.Store
	fail bool
}

func (s *failingProgressReadStore) GetRunProgress(ctx context.Context, scope store.Scope, runID string) ([]store.Message, error) {
	if s.fail {
		return nil, errors.New("injected progress read failure")
	}
	return s.Store.GetRunProgress(ctx, scope, runID)
}

func TestRunDetailSurvivesProgressReadFailureAndRecovers(t *testing.T) {
	ctx := t.Context()
	st := &failingProgressReadStore{Store: store.NewMemoryStore(), fail: true}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"}
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, scope, store.Run{ID: "run", AgentName: "coder", Phase: store.RunPhaseRunning, Input: "request", Output: "saved answer", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendToolCall(ctx, scope, store.ToolCall{ID: "audit", AgentName: "coder", RunID: "run", Tool: "shell", Result: "PASS", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	s.newLiveProgress(ctx, taskRun{Scope: scope, RunID: "run"}, "session", now).delta("Live commentary")
	detail, err := s.runDetailFor(ctx, scope, "run")
	if err != nil || detail.ID != "run" || detail.Phase != string(store.RunPhaseRunning) || detail.Input != "request" || detail.Output != "saved answer" || len(detail.Steps) != 1 || detail.Steps[0].Result != "PASS" || len(detail.Progress) != 0 {
		t.Fatalf("progress failure hid durable run detail: %+v, %v", detail, err)
	}
	st.fail = false
	detail, err = s.runDetailFor(ctx, scope, "run")
	if err != nil || len(detail.Progress) != 1 || detail.Progress[0].Content != "Live commentary" {
		t.Fatalf("progress did not recover on next read: %+v, %v", detail.Progress, err)
	}
}
