// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/faroshq/provider-vibe-studio/session"
)

func TestMemoryStoreAppendCAS(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	scope := Scope{Tenant: "t1"}
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	if err := m.CreateSession(ctx, scope, "s1", "a todo app", now); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Double create conflicts.
	if err := m.CreateSession(ctx, scope, "s1", "again", now); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("double create = %v, want ErrConflict", err)
	}

	e1 := session.NewEvent("s1", "sub1", session.EventSessionCreated, now, session.SessionCreatedData{Input: "a todo app"})
	e2 := session.NewEvent("s1", "sub1", session.EventPhaseChanged, now, session.PhaseChangedData{Phase: session.PhaseIntake})
	last, err := m.AppendEvents(ctx, scope, "s1", 0, []session.Event{e1, e2})
	if err != nil || last != 2 {
		t.Fatalf("AppendEvents = (%d, %v), want (2, nil)", last, err)
	}

	// Stale expectedLast conflicts.
	if _, err := m.AppendEvents(ctx, scope, "s1", 0, []session.Event{e1}); !errors.Is(err, ErrOrdinalConflict) {
		t.Fatalf("stale append = %v, want ErrOrdinalConflict", err)
	}

	// Events read back in order with dense ordinals; fold works.
	events, err := m.ListEvents(ctx, scope, "s1", 0, 0)
	if err != nil || len(events) != 2 {
		t.Fatalf("ListEvents = (%d, %v)", len(events), err)
	}
	for i, e := range events {
		if e.Ordinal != int64(i+1) {
			t.Fatalf("ordinal[%d] = %d", i, e.Ordinal)
		}
	}
	state := session.Fold(events)
	if state.Phase != session.PhaseIntake || state.LastOrdinal != 2 {
		t.Fatalf("fold: %+v", state)
	}

	// After-cursor pagination.
	tail, err := m.ListEvents(ctx, scope, "s1", 1, 0)
	if err != nil || len(tail) != 1 || tail[0].Ordinal != 2 {
		t.Fatalf("ListEvents(after=1) = (%v, %v)", tail, err)
	}

	// Unknown session and foreign tenant are invisible.
	if _, err := m.ListEvents(ctx, scope, "nope", 0, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session = %v, want ErrNotFound", err)
	}
	if _, err := m.ListEvents(ctx, Scope{Tenant: "t2"}, "s1", 0, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant read = %v, want ErrNotFound", err)
	}

	// Listing reflects TouchSession.
	if err := m.TouchSession(ctx, scope, "s1", session.PhaseReview, now.Add(time.Minute)); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
	list, err := m.ListSessions(ctx, scope, 10)
	if err != nil || len(list) != 1 || list[0].Phase != session.PhaseReview {
		t.Fatalf("ListSessions = (%+v, %v)", list, err)
	}
}
