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
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/faroshq/provider-app-studio/workspace"
)

func TestParseSkillBoundsAndRejectsAuthorityFields(t *testing.T) {
	valid := []byte("---\nname: demo\ndescription: A demo skill\ncontext: \"\"\n---\n\n  body\n\n")
	parsed, err := ParseSkill(valid, Limits{})
	if err != nil {
		t.Fatalf("ParseSkill(valid) error = %v", err)
	}
	if parsed.Name != "demo" || parsed.Description != "A demo skill" || parsed.Content != "\n  body\n\n" {
		t.Fatalf("parsed = %#v", parsed)
	}
	for _, field := range []string{"context: fork", "agent: worker", "model: gpt"} {
		t.Run(field, func(t *testing.T) {
			input := []byte("---\nname: demo\ndescription: A demo skill\n" + field + "\n---\nbody")
			if _, err := ParseSkill(input, Limits{}); err == nil || !strings.Contains(err.Error(), "authority-bearing") {
				t.Fatalf("ParseSkill(%q) error = %v, want authority rejection", field, err)
			}
		})
	}
	if _, err := ParseSkill([]byte("---\nname: demo\n---\nbody"), Limits{}); err == nil {
		t.Fatal("missing description accepted")
	}
	if _, err := ParseSkill(append([]byte("---\nname: demo\ndescription: too large\n---\n"), make([]byte, DefaultMaxSkillBytes)...), Limits{}); err == nil {
		t.Fatal("oversized skill accepted")
	}
	for _, description := range []string{`description: "line\nnext"`, "description: |\n  line\n", `description: "line\tvalue"`} {
		t.Run(description, func(t *testing.T) {
			input := []byte("---\nname: demo\n" + description + "\n---\nbody")
			if _, err := ParseSkill(input, Limits{}); err == nil || !strings.Contains(err.Error(), "single line") {
				t.Fatalf("ParseSkill(%q) error = %v, want single-line description rejection", description, err)
			}
		})
	}
}

func TestCatalogQualifiedNamesAndAmbiguousLookup(t *testing.T) {
	system := newMemorySource(ScopeSystem, Package{Path: "one", SkillContent: []byte("---\nname: same\ndescription: system\n---\nsystem")})
	project := newMemorySource(ScopeProject, Package{Path: "two", SkillContent: []byte("---\nname: same\ndescription: project\n---\nproject")})
	catalog, err := NewCatalog(CatalogOptions{Sources: []Source{project, system}})
	if err != nil {
		t.Fatalf("NewCatalog error = %v", err)
	}
	snapshot, err := catalog.Load(context.Background())
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if got := []string{snapshot.Entries[0].QualifiedName, snapshot.Entries[1].QualifiedName}; !slices.Equal(got, []string{"system:same", "project:same"}) {
		t.Fatalf("qualified names = %v", got)
	}
	if _, ok := snapshot.Find("same"); ok {
		t.Fatal("ambiguous unqualified lookup unexpectedly succeeded")
	}
	if entry, ok := snapshot.Find("project:same"); !ok || entry.Scope != ScopeProject {
		t.Fatalf("qualified project lookup = %#v, %v", entry, ok)
	}
	if snapshot.CatalogDigest == "" || snapshot.ContentDigest == "" {
		t.Fatalf("snapshot digests are empty: %#v", snapshot)
	}

	systemOnly, err := Build(context.Background(), CatalogOptions{Sources: []Source{system}})
	if err != nil {
		t.Fatalf("Build(system) error = %v", err)
	}
	if got := systemOnly.Entries[0].QualifiedName; got != "system:same" {
		t.Fatalf("stable qualified name = %q", got)
	}
}

