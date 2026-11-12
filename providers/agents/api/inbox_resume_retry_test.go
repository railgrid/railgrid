// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/railgrid/provider-agents/store"
)

func TestApprovalDecisionIsImmutableAndRetryableBeforeResume(t *testing.T) {
	for _, first := range []store.InboxItemState{store.InboxStateApproved, store.InboxStateDenied} {
		for _, boundary := range []string{"parked", "running", "terminal", "cancelled", "next-checkpoint", "corrupt-checkpoint", "unbound"} {
			t.Run(string(first)+"/"+boundary, func(t *testing.T) {
				ctx := t.Context()
				scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"}
				st := store.NewMemoryStore()
				now := time.Now().UTC()
				run := store.Run{ID: "run", AgentName: "coder", Phase: store.RunPhasePendingApproval, Checkpoint: []byte(`{"inboxID":"approval"}`), CreatedAt: now, UpdatedAt: now}
				if err := st.SaveRun(ctx, scope, run); err != nil {
					t.Fatal(err)
				}
				item := store.InboxItem{ID: "approval", AgentName: "coder", RunID: run.ID, Kind: store.InboxKindApproval, State: store.InboxStatePending, Payload: map[string]any{"tool": "shell", "args": "{}"}, CreatedAt: now, UpdatedAt: now}
				if boundary == "unbound" {
					item.RunID = ""
				}
				if err := st.AddInboxItem(ctx, scope, item); err != nil {
					t.Fatal(err)
				}
				s := &Server{store: st}
				accepted, err := s.resolveInboxDecision(ctx, scope, item.ID, first, "original note", now)
				if err != nil {
					t.Fatal(err)
				}
				opposite := store.InboxStateApproved
				if first == opposite {
					opposite = store.InboxStateDenied
				}
				if _, err := s.resolveInboxDecision(ctx, scope, item.ID, opposite, "late opposite decision", now.Add(time.Second)); !errors.Is(err, store.ErrInboxAlreadyResolved) {
					t.Fatalf("opposing decision changed an accepted verdict: %v", err)
				}
				switch boundary {
				case "running":
					run.Phase = store.RunPhaseRunning
				case "terminal":
					run.Phase = store.RunPhaseSucceeded
				case "cancelled":
					run.CancelRequested = true
				case "next-checkpoint":
					run.Checkpoint = []byte(`{"inboxID":"next"}`)
				case "corrupt-checkpoint":
					run.Checkpoint = []byte(`{`)
				}
				if err := st.SaveRun(ctx, scope, run); err != nil {
					t.Fatal(err)
				}
				retry, err := s.resolveInboxDecision(ctx, scope, item.ID, first, "replacement note", now.Add(2*time.Second))
				if boundary == "parked" {
					if err != nil || retry.State != first || retry.Response != accepted.Response || !retry.UpdatedAt.Equal(accepted.UpdatedAt) {
						t.Fatalf("retry failed to retain saved decision: %+v, %v", retry, err)
					}
				} else if !errors.Is(err, store.ErrInboxAlreadyResolved) {
					t.Fatalf("retry escaped %s fence: %v", boundary, err)
				}
				saved, err := st.GetInboxItem(ctx, scope, item.ID)
				if err != nil || saved.State != first || saved.Response != "original note" || !saved.UpdatedAt.Equal(now) {
					t.Fatalf("accepted decision overwritten: %+v, %v", saved, err)
				}
			})
		}
	}
}

func TestConcurrentApprovalVerdictsShareOneResumeClaim(t *testing.T) {
	ctx := t.Context()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "race", AgentName: "coder"}
	st := store.NewMemoryStore()
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, scope, store.Run{ID: "run", AgentName: "coder", Phase: store.RunPhasePendingApproval, Checkpoint: []byte(`{"inboxID":"approval"}`), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddInboxItem(ctx, scope, store.InboxItem{ID: "approval", AgentName: "coder", RunID: "run", Kind: store.InboxKindApproval, State: store.InboxStatePending, Payload: map[string]any{"tool": "shell", "args": "{}"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	start := make(chan struct{})
	var workers sync.WaitGroup
	var claims atomic.Int32
	verdicts := make(chan store.InboxItemState, 16)
	for i := range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			state := store.InboxStateApproved
			if i%2 == 0 {
				state = store.InboxStateDenied
			}
			item, err := s.resolveInboxDecision(ctx, scope, "approval", state, fmt.Sprint(i), now)
			if errors.Is(err, store.ErrInboxAlreadyResolved) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			verdicts <- item.State
			if _, err := st.ClaimRun(ctx, scope, "run", "", now, 0, "approval"); err == nil {
				claims.Add(1)
			} else if !errors.Is(err, store.ErrRunClaimConflict) {
				t.Error(err)
			}
		}()
	}
	close(start)
	workers.Wait()
	close(verdicts)
	saved, err := st.GetInboxItem(ctx, scope, "approval")
	if err != nil {
		t.Fatal(err)
	}
	for verdict := range verdicts {
		if verdict != saved.State {
			t.Fatalf("resumed verdict %s differs from saved verdict %s", verdict, saved.State)
		}
	}
	if claims.Load() != 1 {
		t.Fatalf("resume claims=%d, want 1", claims.Load())
	}
}
