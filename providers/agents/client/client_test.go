// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package client

import (
	"context"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/tenant"
)

func TestTypedResourceCreateRejectsDuplicateAndPreservesExistingSpec(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		AgentGVR: "AgentList",
	})
	c := NewFromScope(tenant.NewScopeFromDynamic("workspace-cluster", dyn))

	original := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "assistant"},
		Spec: agentsv1alpha1.AgentSpec{
			DisplayName: "Original display name", Description: "original description",
			SystemPrompt: "original system prompt", Autonomy: agentsv1alpha1.AutonomyAsk,
			Tools: agentsv1alpha1.AgentToolPolicy{Interactive: agentsv1alpha1.ToolGrant{Connections: []string{"original-connection"}}},
		},
	}
	if _, err := c.Agents().Create(context.Background(), original, metav1.CreateOptions{}); err != nil {
		t.Fatalf("first create: %v", err)
	}

	duplicate := &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: original.Name},
		Spec: agentsv1alpha1.AgentSpec{
			DisplayName: "Replacement name", Description: "replacement description",
			SystemPrompt: "replacement system prompt", Autonomy: agentsv1alpha1.AutonomyAuto,
			Tools: agentsv1alpha1.AgentToolPolicy{Interactive: agentsv1alpha1.ToolGrant{Connections: []string{"replacement-connection"}}},
		},
	}
	if _, err := c.Agents().Create(context.Background(), duplicate, metav1.CreateOptions{}); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("duplicate create error = %v, want AlreadyExists", err)
	}

	got, err := c.Agents().Get(context.Background(), original.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get original after duplicate create: %v", err)
	}
	if !reflect.DeepEqual(got.Spec, original.Spec) {
		t.Fatalf("duplicate create changed existing spec:\n got: %+v\nwant: %+v", got.Spec, original.Spec)
	}

	actions := dyn.Actions()
	if len(actions) != 3 || actions[0].GetVerb() != "create" || actions[1].GetVerb() != "create" || actions[2].GetVerb() != "get" {
		verbs := make([]string, 0, len(actions))
		for _, action := range actions {
			verbs = append(verbs, action.GetVerb())
		}
		t.Fatalf("request verbs = %v, want create, create, get (no read-before-create/update)", verbs)
	}
}
