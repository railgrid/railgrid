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
	"errors"
	"strings"
	"testing"

	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/executor"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

func TestHarnessSettingsSnapshot(t *testing.T) {
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.Backend.Type = "harness"
	agent.Spec.Backend.Harness = &agentsv1alpha1.AgentHarnessBackend{Model: "gpt-6-luna", ReasoningEffort: "max"}
	snapshot := resolveHarnessSettings(agent, nil)
	if snapshot.Model != "gpt-6-luna" || snapshot.ReasoningEffort != "max" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	agent.Spec.Backend.Harness.Model = "changed"
	agent.Spec.Backend.Harness.ReasoningEffort = "low"
	restored := resolveHarnessSettings(agent, snapshot)
	if *restored != *snapshot || restored == snapshot {
		t.Fatal("snapshot was not preserved as an independent copy")
	}
	empty := resolveHarnessSettings(agent, &store.HarnessSettings{})
	if empty.Model != "" || empty.ReasoningEffort != "" {
		t.Fatal("explicit harness defaults inherited new Agent settings")
	}
}

func TestInvokeHarnessSettings(t *testing.T) {
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.Backend.Type = "harness"
	agent.Spec.Backend.Harness = &agentsv1alpha1.AgentHarnessBackend{Model: "original", ReasoningEffort: "max"}
	ptr := func(s string) *string { return &s }
	for _, tc := range []struct {
		name          string
		model, effort *string
		want          store.HarnessSettings
		bad           bool
	}{
		{"inherit", nil, nil, store.HarnessSettings{Model: "original", ReasoningEffort: "max"}, false},
		{"clear", ptr(""), ptr(""), store.HarnessSettings{}, false},
		{"trim", ptr(" next "), ptr(" low "), store.HarnessSettings{Model: "next", ReasoningEffort: "low"}, false},
		{"malformed", nil, ptr("max!"), store.HarnessSettings{}, true},
		{"uppercase", nil, ptr("MAX"), store.HarnessSettings{}, true},
		{"long", nil, ptr(strings.Repeat("a", 33)), store.HarnessSettings{}, true},
		{"long-model", ptr(strings.Repeat("a", 254)), nil, store.HarnessSettings{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := invokeHarnessSettings(agent, tc.model, tc.effort)
			if (err != nil) != tc.bad {
				t.Fatalf("error = %v", err)
			}
			if !tc.bad && *got != tc.want {
				t.Fatalf("settings = %+v; want %+v", got, tc.want)
			}
		})
	}
	agent.Spec.Backend.Type = "model"
	if _, err := invokeHarnessSettings(agent, nil, ptr("")); err == nil {
		t.Fatal("accepted override for model backend")
	}
	if got, err := invokeHarnessSettings(agent, nil, nil); err != nil || got != nil {
		t.Fatalf("model settings = %+v, %v", got, err)
	}
}

type failingSettingsStore struct {
	store.Store
	err error
}

func (s failingSettingsStore) GetRun(context.Context, store.Scope, string) (store.Run, error) {
	return store.Run{}, s.err
}

func TestQueuedHarnessSettings(t *testing.T) {
	ctx := t.Context()
	agent := &agentsv1alpha1.Agent{TypeMeta: metav1.TypeMeta{APIVersion: "agents.railgrid.ai/v1alpha1", Kind: "Agent"}, ObjectMeta: metav1.ObjectMeta{Name: "coder"}}
	agent.Spec.Backend.Type = "harness"
	agent.Spec.Backend.Harness = &agentsv1alpha1.AgentHarnessBackend{Model: "original", ReasoningEffort: "max"}
	scheme := runtime.NewScheme()
	if err := agentsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, agent)
	mem := store.NewMemoryStore()
	server := &Server{store: mem}
	bg := &background{server: server, scopedFn: func(context.Context, string) (dynamic.Interface, error) { return dyn, nil }}
	server.bg = bg
	job := executor.Job{ClusterID: "tenant", AgentRef: "coder", Task: "test", Trigger: agentsv1alpha1.RunTriggerAPI}
	id, err := bg.submitRun(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	scope := bg.scopeFor(ctx, "tenant", "coder")
	queued, err := mem.GetRun(ctx, scope, id)
	if err != nil {
		t.Fatal(err)
	}
	object, err := dyn.Resource(agentsclient.AgentGVR).Get(ctx, "coder", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	agent.Spec.Backend.Harness.Model = "changed"
	agent.Spec.Backend.Harness.ReasoningEffort = "low"
	object.Object["spec"], err = runtime.DefaultUnstructuredConverter.ToUnstructured(&agent.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(agentsclient.AgentGVR).Update(ctx, object, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	resolved := resolveHarnessSettings(agent, queued.HarnessSettings)
	if resolved.Model != "original" || resolved.ReasoningEffort != "max" {
		t.Fatalf("queued snapshot changed: %+v", resolved)
	}
	job.RunID = id
	_, missingErr := mem.GetRun(ctx, scope, "missing")
	for _, readErr := range []error{errors.New("database unavailable"), missingErr} {
		server.store = failingSettingsStore{Store: mem, err: readErr}
		if err := bg.handle(ctx, job); !errors.Is(err, readErr) {
			t.Fatalf("handle error = %v, want %v", err, readErr)
		}
	}
	server.store = mem
	after, err := mem.GetRun(ctx, scope, id)
	if err != nil || after.Phase != store.RunPhasePending {
		t.Fatalf("run changed after read failure: %+v, %v", after, err)
	}
}

func TestModelBackendHasNoSubmissionSnapshot(t *testing.T) {
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.Backend.Type = "model"
	snapshot := resolveHarnessSettings(agent, nil)
	if snapshot != nil {
		t.Fatal("model-backed run has a harness snapshot")
	}
	agent.Spec.Backend.Type = "harness"
	agent.Spec.Backend.Harness = &agentsv1alpha1.AgentHarnessBackend{Model: "new", ReasoningEffort: "low"}
	resolved := resolveHarnessSettings(agent, snapshot)
	if resolved.Model != "new" || resolved.ReasoningEffort != "low" {
		t.Fatalf("first harness resolution = %+v", resolved)
	}
}
