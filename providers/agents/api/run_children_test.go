// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-sdk/dataplane"
)

func TestRunTraceIncludesDelegatedAgentsWithinItsWorkspace(t *testing.T) {
	ctx := context.Background()
	storage := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
	parent := store.Run{ID: "parent", AgentName: "coordinator", Phase: store.RunPhaseSucceeded, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := storage.SaveRun(ctx, scope, parent); err != nil {
		t.Fatal(err)
	}
	for _, workspace := range []string{"workspace", "foreign-workspace"} {
		childScope := scope
		childScope.AgentName, childScope.WorkspaceUUID = "reviewer", workspace
		child := store.Run{ID: "child-" + workspace, AgentName: "reviewer", ParentRunID: "parent", Trigger: "delegation", Phase: store.RunPhaseSucceeded, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := storage.SaveRun(ctx, childScope, child); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := (&Server{store: storage}).runDetailFor(ctx, scope, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Children) != 1 || detail.Children[0].ID != "child-workspace" {
		t.Fatalf("delegation lineage crossed a workspace or omitted its reviewer: %+v", detail.Children)
	}
}

func TestRunTraceFiltersChildrenByCallerAccess(t *testing.T) {
	st := store.NewMemoryStore()
	ctx := dataplane.WithProxiedIdentity(t.Context(), dataplane.ProxiedIdentity{User: "parent-reader"})
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "parent-agent"}
	now := time.Now()
	for _, run := range []store.Run{
		{ID: "parent", AgentName: scope.AgentName},
		{ID: "allowed", AgentName: "delegate", ParentRunID: "parent"},
		{ID: "denied", AgentName: scope.AgentName, ParentRunID: "parent", Input: "private input", Message: "private error"},
		{ID: "review-error", AgentName: "delegate", ParentRunID: "parent"},
	} {
		runScope := scope
		runScope.AgentName = run.AgentName
		run.CreatedAt, run.UpdatedAt, run.Phase = now, now, store.RunPhaseSucceeded
		if err := st.SaveRun(ctx, runScope, run); err != nil {
			t.Fatal(err)
		}
	}
	provider := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	provider.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		name, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "name")
		resource, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "resource")
		verb, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "verb")
		user, _, _ := unstructured.NestedString(review.Object, "spec", "user")
		if resource != "runs" || verb != "get" || user != "parent-reader" {
			t.Errorf("incorrect child access review: %v", review.Object)
		}
		if name == "review-error" {
			return true, nil, errors.New("authorization unavailable")
		}
		return true, &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"allowed": name == "allowed"}}}, nil
	})
	ctx = withGate(ctx, &gateInfo{provider: provider})
	detail, err := (&Server{store: st}).runDetailFor(ctx, scope, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Children) != 1 || detail.Children[0].ID != "allowed" {
		t.Fatalf("trace returned child Runs without access: %+v", detail.Children)
	}
}
