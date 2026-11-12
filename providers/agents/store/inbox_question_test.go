// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryQuestionResolution(t *testing.T) {
	testQuestionResolution(t, NewMemoryStore(), Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"})
}

func TestPostgresQuestionResolution(t *testing.T) {
	st := openTestPostgres(t)
	testQuestionResolution(t, st, pgScope(t, st))
}

func testQuestionResolution(t *testing.T, st Store, scope Scope) {
	t.Helper()
	ctx, now := t.Context(), time.Now().UTC()
	run := Run{
		ID: "run", AgentName: scope.AgentName, Phase: RunPhasePendingApproval,
		CreatedAt: now, UpdatedAt: now, Checkpoint: []byte(`{"inboxID":"question"}`),
	}
	if err := st.SaveRun(ctx, scope, run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimRun(ctx, scope, run.ID, "", now, 0, "old-question"); !errors.Is(err, ErrRunClaimConflict) {
		t.Fatalf("claim on a different question: %v", err)
	}
	if claimed, err := st.ClaimRun(ctx, scope, run.ID, "", now, 0, "question"); err != nil || claimed.Phase != RunPhaseRunning {
		t.Fatalf("claim on the parked question: phase=%s err=%v", claimed.Phase, err)
	}
	if _, err := st.ClaimRun(ctx, scope, run.ID, "", now, 0, "question"); !errors.Is(err, ErrRunClaimConflict) {
		t.Fatalf("duplicate claim: %v", err)
	}

	for _, item := range []InboxItem{
		{ID: "pending", State: InboxStatePending},
		{ID: "dismissed", State: InboxStateDenied, RunID: run.ID},
		{ID: "standalone", State: InboxStateDenied},
	} {
		t.Run(item.ID, func(t *testing.T) {
			item.AgentName, item.Kind = scope.AgentName, InboxKindQuestion
			item.CreatedAt, item.UpdatedAt = now, now
			item.Payload = map[string]any{"original": "preserved"}
			if err := st.AddInboxItem(ctx, scope, item); err != nil {
				t.Fatal(err)
			}
			answer := InboxResolution{QuestionPayload: map[string]any{"answers": map[string]any{"q1": "yes"}}}
			if item.State == InboxStateDenied {
				cleanup := answer
				cleanup.PendingOnly = true
				if _, err := st.ResolveInboxItem(ctx, scope, item.ID, InboxStateAnswered, "cleanup", now, cleanup); !errors.Is(err, ErrInboxAlreadyResolved) {
					t.Fatalf("pending-only update to dismissed question: %v", err)
				}
			}
			got, err := st.ResolveInboxItem(ctx, scope, item.ID, InboxStateAnswered, "first answer", now, answer)
			if item.ID == "standalone" {
				if !errors.Is(err, ErrInboxAlreadyResolved) {
					t.Fatalf("revival of dismissed standalone question: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			answers, _ := got.Payload["answers"].(map[string]any)
			if got.State != InboxStateAnswered || got.Response != "first answer" || got.Payload["original"] != "preserved" || answers["q1"] != "yes" {
				t.Fatalf("unexpected resolved question: %+v", got)
			}
			if _, err := st.ResolveInboxItem(ctx, scope, item.ID, InboxStateAnswered, "second answer", now, answer); !errors.Is(err, ErrInboxAlreadyResolved) {
				t.Fatalf("duplicate answer: %v", err)
			}
		})
	}
}
