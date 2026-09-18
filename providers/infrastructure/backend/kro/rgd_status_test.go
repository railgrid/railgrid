/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func rgdWithStatus(generation int64, state string, conditions ...map[string]any) *unstructured.Unstructured {
	conds := make([]any, 0, len(conditions))
	for _, c := range conditions {
		conds = append(conds, c)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": rgdAPIVersion,
		"kind":       rgdKind,
		"metadata":   map[string]any{"name": "t", "generation": generation},
	}}
	status := map[string]any{}
	if state != "" {
		status["state"] = state
	}
	if len(conds) > 0 {
		status["conditions"] = conds
	}
	if len(status) > 0 {
		obj.Object["status"] = status
	}
	return obj
}

func TestRGDRejected(t *testing.T) {
	if msg, rejected := rgdRejected(nil); rejected || msg != "" {
		t.Fatalf("nil RGD = (%q, %v), want not rejected", msg, rejected)
	}
	// kro has not observed the object yet: not a verdict.
	if _, rejected := rgdRejected(rgdWithStatus(1, "")); rejected {
		t.Fatal("RGD without status reported rejected")
	}
	if _, rejected := rgdRejected(rgdWithStatus(1, "Active",
		map[string]any{"type": "Ready", "status": "True"})); rejected {
		t.Fatal("Active RGD reported rejected")
	}

	msg, rejected := rgdRejected(rgdWithStatus(2, "Inactive",
		map[string]any{"type": "Ready", "status": "False", "observedGeneration": int64(2)},
		map[string]any{"type": "GraphAccepted", "status": "False", "reason": "InvalidResourceGraph",
			"message": "resource deployment: unknown field spec.foo", "observedGeneration": int64(2)},
		map[string]any{"type": "KindReady", "status": "True", "observedGeneration": int64(2)},
	))
	if !rejected {
		t.Fatal("Inactive RGD not reported rejected")
	}
	if !strings.Contains(msg, "Inactive") || !strings.Contains(msg, "GraphAccepted: resource deployment: unknown field spec.foo") {
		t.Fatalf("message = %q, want the failing condition spelled out", msg)
	}
	if strings.Contains(msg, "KindReady") || strings.Contains(msg, "Ready: ") {
		t.Fatalf("message = %q, want only the False conditions with a message", msg)
	}

	// Conditions from an older generation don't explain the current one, but
	// the Inactive state itself still stands.
	msg, rejected = rgdRejected(rgdWithStatus(3, "Inactive",
		map[string]any{"type": "GraphAccepted", "status": "False", "message": "old", "observedGeneration": int64(2)},
	))
	if !rejected || strings.Contains(msg, "old") {
		t.Fatalf("stale-condition verdict = (%q, %v), want rejected without the stale message", msg, rejected)
	}
}
