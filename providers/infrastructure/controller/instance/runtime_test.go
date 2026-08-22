/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package instance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/dataplane"
)

func developmentTemplate() *infrav1alpha1.Template {
	return &infrav1alpha1.Template{Spec: infrav1alpha1.TemplateSpec{
		Development: &infrav1alpha1.TemplateDevelopment{},
	}}
}

func runtimeForNetwork(generation, observedGeneration int64, phase string, readyStatus string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"generation": generation,
		},
		"spec": map[string]any{
			infrav1alpha1.FarosNetworkPhaseField: phase,
		},
		"status": map[string]any{
			"conditions": []any{map[string]any{
				"type":               "Ready",
				"status":             readyStatus,
				"observedGeneration": observedGeneration,
			}},
		},
	}}
}

func TestRuntimeNetworkPhaseMirrorsOnlyCurrentReadyRuntime(t *testing.T) {
	tests := []struct {
		name    string
		runtime *unstructured.Unstructured
		want    string
	}{
		{
			name:    "setup phase",
			runtime: runtimeForNetwork(1, 1, infrav1alpha1.FarosNetworkPhaseSetup, "True"),
			want:    infrav1alpha1.FarosNetworkPhaseSetup,
		},
		{
			name:    "current runtime generation ready",
			runtime: runtimeForNetwork(2, 2, infrav1alpha1.FarosNetworkPhaseRuntime, "True"),
			want:    infrav1alpha1.FarosNetworkPhaseRuntime,
		},
		{
			name:    "stale ready condition from setup generation",
			runtime: runtimeForNetwork(2, 1, infrav1alpha1.FarosNetworkPhaseRuntime, "True"),
			want:    infrav1alpha1.FarosNetworkPhaseSetup,
		},
		{
			name:    "current generation not ready",
			runtime: runtimeForNetwork(2, 2, infrav1alpha1.FarosNetworkPhaseRuntime, "False"),
			want:    infrav1alpha1.FarosNetworkPhaseSetup,
		},
		{
			name: "runtime absent",
			want: infrav1alpha1.FarosNetworkPhaseSetup,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := runtimeNetworkPhase(developmentTemplate(), test.runtime)
			if !ok || got != test.want {
				t.Fatalf("runtimeNetworkPhase() = %q/%v, want %q/true", got, ok, test.want)
			}
		})
	}
}

