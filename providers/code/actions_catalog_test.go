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

func TestCodeActionsCatalogParityAndDigests(t *testing.T) {
	source, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = yaml.Unmarshal(source, &manifest); err != nil {
		t.Fatal(err)
	}
	expected := manifest["spec"].(map[string]any)["actions"]
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
		if !reflect.DeepEqual(expected, catalog["spec"].(map[string]any)["actions"]) {
			t.Fatal("Code manifest/chart actions differ")
		}
		found = true
	}
	if !found {
		t.Fatal("rendered Code catalog missing")
	}
	actions, ok := expected.([]any)
	if !ok || len(actions) != 12 {
		t.Fatalf("unexpected Code actions: %#v", expected)
	}
	for _, raw := range actions {
		action := raw.(map[string]any)
		schemas, err := json.Marshal(map[string]any{"input": action["inputSchema"], "output": action["outputSchema"]})
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(schemas)
		if action["schemaDigest"] != "sha256:"+hex.EncodeToString(digest[:]) {
			t.Fatalf("schema digest mismatch for %s", action["id"])
		}
		if action["boundResource"].(map[string]any)["resource"] != "repositories" {
			t.Fatal("Code action is not repository-bound")
		}
		if action["limits"].(map[string]any)["maxInputBytes"].(float64) > 1048576 {
			t.Fatal("large artifact leaked into action input contract")
		}
	}
}
