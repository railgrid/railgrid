// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestControllerHealthReadinessIsServingOnly(t *testing.T) {
	health := newControllerHealth(true)
	if got := health.snapshot(); got.State != controllerStateStarting || !got.Required {
		t.Fatalf("initial controller health = %+v, want required/starting", got)
	}
	// Without a dependency gate, readiness no longer follows controller state:
	// under leader election a standby replica runs no manager yet must serve.
	if !health.ready() {
		t.Fatal("health without a dependency gate must be ready regardless of controller state")
	}
	health.markFailed(errors.New("endpoint slice unavailable"))
	if got := health.snapshot(); got.State != controllerStateFailed || got.Error != "endpoint slice unavailable" {
		t.Fatalf("failed controller health = %+v, want failure recorded", got)
	}
	if !health.ready() {
		t.Fatal("controller failure must not fail serving readiness")
	}

	// The dependency gate (factory configured + slice authority discovered)
	// is the only readiness input.
	serving := false
	health.setDependencyReady(func() bool { return serving })
	if health.ready() || health.heartbeatStatus() == "healthy" {
		t.Fatal("dependency-gated health reported ready before the dependency was")
	}
	serving = true
	if !health.ready() || health.heartbeatStatus() != "healthy" {
		t.Fatal("dependency-gated health not ready after the dependency became ready")
	}

	health.markStandby()
	if got := health.snapshot(); got.State != controllerStateStandby {
		t.Fatalf("standby controller health = %+v, want standby", got)
	}
	if !health.ready() {
		t.Fatal("standby replica must stay ready")
	}
}

func TestControllerHealthHeartbeatStatus(t *testing.T) {
	health := newControllerHealth(true)
	ready := false
	health.setDependencyReady(func() bool { return ready })
	if got := health.heartbeatStatus(); got != "starting" {
		t.Fatalf("heartbeat while starting = %q, want starting", got)
	}
	health.markFailed(errors.New("boom"))
	if got := health.heartbeatStatus(); got != "unhealthy" {
		t.Fatalf("heartbeat while failed and unready = %q, want unhealthy", got)
	}
	ready = true
	if got := health.heartbeatStatus(); got != "healthy" {
		t.Fatalf("heartbeat while serving-ready = %q, want healthy", got)
	}
}

func TestControllerOptionsForRetryableManagerAllowsStableNames(t *testing.T) {
	options := controllerOptionsForRetryableManager()
	if options.SkipNameValidation == nil || !*options.SkipNameValidation {
		t.Fatal("per-term managers must allow stable controller names across rebuilds")
	}
}

func TestControllerModeFromEnvRequiresExplicitRESTOnlyOptIn(t *testing.T) {
	t.Setenv("DATABRICKS_CONTROLLER_MODE", "")
	t.Setenv("DATABRICKS_REST_ONLY", "")
	t.Setenv("FAROS_PROVIDER_KUBECONFIG", "")
	t.Setenv("DATABRICKS_KUBECONFIG", "")
	t.Setenv("KUBECONFIG", "")
	if got := controllerModeFromEnv(); got != controllerModeRESTOnly {
		t.Fatalf("mode without kubeconfig = %q, want rest-only", got)
	}
	t.Setenv("KUBECONFIG", "/tmp/kubeconfig")
	if got := controllerModeFromEnv(); got != controllerModeRequired {
		t.Fatalf("mode with kubeconfig = %q, want required", got)
	}
	t.Setenv("DATABRICKS_REST_ONLY", "true")
	if got := controllerModeFromEnv(); got != controllerModeRESTOnly {
		t.Fatalf("explicit REST-only opt-out = %q, want rest-only", got)
	}
	t.Setenv("DATABRICKS_CONTROLLER_MODE", "definitely-not-a-mode")
	if got := controllerModeFromEnv(); got != controllerModeRequired {
		t.Fatalf("invalid mode must fail closed to required, got %q", got)
	}
}

