/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/gorilla/mux"
	"github.com/railgrid/provider-sdk/dataplane"
	"sigs.k8s.io/yaml"
)

// A verb that is served but not declared is a coordinate the hub's
// scoped-identity service cannot verify, so no consumer can ever be granted
// it (pkg/hub/identity/policy.go, clause C). A verb that is declared but not
// served is worse: the hub WILL mint a capability for it, and the call 404s.
// Both are build failures here.
func TestDataPlaneVerbsMatchManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "manifest.yaml"))
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
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}

	declared := []string{}
	for _, v := range manifest.Spec.DataPlane.Verbs {
		declared = append(declared, v.Resource+"/"+v.Verb)
	}
	served := []string{}
	for resource, byVerb := range verbIndex {
		for verb := range byVerb {
			served = append(served, resource+"/"+verb)
		}
	}
	sort.Strings(declared)
	sort.Strings(served)
	if !reflect.DeepEqual(declared, served) {
		t.Fatalf("manifest and served verbs differ:\n declared %v\n served   %v", missing(served, declared), missing(declared, served))
	}
}

// missing returns the entries of a that are absent from b.
func missing(a, b []string) []string {
	have := map[string]bool{}
	for _, v := range b {
		have[v] = true
	}
	out := []string{}
	for _, v := range a {
		if !have[v] {
			out = append(out, v)
		}
	}
	return out
}

// Every declared verb has to survive a round trip through the grammar: the
// hub renders a consumer's coordinate with dataplane.ProviderPath, and a verb
// that does not round-trip is one no consumer can call however it is granted.
func TestEveryVerbIsAddressable(t *testing.T) {
	const cluster = "rgl3jcl2cfl3xa5p"
	for resource, byVerb := range verbIndex {
		for verb, route := range byVerb {
			if len(route.handlers) == 0 {
				t.Errorf("%s/%s has no handler", resource, verb)
			}
			request := dataplane.Request{ClusterID: cluster, Resource: resource, Name: "demo", Verb: verb}
			path, err := request.Path(dataplane.DataplaneRoot)
			if err != nil {
				t.Errorf("%s/%s is not addressable: %v", resource, verb, err)
				continue
			}
			parsed, ok := dataplane.ParsePath(dataplane.DataplaneRoot, path)
			if !ok || parsed != request {
				t.Errorf("%s/%s does not round-trip: %q -> %+v (ok=%v)", resource, verb, path, parsed, ok)
			}
		}
	}
}

// The retired /api table is a test fixture, not a second route table. It must
// not grow: a verb added there and not to the served table would be tested and
// unreachable.
func TestRetiredRouteFixtureIsNotASecondRouteTable(t *testing.T) {
	router := mux.NewRouter()
	(&Server{}).Register(router)
	routes := 0
	if err := router.Walk(func(*mux.Route, *mux.Router, []*mux.Route) error {
		routes++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The count the migration retired. Changing the fixture without changing
	// the served table is what this pins.
	if routes != retiredRouteCount {
		t.Fatalf("retired route fixture has %d routes, want %d — add the verb to dataplane_table.go instead", routes, retiredRouteCount)
	}
}

// retiredRouteCount is the size of the /api table this migration replaced.
const retiredRouteCount = 85
