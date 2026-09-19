// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Command gencatalog writes the curated model catalog to the portal bundle.
//
//	go run ./internal/gencatalog portal/public/model-catalog.json
//
// The catalog is reference data — prices, context windows, capabilities — with
// no tenant content in it, so it is a bundle asset rather than a backend route.
// api.TestModelCatalogAssetMatchesTheCompiledCatalog fails when the two copies
// drift, which is the only way a user gets quoted one price and billed at
// another.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/railgrid/provider-agents/llm"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gencatalog <output.json>")
		os.Exit(2)
	}
	encoded, err := json.MarshalIndent(llm.Catalog(), "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "encoding the catalog:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[1], append(encoded, '\n'), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "writing", os.Args[1]+":", err)
		os.Exit(1)
	}
}