func TestCatalogQualifiedNamesAreInjectiveForAdversarialDuplicatePaths(t *testing.T) {
	source := newMemorySource(ScopeSystem,
		Package{Path: "foo", SkillContent: []byte("---\nname: dup\ndescription: first\n---\nfirst")},
		Package{Path: "foo", SkillContent: []byte("---\nname: dup\ndescription: second\n---\nsecond")},
		Package{Path: "foo#2", SkillContent: []byte("---\nname: dup\ndescription: suffix\n---\nsuffix")},
		Package{Path: "other", SkillContent: []byte("---\nname: dup@3=foo\ndescription: crafted generated-name collision\n---\ncrafted")},
	)
	snapshot, err := Build(context.Background(), CatalogOptions{Sources: []Source{source}})
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	if len(snapshot.Entries) != 4 {
		t.Fatalf("entries = %#v", snapshot.Entries)
	}
	seen := make(map[string]struct{}, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if !strings.Contains(entry.QualifiedName, ":") {
			t.Fatalf("qualified name %q is not accepted by the turn request contract", entry.QualifiedName)
		}
		if _, exists := seen[entry.QualifiedName]; exists {
			t.Fatalf("duplicate qualified name %q in %#v", entry.QualifiedName, snapshot.Entries)
		}
		seen[entry.QualifiedName] = struct{}{}
		if found, ok := snapshot.Find(entry.QualifiedName); !ok || found.PackagePath != entry.PackagePath || found.Description != entry.Description {
			t.Fatalf("qualified lookup %q = %#v, %v", entry.QualifiedName, found, ok)
		}
	}
}

func TestCatalogAliasesNeverShadowCanonicalQualifiedNames(t *testing.T) {
	source := newMemorySource(ScopeSystem,
		Package{Path: "canonical", SkillContent: []byte("---\nname: x\ndescription: canonical target\n---\ncanonical")},
		Package{Path: "alias", SkillContent: []byte("---\nname: system:x\ndescription: crafted alias collision\n---\nalias")},
	)
	snapshot, err := Build(context.Background(), CatalogOptions{Sources: []Source{source}})
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	canonical, ok := snapshot.Find("system:x")
	if !ok || canonical.Name != "x" || canonical.PackagePath != "canonical" {
		t.Fatalf("canonical qualified lookup was shadowed: %#v, %v", canonical, ok)
	}
	alias, ok := snapshot.Find("system:system:x")
	if !ok || alias.Name != "system:x" || alias.PackagePath != "alias" {
		t.Fatalf("crafted-name qualified lookup = %#v, %v", alias, ok)
	}
}

func TestSnapshotResourcePaginationIsBoundedAndImmutable(t *testing.T) {
	source := newMemorySource(ScopeSystem, Package{
		Path:         "demo",
		SkillContent: []byte("---\nname: demo\ndescription: resource test\n---\nbody"),
		Resources:    []ResourceRef{{Path: "notes.txt", Size: 6}},
	})
	source.resources["demo/notes.txt"] = []byte("abcdef")
	snapshot, err := Build(context.Background(), CatalogOptions{Sources: []Source{source}, Limits: Limits{MaxResourceRead: 3}})
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	page, err := snapshot.ReadResource(context.Background(), "demo", "notes.txt", ResourceReadOptions{Offset: 2, Limit: 3})
	if err != nil {
		t.Fatalf("ReadResource error = %v", err)
	}
	if string(page.Content) != "cde" || !page.Truncated || page.NextOffset != 5 {
		t.Fatalf("page = %#v", page)
	}
	page.Content[0] = 'X'
	again, err := snapshot.ReadResource(context.Background(), "demo", "notes.txt", ResourceReadOptions{})
	if err != nil || string(again.Content) != "abc" {
		t.Fatalf("immutable resource = %#v, %v", again, err)
	}
	for _, path := range []string{"../secret", "/etc/passwd", "notes.txt/../secret"} {
		if _, err := snapshot.ReadResource(context.Background(), "demo", path, ResourceReadOptions{}); err == nil {
			t.Fatalf("ReadResource(%q) unexpectedly succeeded", path)
		}
	}
}

