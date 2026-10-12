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
	"fmt"
	"strings"
	"testing"
	"time"
)

func delivery(id, triggerUID, outcome string, at time.Time) TriggerDelivery {
	return TriggerDelivery{
		ID: id, TriggerName: "on-pr", TriggerUID: triggerUID, ReceivedAt: at, Outcome: outcome,
		EventType: "pull_request", Headers: map[string]string{"X-Github-Event": "pull_request"},
		Body: `{"action":"opened","n":"` + id + `"}`,
	}
}

// The delivery log is the one record of traffic that did NOT fire: it keeps
// every outcome, newest first, capped per trigger, with bodies previewed on
// the list and whole on a single read, and it does not leak between triggers
// — including a trigger recreated under the same name, which has a new UID.
func testTriggerDeliveries(t *testing.T, s Store, sc Scope) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	const uid, otherUID = "uid-1", "uid-2"

	for i := range 5 {
		outcome := "filtered"
		if i%2 == 1 {
			outcome = "fired"
		}
		d := delivery(fmt.Sprintf("d%d", i), uid, outcome, base.Add(time.Duration(i)*time.Second))
		if i == 4 {
			d.Body = strings.Repeat("x", TriggerDeliveryPreviewBytes+100)
			d.BodyTruncated = true
		}
		if err := s.RecordTriggerDelivery(ctx, sc, d, 3); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if err := s.RecordTriggerDelivery(ctx, sc, delivery("other", otherUID, "fired", base), 3); err != nil {
		t.Fatal(err)
	}

	rows, err := s.ListTriggerDeliveries(ctx, sc, uid, TriggerDeliveryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(rows); strings.Join(got, ",") != "d4,d3,d2" {
		t.Fatalf("list = %v, want the newest three, newest first (retention 3)", got)
	}
	if rows[0].BodySize != TriggerDeliveryPreviewBytes+100 || len(rows[0].Body) != TriggerDeliveryPreviewBytes || !rows[0].BodyTruncated {
		t.Fatalf("list row body: size %d, preview %d, truncated %v", rows[0].BodySize, len(rows[0].Body), rows[0].BodyTruncated)
	}
	if rows[1].Headers["X-Github-Event"] != "pull_request" || rows[1].EventType != "pull_request" || rows[1].AgentName != sc.AgentName {
		t.Fatalf("list row lost its metadata: %+v", rows[1])
	}

	fired, err := s.ListTriggerDeliveries(ctx, sc, uid, TriggerDeliveryQuery{Outcome: "fired", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(fired); strings.Join(got, ",") != "d3" {
		t.Fatalf("fired = %v, want d3 (d1 was pruned)", got)
	}
	one, err := s.ListTriggerDeliveries(ctx, sc, uid, TriggerDeliveryQuery{Limit: 1})
	if err != nil || len(one) != 1 || one[0].ID != "d4" {
		t.Fatalf("limit 1 = %v, %v", ids(one), err)
	}

	whole, found, err := s.GetTriggerDelivery(ctx, sc, uid, "d4")
	if err != nil || !found {
		t.Fatalf("get d4: found=%v err=%v", found, err)
	}
	if len(whole.Body) != TriggerDeliveryPreviewBytes+100 {
		t.Fatalf("single read body = %d bytes, want the whole body", len(whole.Body))
	}
	if _, found, _ := s.GetTriggerDelivery(ctx, sc, uid, "d0"); found {
		t.Fatal("d0 was pruned and must not be readable")
	}
	if _, found, _ := s.GetTriggerDelivery(ctx, sc, otherUID, "d4"); found {
		t.Fatal("a delivery is readable only under its own trigger's UID")
	}
	if _, found, _ := s.GetTriggerDelivery(ctx, Scope{OrgUUID: "someone", WorkspaceUUID: "else", AgentName: sc.AgentName}, uid, "d4"); found {
		t.Fatal("a delivery is readable only in its own tenant")
	}

	// Retention 0 records nothing — the trigger opted out of the log.
	if err := s.RecordTriggerDelivery(ctx, sc, delivery("d9", uid, "fired", base.Add(time.Minute)), 0); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetTriggerDelivery(ctx, sc, uid, "d9"); found {
		t.Fatal("retention 0 must not record")
	}

	// The agent's teardown takes its triggers' logs with it.
	if err := s.DeleteAgentData(ctx, sc, sc.AgentName); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{uid, otherUID} {
		rows, err := s.ListTriggerDeliveries(ctx, sc, u, TriggerDeliveryQuery{})
		if err != nil || len(rows) != 0 {
			t.Fatalf("after DeleteAgentData, %s has %d deliveries (%v)", u, len(rows), err)
		}
	}
}

func ids(rows []TriggerDelivery) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func TestMemoryStore_TriggerDeliveries(t *testing.T) {
	testTriggerDeliveries(t, NewMemoryStore(), testScope())
}

func TestPostgres_TriggerDeliveries(t *testing.T) {
	ps := openTestPostgres(t)
	testTriggerDeliveries(t, ps, pgScope(t, ps))
}

// A tenant mapping learned after deliveries were filed under the cluster-keyed
// fallback scope carries the log along with it.
func TestMemoryStore_TriggerDeliveriesFollowScopeMigration(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Now().UTC()
	fallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: "cluster-1", AgentName: "helper", ClusterID: "cluster-1"}
	if err := s.RecordTriggerDelivery(ctx, fallback, delivery("d1", "uid-1", "filtered", now), 10); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTenantRef(ctx, "cluster-1", TenantRef{OrgUUID: "org", WorkspaceUUID: "ws", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	mapped := Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "helper"}
	if _, found, err := s.GetTriggerDelivery(ctx, mapped, "uid-1", "d1"); err != nil || !found {
		t.Fatalf("after migration: found=%v err=%v", found, err)
	}
}
