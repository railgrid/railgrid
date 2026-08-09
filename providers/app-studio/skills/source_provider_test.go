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

package skills

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestProviderSkillSourceQualifiesPackagesAndKeepsInvalidSiblingsBounded(t *testing.T) {
	valid := ProviderSkillPackage{
		ProviderName: "databricks",
		PackageName:  "databricks-app-integration",
		Version:      "1.0.0",
		Skill:        "---\nname: databricks-app-integration\ndescription: bounded integration\n---\nUse the bound table.\n",
		Resources:    []ProviderSkillResource{{Path: "references/action-contract.md", Content: "contract\n"}},
	}
	digest, err := ProviderSkillPackageDigest(valid)
	if err != nil {
		t.Fatalf("ProviderSkillPackageDigest() error = %v", err)
	}
	valid.Digest = digest
	invalid := ProviderSkillPackage{
		ProviderName: "databricks",
		PackageName:  "broken",
		Version:      "1.0.0",
		Digest:       "sha256:" + strings.Repeat("0", 64),
		Skill:        valid.Skill,
	}
	source, err := NewProviderSkillSource([]ProviderSkillPackage{invalid, valid})
	if err != nil {
		t.Fatalf("NewProviderSkillSource() error = %v", err)
	}
	if source.Scope() != ScopeSystem {
		t.Fatalf("source scope = %q, want %q", source.Scope(), ScopeSystem)
	}
	list, err := source.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("source.List() error = %v", err)
	}
	if len(list.Packages) != 1 || list.Packages[0].Path != "providers/databricks/databricks-app-integration" {
		t.Fatalf("packages = %#v, want one provider-qualified package", list.Packages)
	}
	if list.Packages[0].Digest != digest || list.Packages[0].Version != "1.0.0" {
		t.Fatalf("package provenance = %#v, want digest/version", list.Packages[0])
	}
	if len(list.Warnings) != 1 || list.Warnings[0].Code != "provider_skill_invalid" || list.Warnings[0].PackagePath != "providers/databricks/broken" {
		t.Fatalf("warnings = %#v, want one bounded invalid-package warning", list.Warnings)
	}
	resource, err := source.ReadResource(context.Background(), list.Packages[0].Path, "references/action-contract.md", ResourceReadOptions{})
	if err != nil || string(resource.Content) != "contract\n" {
		t.Fatalf("resource = %#v, err = %v", resource, err)
	}

	snapshot, err := Build(context.Background(), CatalogOptions{Sources: []Source{source}})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	entry, ok := snapshot.Find("system:databricks-app-integration")
	if !ok {
		t.Fatalf("provider skill entry missing: %#v", snapshot.Entries)
	}
	if entry.PackagePath != "providers/databricks/databricks-app-integration" || entry.Digest != digest || entry.Editable || !entry.Enabled {
		t.Fatalf("provider entry = %#v, want immutable enabled system package", entry)
	}
}

func TestProviderSkillSourceRetainsValidPackagesAcrossProviders(t *testing.T) {
	// Each package is valid under its per-package 4 MiB bound, and the catalog
	// itself allows 4 MiB across entries. A source-local 512 KiB aggregate cap
	// must not make a later provider package disappear from the catalog.
	resourceContent := strings.Repeat("x", ProviderSkillSourceMaxProviderAggregateBytes/2-64)
	makePackage := func(providerName string) ProviderSkillPackage {
		packageValue := ProviderSkillPackage{
			ProviderName: providerName,
			PackageName:  "integration",
			Version:      "1.0.0",
			Skill:        "---\nname: " + providerName + "-integration\ndescription: bounded integration\n---\nUse the bound table.\n",
			Resources:    []ProviderSkillResource{{Path: "references/action-contract.md", Content: resourceContent}},
		}
		digest, err := ProviderSkillPackageDigest(packageValue)
		if err != nil {
			t.Fatalf("ProviderSkillPackageDigest(%q) error = %v", providerName, err)
		}
		packageValue.Digest = digest
		return packageValue
	}

	source, err := NewProviderSkillSource([]ProviderSkillPackage{makePackage("alpha"), makePackage("beta")})
	if err != nil {
		t.Fatalf("NewProviderSkillSource() error = %v", err)
	}
	list, err := source.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("source.List() error = %v", err)
	}
	if len(list.Packages) != 2 {
		t.Fatalf("packages = %d (%#v), want both valid provider packages; warnings = %#v", len(list.Packages), list.Packages, list.Warnings)
	}
	if len(list.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want no invalid-package warning for valid packages", list.Warnings)
	}
}

func TestProviderSkillSourceEnforcesFinalGlobalAggregateBound(t *testing.T) {
	// Each provider stays below the 512 KiB publication bound, but sixteen
	// providers together exceed the source's final 4 MiB flattened-catalog
	// bound. The source must keep valid earlier packages and reject only the
	// package that would cross the global limit.
	resourceContent := strings.Repeat("x", ProviderSkillSourceMaxProviderAggregateBytes/2-64)
	packages := make([]ProviderSkillPackage, 0, 16)
	for index := 0; index < 16; index++ {
		providerName := fmt.Sprintf("provider-%02d", index)
		packageValue := ProviderSkillPackage{
			ProviderName: providerName,
			PackageName:  "integration",
			Version:      "1.0.0",
			Skill:        "---\nname: " + providerName + "-integration\ndescription: bounded integration\n---\nUse the bound table.\n",
			Resources:    []ProviderSkillResource{{Path: "references/action-contract.md", Content: resourceContent}},
		}
		digest, err := ProviderSkillPackageDigest(packageValue)
		if err != nil {
			t.Fatalf("ProviderSkillPackageDigest(%q) error = %v", providerName, err)
		}
		packageValue.Digest = digest
		packages = append(packages, packageValue)
	}

	source, err := NewProviderSkillSource(packages)
	if err != nil {
		t.Fatalf("NewProviderSkillSource() error = %v", err)
	}
	list, err := source.List(context.Background(), 32)
	if err != nil {
		t.Fatalf("source.List() error = %v", err)
	}
	if len(list.Packages) != 15 {
		t.Fatalf("packages = %d, want 15 packages before the global bound; warnings = %#v", len(list.Packages), list.Warnings)
	}
	if len(list.Warnings) != 1 || list.Warnings[0].Code != "provider_skill_invalid" || list.Warnings[0].PackagePath != "providers/provider-15/integration" {
		t.Fatalf("warnings = %#v, want one bounded global-aggregate warning for provider-15", list.Warnings)
	}
}
