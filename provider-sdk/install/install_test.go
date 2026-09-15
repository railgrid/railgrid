/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

import "testing"

func TestSplitSchemaName(t *testing.T) {
	cases := []struct {
		name         string
		wantResource string
		wantGroup    string
	}{
		{"v260522-abc.greetings.hello.cost.railgrid.ai", "greetings", "hello.cost.railgrid.ai"},
		{"v260609-fc69fa2.connections.code.railgrid.ai", "connections", "code.railgrid.ai"},
		{"v1.savedviews.kuery.railgrid.ai", "savedviews", "kuery.railgrid.ai"},
		{"noversion", "", ""},     // no dot
		{"version.only", "", ""},  // missing group segment
		{"trailing.dot.", "", ""}, // trailing dot
	}
	for _, c := range cases {
		group, resource := splitSchemaName(c.name)
		if resource != c.wantResource || group != c.wantGroup {
			t.Errorf("splitSchemaName(%q) = (group=%q, resource=%q), want (group=%q, resource=%q)",
				c.name, group, resource, c.wantGroup, c.wantResource)
		}
	}
}

func TestMergeAPIExportResources(t *testing.T) {
	res := func(group, name string) map[string]any {
		return map[string]any{"group": group, "name": name}
	}
	existing := []any{
		res("code.railgrid.ai", "connections"),         // owned → replaced
		res("code.railgrid.ai", "coderepos"),           // stale in owned group → pruned
		res("infrastructure.railgrid.ai", "templates"), // foreign → preserved
		"unparseable", // kept verbatim
	}
	owned := []any{
		res("code.railgrid.ai", "connections"),
		res("code.railgrid.ai", "repositories"),
	}
	out := mergeAPIExportResources(existing, owned)

	// owned entries come first, in order
	if len(out) != 4 {
		t.Fatalf("expected 4 entries, got %d: %v", len(out), out)
	}
	if m, ok := out[0].(map[string]any); !ok || m["name"] != "connections" {
		t.Errorf("out[0] = %v, want owned connections first", out[0])
	}
	if m, ok := out[1].(map[string]any); !ok || m["name"] != "repositories" {
		t.Errorf("out[1] = %v, want owned repositories second", out[1])
	}
	// foreign templates preserved (not dropped by the connections overlap);
	// stale coderepos in the owned group pruned
	foundTemplates, foundUnparseable, foundStale := false, false, false
	for _, r := range out {
		if m, ok := r.(map[string]any); ok {
			switch m["name"] {
			case "templates":
				foundTemplates = true
			case "coderepos":
				foundStale = true
			}
		}
		if s, ok := r.(string); ok && s == "unparseable" {
			foundUnparseable = true
		}
	}
	if !foundTemplates {
		t.Error("foreign 'templates' resource was dropped")
	}
	if !foundUnparseable {
		t.Error("unparseable entry was dropped")
	}
	if foundStale {
		t.Error("stale 'coderepos' entry in an owned group was not pruned")
	}
}
