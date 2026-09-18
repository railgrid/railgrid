/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package template

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func rgdObj(name string, labels map[string]string) *unstructured.Unstructured {
	rgd := &unstructured.Unstructured{}
	rgd.SetGroupVersionKind(rgdGVK)
	rgd.SetName(name)
	rgd.SetLabels(labels)
	return rgd
}

func TestMapRGDToTemplate(t *testing.T) {
	ours := map[string]string{
		rgdManagedByLabel:      rgdManagedByValue,
		"railgrid.ai/template": "simple-webapp",
	}

	// The template label wins over the RGD name.
	got := mapRGDToTemplate(context.Background(), rgdObj("simple-webapp-v2", ours))
	if len(got) != 1 || got[0].Name != "simple-webapp" || got[0].Namespace != "" {
		t.Fatalf("mapped %v, want the Template named by the label", got)
	}

	// Without the label the RGD name is the Template name (the backend names
	// them 1:1).
	got = mapRGDToTemplate(context.Background(), rgdObj("database", map[string]string{rgdManagedByLabel: rgdManagedByValue}))
	if len(got) != 1 || got[0].Name != "database" {
		t.Fatalf("mapped %v, want the Template named after the RGD", got)
	}

	// Hand-applied RGDs on the runtime cluster are not ours to reconcile.
	if got := mapRGDToTemplate(context.Background(), rgdObj("foreign", map[string]string{"railgrid.ai/template": "foreign"})); len(got) != 0 {
		t.Fatalf("foreign RGD mapped to %v", got)
	}
	if got := mapRGDToTemplate(context.Background(), rgdObj("foreign", nil)); len(got) != 0 {
		t.Fatalf("unlabelled RGD mapped to %v", got)
	}
}

func TestRGDSourceFiltersToRailgridRGDs(t *testing.T) {
	pred := railgridRGD
	if !pred(rgdObj("ours", map[string]string{rgdManagedByLabel: rgdManagedByValue})) {
		t.Fatal("predicate dropped a provider-authored RGD")
	}
	if pred(rgdObj("theirs", map[string]string{rgdManagedByLabel: "someone-else"})) {
		t.Fatal("predicate passed a foreign RGD")
	}
	// Sanity: the source builds without a cache (nothing starts until the
	// controller runs) and is non-syncing, so the Template controller's
	// start never waits on kro's CRDs.
	src := rgdSource(nil)
	if src == nil {
		t.Fatal("rgdSource returned nil")
	}
	if _, syncing := src.(interface{ WaitForSync(context.Context) error }); syncing {
		t.Fatal("RGD source must not be a syncing source")
	}
}
