// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"os"
	"regexp"
	"testing"
)

func TestInitPermissionClaimsMatchPublishedCopies(t *testing.T) {
	if len(permissionClaims) != 1 || permissionClaims[0].Resource != "secrets" || len(permissionClaims[0].Verbs) != 1 || permissionClaims[0].Verbs[0] != "get" {
		t.Fatalf("init permission claims = %#v, want tenant-scoped secrets get", permissionClaims)
	}

	claim := regexp.MustCompile(`(?m)^\s*-\s*resource:\s*secrets\s*$[\s\S]*?^\s*verbs:\s*\[get\]\s*$[\s\S]*?^\s*tenantScoped:\s*true\s*$`)
	for _, path := range []string{
		"manifest.yaml",
		"deploy/chart/templates/catalogentry.yaml",
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !claim.Match(contents) {
			t.Errorf("%s does not publish the init permission claim for tenant-scoped secrets get", path)
		}
	}
}
