/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// TestSeedTemplatesBuildRGD loads every embedded seed Template and runs it
// through buildRGD — the same path SetupTemplate uses. It catches malformed
// schema/backendConfig (bad YAML, schema with no properties, empty resource
// list, unsubstituted-but-required tokens) before they ship in the binary.
func TestSeedTemplatesBuildRGD(t *testing.T) {
	dir := filepath.Join("..", "..", "install", "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}

	var seen int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		seen++
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			tmpl := decodeTemplate(t, raw)

			rgd, err := buildRGD(tmpl, "cloudflare")
			if err != nil {
				t.Fatalf("buildRGD(%s): %v", e.Name(), err)
			}

			// Sanity: the RGD must carry the resource graph and a schema.
			if _, found, _ := unstructured.NestedSlice(rgd.Object, "spec", "resources"); !found {
				t.Errorf("%s: RGD has no spec.resources", e.Name())
			}
			// No kedge tokens may survive into the authored RGD.
			if strings.Contains(mustJSON(t, rgd.Object), "${kedge.") {
				t.Errorf("%s: RGD still contains an unsubstituted ${kedge.*} token", e.Name())
			}
		})
	}
	if seen == 0 {
		t.Fatal("no seed templates found")
	}
}

func decodeTemplate(t *testing.T, raw []byte) *infrav1alpha1.Template {
	t.Helper()
	var obj map[string]any
	if err := utilyaml.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal YAML: %v", err)
	}
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var tmpl infrav1alpha1.Template
	if err := json.Unmarshal(data, &tmpl); err != nil {
		t.Fatalf("unmarshal into Template: %v", err)
	}
	return &tmpl
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
