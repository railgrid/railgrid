// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package agent

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/internal/edgeref"
	agentsscheme "github.com/railgrid/provider-agents/scheme"
)

func TestHarnessReadinessRequiresConnectedEdgeAndReachableService(t *testing.T) {
	for _, tc := range []struct {
		name         string
		connected    bool
		serviceReady string
		wantReady    bool
	}{
		{"connected", true, "True", true},
		{"disconnected with cached harness", false, "True", false},
		{"unreachable service with cached harness", true, "False", false},
		{"unknown service readiness", true, "Unknown", false},
		{"unprobed service", true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edge := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"connected": tc.connected}}}
			gvk, _ := edgeref.EdgeGVK(edgeref.KindLinuxServer)
			edge.SetGroupVersionKind(gvk)
			edge.SetName("devbox")
			service := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{
				"harness":    map[string]any{"name": "codex", "version": "1", "ready": true},
				"conditions": []any{map[string]any{"type": "Ready", "status": tc.serviceReady}},
			}}}
			service.SetGroupVersionKind(edgeref.ServiceGVK())
			service.SetName("devbox-codex")
			cred := &agentsv1alpha1.ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "codex"}}
			cred.Spec.Provider = agentsv1alpha1.ModelProviderCodex
			cred.Status.Conditions = []metav1.Condition{{Type: agentsv1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready"}}
			agent := &agentsv1alpha1.Agent{}
			agent.Spec.Backend = agentsv1alpha1.AgentBackendSpec{Type: agentsv1alpha1.AgentBackendHarness, Harness: &agentsv1alpha1.AgentHarnessBackend{
				CredentialRef: "codex", EdgeRef: agentsv1alpha1.AgentHarnessEdgeRef{Kind: edgeref.KindLinuxServer, Name: "devbox"},
			}}
			c := fake.NewClientBuilder().WithScheme(agentsscheme.NewScheme()).WithObjects(edge, service, cred).Build()
			reason, message, status, err := (&Reconciler{}).validateBackend(t.Context(), c, agent, credentialVerdict{})
			if err != nil || (reason == "") != tc.wantReady {
				t.Fatalf("readiness reason=%q message=%q error=%v", reason, message, err)
			}
			if !tc.wantReady && reason != agentsv1alpha1.ReasonHarnessNotReady {
				t.Fatalf("unexpected reason: %s", reason)
			}
			if status.Harness == nil || status.Harness.Name != "codex" {
				t.Fatalf("lost discovered harness details: %+v", status)
			}
		})
	}
}
