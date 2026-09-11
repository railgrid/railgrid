/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tenant_test

import (
	"context"
	"net/http/httptest"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/faroshq/provider-agents/tenant"
	"github.com/faroshq/provider-agents/tenant/tenanttest"
)

var agentRes = tenant.Resource{
	GVR:    schema.GroupVersionResource{Group: "agents.faros.sh", Version: "v1alpha1", Resource: "agents"},
	Kind:   "Agent",
	Plural: "Agents",
}

func agent(name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "agents.faros.sh/v1alpha1",
		"kind":       "Agent",
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}}
}

func scopeFor(t *testing.T, ws *tenanttest.Server) *tenant.Scope {
	t.Helper()
	srv := httptest.NewServer(ws)
	t.Cleanup(srv.Close)
	scope, err := tenant.NewClient(srv.URL, false).For("c1", "tok")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	return scope
}

func TestForValidatesInputs(t *testing.T) {
	c := tenant.NewClient("https://hub.example", false)
	if _, err := c.For("", "tok"); err == nil {
		t.Fatal("want error for empty cluster id")
	}
	if _, err := c.For("c1", ""); err == nil {
		t.Fatal("want error for empty token")
	}
}

// Apply is an upsert, not a strict create: a second Apply of the same name
// replaces the object, and a stale resourceVersion on the desired object does
// not produce a conflict (the server's is carried over, as the previous
// transport did).
func TestApplyUpserts(t *testing.T) {
	ws := tenanttest.New()
	s := scopeFor(t, ws)
	ctx := context.Background()

	created, err := s.Apply(ctx, agent("a", map[string]any{"model": "one"}))
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if created.GetResourceVersion() == "" {
		t.Fatal("created object should carry the server's resourceVersion")
	}

	desired := agent("a", map[string]any{"model": "two"})
	desired.SetResourceVersion("stale")
	updated, err := s.Apply(ctx, desired)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got, _, _ := unstructured.NestedString(updated.Object, "spec", "model"); got != "two" {
		t.Fatalf("spec.model = %q, want two", got)
	}
	if updated.GetUID() != created.GetUID() {
		t.Fatalf("uid changed across apply: %q -> %q", created.GetUID(), updated.GetUID())
	}
	if writes := ws.Writes(); len(writes) != 2 || writes[0].Verb != "create" || writes[1].Verb != "update" {
		t.Fatalf("want [create update], got %+v", writes)
	}
}

func TestApplyStatusPatchesOnlyStatus(t *testing.T) {
	ws := tenanttest.New()
	ws.Add(agent("a", map[string]any{"model": "one"}))
	s := scopeFor(t, ws)
	ctx := context.Background()

	obj := agent("a", map[string]any{"model": "changed-but-ignored"})
	obj.Object["status"] = map[string]any{"phase": "Ready"}
	if err := s.ApplyStatus(ctx, obj); err != nil {
		t.Fatalf("ApplyStatus: %v", err)
	}
	got, err := s.Get(ctx, agentRes, "", "a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if phase, _, _ := unstructured.NestedString(got.Object, "status", "phase"); phase != "Ready" {
		t.Fatalf("status.phase = %q, want Ready", phase)
	}
	if model, _, _ := unstructured.NestedString(got.Object, "spec", "model"); model != "one" {
		t.Fatalf("status patch must not touch spec, got spec.model = %q", model)
	}
	if err := s.ApplyStatus(ctx, agent("missing", nil)); !apierrors.IsNotFound(err) {
		t.Fatalf("ApplyStatus on absent object: want NotFound, got %v", err)
	}
}

func TestGetListDeleteErrors(t *testing.T) {
	ws := tenanttest.New()
	ws.Add(agent("a", nil), agent("b", nil))
	s := scopeFor(t, ws)
	ctx := context.Background()

	if _, err := s.Get(ctx, agentRes, "", "nope"); !apierrors.IsNotFound(err) {
		t.Fatalf("Get absent: want NotFound, got %v", err)
	}
	items, err := s.List(ctx, agentRes, "")
	if err != nil || len(items) != 2 {
		t.Fatalf("List: got %d items, err %v", len(items), err)
	}
	if err := s.Delete(ctx, agentRes, "", "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(ctx, agentRes, "", "a"); !apierrors.IsNotFound(err) {
		t.Fatalf("Delete twice: want NotFound, got %v", err)
	}
	items, _ = s.List(ctx, agentRes, "")
	if len(items) != 1 || items[0].GetName() != "b" {
		t.Fatalf("after delete want [b], got %d items", len(items))
	}
}
