// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"testing"
	"time"

	"github.com/railgrid/provider-agents/store"
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
