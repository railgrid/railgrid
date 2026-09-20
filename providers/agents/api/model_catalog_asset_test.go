// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/railgrid/provider-agents/llm"
)

// modelCatalogAsset is where the curated model catalog lives now.
//
// It used to be GET /api/catalog: a backend route carrying no tenant data at
// all, just a compiled-in table of prices and context windows. A route exists
// to do something only the server can do; serving a constant over one meant the
// portal could not render its Models tab until a round-trip to the provider
// came back, and it was a route the contract has no class for. The table is
// built into the portal bundle instead, beside the code that renders it.
const modelCatalogAsset = "../portal/public/model-catalog.json"

// TestModelCatalogAssetMatchesTheCompiledCatalog is what keeps the two copies
// honest. Cost attribution on a run uses llm.Catalog(); the Models tab prices
// the same models from the asset. If they drift, a user is quoted one price and
// billed against another.
//
// Regenerate with:
//
//	go run ./internal/gencatalog portal/public/model-catalog.json
//
// or by hand from llm.Catalog() — the encoding is json.MarshalIndent with two
// spaces and a trailing newline.
func TestModelCatalogAssetMatchesTheCompiledCatalog(t *testing.T) {
	onDisk, err := os.ReadFile(modelCatalogAsset)
	if err != nil {
		t.Fatalf("the portal's model catalog asset is missing: %v", err)
	}
	want, err := json.MarshalIndent(llm.Catalog(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != string(want)+"\n" {
		t.Errorf("%s is stale: it no longer matches llm.Catalog(). Regenerate it.", modelCatalogAsset)
	}
}
