// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/railgrid/provider-edges/internal/svccatalog"
)

// update regenerates the committed asset instead of asserting it:
//
//	go test ./... -run TestServiceCatalogAsset -update
var update = flag.Bool("update", false, "rewrite the generated service-catalog asset")

// serviceCatalogAssetPath is the generated file. It sits in the Vite
// project's src/ rather than public/ so it is IMPORTED into the bundle rather
// than fetched beside it: the hub pins the integrity of main.js, and a
// catalog the page fetches separately would be the one part of the UI's
// behaviour that pin does not cover.
const serviceCatalogAssetPath = "portal/src/service-catalog.json"

// The service catalog is static provider metadata: which service types exist,
// what the add/configure form collects for each, and which MCP tools each one
// exposes. It used to be served from an UNAUTHENTICATED /catalog route on the
// provider — a route no Pillar 2 class admits, and one that had to exist only
// because the portal could not read a file it already ships.
//
// It is now a build artifact of the same bundle the portal is, generated from
// internal/svccatalog by this test. The test is the generator so the two can
// never drift: CI runs it without -update and fails if the committed file does
// not match the code it is derived from.
func TestServiceCatalogAsset(t *testing.T) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(svccatalog.All()); err != nil {
		t.Fatalf("encoding the service catalog: %v", err)
	}
	want := buffer.Bytes()

	if *update {
		if err := os.WriteFile(serviceCatalogAssetPath, want, 0o644); err != nil {
			t.Fatalf("writing %s: %v", serviceCatalogAssetPath, err)
		}
		t.Logf("regenerated %s", serviceCatalogAssetPath)
		return
	}

	got, err := os.ReadFile(serviceCatalogAssetPath)
	if err != nil {
		t.Fatalf("reading %s: %v (run: go test ./... -run TestServiceCatalogAsset -update)", serviceCatalogAssetPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; regenerate it with:\n\tgo test ./... -run TestServiceCatalogAsset -update", serviceCatalogAssetPath)
	}
}
