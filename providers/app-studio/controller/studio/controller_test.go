/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package studio

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/railgrid/provider-app-studio/controller/tenantwatch"
)

// Instances arrive on the per-workspace dependency watch, so what matters is
// that the Studio claims only the ones it stamped: a project's instance and a
// tenant's own must wake nothing here.
func TestMapInstanceEventUsesStudioLabel(t *testing.T) {
	owned := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{
		"name":   SearchInstanceName,
		"labels": map[string]any{templateLabel: searchTemplate, studioLabel: "default"},
	}}}
	got := mapInstanceEvent(context.Background(), nil, tenantwatch.Event{GVR: tenantwatch.InstancesGVR, Object: owned})
	if len(got) != 1 || got[0].Name != "default" {
		t.Fatalf("owned instance → %v, want [default]", got)
	}

	foreign := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "shop-dev"}}}
	if got := mapInstanceEvent(context.Background(), nil, tenantwatch.Event{GVR: tenantwatch.InstancesGVR, Object: foreign}); len(got) != 0 {
		t.Fatalf("project instance → %v, want none", got)
	}

	// An event with no object at all (a watch error replayed) names nothing
	// rather than panicking.
	if got := mapInstanceEvent(context.Background(), nil, tenantwatch.Event{GVR: tenantwatch.InstancesGVR}); len(got) != 0 {
		t.Fatalf("empty event → %v, want none", got)
	}
}
