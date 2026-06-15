/*
Copyright 2026 The Faros Authors.

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
		{"v260522-abc.greetings.hello.cost.faros.sh", "greetings", "hello.cost.faros.sh"},
		{"v260609-fc69fa2.connections.code.kedge.faros.sh", "connections", "code.kedge.faros.sh"},
		{"v1.savedviews.kuery.kedge.faros.sh", "savedviews", "kuery.kedge.faros.sh"},
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
		res("code.kedge.faros.sh", "connections"),         // owned → replaced
		res("infrastructure.kedge.faros.sh", "templates"), // foreign → preserved
		"unparseable", // kept verbatim
	}
	owned := []any{
		res("code.kedge.faros.sh", "connections"),
		res("code.kedge.faros.sh", "repositories"),
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
	// foreign templates preserved (not dropped by the connections overlap)
	foundTemplates, foundUnparseable := false, false
	for _, r := range out {
		if m, ok := r.(map[string]any); ok && m["name"] == "templates" {
			foundTemplates = true
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
}
