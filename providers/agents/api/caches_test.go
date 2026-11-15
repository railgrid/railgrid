// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"testing"
	"time"

	"github.com/railgrid/provider-agents/store"

	"github.com/railgrid/railgrid/pkg/runner"
)

func TestSubjectCacheIsKeyedByClusterAndBearerAndExpires(t *testing.T) {
	var c subjectCache
	now := time.Now()
	key := subjectCacheKey("cluster-a", "token-1")
	c.put(key, "alice", now)
	if got, ok := c.get(key, now.Add(subjectCacheTTL/2)); !ok || got != "alice" {
		t.Fatalf("cached subject = %q, %v", got, ok)
	}
	if _, ok := c.get(subjectCacheKey("cluster-b", "token-1"), now); ok {
		t.Fatal("a bearer's subject was reused for another cluster")
	}
	if _, ok := c.get(subjectCacheKey("cluster-a", "token-2"), now); ok {
		t.Fatal("one bearer's subject was reused for another bearer")
	}
	if _, ok := c.get(key, now.Add(subjectCacheTTL)); ok {
		t.Fatal("an expired subject was reused")
	}
}

func TestCleanupObservationCacheHoldsSettledStatesLonger(t *testing.T) {
	var c cleanupObservationCache
	now := time.Now()
	running := cleanupObservationKey{clusterID: "c", run: "r1", attempt: "a1"}
	done := cleanupObservationKey{clusterID: "c", run: "r2", attempt: "a2"}
	c.put(running, &runner.CleanupObservation{State: runner.CleanupRunning}, now)
	c.put(done, &runner.CleanupObservation{State: runner.CleanupDone}, now)
	later := now.Add(cleanupObservationTTL + time.Second)
	if _, ok := c.get(running, later); ok {
		t.Fatal("a changing cleanup state outlived its short TTL")
	}
	if got, ok := c.get(done, later); !ok || got.State != runner.CleanupDone {
		t.Fatalf("settled cleanup state = %+v, %v", got, ok)
	}
	if _, ok := c.get(cleanupObservationKey{clusterID: "other", run: "r2", attempt: "a2"}, now); ok {
		t.Fatal("an observation leaked across clusters")
	}
}

func TestInboxRepairThrottleOnlyAfterACleanSweep(t *testing.T) {
	var throttle inboxRepairThrottle
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "a"}
	now := time.Now()
	if throttle.recent(scope, now) {
		t.Fatal("an Agent never swept was treated as recently swept")
	}
	throttle.mark(scope, now)
	if !throttle.recent(scope, now.Add(inboxRepairInterval/2)) {
		t.Fatal("a clean sweep was not remembered")
	}
	if throttle.recent(store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "b"}, now) {
		t.Fatal("one Agent's sweep throttled another")
	}
	if throttle.recent(scope, now.Add(inboxRepairInterval)) {
		t.Fatal("a sweep was trusted past its interval")
	}
}
