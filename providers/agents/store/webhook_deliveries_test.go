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

// delivery builds a sink row whose routes are one per trigger name given, each
// with the row's outcome.
func delivery(id, webhookUID, outcome string, at time.Time, triggers ...string) WebhookDelivery {
	routes := []WebhookRoute{}
	for _, tr := range triggers {
		routes = append(routes, WebhookRoute{Trigger: tr, Outcome: outcome})
	}
	return WebhookDelivery{
		ID: id, WebhookName: "github", WebhookUID: webhookUID, ReceivedAt: at, Outcome: outcome,
		EventType: "pull_request", Headers: map[string]string{"X-Github-Event": "pull_request"},
		Body:   `{"action":"opened","n":"` + id + `"}`,
		Routes: routes,
	}
}

// The sink is the one record of traffic that did NOT fire: it keeps every
// outcome, newest first, capped per webhook, with bodies previewed on the list
// and whole on a single read, narrowed by outcome or by routed trigger, and it
// does not leak between webhooks — including a webhook recreated under the
// same name, which has a new UID.
func testWebhookDeliveries(t *testing.T, s Store, sc Scope) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	const uid, otherUID = "uid-1", "uid-2"
	// A webhook delivery belongs to no agent: the scope is org+workspace.
	sc.AgentName = ""

	for i := range 5 {
		outcome, triggers := "filtered", []string{"on-pr"}
		if i%2 == 1 {
			outcome, triggers = "fired", []string{"on-pr", "on-push"}
		}
		d := delivery(fmt.Sprintf("d%d", i), uid, outcome, base.Add(time.Duration(i)*time.Second), triggers...)
		if i == 4 {
			d.Body = strings.Repeat("x", WebhookDeliveryPreviewBytes+100)
			d.BodyTruncated = true
		}
		if err := s.RecordWebhookDelivery(ctx, sc, d, 3); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if err := s.RecordWebhookDelivery(ctx, sc, delivery("other", otherUID, "fired", base, "on-pr"), 3); err != nil {
		t.Fatal(err)
	}

	rows, err := s.ListWebhookDeliveries(ctx, sc, uid, WebhookDeliveryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(rows); strings.Join(got, ",") != "d4,d3,d2" {
		t.Fatalf("list = %v, want the newest three, newest first (retention 3)", got)
	}
	if rows[0].BodySize != WebhookDeliveryPreviewBytes+100 || len(rows[0].Body) != WebhookDeliveryPreviewBytes || !rows[0].BodyTruncated {
		t.Fatalf("list row body: size %d, preview %d, truncated %v", rows[0].BodySize, len(rows[0].Body), rows[0].BodyTruncated)
	}
	if rows[1].Headers["X-Github-Event"] != "pull_request" || rows[1].EventType != "pull_request" || rows[1].WebhookName != "github" {
		t.Fatalf("list row lost its metadata: %+v", rows[1])
	}
	if len(rows[1].Routes) != 2 || rows[1].Routes[1].Trigger != "on-push" || rows[1].Routes[1].Outcome != "fired" {
		t.Fatalf("list row lost its routes: %+v", rows[1].Routes)
	}

	fired, err := s.ListWebhookDeliveries(ctx, sc, uid, WebhookDeliveryQuery{Outcome: "fired", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(fired); strings.Join(got, ",") != "d3" {
		t.Fatalf("fired = %v, want d3 (d1 was pruned)", got)
	}
	pushes, err := s.ListWebhookDeliveries(ctx, sc, uid, WebhookDeliveryQuery{Trigger: "on-push"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(pushes); strings.Join(got, ",") != "d3" {
		t.Fatalf("trigger=on-push = %v, want d3 only", got)
	}
	if none, err := s.ListWebhookDeliveries(ctx, sc, uid, WebhookDeliveryQuery{Trigger: "ghost"}); err != nil || len(none) != 0 {
		t.Fatalf("trigger=ghost = %v, %v; want nothing", ids(none), err)
	}
	one, err := s.ListWebhookDeliveries(ctx, sc, uid, WebhookDeliveryQuery{Limit: 1})
	if err != nil || len(one) != 1 || one[0].ID != "d4" {
		t.Fatalf("limit 1 = %v, %v", ids(one), err)
	}

	whole, found, err := s.GetWebhookDelivery(ctx, sc, uid, "d4")
	if err != nil || !found {
		t.Fatalf("get d4: found=%v err=%v", found, err)
	}
	if len(whole.Body) != WebhookDeliveryPreviewBytes+100 || whole.BodySize != len(whole.Body) {
		t.Fatalf("single read body = %d bytes (size %d), want the whole body", len(whole.Body), whole.BodySize)
	}
	if _, found, _ := s.GetWebhookDelivery(ctx, sc, uid, "d0"); found {
		t.Fatal("d0 was pruned and must not be readable")
	}
	if _, found, _ := s.GetWebhookDelivery(ctx, sc, otherUID, "d4"); found {
		t.Fatal("a delivery is readable only under its own webhook's UID")
	}
	if _, found, _ := s.GetWebhookDelivery(ctx, Scope{OrgUUID: "someone", WorkspaceUUID: "else"}, uid, "d4"); found {
		t.Fatal("a delivery is readable only in its own tenant")
	}

	// An unrouted delivery has an empty routes list, not a missing one.
	if err := s.RecordWebhookDelivery(ctx, sc, delivery("d5", uid, WebhookDeliveryUnrouted, base.Add(10*time.Second)), 3); err != nil {
		t.Fatal(err)
	}
	unrouted, found, err := s.GetWebhookDelivery(ctx, sc, uid, "d5")
	if err != nil || !found || unrouted.Routes == nil || len(unrouted.Routes) != 0 {
		t.Fatalf("unrouted row: found=%v err=%v routes=%#v", found, err, unrouted.Routes)
	}

	// Retention 0 records nothing — the webhook opted out of the sink.
	if err := s.RecordWebhookDelivery(ctx, sc, delivery("d9", uid, "fired", base.Add(time.Minute)), 0); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetWebhookDelivery(ctx, sc, uid, "d9"); found {
		t.Fatal("retention 0 must not record")
	}

	// The webhook's teardown empties its sink and nobody else's.
	if err := s.DeleteWebhookDeliveries(ctx, sc, uid); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.ListWebhookDeliveries(ctx, sc, uid, WebhookDeliveryQuery{}); err != nil || len(rows) != 0 {
		t.Fatalf("after DeleteWebhookDeliveries, %s has %d deliveries (%v)", uid, len(rows), err)
	}
	if rows, err := s.ListWebhookDeliveries(ctx, sc, otherUID, WebhookDeliveryQuery{}); err != nil || len(rows) != 1 {
		t.Fatalf("another webhook's sink must survive: %d rows (%v)", len(rows), err)
	}
}

func ids(rows []WebhookDelivery) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func TestWebhookDeliveryRollup(t *testing.T) {
	for _, tc := range []struct {
		routes []string
		want   string
	}{
		{nil, WebhookDeliveryUnrouted},
		{[]string{"filtered", "suspended"}, "filtered"},
		{[]string{"filtered", "failed"}, "failed"},
		{[]string{"failed", "fired"}, "fired"},
		{[]string{"noTask"}, "filtered"},
	} {
		routes := []WebhookRoute{}
		for i, o := range tc.routes {
			routes = append(routes, WebhookRoute{Trigger: fmt.Sprint(i), Outcome: o})
		}
		if got := WebhookDeliveryRollup(routes); got != tc.want {
			t.Errorf("rollup(%v) = %q, want %q", tc.routes, got, tc.want)
		}
	}
}

func TestMemoryStore_WebhookDeliveries(t *testing.T) {
	testWebhookDeliveries(t, NewMemoryStore(), testScope())
}

func TestPostgres_WebhookDeliveries(t *testing.T) {
	ps := openTestPostgres(t)
	testWebhookDeliveries(t, ps, pgScope(t, ps))
}

// A tenant mapping learned after deliveries were filed under the cluster-keyed
// fallback scope carries the sink along with it.
func TestMemoryStore_WebhookDeliveriesFollowScopeMigration(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Now().UTC()
	fallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: "cluster-1", ClusterID: "cluster-1"}
	if err := s.RecordWebhookDelivery(ctx, fallback, delivery("d1", "uid-1", "filtered", now, "on-pr"), 10); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTenantRef(ctx, "cluster-1", TenantRef{OrgUUID: "org", WorkspaceUUID: "ws", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	mapped := Scope{OrgUUID: "org", WorkspaceUUID: "ws"}
	if _, found, err := s.GetWebhookDelivery(ctx, mapped, "uid-1", "d1"); err != nil || !found {
		t.Fatalf("after migration: found=%v err=%v", found, err)
	}
}
