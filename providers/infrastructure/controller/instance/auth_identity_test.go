// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package instance

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
)

func TestAuthInstanceUIDValueUsesTenantInstanceUID(t *testing.T) {
	inst := &unstructured.Unstructured{Object: map[string]any{}}
	inst.SetUID(types.UID("tenant-instance-uid"))
	if err := unstructured.SetNestedMap(inst.Object, map[string]any{
		infrav1alpha1.RailgridAuthEnabledField:     true,
		infrav1alpha1.RailgridAuthAppIDField:       "project-uid/development",
		infrav1alpha1.RailgridAuthInstanceUIDField: "stale-runtime-uid",
	}, "spec", "values"); err != nil {
		t.Fatal(err)
	}

	got, configured, err := authInstanceUIDValue(inst)
	if err != nil {
		t.Fatalf("authInstanceUIDValue: %v", err)
	}
	if !configured || got != "tenant-instance-uid" {
		t.Fatalf("authInstanceUIDValue = %q, configured=%t; want tenant Instance UID", got, configured)
	}

	if err := unstructured.SetNestedField(inst.Object, false, "spec", "values", infrav1alpha1.RailgridAuthEnabledField); err != nil {
		t.Fatal(err)
	}
	got, configured, err = authInstanceUIDValue(inst)
	if err != nil {
		t.Fatalf("disabled authInstanceUIDValue: %v", err)
	}
	if !configured || got != "tenant-instance-uid" {
		t.Fatalf("disabled authInstanceUIDValue = %q, configured=%t; want trusted tenant Instance UID retained while AppID remains configured", got, configured)
	}

	if err := unstructured.SetNestedField(inst.Object, "", "spec", "values", infrav1alpha1.RailgridAuthAppIDField); err != nil {
		t.Fatal(err)
	}
	got, configured, err = authInstanceUIDValue(inst)
	if err != nil {
		t.Fatalf("legacy disabled authInstanceUIDValue: %v", err)
	}
	if !configured || got != "" {
		t.Fatalf("legacy disabled authInstanceUIDValue = %q, configured=%t; want configured empty value", got, configured)
	}

	appIDOnly := &unstructured.Unstructured{Object: map[string]any{}}
	appIDOnly.SetUID(types.UID("tenant-instance-uid"))
	if err := unstructured.SetNestedField(appIDOnly.Object, "project-uid/development", "spec", "values", infrav1alpha1.RailgridAuthAppIDField); err != nil {
		t.Fatal(err)
	}
	if got, configured, err := authInstanceUIDValue(appIDOnly); err != nil || !configured || got != "tenant-instance-uid" {
		t.Fatalf("configured AppID without an explicit toggle = %q, configured=%t, err=%v; want authoritative tenant Instance UID", got, configured, err)
	}

	other := &unstructured.Unstructured{Object: map[string]any{}}
	if got, configured, err := authInstanceUIDValue(other); err != nil || configured || got != "" {
		t.Fatalf("non-auth template value = %q, configured=%t, err=%v; want untouched", got, configured, err)
	}

	missingUID := &unstructured.Unstructured{Object: map[string]any{}}
	if err := unstructured.SetNestedField(missingUID.Object, true, "spec", "values", infrav1alpha1.RailgridAuthEnabledField); err != nil {
		t.Fatal(err)
	}
	if _, _, err := authInstanceUIDValue(missingUID); err == nil {
		t.Fatal("Railgrid Auth without a persisted tenant Instance UID was accepted")
	}
	missingUID.Object["spec"].(map[string]any)["values"].(map[string]any)[infrav1alpha1.RailgridAuthEnabledField] = false
	missingUID.Object["spec"].(map[string]any)["values"].(map[string]any)[infrav1alpha1.RailgridAuthAppIDField] = "project-uid/development"
	if _, _, err := authInstanceUIDValue(missingUID); err == nil {
		t.Fatal("configured Railgrid Auth verifier without a persisted tenant Instance UID was accepted after gate disable")
	}
}

func TestStampAuthInstanceUIDPreservesConfiguredVerifierWhileDisabled(t *testing.T) {
	inst := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": instanceGVK.GroupVersion().String(),
		"kind":       instanceGVK.Kind,
		"metadata":   map[string]any{"name": "app", "uid": "tenant-instance-uid"},
		"spec": map[string]any{"values": map[string]any{
			infrav1alpha1.RailgridAuthEnabledField:     false,
			infrav1alpha1.RailgridAuthAppIDField:       "project-uid/development",
			infrav1alpha1.RailgridAuthInstanceUIDField: "stale-runtime-uid",
		}},
	}}
	inst.SetGroupVersionKind(instanceGVK)
	apiClient := clientfake.NewClientBuilder().WithObjects(inst).Build()

	changed, err := (&Controller{}).stampAuthInstanceUID(context.Background(), apiClient, inst)
	if err != nil {
		t.Fatalf("stampAuthInstanceUID with gate disabled: %v", err)
	}
	if !changed {
		t.Fatal("stampAuthInstanceUID did not replace the stale UID after the gate was disabled")
	}
	if got := nestedString(inst, "spec", "values", infrav1alpha1.RailgridAuthInstanceUIDField); got != "tenant-instance-uid" {
		t.Fatalf("stamped Instance UID = %q, want authoritative tenant Instance UID", got)
	}

	if err := unstructured.SetNestedField(inst.Object, "", "spec", "values", infrav1alpha1.RailgridAuthAppIDField); err != nil {
		t.Fatal(err)
	}
	if err := apiClient.Update(context.Background(), inst); err != nil {
		t.Fatalf("clear legacy AppID: %v", err)
	}
	changed, err = (&Controller{}).stampAuthInstanceUID(context.Background(), apiClient, inst)
	if err != nil {
		t.Fatalf("stamp legacy unconfigured Instance: %v", err)
	}
	if !changed {
		t.Fatal("stampAuthInstanceUID did not clear the UID for a never-configured Instance")
	}
	if got := nestedString(inst, "spec", "values", infrav1alpha1.RailgridAuthInstanceUIDField); got != "" {
		t.Fatalf("legacy unconfigured Instance UID = %q, want empty", got)
	}
}
