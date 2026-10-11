// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane/conformance"

	agentsclient "github.com/railgrid/provider-agents/client"
)

func disabledTriggerObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.TriggerGVR.GroupVersion().String(),
		"kind":       "Trigger",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"agentRef": conformanceAgent, "source": "webhook", "task": "review it"},
		"status": map[string]any{
			"disabledReason":      "disabled after 6 consecutive failures: the request exceeds the 2MiB protocol limit",
			"consecutiveFailures": int64(6),
			"webhookPath":         "/services/providers/agents/webhooks/triggers/x/" + name + "/t",
		},
	}}
}

func disabledScheduleObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.ScheduleGVR.GroupVersion().String(),
		"kind":       "Schedule",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"agentRef": conformanceAgent, "type": "cron", "schedule": "0 9 * * *", "task": "report"},
		"status": map[string]any{
			"disabledReason":      "disabled after 6 consecutive failures: agent gone",
			"consecutiveFailures": int64(6),
		},
	}}
}

// The provider disables a trigger or schedule after repeated failures and
// nothing used to clear that: the portal showed the reason and the webhook
// answered 409 until the object was recreated. The enable verb clears the
// disabling and the failure count, leaves everything else — the minted webhook
// path, spec.suspend — alone, and is idempotent.
func TestEnableVerbClearsTheDisabling(t *testing.T) {
	for _, tc := range []struct {
		resource string
		gvr      schema.GroupVersionResource
		object   *unstructured.Unstructured
	}{
		{"triggers", agentsclient.TriggerGVR, disabledTriggerObject("on-pr")},
		{"schedules", agentsclient.ScheduleGVR, disabledScheduleObject("daily")},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			callers := &conformance.FakeCallers{
				Cluster: conformanceCluster,
				User:    conformanceUser,
				Objects: []*unstructured.Unstructured{agentObject(conformanceAgent), tc.object},
				ListKinds: map[schema.GroupVersionResource]string{
					agentsclient.AgentGVR:    "AgentList",
					agentsclient.TriggerGVR:  "TriggerList",
					agentsclient.ScheduleGVR: "ScheduleList",
				},
				Allow: func(a conformance.Attributes) bool { return a.Verb == "get" && a.Resource == tc.resource },
			}
			handler := gatedHandler(t, newGatedTestServer(t, callers))
			name := tc.object.GetName()
			path := verbBase(conformanceCluster, tc.resource, name) + "/enable"

			for round, why := range []string{"clears the disabling", "is idempotent once there is nothing to clear"} {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, stamped(http.MethodPost, path, nil, conformanceUser))
				if recorder.Code != http.StatusOK {
					t.Fatalf("round %d (%s): POST %s → %d: %s", round, why, path, recorder.Code, recorder.Body.String())
				}
				var answered struct {
					Status map[string]any `json:"status"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &answered); err != nil {
					t.Fatalf("round %d: response is not the object: %v: %s", round, err, recorder.Body.String())
				}
				if reason, ok := answered.Status["disabledReason"]; ok && reason != "" {
					t.Errorf("round %d: response still carries disabledReason %q", round, reason)
				}
				if failures, ok := answered.Status["consecutiveFailures"]; ok && failures != float64(0) {
					t.Errorf("round %d: response still carries consecutiveFailures %v", round, failures)
				}
			}

			dyn, err := callers.AsProvider(conformanceCluster)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := dyn.Resource(tc.gvr).Get(t.Context(), name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if reason, _, _ := unstructured.NestedString(stored.Object, "status", "disabledReason"); reason != "" {
				t.Errorf("stored object still disabled: %q", reason)
			}
			if failures, _, _ := unstructured.NestedInt64(stored.Object, "status", "consecutiveFailures"); failures != 0 {
				t.Errorf("stored consecutiveFailures = %d, want 0", failures)
			}
			if tc.resource == "triggers" {
				if path, _, _ := unstructured.NestedString(stored.Object, "status", "webhookPath"); path == "" {
					t.Error("the minted webhook path was lost: enable must touch only the disabling")
				}
			}
		})
	}
}

// A caller who may not see the object is told nothing about it, and a
// resource with no such object answers the same way.
func TestEnableVerbIsGatedOnTheObject(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster:   conformanceCluster,
		User:      conformanceUser,
		Objects:   []*unstructured.Unstructured{agentObject(conformanceAgent), disabledTriggerObject("on-pr")},
		ListKinds: map[schema.GroupVersionResource]string{agentsclient.AgentGVR: "AgentList", agentsclient.TriggerGVR: "TriggerList"},
		Allow:     func(conformance.Attributes) bool { return false },
	}
	handler := gatedHandler(t, newGatedTestServer(t, callers))
	for _, name := range []string{"on-pr", "no-such-trigger"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, stamped(http.MethodPost, verbBase(conformanceCluster, "triggers", name)+"/enable", nil, conformanceUser))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("POST enable on %s by a caller who may not see it → %d, want 404", name, recorder.Code)
		}
	}
}
