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

package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type migrationSnapshotEntry struct {
	Path         string `json:"path"`
	Existed      bool   `json:"existed"`
	Content      []byte `json:"content,omitempty"`
	Mode         uint32 `json:"mode,omitempty"`
	AfterExisted bool   `json:"afterExisted"`
	After        []byte `json:"after,omitempty"`
	AfterMode    uint32 `json:"afterMode,omitempty"`
}

func TestFileStoreMigratesLegacyWorkspaceStateAndSnapshotsOnce(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	legacyScope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	legacyWorkspace := filepath.Join(root, legacyScope.OrgUUID, legacyScope.WorkspaceUUID, legacyScope.ProjectName)
	if err := os.MkdirAll(filepath.Join(legacyWorkspace, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyWorkspace, "src", "App.tsx"), []byte("legacy source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyWorkspace, "package.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	legacySnapshots := filepath.Join(root, workspaceSnapshotDirectory, legacyScope.OrgUUID, legacyScope.WorkspaceUUID, legacyScope.ProjectName)
	if err := os.MkdirAll(filepath.Join(legacySnapshots, "run-legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := migrationSnapshotEntry{
		Path:         "src/App.tsx",
		Existed:      true,
		Content:      []byte("legacy source\n"),
		AfterExisted: true,
		After:        []byte("changed source\n"),
		Mode:         0o600,
		AfterMode:    0o600,
	}
	entryRaw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	entryName := filepath.Join(legacySnapshots, "run-legacy", "entry.json")
	if err := os.WriteFile(entryName, entryRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	stateRaw := []byte(`{"uncommittedPaths":["src/App.tsx"]}`)
	if err := os.WriteFile(filepath.Join(legacySnapshots, workspaceSourceStateFile), stateRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewFileStore(root)
	first := legacyScope
	first.ProjectUID = "project-first"
	read, err := store.ReadFile(ctx, first, ReadOptions{Path: "src/App.tsx"})
	if err != nil || read.Content != "legacy source\n" {
		t.Fatalf("migrated source = %#v, err=%v", read, err)
	}
	paths, err := store.UncommittedPaths(ctx, first)
	if err != nil || !reflect.DeepEqual(paths, []string{"src/App.tsx"}) {
		t.Fatalf("migrated source state = %v, err=%v", paths, err)
	}
	if _, err := store.WriteFile(ctx, first, WriteOptions{Path: "src/App.tsx", Content: "changed source\n"}); err != nil {
		t.Fatal(err)
	}
	read, err = store.ReadFile(ctx, first, ReadOptions{Path: "src/App.tsx"})
	if err != nil || read.Content != "changed source\n" {
		t.Fatalf("migrated source after live write = %#v, err=%v", read, err)
	}
	migratedSnapshot := filepath.Join(root, workspaceSnapshotDirectory, first.OrgUUID, first.WorkspaceUUID, first.ProjectName, first.ProjectUID, "run-legacy", "entry.json")
	if _, err := os.Stat(migratedSnapshot); err != nil {
		t.Fatalf("migrated snapshot data was not preserved: %v", err)
	}

	second := legacyScope
	second.ProjectUID = "project-second"
	if _, err := store.ReadFile(ctx, second, ReadOptions{Path: "src/App.tsx"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("recreated project inherited migrated source: %v", err)
	}
	paths, err = store.UncommittedPaths(ctx, second)
	if err != nil || len(paths) != 0 {
		t.Fatalf("recreated project inherited source state = %v, err=%v", paths, err)
	}
	secondSnapshot := filepath.Join(root, workspaceSnapshotDirectory, second.OrgUUID, second.WorkspaceUUID, second.ProjectName, second.ProjectUID, "run-legacy")
	if _, err := os.Stat(secondSnapshot); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("recreated project inherited migrated snapshot: %v", err)
	}

	legacySourcePath := filepath.Join(root, legacyScope.OrgUUID, legacyScope.WorkspaceUUID, legacyScope.ProjectName, "src", "App.tsx")
	if _, err := os.Stat(legacySourcePath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("legacy source path still visible after migration: %v", err)
	}
}

func TestFileStoreLegacyMigrationBindsSnapshotsToWorkspaceFirstUID(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	legacyScope := seedLegacyMigrationFixture(t, root)
	first := legacyScope
	first.ProjectUID = "project-first"
	second := legacyScope
	second.ProjectUID = "project-second"
	store := NewFileStore(root)

	if _, err := store.ReadFile(ctx, first, ReadOptions{Path: "src/App.tsx"}); err != nil {
		t.Fatalf("migrate workspace with first UID: %v", err)
	}
	paths, err := store.UncommittedPaths(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("UID2 claimed snapshots after workspace-first migration: %v", paths)
	}
	paths, err = store.UncommittedPaths(ctx, first)
	if err != nil || !reflect.DeepEqual(paths, []string{"src/App.tsx"}) {
		t.Fatalf("UID1 snapshots after workspace-first migration = %v, err=%v", paths, err)
	}
}

func TestFileStoreLegacyMigrationBindsWorkspaceToSnapshotsFirstUID(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	legacyScope := seedLegacyMigrationFixture(t, root)
	first := legacyScope
	first.ProjectUID = "project-first"
	second := legacyScope
	second.ProjectUID = "project-second"
	store := NewFileStore(root)

	paths, err := store.UncommittedPaths(ctx, first)
	if err != nil || !reflect.DeepEqual(paths, []string{"src/App.tsx"}) {
		t.Fatalf("migrate snapshots with first UID = %v, err=%v", paths, err)
	}
	if _, err := store.ReadFile(ctx, second, ReadOptions{Path: "src/App.tsx"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("UID2 claimed workspace after snapshots-first migration: %v", err)
	}
	read, err := store.ReadFile(ctx, first, ReadOptions{Path: "src/App.tsx"})
	if err != nil || read.Content != "legacy source\n" {
		t.Fatalf("UID1 workspace after snapshots-first migration = %#v, err=%v", read, err)
	}
}

func TestFileStoreDoesNotNestExistingProjectUIDTrees(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	baseScope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	oldUID := "95fc57ea-3962-47f0-a30c-9b6f80960776"
	siblingUID := "c2d2a4c1-d5a5-4f9f-9e15-91e7b1fcb5a4"
	newUID := "f20e046e-6b22-4f2e-a72c-d2a1f665c2c9"
	oldScope := baseScope
	oldScope.ProjectUID = oldUID
	siblingScope := baseScope
	siblingScope.ProjectUID = siblingUID
	newScope := baseScope
	newScope.ProjectUID = newUID

	workspaceBase := filepath.Join(root, baseScope.OrgUUID, baseScope.WorkspaceUUID, baseScope.ProjectName)
	oldWorkspacePath := filepath.Join(workspaceBase, oldUID, "src", "App.jsx")
	siblingWorkspacePath := filepath.Join(workspaceBase, siblingUID, "src", "Sibling.jsx")
	if err := os.MkdirAll(filepath.Dir(oldWorkspacePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(siblingWorkspacePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldWorkspacePath, []byte("old incarnation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(siblingWorkspacePath, []byte("sibling incarnation\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldSnapshotPath := writeMigrationSnapshot(t, root, oldScope, "run-old", migrationSnapshotEntry{
		Path:         "src/App.jsx",
		Existed:      true,
		Content:      []byte("before old incarnation\n"),
		AfterExisted: true,
		After:        []byte("old incarnation\n"),
		Mode:         0o600,
		AfterMode:    0o600,
	})
	siblingSnapshotPath := writeMigrationSnapshot(t, root, siblingScope, "run-sibling", migrationSnapshotEntry{
		Path:         "src/Sibling.jsx",
		Existed:      true,
		Content:      []byte("before sibling incarnation\n"),
		AfterExisted: true,
		After:        []byte("sibling incarnation\n"),
		Mode:         0o600,
		AfterMode:    0o600,
	})

	store := NewFileStore(root)
	files, err := store.ListFiles(ctx, newScope, ListOptions{})
	if err != nil {
		t.Fatalf("list recreated project files: %v", err)
	}
	if len(files.Files) != 0 {
		t.Fatalf("recreated project inherited files: %#v", files.Files)
	}
	if _, err := store.ReadFile(ctx, newScope, ReadOptions{Path: "src/App.jsx"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("recreated project inherited old source: %v", err)
	}
	newSnapshot := filepath.Join(root, workspaceSnapshotDirectory, newScope.OrgUUID, newScope.WorkspaceUUID, newScope.ProjectName, newScope.ProjectUID, "run-old")
	if _, err := os.Stat(newSnapshot); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("recreated project inherited old snapshot: %v", err)
	}

	for _, nested := range []string{
		filepath.Join(workspaceBase, newUID, oldUID),
		filepath.Join(workspaceBase, newUID, siblingUID),
		filepath.Join(root, workspaceSnapshotDirectory, baseScope.OrgUUID, baseScope.WorkspaceUUID, baseScope.ProjectName, newUID, oldUID),
		filepath.Join(root, workspaceSnapshotDirectory, baseScope.OrgUUID, baseScope.WorkspaceUUID, baseScope.ProjectName, newUID, siblingUID),
	} {
		if _, err := os.Stat(nested); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("stale UID tree nested under recreated project at %q: %v", nested, err)
		}
	}

	oldRead, err := store.ReadFile(ctx, oldScope, ReadOptions{Path: "src/App.jsx"})
	if err != nil || oldRead.Content != "old incarnation\n" {
		t.Fatalf("old incarnation source = %#v, err=%v", oldRead, err)
	}
	siblingRead, err := store.ReadFile(ctx, siblingScope, ReadOptions{Path: "src/Sibling.jsx"})
	if err != nil || siblingRead.Content != "sibling incarnation\n" {
		t.Fatalf("sibling incarnation source = %#v, err=%v", siblingRead, err)
	}

	for _, preserved := range []string{oldWorkspacePath, siblingWorkspacePath, oldSnapshotPath, siblingSnapshotPath} {
		if _, err := os.Stat(preserved); err != nil {
			t.Fatalf("existing UID data was not preserved at %q: %v", preserved, err)
		}
	}
}

func TestFileStorePreservesMarkerlessArbitraryUIDTree(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	base := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	old := base
	old.ProjectUID = "src"
	current := base
	current.ProjectUID = "new-project-incarnation"
	oldPath := filepath.Join(root, base.OrgUUID, base.WorkspaceUUID, base.ProjectName, old.ProjectUID, "src", "App.jsx")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewFileStore(root)
	files, err := store.ListFiles(ctx, current, ListOptions{})
	if err != nil {
		t.Fatalf("list recreated arbitrary-UID project: %v", err)
	}
	if len(files.Files) != 0 {
		t.Fatalf("recreated arbitrary-UID project inherited files: %#v", files.Files)
	}
	if _, err := store.ReadFile(ctx, current, ReadOptions{Path: "src/App.jsx"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("recreated arbitrary-UID project inherited old source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, base.OrgUUID, base.WorkspaceUUID, base.ProjectName, current.ProjectUID, old.ProjectUID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("arbitrary stale UID tree nested under recreated project: %v", err)
	}
	read, err := store.ReadFile(ctx, old, ReadOptions{Path: "src/App.jsx"})
	if err != nil || read.Content != "old\n" {
		t.Fatalf("old arbitrary-UID project = %#v, err=%v", read, err)
	}
}

func TestFileStoreLeavesMixedLegacyAndUIDTreeUnchanged(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	base := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	current := base
	current.ProjectUID = "new-project-incarnation"
	oldUID := "95fc57ea-3962-47f0-a30c-9b6f80960776"
	legacyPath := filepath.Join(root, base.OrgUUID, base.WorkspaceUUID, base.ProjectName, "package.json")
	oldPath := filepath.Join(root, base.OrgUUID, base.WorkspaceUUID, base.ProjectName, oldUID, "src", "App.jsx")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("legacy root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewFileStore(root)
	if _, err := store.ListFiles(ctx, current, ListOptions{}); err != nil {
		t.Fatalf("list mixed recreated project: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, base.OrgUUID, base.WorkspaceUUID, base.ProjectName, current.ProjectUID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("mixed project created a new target directory: %v", err)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("mixed direct legacy file moved or removed: %v", err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("mixed existing UID file moved or removed: %v", err)
	}
}

func TestFileStoreResumesOnlyBoundMigrationStage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	base := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	first := base
	first.ProjectUID = "first-project-incarnation"
	second := base
	second.ProjectUID = "second-project-incarnation"
	legacy := filepath.Join(root, base.OrgUUID, base.WorkspaceUUID, base.ProjectName)
	stage := filepath.Join(root, base.OrgUUID, base.WorkspaceUUID, base.ProjectName+workspaceMigrationStage)
	legacyFile := filepath.Join(legacy, "package.json")
	if err := os.MkdirAll(filepath.Dir(legacyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyFile, []byte("legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(legacy, stage); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(root)
	if _, err := store.ReadFile(ctx, second, ReadOptions{Path: "package.json"}); err == nil {
		t.Fatal("unbound migration stage was attributed to the caller")
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("unbound migration stage was modified: %v", err)
	}
	markerPath, err := store.migrationMarkerPath(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceMigrationMarker(markerPath, workspaceMigrationMarker{Version: workspaceMigrationVersion, ProjectUID: first.ProjectUID}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ReadFile(ctx, second, ReadOptions{Path: "package.json"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("second project resumed first project's stage: %v", err)
	}
	read, err := store.ReadFile(ctx, first, ReadOptions{Path: "package.json"})
	if err != nil || read.Content != "legacy\n" {
		t.Fatalf("first project after staged migration = %#v, err=%v", read, err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("bound migration stage remains after resume: %v", err)
	}
}

func TestFileStoreGlobalMigrationDispositionPreservesBothHalves(t *testing.T) {
	for _, tc := range []struct {
		name           string
		sourceScoped   bool
		snapshotsFirst bool
	}{
		{name: "scoped-source-source-first", sourceScoped: true},
		{name: "scoped-source-snapshots-first", sourceScoped: true, snapshotsFirst: true},
		{name: "scoped-snapshots-source-first", snapshotsFirst: false},
		{name: "scoped-snapshots-snapshots-first", snapshotsFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			base := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
			old := base
			old.ProjectUID = "95fc57ea-3962-47f0-a30c-9b6f80960776"
			current := base
			current.ProjectUID = "f20e046e-6b22-4f2e-a72c-d2a1f665c2c9"
			workspaceBase := filepath.Join(root, base.OrgUUID, base.WorkspaceUUID, base.ProjectName)
			snapshotBase := filepath.Join(root, workspaceSnapshotDirectory, base.OrgUUID, base.WorkspaceUUID, base.ProjectName)
			sourcePath := "src/App.jsx"
			var preservedSource string
			if tc.sourceScoped {
				preservedSource = filepath.Join(workspaceBase, old.ProjectUID, filepath.FromSlash(sourcePath))
				if err := os.MkdirAll(filepath.Dir(preservedSource), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				sourcePath = "package.json"
				preservedSource = filepath.Join(workspaceBase, sourcePath)
				if err := os.MkdirAll(filepath.Dir(preservedSource), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(preservedSource, []byte("source\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			var preservedSnapshot string
			entry := migrationSnapshotEntry{
				Path:         sourcePath,
				Existed:      true,
				Content:      []byte("before\n"),
				AfterExisted: true,
				After:        []byte("source\n"),
				Mode:         0o600,
				AfterMode:    0o600,
			}
			if tc.sourceScoped {
				// The source half is scoped, so the snapshots half is the clear
				// direct legacy counterpart for this order pair.
				preservedSnapshot = writeDirectMigrationSnapshot(t, root, base, "run-legacy", entry)
				if err := os.WriteFile(filepath.Join(snapshotBase, workspaceSourceStateFile), []byte(`{"uncommittedPaths":["src/App.jsx"]}`), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				// The source half is clear direct legacy, so the snapshots half is
				// an existing UID-scoped tree that must veto source migration.
				preservedSnapshot = writeMigrationSnapshot(t, root, old, "run-old", entry)
			}

			store := NewFileStore(root)
			if tc.snapshotsFirst {
				if _, err := store.UncommittedPaths(ctx, current); err != nil {
					t.Fatalf("snapshot-first migration: %v", err)
				}
				if _, err := store.ReadFile(ctx, current, ReadOptions{Path: sourcePath}); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("snapshot-first source inherited markerless data: %v", err)
				}
			} else {
				if _, err := store.ReadFile(ctx, current, ReadOptions{Path: sourcePath}); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("source-first source inherited markerless data: %v", err)
				}
				if _, err := store.UncommittedPaths(ctx, current); err != nil {
					t.Fatalf("source-first migration: %v", err)
				}
			}

			markerPath, err := store.migrationMarkerPath(base)
			if err != nil {
				t.Fatal(err)
			}
			marker, err := readWorkspaceMigrationMarker(markerPath)
			if err != nil {
				t.Fatal(err)
			}
			if marker.Disposition != workspaceMigrationDispositionPreserve {
				t.Fatalf("global migration disposition = %q, want preserve", marker.Disposition)
			}
			if marker.ProjectUID != current.ProjectUID || marker.WorkspaceProjectUID != current.ProjectUID || marker.SnapshotsProjectUID != current.ProjectUID {
				t.Fatalf("global migration marker = %#v, want both halves bound to current UID", marker)
			}

			for _, target := range []string{
				filepath.Join(workspaceBase, current.ProjectUID),
				filepath.Join(snapshotBase, current.ProjectUID),
			} {
				if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("markerless data nested or target created at %q: %v", target, err)
				}
			}
			for _, preserved := range []string{preservedSource, preservedSnapshot} {
				if _, err := os.Stat(preserved); err != nil {
					t.Fatalf("markerless data was not preserved at %q: %v", preserved, err)
				}
			}
			if !tc.sourceScoped {
				if _, err := os.Stat(filepath.Join(snapshotBase, old.ProjectUID)); err != nil {
					t.Fatalf("scoped snapshot tree was not preserved: %v", err)
				}
			}
		})
	}
}

func TestFileStoreUpgradesMigrationDispositionOnLateScopedTree(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	base := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	current := base
	current.ProjectUID = "f20e046e-6b22-4f2e-a72c-d2a1f665c2c9"
	oldUID := "95fc57ea-3962-47f0-a30c-9b6f80960776"
	oldPath := filepath.Join(root, base.OrgUUID, base.WorkspaceUUID, base.ProjectName, oldUID, "src", "App.jsx")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(root)
	markerPath, err := store.migrationMarkerPath(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceMigrationMarker(markerPath, workspaceMigrationMarker{
		Version:     workspaceMigrationVersion,
		ProjectUID:  current.ProjectUID,
		Disposition: workspaceMigrationDispositionMigrate,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ReadFile(ctx, current, ReadOptions{Path: "src/App.jsx"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("late scoped tree leaked into migrated project: %v", err)
	}
	marker, err := readWorkspaceMigrationMarker(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if marker.Disposition != workspaceMigrationDispositionPreserve || marker.WorkspaceProjectUID != current.ProjectUID || marker.SnapshotsProjectUID != current.ProjectUID {
		t.Fatalf("late scoped tree did not upgrade marker to preserve: %#v", marker)
	}
	if _, err := os.Stat(filepath.Join(root, base.OrgUUID, base.WorkspaceUUID, base.ProjectName, current.ProjectUID)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("late scoped tree was nested under current project: %v", err)
	}
}

func writeMigrationSnapshot(t *testing.T, root string, scope Scope, snapshotID string, entry migrationSnapshotEntry) string {
	t.Helper()
	dir := filepath.Join(root, workspaceSnapshotDirectory, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, snapshotID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "entry.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeDirectMigrationSnapshot(t *testing.T, root string, scope Scope, snapshotID string, entry migrationSnapshotEntry) string {
	t.Helper()
	dir := filepath.Join(root, workspaceSnapshotDirectory, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, snapshotID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "entry.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func seedLegacyMigrationFixture(t *testing.T, root string) Scope {
	t.Helper()
	legacyScope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	legacyWorkspace := filepath.Join(root, legacyScope.OrgUUID, legacyScope.WorkspaceUUID, legacyScope.ProjectName)
	if err := os.MkdirAll(filepath.Join(legacyWorkspace, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyWorkspace, "src", "App.tsx"), []byte("legacy source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyWorkspace, "package.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacySnapshots := filepath.Join(root, workspaceSnapshotDirectory, legacyScope.OrgUUID, legacyScope.WorkspaceUUID, legacyScope.ProjectName, "run-legacy")
	if err := os.MkdirAll(legacySnapshots, 0o700); err != nil {
		t.Fatal(err)
	}
	entryRaw, err := json.Marshal(migrationSnapshotEntry{
		Path:         "src/App.tsx",
		Existed:      true,
		Content:      []byte("legacy source\n"),
		AfterExisted: true,
		After:        []byte("changed source\n"),
		Mode:         0o600,
		AfterMode:    0o600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacySnapshots, "entry.json"), entryRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(legacySnapshots), workspaceSourceStateFile), []byte(`{"uncommittedPaths":["src/App.tsx"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return legacyScope
}

func TestFileStoreUnifiedDeleteRejectsOversizedTarget(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	content := string(make([]byte, MaxWriteBytes+1))
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyPatch(ctx, scope, PatchOptions{Patch: "*** Begin Patch\n*** Delete File: large.txt\n*** End Patch"})
	var patchErr *PatchError
	if !errors.As(err, &patchErr) || patchErr.Code != PatchErrorInvalidPatch {
		t.Fatalf("oversized delete error = %v (%T), want invalid_patch", err, err)
	}
	if _, err := store.ReadFile(ctx, scope, ReadOptions{Path: "large.txt", MaxBytes: MaxWriteBytes}); err != nil {
		t.Fatalf("oversized target disappeared after rejected delete: %v", err)
	}
}
