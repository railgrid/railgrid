// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
)

// TestAgentActionsMatchManifest holds the catalogued actions to the table
// that serves them: every declared action is served with exactly the limits
// it declares, nothing is served undeclared, each schemaDigest is re-derived
// from the schemas it covers, and every declaration passes the same
// validation the hub runs at catalog admission — a malformed one would fail
// the whole CatalogEntry closed, taking every verb down with it.
func TestAgentActionsMatchManifest(t *testing.T) {
	raw, err := os.ReadFile("../manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var entry providersv1alpha1.CatalogEntry
	if err := yaml.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("manifest.yaml is not a CatalogEntry: %v", err)
	}
	var generic map[string]any
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}

	declared := map[string]providersv1alpha1.ProviderAction{}
	for _, resource := range entry.Spec.Export.Resources {
		for _, action := range resource.Actions {
			if resource.Name != "agents" {
				t.Errorf("action %s is declared on %s; this provider serves actions only on agents", action.Name, resource.Name)
				continue
			}
			declared[action.Name] = action
		}
	}
	if len(declared) != len(actionLimits) {
		t.Errorf("manifest declares %d actions, the action table serves %d", len(declared), len(actionLimits))
	}

	for name, limits := range actionLimits {
		action, ok := declared[name]
		if !ok {
			t.Errorf("%s is served but not declared, so kcp never routes it here", name)
			continue
		}
		if action.Version != actionContractVersion {
			t.Errorf("%s is declared at version %s, served at %s", name, action.Version, actionContractVersion)
		}
		got := action.Limits
		if time.Duration(got.TimeoutSeconds)*time.Second != limits.Timeout ||
			got.MaxInputBytes != limits.MaxInputBytes || got.MaxOutputBytes != limits.MaxOutputBytes ||
			got.MaxResultItems != limits.MaxResultItems {
			t.Errorf("%s: declared limits %+v, served %+v", name, got, limits)
		}
		// App Studio's automatic integration path grants every consent-free
		// action to every Project on each assistant turn. Neither of these may
		// be granted that way: one spends the agent's budget, the other reads
		// what its tools returned.
		if !action.Consent.Required || action.Consent.Prompt == "" {
			t.Errorf("%s must require consent with a prompt, or every Project is granted it automatically", name)
		}
		if err := providersv1alpha1.ValidateProviderAction(action); err != nil {
			t.Errorf("%s: the hub would refuse this declaration: %v", name, err)
		}
		digest, err := providersv1alpha1.ProviderActionSchemaDigest(action)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if action.SchemaDigest != digest {
			t.Errorf("%s: schemaDigest %s, the schemas digest to %s", name, action.SchemaDigest, digest)
		}
		if want := legacyDigest(t, generic, name); want != digest {
			t.Errorf("%s: the hub digest %s disagrees with the canonical-envelope digest %s", name, digest, want)
		}
	}
}

// legacyDigest re-derives a digest the way the code provider's catalog test
// does, straight from the YAML, as an independent check on the API helper.
func legacyDigest(t *testing.T, manifest map[string]any, action string) string {
	t.Helper()
	export := manifest["spec"].(map[string]any)["export"].(map[string]any)
	for _, r := range export["resources"].([]any) {
		resource := r.(map[string]any)
		actions, _ := resource["actions"].([]any)
		for _, a := range actions {
			declaration := a.(map[string]any)
			if declaration["name"] != action {
				continue
			}
			data, err := json.Marshal(map[string]any{"input": declaration["inputSchema"], "output": declaration["outputSchema"]})
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			return "sha256:" + hex.EncodeToString(sum[:])
		}
	}
	t.Fatalf("action %s not found", action)
	return ""
}
