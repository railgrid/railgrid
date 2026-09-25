// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"os"
	"testing"

	"sigs.k8s.io/yaml"
)

// catalogEntrySpec is the slice of providers/code/manifest.yaml this test
// reasons about: spec.export.resources[], where what the provider DECLARES
// hangs off the resource it is served on — its verbs and its actions. The
// chart copy is checked against this file by the root actions_catalog_test.go
// and by hack/verify-provider-contract.mjs, so declaring in one place is enough
// here.
type catalogEntrySpec struct {
	Spec struct {
		Export struct {
			Resources []struct {
				Name  string `json:"name"`
				Verbs []struct {
					Name string `json:"name"`
				} `json:"verbs"`
				Actions []struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"actions"`
			} `json:"resources"`
		} `json:"export"`
	} `json:"spec"`
}

func loadCatalogEntry(t *testing.T) catalogEntrySpec {
	t.Helper()
	raw, err := os.ReadFile("../manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var entry catalogEntrySpec
	if err := yaml.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	if len(entry.Spec.Export.Resources) == 0 {
		t.Fatal("manifest declares no export resources")
	}
	return entry
}

// servedVerbs is every verb this server answers, by the resource it is served
// on — the two `served`/`connectionActions` tables ServeHTTP dispatches from.
func servedVerbs() map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{
		repositories.Resource: {},
		connections.Resource:  {},
	}
	for verb := range served {
		out[repositories.Resource][verb] = struct{}{}
	}
	for verb := range connectionActions {
		out[connections.Resource][verb] = struct{}{}
	}
	return out
}

// catalogued maps each resource to the action names declared on it. An action's
// name IS the RBAC coordinate's verb half; its version is a separate field and
// appears in no path.
func catalogued(entry catalogEntrySpec) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	for _, resource := range entry.Spec.Export.Resources {
		if out[resource.Name] == nil {
			out[resource.Name] = map[string]struct{}{}
		}
		for _, action := range resource.Actions {
			out[resource.Name][action.Name] = struct{}{}
		}
	}
	return out
}

// declaredVerbs maps each resource to the plain (unversioned, unschema'd) verb
// names declared on it.
func declaredVerbs(entry catalogEntrySpec) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	for _, resource := range entry.Spec.Export.Resources {
		if out[resource.Name] == nil {
			out[resource.Name] = map[string]struct{}{}
		}
		for _, verb := range resource.Verbs {
			out[resource.Name][verb.Name] = struct{}{}
		}
	}
	return out
}

// TestServedButUncataloguedVerbsAreDeclaredAsVerbs closes the gap that made
// stage-snapshot and stage-commit-bundle ungrantable.
//
// Both are served on the actions grammar and gated exactly like a catalogued
// action, but they are deliberately absent from actions[]: their bodies exceed
// limits.maxInputBytes, which the CatalogEntry API caps at 1 MiB, so no honest
// action declaration exists (docs/provider-actions.md, "Uncatalogued
// large-upload verbs").
//
// The hub's identity policy (pkg/hub/identity/policy.go, clause C) mints
// `create` on {resource}/{verb} only for a coordinate the owning provider
// declares — as an action or as a verb. A verb this server answers but declares
// in NEITHER place can never be granted to a consumer identity, so an App
// Studio project or a factory runner that must upload more than a mebibyte is
// stuck no matter what the hub is asked for. Serving a verb and declaring its
// coordinate are therefore one edit.
func TestServedButUncataloguedVerbsAreDeclaredAsVerbs(t *testing.T) {
	entry := loadCatalogEntry(t)
	declared := declaredVerbs(entry)
	inCatalog := catalogued(entry)

	for resource, verbs := range servedVerbs() {
		for verb := range verbs {
			if _, ok := inCatalog[resource][verb]; ok {
				continue
			}
			if _, ok := declared[resource][verb]; !ok {
				t.Errorf("%s/%s is served but declared in neither actions[] nor verbs[] on that resource: "+
					"the hub can never mint create on it, so no consumer identity can invoke it", resource, verb)
			}
		}
	}

	// The two known exceptions, pinned by name: if either ever became a
	// catalogued action the assertion above would still pass, and this says
	// out loud which verbs the carve-out covers today. It is also what lets
	// catalogentry.go route every declared verb to the actions handler without
	// naming either of them.
	for _, verb := range []string{StageSnapshot, StageCommitBundle} {
		if _, ok := declared[repositories.Resource][verb]; !ok {
			t.Errorf("%s is the uncatalogued large-upload carve-out and must stay declared under spec.export.resources[repositories].verbs", verb)
		}
	}
}

// TestVerbsDoNotRestateCataloguedActions keeps the two declarations disjoint.
// An action and a plain verb are two descriptions of the SAME RBAC coordinate:
// an action is versioned, schema'd and request/response, a verb is unversioned
// and streaming or proxying (docs/provider-connectivity-contract.md, "Declare
// the verbs you serve"). Declaring a verb both ways would give a consumer two
// contradictory descriptions of one coordinate — one with a pinned schema
// digest, one with no schema at all — and the CatalogEntry API refuses it
// outright, since verbs and actions share one coordinate namespace per resource
// (ValidateProviderExportResource).
//
// It also refuses a declared verb this server does not answer: that is a
// coordinate the hub would happily mint a capability for and the provider
// would 404.
func TestVerbsDoNotRestateCataloguedActions(t *testing.T) {
	entry := loadCatalogEntry(t)
	inCatalog := catalogued(entry)
	serving := servedVerbs()

	total := 0
	for _, resource := range entry.Spec.Export.Resources {
		for _, verb := range resource.Verbs {
			total++
			if _, ok := inCatalog[resource.Name][verb.Name]; ok {
				t.Errorf("%s/%s is declared both as an action and as a verb; declare each capability as exactly one",
					resource.Name, verb.Name)
			}
			if _, ok := serving[resource.Name][verb.Name]; !ok {
				t.Errorf("%s/%s is declared as a verb but this server serves no such verb", resource.Name, verb.Name)
			}
		}
	}
	if total == 0 {
		t.Fatal("manifest declares no verbs; the uncatalogued upload verbs must be there")
	}
}

// TestCataloguedActionsAreServedAtTheContractVersion: every action's declared
// version is the one version this server implements, because the path does not
// carry it and serve's adapter restores it from the declaration. A manifest
// that bumped an action to v2 without a handler change would route a v2 caller
// into the v1 code.
func TestCataloguedActionsAreServedAtTheContractVersion(t *testing.T) {
	for _, resource := range loadCatalogEntry(t).Spec.Export.Resources {
		for _, action := range resource.Actions {
			if action.Version != ContractVersion {
				t.Errorf("%s/%s is declared at %s but this server implements %s only",
					resource.Name, action.Name, action.Version, ContractVersion)
			}
		}
	}
}
