// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"testing"
	"time"
)

func TestHarnessMCPSessionsExpireAndPrune(t *testing.T) {
	ctx := t.Context()
	m := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws"}
	now := time.Now().UTC()
	first := HarnessMCPSession{TokenHash: "a", RunID: "run-1", AgentName: "bob", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := m.PutHarnessMCPSession(ctx, scope, first); err != nil {
		t.Fatal(err)
	}
	// A relaunch of the same run adds a session; the first stays valid, because
	// a harness answered by a permission resume is still holding it.
	if err := m.PutHarnessMCPSession(ctx, scope, HarnessMCPSession{TokenHash: "b", RunID: "run-1", ExpiresAt: now.Add(time.Hour), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := m.GetHarnessMCPSession(ctx, "a", now); !ok || got.AgentName != "bob" || got.Scope != scope {
		t.Fatalf("first session = %+v, %v", got, ok)
	}
	if _, ok, _ := m.GetHarnessMCPSession(ctx, "a", now.Add(2*time.Hour)); ok {
		t.Fatal("an expired session was returned")
	}
	// Writing a session after both expired prunes them.
	later := now.Add(2 * time.Hour)
	if err := m.PutHarnessMCPSession(ctx, scope, HarnessMCPSession{TokenHash: "c", RunID: "run-2", ExpiresAt: later.Add(time.Hour), CreatedAt: later}); err != nil {
		t.Fatal(err)
	}
	if len(m.mcpSessions) != 1 {
		t.Fatalf("%d sessions kept, want only the live one", len(m.mcpSessions))
	}
	if err := m.PutHarnessMCPSession(ctx, scope, HarnessMCPSession{RunID: "run-3"}); err == nil {
		t.Fatal("a session without a token hash was stored")
	}
}
