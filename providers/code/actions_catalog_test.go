// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestCodeActionsCatalogParityAndDigests holds manifest.yaml and the chart's
// rendered copy to one declaration, and re-derives every schemaDigest from the
// schemas it covers.
//
// Parity is asserted over the whole of spec.export — the export's name, its
// resources, and the verbs and actions hanging off each — because a coordinate
// is now declared by where it sits, so a resource that drifted (a different
// apiVersion, an action moved to another kind) would change what kcp routes
// without changing any action's own fields.
func TestCodeActionsCatalogParityAndDigests(t *testing.T) {
	source, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = yaml.Unmarshal(source, &manifest); err != nil {
		t.Fatal(err)
	}
	spec := manifest["spec"].(map[string]any)
	expected := spec["export"]
	rendered, err := exec.Command("helm", "template", "code", "deploy/chart", "--set", "catalogEntry.enabled=true").CombinedOutput()
	if err != nil {
		t.Fatalf("helm: %v %s", err, rendered)
	}
	found := false
	for _, doc := range bytes.Split(rendered, []byte("\n---")) {
		var object map[string]any
		if yaml.Unmarshal(doc, &object) != nil || object["kind"] != "ConfigMap" {
			continue
		}
		data, _ := object["data"].(map[string]any)
		raw, _ := data["catalogentry.yaml"].(string)
		if raw == "" {
			continue
		}
		var catalog map[string]any
		if err = yaml.Unmarshal([]byte(raw), &catalog); err != nil {
			t.Fatal(err)
		}
		chartSpec := catalog["spec"].(map[string]any)
		if !reflect.DeepEqual(expected, chartSpec["export"]) {
			t.Fatal("Code manifest/chart spec.export differ")
		}
		// The claims the export is generated with travel with it.
		if !reflect.DeepEqual(spec["requires"], chartSpec["requires"]) {
			t.Fatal("Code manifest/chart spec.requires differ")
		}
		found = true
	}
	if !found {
		t.Fatal("rendered Code catalog missing")
	}

	export := expected.(map[string]any)
	if name := export["name"]; name != "code.providers.railgrid.ai" {
		t.Fatalf("unexpected export name %v", name)
	}
	resources, ok := export["resources"].([]any)
	if !ok || len(resources) != 2 {
		t.Fatalf("unexpected Code export resources: %#v", export["resources"])
	}

	total := 0
	for _, rawResource := range resources {
		resource := rawResource.(map[string]any)
		// Every coordinate is bound to one of this provider's own kinds. The
		// resource decides which object gate 1 reads and which subresource
		// gate 2 asks about, so a coordinate on anything else would be served
		// under a grant nobody can be given. The apiVersion and kind are
		// declared once, here, for every verb and action below.
		switch resource["name"] {
		case "repositories":
			if resource["kind"] != "Repository" {
				t.Fatalf("repositories is bound to kind %v", resource["kind"])
			}
		case "connections":
			if resource["kind"] != "Connection" {
				t.Fatalf("connections is bound to kind %v", resource["kind"])
			}
		default:
			t.Fatalf("Code declares coordinates on %v, a kind this provider does not serve", resource["name"])
		}
		if resource["apiVersion"] != "code.railgrid.ai/v1alpha1" {
			t.Fatalf("%v declares apiVersion %v", resource["name"], resource["apiVersion"])
		}

		actions, _ := resource["actions"].([]any)
		total += len(actions)
		for _, raw := range actions {
			action := raw.(map[string]any)
			id := action["name"]
			// The digest covers the two schemas and nothing else, so it is
			// unaffected by where the action is declared: the values predate
			// this reshape and must keep validating.
			schemas, err := json.Marshal(map[string]any{"input": action["inputSchema"], "output": action["outputSchema"]})
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(schemas)
			if action["schemaDigest"] != "sha256:"+hex.EncodeToString(digest[:]) {
				t.Fatalf("schema digest mismatch for %v", id)
			}
			// An action's id is derived, "<name>/<version>", and the version is
			// a field of its own rather than a suffix of a declared string.
			if action["version"] != "v1" {
				t.Fatalf("Code action %v is declared at version %v", id, action["version"])
			}
			if action["limits"].(map[string]any)["maxInputBytes"].(float64) > 1048576 {
				t.Fatal("large artifact leaked into action input contract")
			}
		}
	}
	if total != 15 {
		t.Fatalf("Code declares %d actions, want 15", total)
	}
}
