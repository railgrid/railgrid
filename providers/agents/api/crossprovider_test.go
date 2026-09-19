// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane/conformance"

	"github.com/railgrid/provider-agents/tools"
)

// The provider name is a CANDIDATE derived from the API group and then
// confirmed against the tenant's own APIBinding. Both halves matter: without
// the candidate there is nothing to get by name (and a list is a grant an
// agent's scoped identity cannot hold); without the confirmation this would be
// the hardcoded name with extra steps.
func TestProviderNameForAPIGroup(t *testing.T) {
	for group, want := range map[string]string{
		"infrastructure.railgrid.ai": "infrastructure",
		"agents.railgrid.ai":         "agents",
		"":                           "",
	} {
		if got := ProviderNameForAPIGroup(group); got != want {
			t.Errorf("ProviderNameForAPIGroup(%q) = %q, want %q", group, got, want)
		}
	}
}

// bindingServesGroup is the consistency check. status.boundResources is
// authoritative — it is what kcp actually bound — and the export name is the
// fallback for a binding that has not reported yet.
func TestBindingServesGroup(t *testing.T) {
	const group = "infrastructure.railgrid.ai"
	for _, tc := range []struct {
		name   string
		object map[string]any
		want   bool
	}{
		{
			name: "bound resources name the group",
			object: map[string]any{
				"status": map[string]any{"boundResources": []any{
					map[string]any{"group": group, "resource": "instances"},
				}},
			},
			want: true,
		},
		{
			name: "not bound yet, but the export is named after the group",
			object: map[string]any{
				"spec": map[string]any{"reference": map[string]any{"export": map[string]any{"name": group}}},
			},
			want: true,
		},
		{
			name: "a binding for something else entirely",
			object: map[string]any{
				"status": map[string]any{"boundResources": []any{
					map[string]any{"group": "code.railgrid.ai", "resource": "repositories"},
				}},
				"spec": map[string]any{"reference": map[string]any{"export": map[string]any{"name": "code.railgrid.ai"}}},
			},
			want: false,
		},
		{name: "an empty object", object: map[string]any{}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bindingServesGroup(&unstructured.Unstructured{Object: tc.object}, group); got != tc.want {
				t.Errorf("bindingServesGroup = %v, want %v", got, tc.want)
			}
		})
	}
}

// A binding that exists under the conventional name but exports something else
// must NOT be turned into a data-plane URL: addressing the wrong provider is
// worse than reporting that we could not tell.
func TestAMismatchedBindingIsRefused(t *testing.T) {
	const cluster = "aaaaaaaaaaaaaaaa"
	callers := &conformance.FakeCallers{
		Cluster: cluster,
		Token:   "caller-token",
		Objects: []*unstructured.Unstructured{{Object: map[string]any{
			"apiVersion": "apis.kcp.io/v1alpha2",
			"kind":       "APIBinding",
			// Named "infrastructure", exporting something else — the shape the
			// consistency check exists for.
			"metadata": map[string]any{"name": "infrastructure"},
			"spec":     map[string]any{"reference": map[string]any{"export": map[string]any{"name": "code.railgrid.ai"}}},
		}}},
		ListKinds: map[schema.GroupVersionResource]string{APIBindingGVR: "APIBindingList"},
		Allow:     func(conformance.Attributes) bool { return true },
	}
	s := &Server{callers: callers, providerLookups: newProviderLookupCache()}

	if got := s.providerForAPIGroup(t.Context(), cluster, "caller-token", tools.InstanceAPIGroup); got != "" {
		t.Errorf("resolved %q from a binding that does not serve %s", got, tools.InstanceAPIGroup)
	}
	// And a failure is not cached: the next caller may find a workspace that
	// has since been fixed.
	if _, cached := s.providerLookups.get(cluster + "|" + tools.InstanceAPIGroup); cached {
		t.Error("a failed resolution was cached")
	}
}
