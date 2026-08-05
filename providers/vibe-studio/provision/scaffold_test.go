// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package provision

import "testing"

func TestScaffoldKeepsBuildWorkflow(t *testing.T) {
	// Every scaffold ships CI that pushes one image per component tagged
	// sha-<commit>; dropping it left seeded projects with no builds and
	// promotion with no digests to pin.
	keep := []string{
		".github/workflows/build.yaml",
		".gitignore",
		"AGENTS.md",
		"web/package.json",
		"api/index.js",
	}
	for _, p := range keep {
		if scaffoldSkippedPath(p) {
			t.Errorf("%s was skipped; it belongs in the seeded project", p)
		}
	}
	drop := []string{"LICENSE", "README.md", ".git/config", ".git/objects/ab/cdef"}
	for _, p := range drop {
		if !scaffoldSkippedPath(p) {
			t.Errorf("%s was kept; it belongs to the scaffold repository, not the project", p)
		}
	}
}
