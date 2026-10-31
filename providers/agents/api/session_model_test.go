// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

func TestChatModelSettingsPinOnlyCurrentConversation(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "agent"}
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.SetModelCredential(llm.PurposeChat, "original")
	if err := s.store.PutSessionModelSettings(ctx, scope, "one", store.SessionModelSettings{ModelCredential: "chosen"}); err != nil {
		t.Fatal(err)
	}
	run := taskRun{Scope: scope, Agent: agent, SessionID: "one", Trigger: agentsv1alpha1.RunTriggerChat}
	if err := s.resolveChatModelSettings(ctx, &run); err != nil {
		t.Fatal(err)
	}
	if run.ModelSettings.ModelCredential != "chosen" || agent.Spec.ModelCredentialFor(llm.PurposeChat) != "original" {
		t.Fatal("selection changed Agent default")
	}
	if err := s.store.PutSessionModelSettings(ctx, scope, "one", store.SessionModelSettings{ModelCredential: "later"}); err != nil {
		t.Fatal(err)
	}
	if err := s.resolveChatModelSettings(ctx, &run); err != nil {
		t.Fatal(err)
	}
	if run.ModelSettings.ModelCredential != "chosen" {
		t.Fatal("resume lost pinned selection")
	}
	run.ModelSettings = nil
	run.SessionID = "two"
	if err := s.resolveChatModelSettings(ctx, &run); err != nil {
		t.Fatal(err)
	}
	if run.ModelSettings.ModelCredential != "original" {
		t.Fatal("another chat inherited override")
	}
	run.ModelSettings = nil
	run.Trigger = "schedule"
	if err := s.resolveChatModelSettings(ctx, &run); err != nil {
		t.Fatal(err)
	}
	if run.ModelSettings != nil {
		t.Fatal("background run inherited chat preferences")
	}
}
