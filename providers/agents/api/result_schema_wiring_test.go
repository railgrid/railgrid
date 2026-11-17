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
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"

	"github.com/railgrid/provider-agents/executor"
)

// These pin the two places a run is rebuilt from its stored row rather than
// from the request that admitted it: the background executor picking up a
// queued run (every start-run goes this way), and a resume. Each must carry
// the run's result schema across, or a run admitted with one silently stops
// being held to it — and get-run reports no structured result at all.

const wiringSchema = `{"type":"object","required":["company"],"properties":{"company":{"type":"string"}}}`

// wiringAgent is a model-backed agent whose every purpose resolves to the one
// credential the fake model serves.
func wiringAgent() *agentsv1alpha1.Agent {
	a := &agentsv1alpha1.Agent{
		TypeMeta:   metav1.TypeMeta{APIVersion: agentsclient.AgentGVR.GroupVersion().String(), Kind: "Agent"},
		ObjectMeta: metav1.ObjectMeta{Name: "scout", UID: "scout-uid"},
	}
	a.Spec.SystemPrompt = "You research companies."
	a.Spec.Backend.Model = &agentsv1alpha1.AgentModelBackend{Credentials: map[string]string{"chat": "fake", "background": "fake"}}
	a.Spec.Autonomy = agentsv1alpha1.AutonomyAuto
	return a
}

func toUnstructured(t *testing.T, obj runtime.Object, gvk schema.GroupVersionKind) *unstructured.Unstructured {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(gvk)
	return u
}

func TestBackgroundRunKeepsItsStoredResultSchema(t *testing.T) {
	ctx := t.Context()
	model := newFakeLLM(t, "Acme looks promising.")
	creds := credsFor(model.srv.URL, map[string]string{"fake": "gpt-4o"})
	agent := wiringAgent()
	objects := []runtime.Object{
		toUnstructured(t, agent, agentsclient.AgentGVR.GroupVersion().WithKind("Agent")),
		toUnstructured(t, creds.creds["fake"], agentsclient.ModelCredentialGVR.GroupVersion().WithKind("ModelCredential")),
	}
	secret := creds.secrets[CredentialSecretName("fake")].DeepCopy()
	secret.Namespace = llm.SecretNamespace
	objects = append(objects, toUnstructured(t, secret, corev1.SchemeGroupVersion.WithKind("Secret")))
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		agentsclient.AgentGVR:           "AgentList",
		agentsclient.ModelCredentialGVR: "ModelCredentialList",
		agentsclient.SecretGVR:          "SecretList",
	}, objects...)

	s := &Server{store: store.NewMemoryStore(), engine: engine.New(), events: newEventBus(), liveRuns: newRunRegistry()}
	bg := &background{server: s, scopedFn: func(context.Context, string) (dynamic.Interface, error) { return dyn, nil }}
	s.bg = bg
	scope := bg.scopeFor(ctx, "tenant", agent.Name)
	now := time.Now().UTC()
	// The row start-run's admission writes, and nothing else: the executor
	// that picks it up has only this to go on.
	if err := s.store.SaveRun(ctx, scope, store.Run{
		ID: "queued", AgentName: agent.Name, Trigger: agentsv1alpha1.RunTriggerAPI, Phase: store.RunPhasePending,
		AgentUID: string(agent.UID), ResultSchema: json.RawMessage(wiringSchema),
		Input: "Research acme.example", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	_ = bg.handle(ctx, executor.Job{
		ClusterID: "tenant", AgentRef: agent.Name, RunID: "queued", Task: "Research acme.example",
		ID: "queued", Trigger: agentsv1alpha1.RunTriggerAPI,
	})
	if model.calls.Load() == 0 {
		t.Fatal("the queued run never reached the model; the test exercised nothing")
	}
	got, err := s.store.GetRun(ctx, scope, "queued")
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != store.RunPhaseFailed || got.StructuredResult == nil || got.StructuredResult.State != "invalid" {
		t.Fatalf("a prose answer to a schema'd run = phase %s, structured %+v; want Failed/invalid — the executor dropped the schema", got.Phase, got.StructuredResult)
	}
}

func TestResumedRunKeepsItsStoredResultSchema(t *testing.T) {
	ctx := context.Background()
	model := newFakeLLM(t, "Acme looks promising.")
	creds := credsFor(model.srv.URL, map[string]string{"fake": "gpt-4o"})
	agent := wiringAgent()
	f := newRecoverFixture(t)
	f.s.engine = engine.New()

	at := time.Now().UTC().Add(-time.Minute)
	checkpoint, err := json.Marshal(runCheckpoint{Engine: engine.Checkpoint{
		Messages: []engine.CheckpointMessage{{Role: "user", Content: "Research acme.example"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.store.SaveRun(ctx, f.scope, store.Run{
		ID: "interrupted", AgentName: agent.Name, Trigger: agentsv1alpha1.RunTriggerAPI, Phase: store.RunPhaseRunning,
		AgentUID: string(agent.UID), ResultSchema: json.RawMessage(wiringSchema), RecoveryIdentity: store.RecoveryIdentityAgent,
		Input: "Research acme.example", Checkpoint: checkpoint, CreatedAt: at, UpdatedAt: at, StartedAt: &at,
	}); err != nil {
		t.Fatal(err)
	}
	// Claimed the way the recovery hook claims it: by this replica, so the
	// resumed turn's writes are fenced to the owner that holds the run.
	f.s.cfg.ProcessID = "agents-replica-1"
	claimed, err := f.s.store.ClaimRecovery(ctx, f.scope, "interrupted", at, newExecutionOwner(f.s.cfg.ProcessID), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	res, rerr := f.s.resumeRun(ctx, f.scope, "interrupted", runAccess{Creds: creds, CR: fixedAgentCR{agent: agent}},
		resumeIntent{FromPhase: store.RunPhaseRunning, RecoveryClaimedAt: claimed.UpdatedAt})
	if rerr == nil || res.Phase != store.RunPhaseFailed {
		t.Fatalf("resume = %+v, %v; want the turn to fail its schema", res, rerr)
	}
	if model.calls.Load() == 0 {
		t.Fatal("the resumed run never reached the model; the test exercised nothing")
	}
	got := f.phaseOf(t, "interrupted")
	if got.Phase != store.RunPhaseFailed || got.StructuredResult == nil || got.StructuredResult.State != "invalid" {
		t.Fatalf("a prose answer to a resumed schema'd run = phase %s, structured %+v; want Failed/invalid — the resume dropped the schema", got.Phase, got.StructuredResult)
	}
}
