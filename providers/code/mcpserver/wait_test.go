/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
)

// newTenantFake is a fake tenant client seeded with objects; its tracker
// serves the Get and Watch the wait helper issues.
func newTenantFake(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
}

func checkoutObject(name, phase string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": codev1alpha1.SchemeGroupVersion.String(),
		"kind":       "RepositoryCheckout",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"repositoryRef": "demo-app"},
	}}
	if phase != "" {
		obj.Object["status"] = map[string]any{"phase": phase}
	}
	return obj
}

var checkoutTerminal = phaseIn(string(codev1alpha1.RepositoryCheckoutPhaseSucceeded), string(codev1alpha1.RepositoryCheckoutPhaseFailed))

// An object that is already terminal returns from the initial list, without
// waiting for a watch event.
func TestWaitForPhaseReturnsTerminalObjectImmediately(t *testing.T) {
	dyn := newTenantFake(checkoutObject("done", "Succeeded"), checkoutObject("other", "Failed"))
	obj, done, err := waitForPhase(context.Background(), dyn, repositoryCheckoutsGVR, "RepositoryCheckout", "done", 5*time.Second, checkoutTerminal)
	if err != nil || !done || obj == nil || obj.GetName() != "done" {
		t.Fatalf("waitForPhase = %v, %v, %v; want the terminal object", obj, done, err)
	}
}

// A pending object completes through a watch event, not a poll.
func TestWaitForPhaseObservesLaterTransition(t *testing.T) {
	dyn := newTenantFake(checkoutObject("pending", "Running"))
	go func() {
		time.Sleep(50 * time.Millisecond)
		// A sibling's transition must not satisfy the wait.
		_ = dyn.Tracker().Add(checkoutObject("other", "Succeeded"))
		time.Sleep(50 * time.Millisecond)
		_ = dyn.Tracker().Update(repositoryCheckoutsGVR, checkoutObject("pending", "Failed"), "")
	}()
	start := time.Now()
	obj, done, err := waitForPhase(context.Background(), dyn, repositoryCheckoutsGVR, "RepositoryCheckout", "pending", 5*time.Second, checkoutTerminal)
	if err != nil || !done || obj == nil {
		t.Fatalf("waitForPhase = %v, %v, %v; want done", obj, done, err)
	}
	if phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase"); phase != "Failed" {
		t.Fatalf("phase = %q, want Failed", phase)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("wait took %s; a watch should notice the transition well under the 1s poll interval it replaced", elapsed)
	}
	gets, watches := 0, 0
	for _, action := range dyn.Actions() {
		switch action.GetVerb() {
		case "get":
			gets++
		case "watch":
			watches++
		}
	}
	if gets != 1 || watches != 1 {
		t.Fatalf("gets = %d, watches = %d; want one read followed by one watch, not a poll", gets, watches)
	}
}

// Timing out is not a failure: the last state seen comes back with done=false.
func TestWaitForPhaseTimeoutReturnsLastSeen(t *testing.T) {
	dyn := newTenantFake(checkoutObject("pending", "Running"))
	obj, done, err := waitForPhase(context.Background(), dyn, repositoryCheckoutsGVR, "RepositoryCheckout", "pending", 100*time.Millisecond, checkoutTerminal)
	if err != nil || done {
		t.Fatalf("waitForPhase = %v, %v, %v; want a clean timeout", obj, done, err)
	}
	if obj == nil || obj.GetName() != "pending" {
		t.Fatalf("last seen = %v, want the pending object", obj)
	}

	// The caller's context ending is the same clean timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	obj, done, err = waitForPhase(ctx, dyn, repositoryCheckoutsGVR, "RepositoryCheckout", "pending", time.Minute, checkoutTerminal)
	if err != nil || done {
		t.Fatalf("waitForPhase = %v, %v, %v; want a clean timeout", obj, done, err)
	}

	// An object that does not exist is a failure, not a timeout.
	obj, done, err = waitForPhase(context.Background(), dyn, repositoryCheckoutsGVR, "RepositoryCheckout", "missing", 50*time.Millisecond, checkoutTerminal)
	if done || obj != nil || err == nil || !strings.Contains(err.Error(), `get RepositoryCheckout "missing"`) {
		t.Fatalf("waitForPhase = %v, %v, %v; want a lookup error", obj, done, err)
	}
}

// An object that vanishes mid-wait is an error, as a NotFound poll was.
func TestWaitForPhaseDeletedIsAnError(t *testing.T) {
	dyn := newTenantFake(checkoutObject("pending", "Running"))
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = dyn.Tracker().Delete(repositoryCheckoutsGVR, "", "pending", metav1.DeleteOptions{})
	}()
	_, done, err := waitForPhase(context.Background(), dyn, repositoryCheckoutsGVR, "RepositoryCheckout", "pending", 5*time.Second, checkoutTerminal)
	if done || err == nil || !strings.Contains(err.Error(), `RepositoryCheckout "pending" was deleted`) {
		t.Fatalf("waitForPhase = %v, %v; want a deletion error", done, err)
	}
}
