/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"testing"

	"github.com/faroshq/provider-app-studio/store"
)

func TestCreateProjectMessageAssistantActionDefaultsToAuto(t *testing.T) {
	action, err := (CreateProjectMessageRequest{}).assistantAction()
	if err != nil {
		t.Fatalf("assistantAction: %v", err)
	}
	if action != projectAssistantActionAuto {
		t.Fatalf("action = %q, want auto", action)
	}
}

func TestCreateProjectMessageAssistantActionValidatesBuildAndContinue(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  CreateProjectMessageRequest
		want projectAssistantAction
		ok   bool
	}{
		{name: "auto", req: CreateProjectMessageRequest{AssistantAction: "auto"}, want: projectAssistantActionAuto, ok: true},
		{name: "ask", req: CreateProjectMessageRequest{AssistantAction: "ask"}, want: projectAssistantActionAsk, ok: true},
		{name: "build", req: CreateProjectMessageRequest{AssistantAction: "build"}, want: projectAssistantActionBuild, ok: true},
		{name: "continue", req: CreateProjectMessageRequest{AssistantAction: "continue", WorkItemID: "wi-1", WorkItemRevision: 2}, want: projectAssistantActionContinue, ok: true},
		{name: "continue requires item", req: CreateProjectMessageRequest{AssistantAction: "continue"}},
		{name: "auto rejects selection", req: CreateProjectMessageRequest{AssistantAction: "auto", WorkItemID: "wi-1", WorkItemRevision: 1}},
		{name: "build rejects selection", req: CreateProjectMessageRequest{AssistantAction: "build", WorkItemID: "wi-1", WorkItemRevision: 1}},
		{name: "unknown", req: CreateProjectMessageRequest{AssistantAction: "mutate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.req.assistantAction()
			if tc.ok {
				if err != nil || got != tc.want {
					t.Fatalf("assistantAction = %q, %v; want %q, nil", got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("assistantAction = %q, nil; want validation error", got)
			}
		})
	}
}

func TestAutoStartsAdaptiveRegardlessOfWording(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := testProjectMessageScope("org-a", "workspace-a", "demo")

	started, err := server.startProjectAssistantAdaptiveRunDurably(
		context.Background(),
		scope,
		"alice",
		"I just tried to use the queue custom toast but it didnt work",
		"auto-exact-wording-1",
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if started.Run.Mode != store.AssistantRunModeAdaptive {
		t.Fatalf("run mode = %q, want adaptive", started.Run.Mode)
	}
	if started.Run.WorkItemID != "" {
		t.Fatalf("work item = %q, want none before escalation", started.Run.WorkItemID)
	}
}
