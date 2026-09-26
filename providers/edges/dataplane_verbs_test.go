// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"sort"
	"testing"

	"github.com/railgrid/provider-sdk/apiexportgen"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
	sdktunnel "github.com/railgrid/provider-edges/internal/tunnel"
)

// A declared verb grants nothing and serves nothing; what it buys is that the
// coordinate is machine-readable, so the hub's scoped-identity service will
// mint a capability for it (clause C) and a consumer can claim it under its own
// spec.requires instead of hardcoding it. That is worth exactly as much as the
// declaration's accuracy, so manifest.yaml's spec.export.resources[].verbs and
// the handler's own verb table (internal/tunnel/grammar.go) are pinned to each
// other here: the table stays hand-written, because it is the in-process gate
// that 404s an unserved coordinate before any object is touched, and this test
// is what stops the two from drifting.
//
// The chart's CatalogEntry is a copy of the manifest and is checked for parity
// by hack/verify-provider-contract.mjs, so pinning the manifest pins all three.
func TestDeclaredDataPlaneVerbsMatchWhatIsServed(t *testing.T) {
	// Read through the same parser apiexportgen uses to publish the custom
	// subresources, so this test cannot agree with a manifest the generator
	// would read differently.
	export, err := apiexportgen.LoadExport("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if export.Name != apiExportName {
		t.Errorf("manifest declares spec.export.name %q, init applies %q", export.Name, apiExportName)
	}

	declared := map[string][]string{}
	for _, resource := range export.Resources {
		for _, verb := range resource.Verbs {
			declared[resource.Name] = append(declared[resource.Name], verb.Name)
		}
	}
	served := sdktunnel.DataPlaneVerbs()

	normalize := func(m map[string][]string) {
		for _, verbs := range m {
			sort.Strings(verbs)
		}
	}
	normalize(declared)
	normalize(served)

	for resource, verbs := range served {
		got, ok := declared[resource]
		if !ok {
			t.Errorf("the provider serves verbs on %q that manifest.yaml does not declare: %v", resource, verbs)
			continue
		}
		if !equalStrings(got, verbs) {
			t.Errorf("%s: manifest declares %v, the handler serves %v", resource, got, verbs)
		}
	}
	for resource, verbs := range declared {
		if _, ok := served[resource]; !ok {
			t.Errorf("manifest.yaml declares verbs on %q that nothing serves: %v", resource, verbs)
		}
	}
}

// TestDeclaredResourcesNameThisProvidersKinds: the apiVersion and kind are
// declared once per resource and are what lets a consumer address a coordinate
// without knowing this provider's group, so a typo there sends every caller to
// a group nothing answers on. The verbs are served on
// edges.railgrid.ai/v1alpha1 and on nothing else.
func TestDeclaredResourcesNameThisProvidersKinds(t *testing.T) {
	export, err := apiexportgen.LoadExport("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}

	wantKind := map[string]string{
		edgesv1alpha1.KubernetesClusterResource: "KubernetesCluster",
		edgesv1alpha1.LinuxServerResource:       "LinuxServer",
		edgesv1alpha1.MacOSServerResource:       "MacOSServer",
		edgesv1alpha1.ServiceResource:           "Service",
	}
	apiVersion := edgesv1alpha1.SchemeGroupVersion.String()

	seen := map[string]bool{}
	for _, resource := range export.Resources {
		kind, ok := wantKind[resource.Name]
		if !ok {
			t.Errorf("spec.export declares %q, which is not one of this provider's verb-serving kinds", resource.Name)
			continue
		}
		seen[resource.Name] = true
		if resource.Kind != kind {
			t.Errorf("%s: declared kind %q, want %q", resource.Name, resource.Kind, kind)
		}
		if resource.APIVersion != apiVersion {
			t.Errorf("%s: declared apiVersion %q, want %q", resource.Name, resource.APIVersion, apiVersion)
		}
		if len(resource.Actions) != 0 {
			t.Errorf("%s: declares %d action(s); this provider declares none", resource.Name, len(resource.Actions))
		}
	}
	for resource := range wantKind {
		if !seen[resource] {
			t.Errorf("spec.export.resources omits %q, so none of its verbs is published", resource)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
