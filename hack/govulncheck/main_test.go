/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowlistFields is one complete entry, in the order the header documents
// them. Every one is required.
var allowlistFields = [][2]string{
	{"id", "GO-2026-0001"},
	{"module", "example.com/dep"},
	{"reason", "no upstream fix"},
	{"exposure", "the vulnerable symbol is only reachable from a test helper"},
	{"reviewBy", "2026-12-01"},
}

// renderAllowlist writes a one-entry allowlist with the named field omitted
// ("" omits nothing) and returns its path.
func renderAllowlist(t *testing.T, omit string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("allow:\n")
	first := true
	for _, f := range allowlistFields {
		if f[0] == omit {
			continue
		}
		prefix := "    "
		if first {
			prefix, first = "  - ", false
		}
		b.WriteString(prefix + f[0] + ": " + f[1] + "\n")
	}
	p := filepath.Join(t.TempDir(), "allow.yaml")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadAllowlistRequiresEveryDocumentedField pins the contract the
// allowlist header states: an entry without one of id / module / reason /
// exposure / reviewBy is rejected, so a suppression never lands without the
// faros-specific justification the review depends on.
func TestLoadAllowlistRequiresEveryDocumentedField(t *testing.T) {
	entries, order, err := loadAllowlist(renderAllowlist(t, ""))
	if err != nil {
		t.Fatalf("complete entry rejected: %v", err)
	}
	if len(entries) != 1 || len(order) != 1 || order[0] != "GO-2026-0001" {
		t.Fatalf("entries = %v order = %v", entries, order)
	}

	for _, f := range allowlistFields {
		field := f[0]
		t.Run("missing "+field, func(t *testing.T) {
			_, _, err := loadAllowlist(renderAllowlist(t, field))
			if err == nil {
				t.Fatalf("entry without %s was accepted", field)
			}
			if !strings.Contains(err.Error(), "`"+field+"`") {
				t.Fatalf("error does not name the missing field %s: %v", field, err)
			}
		})
	}
}