func TestCatalogBoundsAggregateResourceMaterialization(t *testing.T) {
	source := newMemorySource(ScopeSystem, Package{
		Path:         "demo",
		SkillContent: []byte("---\nname: demo\ndescription: aggregate bound\n---\nbody"),
		Resources:    []ResourceRef{{Path: "large.txt", Size: 512}},
	})
	source.resources["demo/large.txt"] = []byte(strings.Repeat("x", 512))
	snapshot, err := Build(context.Background(), CatalogOptions{
		Sources: []Source{source},
		Limits:  Limits{MaxAggregateBytes: 256, MaxResourceBytes: 1024},
	})
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	if len(snapshot.Entries) != 1 {
		t.Fatalf("entries = %#v, want bounded skill metadata", snapshot.Entries)
	}
	if _, err := snapshot.ReadResource(context.Background(), "system:demo", "large.txt", ResourceReadOptions{}); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("oversized aggregate resource error = %v, want ErrResourceNotFound", err)
	}
	if len(snapshot.Warnings) == 0 || snapshot.Warnings[0].Code != "resource_aggregate_limit" {
		t.Fatalf("warnings = %#v, want resource aggregate warning", snapshot.Warnings)
	}
}

func TestProjectSourceUsesScopedFileStoreAndListsResources(t *testing.T) {
	store := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	if err := store.ApplyFiles(context.Background(), scope, []workspace.File{
		{Path: "skills/demo/SKILL.md", Content: "---\nname: demo\ndescription: project skill\n---\nbody"},
		{Path: "skills/demo/scripts/check.sh", Content: "echo ok\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles error = %v", err)
	}
	source, err := NewProjectSourceWithRoot(store, scope, "skills")
	if err != nil {
		t.Fatalf("NewProjectSource error = %v", err)
	}
	list, err := source.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(list.Packages) != 1 || len(list.Packages[0].Resources) != 1 || list.Packages[0].Resources[0].Path != "scripts/check.sh" {
		t.Fatalf("project packages = %#v", list.Packages)
	}
	catalog, err := NewCatalog(CatalogOptions{Sources: []Source{source}})
	if err != nil {
		t.Fatalf("NewCatalog error = %v", err)
	}
	snapshot, err := catalog.Load(context.Background())
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	resource, err := snapshot.ReadResource(context.Background(), "demo", "scripts/check.sh", ResourceReadOptions{})
	if err != nil || string(resource.Content) != "echo ok\n" {
		t.Fatalf("project resource = %#v, %v", resource, err)
	}
	if _, err := source.ReadResource(context.Background(), "demo", "../outside", ResourceReadOptions{}); err == nil {
		t.Fatal("project source accepted resource traversal")
	}
}

type memorySource struct {
	scope     Scope
	packages  []Package
	resources map[string][]byte
}

func newMemorySource(scope Scope, packages ...Package) *memorySource {
	return &memorySource{scope: scope, packages: packages, resources: make(map[string][]byte)}
}

func (s *memorySource) Scope() Scope { return s.scope }

func (s *memorySource) List(context.Context, int) (PackageList, error) {
	packages := make([]Package, len(s.packages))
	copy(packages, s.packages)
	return PackageList{Packages: packages}, nil
}

func (s *memorySource) ReadResource(_ context.Context, packagePath, resourcePath string, opts ResourceReadOptions) (ResourceReadResult, error) {
	data, ok := s.resources[packagePath+"/"+resourcePath]
	if !ok {
		return ResourceReadResult{}, ErrResourceNotFound
	}
	if opts.Limit <= 0 {
		opts.Limit = len(data)
	}
	if opts.Offset < 0 || opts.Offset > int64(len(data)) {
		return ResourceReadResult{}, errors.New("bad offset")
	}
	end := opts.Offset + int64(opts.Limit)
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return ResourceReadResult{Path: resourcePath, Content: append([]byte(nil), data[opts.Offset:end]...), Size: int64(len(data)), Offset: opts.Offset, Truncated: end < int64(len(data)), NextOffset: end}, nil
}
