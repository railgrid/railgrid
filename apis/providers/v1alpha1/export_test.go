/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	"strings"
	"testing"
)

func testExport() *ProviderExport {
	return &ProviderExport{
		Name: "infrastructure.providers.railgrid.ai",
		Resources: []ProviderExportResource{{
			Name:       "instances",
			APIVersion: "infrastructure.railgrid.ai/v1alpha1",
			Kind:       "Instance",
			Verbs: []ProviderVerb{
				{Name: "exec", Description: "Run a command.", Stream: true},
				{Name: "log", Description: "Stream logs.", Stream: true, ReadOnly: true},
				{Name: "runtime-status", ReadOnly: true},
			},
		}, {
			Name:       "templates",
			APIVersion: "infrastructure.railgrid.ai/v1alpha1",
			Kind:       "Template",
		}},
	}
}

func TestValidateProviderExportAcceptsADeclaration(t *testing.T) {
	if err := ValidateProviderExport(testExport()); err != nil {
		t.Fatalf("valid export rejected: %v", err)
	}
	if err := ValidateProviderExport(nil); err != nil {
		t.Fatalf("a provider exporting nothing was rejected: %v", err)
	}
}

func TestValidateProviderExportRejectsWhatItMust(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ProviderExport)
		want   string
	}{
		{"no name", func(e *ProviderExport) { e.Name = "" }, "export.name is required"},
		{"resource is not plural-shaped", func(e *ProviderExport) { e.Resources[0].Name = "Instances" }, "lowercase DNS-like plural"},
		{"apiVersion has no group", func(e *ProviderExport) { e.Resources[0].APIVersion = "v1alpha1" }, "group/version"},
		{"apiVersion has no version", func(e *ProviderExport) { e.Resources[0].APIVersion = "infrastructure.railgrid.ai" }, "group/version"},
		{"kind is not a kind", func(e *ProviderExport) { e.Resources[0].Kind = "instance" }, "UpperCamelCase"},
		{"duplicate resource", func(e *ProviderExport) { e.Resources = append(e.Resources, e.Resources[0]) }, "duplicate resource"},
		{
			"a standard verb cannot name a coordinate",
			func(e *ProviderExport) { e.Resources[0].Verbs[0].Name = "delete" },
			"standard Kubernetes verb",
		},
		{
			"a schema-owned subresource cannot be a verb",
			func(e *ProviderExport) { e.Resources[0].Verbs[0].Name = "status" },
			"the kind's own schema owns",
		},
		{
			"a verb carries no version",
			func(e *ProviderExport) { e.Resources[0].Verbs[0].Name = "exec/v1" },
			"no version and no slash",
		},
		{
			"duplicate verb",
			func(e *ProviderExport) { e.Resources[0].Verbs[1].Name = "exec" },
			"declared twice",
		},
		{
			"a description cannot smuggle newlines",
			func(e *ProviderExport) { e.Resources[0].Verbs[0].Description = "one\ntwo" },
			"prohibited characters",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			export := testExport()
			tc.mutate(export)
			if err := ValidateProviderExport(export); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// A verb and an action share one coordinate namespace: both generate the
// APIExport entry "<resource>/<name>", so the collision has to be refused
// where it is declared rather than resolved by whichever the generator
// happens to walk second.
func TestValidateProviderExportRefusesAVerbAndActionOfTheSameName(t *testing.T) {
	export := testExport()
	action := testProviderAction()
	action.Name = "exec"
	export.Resources[0].Actions = []ProviderAction{action}
	if err := ValidateProviderExport(export); err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("validation error = %v, want a collision on instances/exec", err)
	}
}

func TestCoordinatesFlattensVerbsAndActions(t *testing.T) {
	export := testExport()
	action := testProviderAction()
	export.Resources[0].Actions = []ProviderAction{action}

	got := export.Coordinates()
	if len(got) != 4 {
		t.Fatalf("Coordinates() returned %d entries, want 4: %+v", len(got), got)
	}
	// Verbs first, in declaration order, then actions.
	if got[0].String() != "instances/exec" || got[0].Action || got[0].Version != "" {
		t.Fatalf("first coordinate = %+v, want the exec verb", got[0])
	}
	if got[0].Kind != "Instance" || got[0].GroupVersion().Group != "infrastructure.railgrid.ai" {
		t.Fatalf("a coordinate must carry its parent's apiVersion and kind: %+v", got[0])
	}
	last := got[len(got)-1]
	if last.String() != "instances/query_table" || !last.Action || last.Version != "v1" {
		t.Fatalf("last coordinate = %+v, want the action with its contract version", last)
	}
	if len(export.Actions()) != 1 {
		t.Fatalf("Actions() = %+v, want only the catalogued action", export.Actions())
	}
}

// The identity policy asks this one question — "did anybody declare this
// coordinate?" — and draws no distinction between a verb and an action.
func TestProviderDeclaredVerbsIsScopedToItsResourceAndCoversActions(t *testing.T) {
	export := testExport()
	action := testProviderAction()
	export.Resources[0].Actions = []ProviderAction{action}

	got := ProviderDeclaredVerbs(export, "instances")
	want := []string{"exec", "log", "runtime-status", "query_table"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ProviderDeclaredVerbs(instances) = %v, want %v", got, want)
	}
	if got := ProviderDeclaredVerbs(export, "templates"); len(got) != 0 {
		t.Fatalf("a resource declaring no verbs reported %v", got)
	}
	if got := ProviderDeclaredVerbs(export, "unbound"); len(got) != 0 {
		t.Fatalf("an undeclared resource reported %v", got)
	}
	if got := ProviderDeclaredVerbs(nil, "instances"); len(got) != 0 {
		t.Fatalf("a provider exporting nothing reported %v", got)
	}
}
