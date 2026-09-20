/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package store

import (
	"context"
	"testing"
	"time"
)

func TestThreadEventKeyIsUnambiguous(t *testing.T) {
	a := threadEventKey(Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "p", ProjectUID: "u"}, "thread")
	b := threadEventKey(Scope{OrgUUID: "org", WorkspaceUUID: "wsp", ProjectName: "", ProjectUID: "u"}, "thread")
	if a == b {
		t.Fatalf("adjacent scope fields aliased onto one key: %q", a)
	}
	if threadEventKey(Scope{OrgUUID: "org"}, " thread ") != threadEventKey(Scope{OrgUUID: "org"}, "thread") {
		t.Fatal("thread id whitespace changed the key")
	}
}

func TestThreadEventBroadcasterCoalescesAndReleases(t *testing.T) {
	var b threadEventBroadcaster
	ch, release := b.subscribe("k")

	// Two publishes with nothing reading in between collapse into one "look
	// again" edge; the reader re-reads from its own cursor either way.
	b.publish("k")
	b.publish("k")
	select {
	case <-ch:
	default:
		t.Fatal("subscriber was not signalled")
	}
	select {
	case <-ch:
		t.Fatal("signals were not coalesced")
	default:
	}

	// A publish on another key is not delivered here.
	b.publish("other")
	select {
	case <-ch:
		t.Fatal("subscriber received another thread's signal")
	default:
	}

	release()
	release() // idempotent
	b.publish("k")
	select {
	case <-ch:
		t.Fatal("released subscriber still received a signal")
	default:
	}
}

func TestThreadEventBroadcasterPublishAllWakesEveryThread(t *testing.T) {
	var b threadEventBroadcaster
	first, releaseFirst := b.subscribe("a")
	second, releaseSecond := b.subscribe("b")
	defer releaseFirst()
	defer releaseSecond()

	b.publishAll()
	for name, ch := range map[string]<-chan struct{}{"a": first, "b": second} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("subscriber %q was not woken by a reconnect", name)
		}
	}
}

func TestMemoryStoreWatchAssistantThreadEventsSignalsOnAppend(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid"}
	thread, err := s.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-1", ActorID: "alice", Status: AssistantThreadStatusActive}, nil)
	if err != nil {
		t.Fatalf("CreateAssistantThread: %v", err)
	}

	arrivals, release, err := s.WatchAssistantThreadEvents(ctx, scope, thread.ID)
	if err != nil {
		t.Fatalf("WatchAssistantThreadEvents: %v", err)
	}
	defer release()

	if _, err := s.AppendAssistantThreadEvent(ctx, scope, AssistantThreadEvent{ThreadID: thread.ID, TurnID: "turn-1", Type: "turn.started"}, 0); err != nil {
		t.Fatalf("AppendAssistantThreadEvent: %v", err)
	}
	select {
	case <-arrivals:
	case <-time.After(2 * time.Second):
		t.Fatal("append did not wake the watcher")
	}

	if _, _, err := s.WatchAssistantThreadEvents(ctx, Scope{}, thread.ID); err == nil {
		t.Fatal("an invalid scope must not produce a subscription")
	}
}

func TestMemoryStoreAssistantThreadActivity(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid"}
	if _, err := s.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-1", ActorID: "alice", Status: AssistantThreadStatusActive}, nil); err != nil {
		t.Fatalf("CreateAssistantThread: %v", err)
	}
	activity, err := s.AssistantThreadActivity(ctx, scope, "thread-1")
	if err != nil {
		t.Fatalf("AssistantThreadActivity: %v", err)
	}
	if activity.TurnCount != 0 || activity.LastActivityAt.IsZero() {
		t.Fatalf("empty thread activity = %+v, want no turns and the thread's own timestamp", activity)
	}
	if _, err := s.CreateAssistantTurn(ctx, scope, AssistantTurn{ID: "turn-1", ThreadID: "thread-1", ActorID: "alice", ClientUserMessageID: "msg-1", Status: AssistantTurnStatusInProgress}, nil); err != nil {
		t.Fatalf("CreateAssistantTurn: %v", err)
	}
	activity, err = s.AssistantThreadActivity(ctx, scope, "thread-1")
	if err != nil {
		t.Fatalf("AssistantThreadActivity: %v", err)
	}
	if activity.TurnCount != 1 {
		t.Fatalf("turn count = %d, want 1", activity.TurnCount)
	}
	if _, err := s.AssistantThreadActivity(ctx, scope, "missing"); err == nil {
		t.Fatal("a missing thread must report ErrAssistantThreadNotFound")
	}
}
