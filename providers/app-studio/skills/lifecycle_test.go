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
	"io/fs"
	"testing"

	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectActivationPersistsAndDisablesSnapshotEntries(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: ".agents/skills/demo/SKILL.md", Content: "---\nname: demo\ndescription: Demo skill\n---\nbody"}}); err != nil {
		t.Fatal(err)
	}
	source, err := NewProjectSource(files, scope)
	if err != nil {
		t.Fatal(err)
	}
	list, err := source.List(ctx, 10)
	if err != nil || len(list.Packages) != 1 || !list.Packages[0].Enabled {
		t.Fatalf("default activation = %#v, %v", list, err)
	}
	metadata, version, err := ReadProjectMetadata(ctx, files, scope)
	if err != nil || version != "" {
		t.Fatalf("initial metadata = %#v version=%q err=%v", metadata, version, err)
	}
	metadata.Packages["demo"] = Activation{Enabled: false, Version: "sha256:version", Digest: "sha256:digest"}
	if _, err := WriteProjectMetadata(ctx, files, scope, metadata, version); err != nil {
		t.Fatal(err)
	}
	list, err = source.List(ctx, 10)
	if err != nil || len(list.Packages) != 1 || list.Packages[0].Enabled {
		t.Fatalf("disabled activation = %#v, %v", list, err)
	}
	catalog, err := NewCatalog(CatalogOptions{Sources: []Source{source}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Load(ctx)
	if err != nil || len(snapshot.Entries) != 1 || snapshot.Entries[0].Enabled {
		t.Fatalf("disabled catalog entry = %#v, %v", snapshot.Entries, err)
	}
	if enabled := snapshot.EnabledOnly(); len(enabled.Entries) != 0 {
		t.Fatalf("disabled entry survived EnabledOnly: %#v", enabled.Entries)
	}
	if _, _, err := ReadProjectMetadata(ctx, files, scope); err != nil {
		t.Fatal("metadata did not survive reload: ", err)
	}
}

func TestProjectMetadataRejectsTraversalAndStaleReplacement(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	metadata := defaultProjectMetadata()
	metadata.Packages["../escape"] = Activation{Enabled: true}
	if _, err := WriteProjectMetadata(ctx, files, scope, metadata, ""); err == nil {
		t.Fatal("metadata traversal unexpectedly accepted")
	}
	metadata = defaultProjectMetadata()
	if _, err := WriteProjectMetadata(ctx, files, scope, metadata, ""); err != nil {
		t.Fatal(err)
	}
	current, version, err := ReadProjectMetadata(ctx, files, scope)
	if err != nil || version == "" {
		t.Fatalf("read metadata = %#v version=%q err=%v", current, version, err)
	}
	if _, err := WriteProjectMetadata(ctx, files, scope, current, "sha256:stale"); err == nil {
		t.Fatal("stale metadata replacement unexpectedly succeeded")
	}
	if _, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: ".agents/skills/.kedge-catalog.json"}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestSystemActivationOverlayIsBoundedScopedAndDigestVisible(t *testing.T) {
	ctx := context.Background()
	system := newMemorySource(ScopeSystem, Package{Path: "bundle", SkillContent: []byte("---\nname: bundle\ndescription: bundled guidance\n---\nbody")})
	project := newMemorySource(ScopeProject, Package{Path: "bundle", SkillContent: []byte("---\nname: bundle\ndescription: project guidance\n---\nproject body")})
	initial, err := Build(ctx, CatalogOptions{Sources: []Source{system, project}})
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := NewActivationSource(system, map[string]Activation{"bundle": {Enabled: false}}, false)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := Build(ctx, CatalogOptions{Sources: []Source{overlay, project}})
	if err != nil {
		t.Fatal(err)
	}
	systemEntry, err := updated.Get("system:bundle")
	if err != nil || systemEntry.Enabled {
		t.Fatalf("system overlay entry = %#v, err=%v", systemEntry, err)
	}
	projectEntry, err := updated.Get("project:bundle")
	if err != nil || !projectEntry.Enabled {
		t.Fatalf("project entry changed with system overlay = %#v, err=%v", projectEntry, err)
	}
	if initial.CatalogDigest == updated.CatalogDigest {
		t.Fatal("activation overlay did not change catalog digest")
	}
	if enabled := updated.EnabledOnly(); len(enabled.Entries) != 1 || enabled.Entries[0].Scope != ScopeProject {
		t.Fatalf("enabled-only snapshot = %#v", enabled.Entries)
	}
	if _, err := NewActivationSource(system, map[string]Activation{"../escape": {Enabled: false}}, false); err == nil {
		t.Fatal("traversal activation key accepted")
	}
	if _, err := NewActivationSource(project, nil, false); err == nil {
		t.Fatal("project source accepted as a system activation source")
	}
	stale, err := NewActivationSource(system, map[string]Activation{"bundle": {Enabled: true, Version: "sha256:stale"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	staleSnapshot, err := Build(ctx, CatalogOptions{Sources: []Source{stale}})
	if err != nil {
		t.Fatal(err)
	}
	if entry, err := staleSnapshot.Get("system:bundle"); err != nil || entry.Enabled {
		t.Fatalf("stale activation was not fail-closed: %#v, err=%v", entry, err)
	}
}

func TestProjectSourceListsSupportingResourcesAfterCreate(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{
		{Path: ".agents/skills/review/custom/SKILL.md", Content: "---\nname: custom\ndescription: custom\n---\nbody"},
		{Path: ".agents/skills/review/custom/notes.txt", Content: "resource"},
	}); err != nil {
		t.Fatal(err)
	}
	source, err := NewProjectSource(files, scope)
	if err != nil {
		t.Fatal(err)
	}
	list, err := source.List(ctx, 10)
	if err != nil || len(list.Packages) != 1 || len(list.Packages[0].Resources) != 1 || list.Packages[0].Resources[0].Path != "notes.txt" {
		t.Fatalf("project resources = %#v, err=%v", list, err)
	}
}