func TestDesiredNetworkPhaseDoesNotOscillateDuringRuntimeRollout(t *testing.T) {
	tests := []struct {
		name    string
		runtime *unstructured.Unstructured
		want    string
	}{
		{
			name: "runtime absent stays setup",
			want: infrav1alpha1.FarosNetworkPhaseSetup,
		},
		{
			name:    "setup generation not ready stays setup",
			runtime: runtimeForNetwork(2, 1, infrav1alpha1.FarosNetworkPhaseSetup, "True"),
			want:    infrav1alpha1.FarosNetworkPhaseSetup,
		},
		{
			name:    "setup generation ready transitions to runtime",
			runtime: runtimeForNetwork(2, 2, infrav1alpha1.FarosNetworkPhaseSetup, "True"),
			want:    infrav1alpha1.FarosNetworkPhaseRuntime,
		},
		{
			name:    "selected runtime stays runtime while rollout is unready",
			runtime: runtimeForNetwork(3, 2, infrav1alpha1.FarosNetworkPhaseRuntime, "False"),
			want:    infrav1alpha1.FarosNetworkPhaseRuntime,
		},
		{
			name:    "selected runtime stays runtime while readiness is stale",
			runtime: runtimeForNetwork(3, 2, infrav1alpha1.FarosNetworkPhaseRuntime, "True"),
			want:    infrav1alpha1.FarosNetworkPhaseRuntime,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := desiredNetworkPhase(test.runtime); got != test.want {
				t.Fatalf("desiredNetworkPhase() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestInstanceRequeueAfterWaitsForCurrentRuntimeNetwork(t *testing.T) {
	tmpl := developmentTemplate()
	created := metav1.Time{}
	now := time.Time{}

	if got := instanceRequeueAfter(now, created, tmpl,
		runtimeForNetwork(3, 2, infrav1alpha1.FarosNetworkPhaseRuntime, "True"), true); got != requeueNotReady {
		t.Fatalf("stale runtime requeue = %s, want convergence interval %s", got, requeueNotReady)
	}
	if got := instanceRequeueAfter(now, created, tmpl,
		runtimeForNetwork(3, 3, infrav1alpha1.FarosNetworkPhaseRuntime, "True"), true); got != requeueReady {
		t.Fatalf("current runtime requeue = %s, want ready interval %s", got, requeueReady)
	}
}

func TestRuntimeReadyForNetworkAcceptsStatusObservedGeneration(t *testing.T) {
	runtime := runtimeForNetwork(3, 0, infrav1alpha1.FarosNetworkPhaseRuntime, "True")
	status := runtime.Object["status"].(map[string]any)
	status["phase"] = "Ready"
	status["observedGeneration"] = int64(3)
	delete(status["conditions"].([]any)[0].(map[string]any), "observedGeneration")
	if !runtimeReadyForNetwork(runtime) {
		t.Fatal("status.observedGeneration matching metadata.generation should authorize runtime readiness")
	}
}

func TestStampConditionObservedGenerationUsesTenantGeneration(t *testing.T) {
	conditions := []any{map[string]any{
		"type":               "Ready",
		"status":             "True",
		"observedGeneration": int64(11),
	}}
	stampConditionObservedGeneration(conditions, "Ready", 12)
	condition := conditions[0].(map[string]any)
	if got := condition["observedGeneration"]; got != int64(12) {
		t.Fatalf("Ready observedGeneration = %#v, want 12", got)
	}
}

func TestMirroredRuntimeStatusAllowsHandlerWithoutTenantPhaseSpec(t *testing.T) {
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.faros.sh/v1alpha1",
		"kind":       "Instance",
		"metadata": map[string]any{
			"name":       "app",
			"namespace":  "default",
			"generation": int64(2),
		},
		"spec": map[string]any{
			"template": infrav1alpha1.UniversalCodingSandboxTemplateName,
			"values":   map[string]any{},
		},
	}}
	instance.SetGroupVersionKind(instanceGVK)
	tmpl := developmentTemplate()
	tmpl.Name = infrav1alpha1.UniversalCodingSandboxTemplateName
	tmpl.Spec.InstanceCRD = infrav1alpha1.TemplateInstanceCRD{
		Group: infrav1alpha1.GroupName, Version: infrav1alpha1.Version,
		Resource: infrav1alpha1.InstancesResource, Kind: "Instance",
	}
	runtimeObj := runtimeForNetwork(2, 2, infrav1alpha1.FarosNetworkPhaseRuntime, "True")
	runtimeObj.SetName("app")
	runtimeObj.SetNamespace("ws-default")
	tenantClient := mirroredStatusClient{}
	controller := &Controller{}
	if _, err := controller.mirrorStatus(context.Background(), tenantClient, instance, tmpl, runtimeObj,
		validCondition(metav1.ConditionTrue, infrav1alpha1.ReasonReady, ""), nil); err != nil {
		t.Fatalf("mirrorStatus: %v", err)
	}
	status, found, err := unstructured.NestedMap(instance.Object, "status")
	if err != nil || !found {
		t.Fatalf("mirrored status = %#v/%v (err %v), want status", status, found, err)
	}
	if got := status[infrav1alpha1.FarosNetworkPhaseStatusField]; got != infrav1alpha1.FarosNetworkPhaseRuntime {
		t.Fatalf("mirrored network phase = %#v, want runtime", got)
	}
	if values, _, _ := unstructured.NestedMap(instance.Object, "spec", "values"); values[infrav1alpha1.FarosNetworkPhaseField] != nil {
		t.Fatalf("tenant spec carried network phase %#v; controller status must be authoritative", values[infrav1alpha1.FarosNetworkPhaseField])
	}

	contract := &infrav1alpha1.TemplateDataPlane{
		RuntimeNamespacePath: "status.runtimeNamespace",
		Components: map[string]infrav1alpha1.TemplateDataPlaneComponent{
			"backend": {Exec: &infrav1alpha1.TemplateDataPlaneExec{MaxTimeoutSeconds: 30, MaxOutputBytes: 8}},
		},
	}
	// ResolveComponentExecTarget needs the control Service reference even
	// though the fake executor handles the request locally.
	status["runtimeNamespace"] = "ws-default"
	status["components"] = map[string]any{"backend": map[string]any{
		"controlServiceRef": map[string]any{"name": "backend", "namespace": "ws-default"},
	}}
	if err := unstructured.SetNestedMap(instance.Object, status, "status"); err != nil {
		t.Fatalf("set mirrored runtime status: %v", err)
	}
	contract.Components["backend"] = infrav1alpha1.TemplateDataPlaneComponent{
		Exec: &infrav1alpha1.TemplateDataPlaneExec{MaxTimeoutSeconds: 30, MaxOutputBytes: 8},
		Endpoints: map[string]infrav1alpha1.TemplateDataPlaneEndpoint{
			"sync": {ServicePath: "status.components.backend.controlServiceRef", Port: "control", UpstreamPath: "/sync", Methods: []string{http.MethodPost}},
		},
	}
	handler := dataplane.NewHandler(
		mirroredInstanceGetter{instance: instance},
		mirroredContractGetter{contract: contract},
		mirroredRuntime{},
		dataplane.WithExec(mirroredExecutor{}, mirroredAuthorizer{}),
		dataplane.WithDevelopmentGetter(mirroredDevelopmentGetter{}),
	)
	body, err := json.Marshal(map[string]any{
		"action": "start", "requestID": "run-1", "sourceRevision": int64(1),
		"sourceDigest": "sha256:test", "argv": []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, dataplane.PathPrefix+"clusters/ws/instances/app/components/backend/exec", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer caller")
	req.Header.Set("Idempotency-Key", "run-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("handler status = %d, body %q; mirrored status %#v", recorder.Code, recorder.Body.String(), status)
	}
}

type mirroredInstanceGetter struct{ instance *unstructured.Unstructured }

type mirroredStatusClient struct{ ctrlclient.Client }

func (mirroredStatusClient) Status() ctrlclient.SubResourceWriter { return mirroredStatusWriter{} }

type mirroredStatusWriter struct{ ctrlclient.SubResourceWriter }

func (mirroredStatusWriter) Update(context.Context, ctrlclient.Object, ...ctrlclient.SubResourceUpdateOption) error {
	return nil
}

func (g mirroredInstanceGetter) Get(context.Context, string, string, string, string) (*unstructured.Unstructured, error) {
	return g.instance, nil
}

type mirroredContractGetter struct {
	contract *infrav1alpha1.TemplateDataPlane
}

func (g mirroredContractGetter) For(context.Context, string) (*infrav1alpha1.TemplateDataPlane, error) {
	return g.contract, nil
}

type mirroredDevelopmentGetter struct{}

func (mirroredDevelopmentGetter) DevelopmentFor(context.Context, string, string) (*infrav1alpha1.TemplateDevelopmentComponent, error) {
	return &infrav1alpha1.TemplateDevelopmentComponent{}, nil
}

type mirroredRuntime struct{}

func (mirroredRuntime) Host() string { return "https://runtime.example" }

func (mirroredRuntime) Transport() (http.RoundTripper, error) { return http.DefaultTransport, nil }

func (mirroredRuntime) ControlToken(context.Context, string, string) (string, error) {
	return "token", nil
}

type mirroredExecutor struct{}

func (mirroredExecutor) Start(context.Context, dataplane.ExecCall) (dataplane.ExecResult, error) {
	return dataplane.ExecResult{SessionID: "session-1", State: "running"}, nil
}

func (mirroredExecutor) Poll(context.Context, dataplane.ExecCall) (dataplane.ExecResult, error) {
	return dataplane.ExecResult{SessionID: "session-1", State: "running"}, nil
}

func (mirroredExecutor) Cancel(context.Context, dataplane.ExecCall) (dataplane.ExecResult, error) {
	return dataplane.ExecResult{SessionID: "session-1", State: "canceled"}, nil
}

type mirroredAuthorizer struct{}

func (mirroredAuthorizer) AuthorizeExec(context.Context, dataplane.ExecAuthorization) error {
	return nil
}
