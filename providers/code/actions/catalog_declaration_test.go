// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// catalogEntrySpec is the slice of providers/code/manifest.yaml this test
// reasons about: what the provider DECLARES, as actions and as data-plane
// verbs. The chart copy is checked against this file by the root
// actions_catalog_test.go and by hack/verify-provider-contract.mjs, so
// declaring in one place is enough here.
type catalogEntrySpec struct {
	Spec struct {
		Actions []struct {
			ID            string `json:"id"`
			BoundResource struct {
				Resource string `json:"resource"`
			} `json:"boundResource"`
		} `json:"actions"`
		DataPlane struct {
			Verbs []struct {
				Resource string `json:"resource"`
				Verb     string `json:"verb"`
			} `json:"verbs"`
		} `json:"dataPlane"`
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
	if len(entry.Spec.Actions) == 0 {
		t.Fatal("manifest declares no actions")
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

// catalogued maps each resource to the verbs spec.actions declares on it. An
// action id is "{verb}/{version}"; the RBAC coordinate is the verb half.
func catalogued(entry catalogEntrySpec) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	for _, action := range entry.Spec.Actions {
		resource := action.BoundResource.Resource
		if out[resource] == nil {
			out[resource] = map[string]struct{}{}
		}
		out[resource][strings.SplitN(action.ID, "/", 2)[0]] = struct{}{}
	}
	return out
}

// TestServedButUncataloguedVerbsAreDeclaredAsDataPlaneVerbs closes the gap that
// made stage_snapshot and stage_commit_bundle ungrantable.
//
// Both are served on the actions grammar and gated exactly like a catalogued
// action, but they are deliberately absent from spec.actions: their bodies
// exceed limits.maxInputBytes, which the CatalogEntry API caps at 1 MiB, so
// no honest action declaration exists (docs/provider-actions.md,
// "Uncatalogued large-upload verbs").
//
// The hub's identity policy (pkg/hub/identity/policy.go, clause C) mints
// `create` on {resource}/{verb} only for a coordinate the owning provider
// declares — as an action or as a data-plane verb. A verb this server answers
// but declares in NEITHER place can never be granted to a consumer identity,
// so an App Studio project or a factory runner that must upload more than a
// mebibyte is stuck no matter what the hub is asked for. Serving a verb and
// declaring its coordinate are therefore one edit.
func TestServedButUncataloguedVerbsAreDeclaredAsDataPlaneVerbs(t *testing.T) {
	entry := loadCatalogEntry(t)
	declared := map[string]map[string]struct{}{}
	for _, verb := range entry.Spec.DataPlane.Verbs {
		if declared[verb.Resource] == nil {
			declared[verb.Resource] = map[string]struct{}{}
		}
		declared[verb.Resource][verb.Verb] = struct{}{}
	}
	inCatalog := catalogued(entry)

	for resource, verbs := range servedVerbs() {
		for verb := range verbs {
			if _, ok := inCatalog[resource][verb]; ok {
				continue
			}
			if _, ok := declared[resource][verb]; !ok {
				t.Errorf("%s/%s is served but declared in neither spec.actions nor spec.dataPlane.verbs: "+
					"the hub can never mint create on it, so no consumer identity can invoke it", resource, verb)
			}
		}
	}

	// The two known exceptions, pinned by name: if either ever became a
	// catalogued action the assertion above would still pass, and this says
	// out loud which verbs the carve-out covers today.
	for _, verb := range []string{StageSnapshot, StageCommitBundle} {
		if _, ok := declared[repositories.Resource][verb]; !ok {
			t.Errorf("%s is the uncatalogued large-upload carve-out and must stay declared under spec.dataPlane.verbs", verb)
		}
	}
}

// TestDataPlaneVerbsDoNotRestateCataloguedActions keeps the two declarations
// disjoint. An action and a data-plane verb are two descriptions of the SAME
// RBAC coordinate: an action is versioned, schema'd and request/response, a
// data-plane verb is unversioned and streaming or proxying
// (docs/provider-connectivity-contract.md, "Declare the verbs you serve").
// Declaring a verb both ways would give a consumer two contradictory
// descriptions of one coordinate — one with a pinned schema digest, one with
// no schema at all.
//
// It also refuses a declared verb this server does not answer: that is a
// coordinate the hub would happily mint a capability for and the provider
// would 404.
func TestDataPlaneVerbsDoNotRestateCataloguedActions(t *testing.T) {
	entry := loadCatalogEntry(t)
	inCatalog := catalogued(entry)
	serving := servedVerbs()

	if len(entry.Spec.DataPlane.Verbs) == 0 {
		t.Fatal("manifest declares no data-plane verbs; the uncatalogued upload verbs must be there")
	}
	for _, verb := range entry.Spec.DataPlane.Verbs {
		if _, ok := inCatalog[verb.Resource][verb.Verb]; ok {
			t.Errorf("%s/%s is declared both as an action and as a data-plane verb; declare each capability as exactly one",
				verb.Resource, verb.Verb)
		}
		if _, ok := serving[verb.Resource][verb.Verb]; !ok {
			t.Errorf("%s/%s is declared as a data-plane verb but this server serves no such verb", verb.Resource, verb.Verb)
		}
	}
}
