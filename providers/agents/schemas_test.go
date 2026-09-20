// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChartShipsEverySchema guards the one place this provider's kinds can go
// missing without anything else noticing.
//
// The APIExport is generated from deploy/chart/files/schemas/, and that
// directory is filled by a copy loop in the root Makefile that names each kind
// explicitly:
//
//	for r in agents connections schedules triggers toolsets runs; do …
//
// A kind added to apis/v1alpha1 gets a CRD, an APIResourceSchema and a
// deepcopy from codegen, and then is silently absent from the APIExport —
// which means the tenant's binding never serves it, every read 404s, and the
// only symptom is a provider that appears to work until someone uses the new
// kind. `go build` cannot see this and neither can the contract verifier.
//
// So: the chart must carry a schema for every schema codegen produced, and
// each copy must be current. If this fails after adding a kind, add it to the
// Makefile loop and re-run `make codegen-agents-provider`.
func TestChartShipsEverySchema(t *testing.T) {
	const (
		generated = "config/kcp"
		shipped   = "deploy/chart/files/schemas"
	)
	entries, err := os.ReadDir(generated)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, entry := range entries {
		name := entry.Name()
		kind, ok := strings.CutPrefix(name, "apiresourceschema-")
		if !ok || !strings.HasSuffix(kind, ".yaml") {
			continue
		}
		found++
		want, err := os.ReadFile(filepath.Join(generated, name))
		if err != nil {
			t.Fatal(err)
		}
		// The chart drops the "apiresourceschema-" prefix; the rest of the name
		// is identical.
		target := filepath.Join(shipped, kind)
		got, err := os.ReadFile(target)
		if os.IsNotExist(err) {
			t.Errorf("%s is generated but the chart does not ship it: add its kind to the codegen-agents-provider copy loop in the root Makefile, or the APIExport will not serve it", target)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s is stale: re-run `make codegen-agents-provider`", target)
		}
	}
	if found == 0 {
		t.Fatal("no generated APIResourceSchemas found; has codegen ever run?")
	}
}
