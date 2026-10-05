// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

const concurrentHarnessTurnCallers = 24

func TestMemoryStore_NextHarnessTurnAllocatesUniqueTurnsConcurrently(t *testing.T) {
	assertConcurrentHarnessTurnAllocations(t, NewMemoryStore(), testScope())
}

func TestPostgres_NextHarnessTurnAllocatesUniqueTurnsConcurrently(t *testing.T) {
	ps := openTestPostgres(t)
	assertConcurrentHarnessTurnAllocations(t, ps, pgScope(t, ps))
}

func assertConcurrentHarnessTurnAllocations(t *testing.T, st Store, scope Scope) {
	t.Helper()
	ctx := context.Background()
	identity := HarnessIdentity{
		TaskID:       "uid-task",
		LegacyTaskID: "legacy-task",
		AgentUID:     "agent-uid",
		CreatedAt:    time.Now().UTC().Add(-time.Minute),
	}
	base := time.Now().UTC()
	start := make(chan struct{})
	results := make(chan HarnessSession, concurrentHarnessTurnCallers)
	errs := make(chan error, concurrentHarnessTurnCallers)
	var ready sync.WaitGroup
	var finished sync.WaitGroup
	ready.Add(concurrentHarnessTurnCallers)
	finished.Add(concurrentHarnessTurnCallers)
	for i := range concurrentHarnessTurnCallers {
		go func(i int) {
			defer finished.Done()
			ready.Done()
			<-start
			got, err := st.NextHarnessTurn(ctx, scope, "same-session", base.Add(time.Duration(i)*time.Millisecond), identity)
			if err != nil {
				errs <- err
				return
			}
			results <- got
		}(i)
	}
	ready.Wait()
	close(start)
	finished.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("allocate harness turn: %v", err)
	}

	seen := make([]bool, concurrentHarnessTurnCallers+1)
	count := 0
	for got := range results {
		count++
		if got.Turns < 1 || got.Turns > concurrentHarnessTurnCallers {
			t.Errorf("allocated turn %d outside [1,%d]", got.Turns, concurrentHarnessTurnCallers)
			continue
		}
		if seen[got.Turns] {
			t.Errorf("turn %d was allocated more than once", got.Turns)
		}
		seen[got.Turns] = true
		if got.TaskID != identity.TaskID || got.AgentUID != identity.AgentUID {
			t.Errorf("turn %d has identity task=%q uid=%q, want task=%q uid=%q", got.Turns, got.TaskID, got.AgentUID, identity.TaskID, identity.AgentUID)
		}
	}
	if count != concurrentHarnessTurnCallers {
		t.Fatalf("got %d successful allocations, want %d", count, concurrentHarnessTurnCallers)
	}
	for turn := 1; turn <= concurrentHarnessTurnCallers; turn++ {
		if !seen[turn] {
			t.Errorf("turn %d was not allocated", turn)
		}
	}
}
