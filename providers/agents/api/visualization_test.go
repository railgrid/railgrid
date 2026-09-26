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
	"slices"
	"testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tools"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestVisualizationGrantIsOptInAndTriggerScoped(t *testing.T) {
	s := &Server{store: store.NewMemoryStore()}
	agent := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "scout"}}
	deps := tools.Deps{Store: s.store, Agent: agent, CR: fakeCR{}, Scope: store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "scout"}}
	check := func(trigger string, want bool) {
		t.Helper()
		got, _, close := s.buildToolset(context.Background(), deps, taskRun{Agent: agent, Trigger: trigger})
		defer close()
		if slices.Contains(toolNames(got), "visualize_data") != want {
			t.Fatalf("trigger %s tools %v: visualization want %v", trigger, toolNames(got), want)
		}
	}
	check(agentsv1alpha1.RunTriggerChat, false)
	agent.Spec.Tools.Interactive.Families = []string{"visualization"}
	check(agentsv1alpha1.RunTriggerChat, true)
	check(agentsv1alpha1.RunTriggerAPI, false)
	agent.Spec.Tools.Interactive = agentsv1alpha1.ToolGrant{Toolsets: []string{"charts"}}
	deps.CR = fakeCR{toolsets: map[string]*agentsv1alpha1.Toolset{"charts": mkToolset("charts", []string{"visualization"}, nil, nil)}}
	check(agentsv1alpha1.RunTriggerChat, true)
	agent.Spec.Tools.Background.Families = []string{"visualization"}
	check(agentsv1alpha1.RunTriggerAPI, true)
}
