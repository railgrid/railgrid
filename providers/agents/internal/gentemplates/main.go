// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Command gentemplates writes the agent template catalog to the portal bundle
// and the instantiation parity fixtures to the portal's test tree.
//
//	go run ./internal/gentemplates portal/public/agent-templates.json portal/src/test/fixtures/agent-templates
//
// The catalog is shipped content with no tenant data in it, so it is a bundle
// asset rather than a backend route, exactly like model-catalog.json. The
// fixtures hold the TypeScript instantiator (portal/src/agent-template.ts) to
// the Go one's output. templates.TestAssetsAreFresh fails when either copy
// drifts from the YAML under templates/builtin/.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/railgrid/provider-agents/templates"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: gentemplates <agent-templates.json> <fixtures-dir>")
		os.Exit(2)
	}
	catalog, err := templates.CatalogJSON()
	if err != nil {
		fail("encoding the catalog:", err)
	}
	if err := os.WriteFile(os.Args[1], catalog, 0o600); err != nil {
		fail("writing "+os.Args[1]+":", err)
	}
	if err := os.MkdirAll(os.Args[2], 0o750); err != nil {
		fail("creating "+os.Args[2]+":", err)
	}
	// Remove fixtures for templates that no longer exist, so a renamed template
	// does not leave a stale parity case behind.
	entries, err := os.ReadDir(os.Args[2])
	if err != nil {
		fail("reading "+os.Args[2]+":", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			if _, ok := templates.Lookup(e.Name()[:len(e.Name())-len(".json")]); !ok {
				if err := os.Remove(filepath.Join(os.Args[2], e.Name())); err != nil {
					fail("removing stale fixture:", err)
				}
			}
		}
	}
	for _, t := range templates.Builtin() {
		fixture, err := templates.FixtureJSON(t)
		if err != nil {
			fail("rendering fixture:", err)
		}
		path := filepath.Join(os.Args[2], t.Metadata.Name+".json")
		if err := os.WriteFile(path, fixture, 0o600); err != nil {
			fail("writing "+path+":", err)
		}
	}
}

func fail(msg string, err error) {
	fmt.Fprintln(os.Stderr, msg, err)
	os.Exit(1)
}
