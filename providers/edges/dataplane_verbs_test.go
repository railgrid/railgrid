// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"os"
	"sort"
	"testing"

	"sigs.k8s.io/yaml"

	sdktunnel "github.com/railgrid/provider-edges/internal/tunnel"
)

// A declared data-plane verb grants nothing and serves nothing; what it buys
// is that the coordinate is machine-readable, so the hub's scoped-identity
// service will mint a capability for it (clause C) and a consumer can read it
// off /api/providers instead of hardcoding it. That is worth exactly as much
// as the declaration's accuracy, so the manifest and the handler's own verb
// table are pinned to each other here.
//
// The chart's CatalogEntry is a copy of the manifest and is checked for parity
// by hack/verify-provider-contract.mjs, so pinning the manifest pins all three.
func TestDeclaredDataPlaneVerbsMatchWhatIsServed(t *testing.T) {
	source, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Spec struct {
			DataPlane struct {
				Verbs []struct {
					Resource string `json:"resource"`
					Verb     string `json:"verb"`
				} `json:"verbs"`
			} `json:"dataPlane"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(source, &manifest); err != nil {
		t.Fatal(err)
	}

	declared := map[string][]string{}
	for _, v := range manifest.Spec.DataPlane.Verbs {
		declared[v.Resource] = append(declared[v.Resource], v.Verb)
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