func TestControllerDurationsStayWithinSafeBounds(t *testing.T) {
	t.Setenv("DATABRICKS_CONTROLLER_RETRY_INTERVAL", "250ms")
	if got := controllerRetryIntervalFromEnv(); got != controllerRetryIntervalMin {
		t.Fatalf("retry interval below minimum = %s, want %s", got, controllerRetryIntervalMin)
	}
	t.Setenv("DATABRICKS_CONTROLLER_RETRY_INTERVAL", "10m")
	if got := controllerRetryIntervalFromEnv(); got != controllerRetryIntervalMax {
		t.Fatalf("retry interval above maximum = %s, want %s", got, controllerRetryIntervalMax)
	}
	t.Setenv("DATABRICKS_CONTROLLER_RETRY_INTERVAL", "not-a-duration")
	if got := controllerRetryIntervalFromEnv(); got != controllerRetryInterval {
		t.Fatalf("invalid retry interval = %s, want default %s", got, controllerRetryInterval)
	}
	t.Setenv("DATABRICKS_CONTROLLER_STARTUP_TIMEOUT", "500ms")
	if got := controllerStartupTimeoutFromEnv(); got != controllerStartupTimeoutMin {
		t.Fatalf("startup timeout below minimum = %s, want %s", got, controllerStartupTimeoutMin)
	}
	t.Setenv("DATABRICKS_CONTROLLER_STARTUP_TIMEOUT", "10m")
	if got := controllerStartupTimeoutFromEnv(); got != controllerStartupTimeoutMax {
		t.Fatalf("startup timeout above maximum = %s, want %s", got, controllerStartupTimeoutMax)
	}
}

func endpointSliceObject(name string, urls ...string) *unstructured.Unstructured {
	endpoints := make([]any, 0, len(urls))
	for _, u := range urls {
		endpoints = append(endpoints, map[string]any{"url": u})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1",
		"kind":       "APIExportEndpointSlice",
		"metadata":   map[string]any{"name": name},
		"status":     map[string]any{"endpoints": endpoints},
	}}
}

func TestSliceEndpointDiscovered(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), endpointSliceObject(apiExportName, "https://shard-a.example/services/apiexport/x/y"))
	slice := dyn.Resource(apiExportEndpointSliceGVR)

	discovered, err := sliceEndpointDiscovered(context.Background(), slice, apiExportName)
	if err != nil || !discovered {
		t.Fatalf("discovered=%v err=%v, want true/nil", discovered, err)
	}

	// Missing slice is "not yet", not an error.
	discovered, err = sliceEndpointDiscovered(context.Background(), slice, "missing")
	if err != nil || discovered {
		t.Fatalf("missing slice discovered=%v err=%v, want false/nil", discovered, err)
	}

	// A slice whose endpoints all lack URLs is not discovered.
	empty := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), endpointSliceObject(apiExportName, "", "   "))
	discovered, err = sliceEndpointDiscovered(context.Background(), empty.Resource(apiExportEndpointSliceGVR), apiExportName)
	if err != nil || discovered {
		t.Fatalf("empty-url slice discovered=%v err=%v, want false/nil", discovered, err)
	}
}

func TestWaitForSliceEndpointTimesOut(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), endpointSliceObject(apiExportName))
	slice := dyn.Resource(apiExportEndpointSliceGVR)
	start := time.Now()
	err := waitForSliceEndpoint(context.Background(), slice, apiExportName, controllerStartupTimeoutMin)
	if err == nil {
		t.Fatal("expected a discovery timeout for a slice with no endpoints")
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("wait did not respect the bounded timeout: %s", elapsed)
	}
}

func TestWaitForSliceEndpointReturnsOnceDiscovered(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), endpointSliceObject(apiExportName, "https://shard-a.example/services/apiexport/x/y"))
	slice := dyn.Resource(apiExportEndpointSliceGVR)
	if err := waitForSliceEndpoint(context.Background(), slice, apiExportName, time.Minute); err != nil {
		t.Fatalf("waitForSliceEndpoint with a published URL: %v", err)
	}
}
