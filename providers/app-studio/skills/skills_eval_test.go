// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package skills

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEvaluationCatalogBudgetsBoundPackagesResourcesAndPages(t *testing.T) {
	ctx := context.Background()
	source := newMemorySource(ScopeProject,
		Package{
			Path:         "first",
			SkillContent: []byte("---\nname: first\ndescription: first skill\n---\nfirst body"),
			Resources:    []ResourceRef{{Path: "large.txt", Size: 8}, {Path: "second.txt", Size: 3}},
		},
		Package{Path: "second", SkillContent: []byte("---\nname: second\ndescription: second skill\n---\nsecond body")},
		Package{Path: "third", SkillContent: []byte("---\nname: third\ndescription: third skill\n---\nthird body")},
	)
	source.resources["first/large.txt"] = []byte("12345678")
	source.resources["first/second.txt"] = []byte("xyz")

	snapshot, err := Build(ctx, CatalogOptions{Sources: []Source{source}, Limits: Limits{
		MaxPackages:      2,
		MaxResourceCount: 1,
		MaxResourceBytes: 4,
		MaxResourceRead:  2,
	}})
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	if len(snapshot.Entries) != 2 {
		t.Fatalf("package budget produced %d entries, want 2: %#v", len(snapshot.Entries), snapshot.Entries)
	}
	for _, entry := range snapshot.Entries {
		if len(entry.Resources) > 1 {
			t.Fatalf("resource count budget exceeded for %q: %#v", entry.QualifiedName, entry.Resources)
		}
	}
	if len(snapshot.Warnings) == 0 {
		t.Fatal("bounded resource was silently accepted without a warning")
	}
	if _, err := snapshot.ReadResource(ctx, "project:first", "large.txt", ResourceReadOptions{}); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("over-sized resource became readable: %v", err)
	}

	// A separate catalog keeps the materialized resource below the catalog
	// bound while verifying that every page is bounded independently.
	pageSource := newMemorySource(ScopeProject, Package{
		Path:         "paged",
		SkillContent: []byte("---\nname: paged\ndescription: paged skill\n---\nbody"),
		Resources:    []ResourceRef{{Path: "notes.txt", Size: 6}},
	})
	pageSource.resources["paged/notes.txt"] = []byte("abcdef")
	pageSnapshot, err := Build(ctx, CatalogOptions{Sources: []Source{pageSource}, Limits: Limits{MaxResourceRead: 2}})
	if err != nil {
		t.Fatalf("paged Build error = %v", err)
	}
	page, err := pageSnapshot.ReadResource(ctx, "project:paged", "notes.txt", ResourceReadOptions{})
	if err != nil {
		t.Fatalf("bounded resource page error = %v", err)
	}
	if string(page.Content) != "ab" || !page.Truncated || page.NextOffset != 2 {
		t.Fatalf("bounded resource page = %#v, want first two bytes and continuation", page)
	}
}

func TestEvaluationDisabledEntriesRemainVisibleButNeverEnabled(t *testing.T) {
	source := newMemorySource(ScopeProject,
		Package{Path: "disabled", Enabled: false, EnabledSet: true, SkillContent: []byte("---\nname: disabled\ndescription: disabled guidance\n---\nDO NOT LOAD"), Resources: []ResourceRef{{Path: "notes.txt", Size: 4}}},
		Package{Path: "enabled", SkillContent: []byte("---\nname: enabled\ndescription: enabled guidance\n---\nLOAD ME")},
	)
	source.resources["disabled/notes.txt"] = []byte("deny")
	snapshot, err := Build(context.Background(), CatalogOptions{Sources: []Source{source}})
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	disabled, ok := snapshot.Find("project:disabled")
	if !ok || disabled.Enabled {
		t.Fatalf("disabled entry = %#v, found=%v", disabled, ok)
	}
	if enabled := snapshot.EnabledOnly(); len(enabled.Entries) != 1 || enabled.Entries[0].Name != "enabled" {
		t.Fatalf("EnabledOnly = %#v, want only enabled entry", enabled.Entries)
	}
	if _, err := snapshot.ReadResource(context.Background(), "project:disabled", "notes.txt", ResourceReadOptions{}); err != nil {
		t.Fatalf("management snapshot lost disabled resource metadata/readability: %v", err)
	}
}

func TestEvaluationCanonicalIDsWinOverConvenienceAliases(t *testing.T) {
	source := newMemorySource(ScopeSystem,
		Package{Path: "canonical", SkillContent: []byte("---\nname: demo\ndescription: canonical\n---\ncanonical body")},
		Package{Path: "crafted", SkillContent: []byte("---\nname: system:demo\ndescription: crafted alias\n---\ncrafted body")},
		Package{Path: "duplicate-a", SkillContent: []byte("---\nname: duplicate\ndescription: a\n---\na")},
		Package{Path: "duplicate-b", SkillContent: []byte("---\nname: duplicate\ndescription: b\n---\nb")},
	)
	snapshot, err := Build(context.Background(), CatalogOptions{Sources: []Source{source}})
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	canonical, ok := snapshot.Find("system:demo")
	if !ok || canonical.Name != "demo" || canonical.PackagePath != "canonical" {
		t.Fatalf("canonical ID was shadowed by crafted name: %#v, found=%v", canonical, ok)
	}
	crafted, ok := snapshot.Find("system:system:demo")
	if !ok || crafted.Name != "system:demo" || crafted.PackagePath != "crafted" {
		t.Fatalf("crafted name did not receive its own canonical ID: %#v, found=%v", crafted, ok)
	}
	if _, ok := snapshot.Find("duplicate"); ok {
		t.Fatal("ambiguous unqualified duplicate alias unexpectedly resolved")
	}
	for _, entry := range snapshot.Entries {
		if entry.Name != "duplicate" {
			continue
		}
		if strings.TrimSpace(entry.QualifiedName) == "" {
			t.Fatalf("duplicate entry has empty canonical ID: %#v", entry)
		}
		found, foundOK := snapshot.Find(entry.QualifiedName)
		if !foundOK || found.PackagePath != entry.PackagePath {
			t.Fatalf("canonical duplicate lookup %q = %#v, found=%v", entry.QualifiedName, found, foundOK)
		}
	}
}
