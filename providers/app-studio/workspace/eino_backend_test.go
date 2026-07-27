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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	einofs "github.com/cloudwego/eino/adk/filesystem"
)

func TestEinoReadOnlyBackendIsScopedToOneProject(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scopeA := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "alpha"}
	scopeB := Scope{OrgUUID: "org-b", WorkspaceUUID: "ws-2", ProjectName: "beta"}
	if err := store.ApplyFiles(context.Background(), scopeA, []File{{Path: "src/a.go", Content: "package alpha\n"}}); err != nil {
		t.Fatalf("ApplyFiles scope A returned error: %v", err)
	}
	if err := store.ApplyFiles(context.Background(), scopeB, []File{{Path: "secret.txt", Content: "beta secret\n"}}); err != nil {
		t.Fatalf("ApplyFiles scope B returned error: %v", err)
	}

	backend, err := NewEinoReadOnlyBackend(store, scopeA)
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
	}
	infos, err := backend.GlobInfo(context.Background(), &einofs.GlobInfoRequest{Pattern: "**/*"})
	if err != nil {
		t.Fatalf("GlobInfo returned error: %v", err)
	}
	if got := einoFileInfoPaths(infos); !slices.Equal(got, []string{"src/a.go"}) {
		t.Fatalf("paths = %v, want only alpha project file", got)
	}
}

func TestEinoReadOnlyBackendRejectsSymlinkedScopeComponents(t *testing.T) {
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "alpha"}
	for _, component := range []string{"org", "workspace", "project"} {
		t.Run(component, func(t *testing.T) {
			store := NewFileStore(t.TempDir())
			outside := t.TempDir()
			var link string
			switch component {
			case "org":
				if err := os.MkdirAll(filepath.Join(outside, scope.WorkspaceUUID, scope.ProjectName), 0o755); err != nil {
					t.Fatalf("MkdirAll outside org returned error: %v", err)
				}
				link = filepath.Join(store.Root(), scope.OrgUUID)
			case "workspace":
				if err := os.MkdirAll(filepath.Join(store.Root(), scope.OrgUUID), 0o755); err != nil {
					t.Fatalf("MkdirAll org returned error: %v", err)
				}
				if err := os.MkdirAll(filepath.Join(outside, scope.ProjectName), 0o755); err != nil {
					t.Fatalf("MkdirAll outside workspace returned error: %v", err)
				}
				link = filepath.Join(store.Root(), scope.OrgUUID, scope.WorkspaceUUID)
			case "project":
				if err := os.MkdirAll(filepath.Join(store.Root(), scope.OrgUUID, scope.WorkspaceUUID), 0o755); err != nil {
					t.Fatalf("MkdirAll workspace returned error: %v", err)
				}
				link = filepath.Join(store.Root(), scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName)
			}
			if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside secret\n"), 0o644); err != nil {
				t.Fatalf("WriteFile outside secret returned error: %v", err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatalf("Symlink returned error: %v", err)
			}

			if backend, err := NewEinoReadOnlyBackend(store, scope); err == nil || backend != nil {
				t.Fatalf("NewEinoReadOnlyBackend = (%#v, %v), want scope symlink rejection before Read/List/Glob/Grep", backend, err)
			}
		})
	}
}

func TestEinoReadOnlyBackendRejectsSymlinkedStoreRoot(t *testing.T) {
	outside := t.TempDir()
	root := filepath.Join(t.TempDir(), "workspace-root")
	if err := os.Symlink(outside, root); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}
	if backend, err := NewEinoReadOnlyBackend(NewFileStore(root), Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "alpha"}); err == nil || backend != nil {
		t.Fatalf("NewEinoReadOnlyBackend = (%#v, %v), want store root symlink rejection", backend, err)
	}
}

func TestEinoReadOnlyBackendRejectsSymlinkedStoreRootWithTrailingSeparators(t *testing.T) {
	for _, suffix := range []string{"/", "/."} {
		t.Run(suffix, func(t *testing.T) {
			outside := t.TempDir()
			root := filepath.Join(t.TempDir(), "workspace-root")
			if err := os.Symlink(outside, root); err != nil {
				t.Fatalf("Symlink returned error: %v", err)
			}
			if backend, err := NewEinoReadOnlyBackend(NewFileStore(root+suffix), Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "alpha"}); err == nil || backend != nil {
				t.Fatalf("NewEinoReadOnlyBackend = (%#v, %v), want canonicalized store root symlink rejection", backend, err)
			}
		})
	}
}

func TestEinoReadOnlyBackendAllowsNonexistentStoreRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace-root")
	backend, err := NewEinoReadOnlyBackend(NewFileStore(root), Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "new-project"})
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error for a nonexistent root: %v", err)
	}
	infos, err := backend.GlobInfo(context.Background(), &einofs.GlobInfoRequest{Pattern: "**/*"})
	if err != nil {
		t.Fatalf("GlobInfo returned error for a nonexistent root: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("GlobInfo nonexistent root = %#v, want no files", infos)
	}
}

func TestEinoReadOnlyBackendRejectsStoreRootReplacedBySymlink(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "alpha"}
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "inside.txt", Content: "inside\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	backend, err := NewEinoReadOnlyBackend(store, scope)
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName), 0o755); err != nil {
		t.Fatalf("MkdirAll outside scope returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, "secret.txt"), []byte("outside secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile outside secret returned error: %v", err)
	}
	if err := os.RemoveAll(store.Root()); err != nil {
		t.Fatalf("RemoveAll store root returned error: %v", err)
	}
	if err := os.Symlink(outside, store.Root()); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}
	assertEinoReadOperationsRejectSymlink(t, ctx, backend)
}

func TestEinoReadOnlyBackendRejectsCanonicalizedStoreRootReplacement(t *testing.T) {
	ctx := context.Background()
	for _, suffix := range []string{"/", "/."} {
		t.Run(suffix, func(t *testing.T) {
			root := t.TempDir()
			store := NewFileStore(root + suffix)
			scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "alpha"}
			if err := store.ApplyFiles(ctx, scope, []File{{Path: "inside.txt", Content: "inside\n"}}); err != nil {
				t.Fatalf("ApplyFiles returned error: %v", err)
			}
			backend, err := NewEinoReadOnlyBackend(store, scope)
			if err != nil {
				t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
			}
			outside := t.TempDir()
			if err := os.MkdirAll(filepath.Join(outside, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName), 0o755); err != nil {
				t.Fatalf("MkdirAll outside scope returned error: %v", err)
			}
			if err := os.WriteFile(filepath.Join(outside, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, "secret.txt"), []byte("outside secret\n"), 0o644); err != nil {
				t.Fatalf("WriteFile outside secret returned error: %v", err)
			}
			if err := os.RemoveAll(root); err != nil {
				t.Fatalf("RemoveAll store root returned error: %v", err)
			}
			if err := os.Symlink(outside, root); err != nil {
				t.Fatalf("Symlink returned error: %v", err)
			}
			assertEinoReadOperationsRejectSymlink(t, ctx, backend)
		})
	}
}

func TestEinoReadOnlyBackendRejectsScopeReplacedBySymlink(t *testing.T) {
	ctx := context.Background()
	for _, component := range []string{"org", "workspace", "project"} {
		t.Run(component, func(t *testing.T) {
			store := NewFileStore(t.TempDir())
			scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "alpha"}
			if err := store.ApplyFiles(ctx, scope, []File{{Path: "inside.txt", Content: "inside\n"}}); err != nil {
				t.Fatalf("ApplyFiles returned error: %v", err)
			}
			backend, err := NewEinoReadOnlyBackend(store, scope)
			if err != nil {
				t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
			}
			outside := t.TempDir()
			var replaced string
			var secretDir string
			switch component {
			case "org":
				replaced = filepath.Join(store.Root(), scope.OrgUUID)
				secretDir = filepath.Join(outside, scope.WorkspaceUUID, scope.ProjectName)
				if err := os.MkdirAll(secretDir, 0o755); err != nil {
					t.Fatalf("MkdirAll outside org returned error: %v", err)
				}
			case "workspace":
				replaced = filepath.Join(store.Root(), scope.OrgUUID, scope.WorkspaceUUID)
				secretDir = filepath.Join(outside, scope.ProjectName)
				if err := os.MkdirAll(secretDir, 0o755); err != nil {
					t.Fatalf("MkdirAll outside workspace returned error: %v", err)
				}
			case "project":
				replaced = filepath.Join(store.Root(), scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName)
				secretDir = outside
			}
			if err := os.WriteFile(filepath.Join(secretDir, "secret.txt"), []byte("outside secret\n"), 0o644); err != nil {
				t.Fatalf("WriteFile outside secret returned error: %v", err)
			}
			if err := os.RemoveAll(replaced); err != nil {
				t.Fatalf("RemoveAll replaced scope returned error: %v", err)
			}
			if err := os.Symlink(outside, replaced); err != nil {
				t.Fatalf("Symlink returned error: %v", err)
			}
			assertEinoReadOperationsRejectSymlink(t, ctx, backend)
		})
	}
}

func assertEinoReadOperationsRejectSymlink(t *testing.T, ctx context.Context, backend *EinoReadOnlyBackend) {
	t.Helper()
	for name, operation := range map[string]func() error{
		"Read": func() error {
			_, err := backend.Read(ctx, &einofs.ReadRequest{FilePath: "secret.txt"})
			return err
		},
		"LsInfo": func() error {
			_, err := backend.LsInfo(ctx, &einofs.LsInfoRequest{})
			return err
		},
		"GlobInfo": func() error {
			_, err := backend.GlobInfo(ctx, &einofs.GlobInfoRequest{Pattern: "**/*"})
			return err
		},
		"GrepRaw": func() error {
			_, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "secret"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); err == nil {
				t.Fatalf("%s returned nil error after a scope component became a symlink", name)
			}
		})
	}
}

func TestEinoReadOnlyBackendAllowsNonexistentProject(t *testing.T) {
	backend, err := NewEinoReadOnlyBackend(NewFileStore(t.TempDir()), Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "new-project"})
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error for a new project: %v", err)
	}
	infos, err := backend.GlobInfo(context.Background(), &einofs.GlobInfoRequest{Pattern: "**/*"})
	if err != nil {
		t.Fatalf("GlobInfo returned error for a new project: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("GlobInfo new project = %#v, want no files", infos)
	}
}

func TestEinoReadOnlyBackendRejectsUnsafePathsAndSymlinks(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "alpha"}
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "safe.txt", Content: "safe\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatalf("scopeDir returned error: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.txt"), filepath.Join(dir, "linked.txt")); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}

	backend, err := NewEinoReadOnlyBackend(store, scope)
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
	}
	for _, raw := range []string{"/etc/passwd", "../beta/secret.txt", ".git/config", "node_modules/pkg/index.js"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := backend.Read(ctx, &einofs.ReadRequest{FilePath: raw}); err == nil {
				t.Fatalf("Read(%q) returned nil error", raw)
			}
			if _, err := backend.GlobInfo(ctx, &einofs.GlobInfoRequest{Pattern: raw}); err == nil {
				t.Fatalf("GlobInfo(%q) returned nil error", raw)
			}
			if _, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "safe", Path: raw}); err == nil {
				t.Fatalf("GrepRaw(%q) returned nil error", raw)
			}
		})
	}
	if infos, err := backend.GlobInfo(ctx, &einofs.GlobInfoRequest{Pattern: "**/*"}); err != nil {
		t.Fatalf("GlobInfo returned error: %v", err)
	} else if got := einoFileInfoPaths(infos); slices.Contains(got, "linked.txt") {
		t.Fatalf("GlobInfo paths = %v, must not list symlink", got)
	}
	if _, err := backend.Read(ctx, &einofs.ReadRequest{FilePath: "linked.txt"}); err == nil {
		t.Fatal("Read symlink returned nil error")
	}
}

func TestEinoReadOnlyBackendListsImmediateChildren(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := store.ApplyFiles(ctx, scope, []File{
		{Path: "README.md", Content: "readme\n"},
		{Path: "src/App.tsx", Content: "app\n"},
		{Path: "src/components/Card.tsx", Content: "card\n"},
		{Path: "test/App.test.tsx", Content: "test\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	backend, err := NewEinoReadOnlyBackend(store, scope)
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
	}

	root, err := backend.LsInfo(ctx, &einofs.LsInfoRequest{})
	if err != nil {
		t.Fatalf("LsInfo root returned error: %v", err)
	}
	wantRoot := []einofs.FileInfo{
		{Path: "README.md", IsDir: false, Size: int64(len("readme\n"))},
		{Path: "src", IsDir: true},
		{Path: "test", IsDir: true},
	}
	if !slices.Equal(root, wantRoot) {
		t.Fatalf("root = %#v, want %#v", root, wantRoot)
	}

	src, err := backend.LsInfo(ctx, &einofs.LsInfoRequest{Path: "src"})
	if err != nil {
		t.Fatalf("LsInfo src returned error: %v", err)
	}
	if got := einoFileInfoPaths(src); !slices.Equal(got, []string{"App.tsx", "components"}) {
		t.Fatalf("src paths = %v, want App.tsx and components", got)
	}
}

func TestEinoReadOnlyBackendReadsLinePages(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := store.ApplyFiles(ctx, scope, []File{
		{Path: "src/App.tsx", Content: "line one\nline two\nline three\nline four\n"},
		{Path: "empty.txt", Content: ""},
	}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatalf("scopeDir returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binary.bin"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	backend, err := NewEinoReadOnlyBackend(store, scope)
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
	}

	read, err := backend.Read(ctx, &einofs.ReadRequest{FilePath: "src/App.tsx", Offset: 2, Limit: 2})
	if err != nil {
		t.Fatalf("Read page returned error: %v", err)
	}
	if read.Content != "line two\nline three" {
		t.Fatalf("content = %q, want literal two-line page", read.Content)
	}
	full, err := backend.Read(ctx, &einofs.ReadRequest{FilePath: "src/App.tsx", Limit: 0})
	if err != nil {
		t.Fatalf("Read full file returned error: %v", err)
	}
	if full.Content != "line one\nline two\nline three\nline four\n" {
		t.Fatalf("full content = %q", full.Content)
	}
	afterEOF, err := backend.Read(ctx, &einofs.ReadRequest{FilePath: "src/App.tsx", Offset: 99})
	if err != nil {
		t.Fatalf("Read after EOF returned error: %v", err)
	}
	if afterEOF.Content != "" {
		t.Fatalf("after EOF content = %q, want empty", afterEOF.Content)
	}
	empty, err := backend.Read(ctx, &einofs.ReadRequest{FilePath: "empty.txt"})
	if err != nil {
		t.Fatalf("Read empty file returned error: %v", err)
	}
	if empty.Content != "" {
		t.Fatalf("empty content = %q, want empty", empty.Content)
	}
	if _, err := backend.Read(ctx, &einofs.ReadRequest{FilePath: "binary.bin"}); err == nil {
		t.Fatal("Read binary file returned nil error")
	}
}

func TestEinoReadOnlyBackendGlobsProjectRelativePaths(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := store.ApplyFiles(ctx, scope, []File{
		{Path: "README.md", Content: "readme\n"},
		{Path: "src/App.tsx", Content: "app\n"},
		{Path: "src/components/Card.tsx", Content: "card\n"},
		{Path: "test/App.test.tsx", Content: "test\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	backend, err := NewEinoReadOnlyBackend(store, scope)
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
	}

	glob, err := backend.GlobInfo(ctx, &einofs.GlobInfoRequest{Path: "src", Pattern: "**/*.tsx"})
	if err != nil {
		t.Fatalf("GlobInfo returned error: %v", err)
	}
	if got := einoFileInfoPaths(glob); !slices.Equal(got, []string{"src/App.tsx", "src/components/Card.tsx"}) {
		t.Fatalf("glob paths = %v", got)
	}
	for _, info := range glob {
		if info.ModifiedAt != "" {
			t.Fatalf("glob info %#v has synthetic ModifiedAt, want empty", info)
		}
	}
}

func TestEinoReadOnlyBackendEnforcesBoundsAndBinaryRules(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "large"}
	files := make([]File, 0, MaxListLimit+1)
	for i := 0; i <= MaxListLimit; i++ {
		files = append(files, File{Path: fmt.Sprintf("src/file-%03d.txt", i), Content: "x"})
	}
	if err := store.ApplyFiles(ctx, scope, files); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	backend, err := NewEinoReadOnlyBackend(store, scope)
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
	}
	if _, err := backend.GlobInfo(ctx, &einofs.GlobInfoRequest{Pattern: "**/*"}); err == nil || !strings.Contains(err.Error(), "narrow path or glob") {
		t.Fatalf("GlobInfo large inventory error = %v, want narrow request error", err)
	}
}

func TestEinoReadOnlyBackendNarrowsCandidatesInLargeProjects(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "large-narrowed"}
	files := make([]File, 0, MaxListLimit+5)
	for i := 0; i <= MaxListLimit; i++ {
		files = append(files, File{
			Path:    fmt.Sprintf("unrelated/file-%03d.txt", i),
			Content: "needle unrelated\n",
		})
	}
	files = append(files,
		File{Path: "target/App.tsx", Content: "needle app\n"},
		File{Path: "target/components/Card.tsx", Content: "card\n"},
		File{Path: "target/skip.txt", Content: "needle skip\n"},
		File{Path: "target/view.jsx", Content: "needle view\n"},
	)
	if err := store.ApplyFiles(ctx, scope, files); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	backend, err := NewEinoReadOnlyBackend(store, scope)
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
	}

	t.Run("ls", func(t *testing.T) {
		infos, err := backend.LsInfo(ctx, &einofs.LsInfoRequest{Path: "target"})
		if err != nil {
			t.Fatalf("narrowed LsInfo returned error: %v", err)
		}
		if got := einoFileInfoPaths(infos); !slices.Equal(got, []string{"App.tsx", "components", "skip.txt", "view.jsx"}) {
			t.Fatalf("narrowed ls paths = %v", got)
		}
		if _, err := backend.LsInfo(ctx, &einofs.LsInfoRequest{}); err == nil || !strings.Contains(err.Error(), "narrow") {
			t.Fatalf("broad LsInfo error = %v, want actionable narrowing error", err)
		}
	})

	t.Run("glob", func(t *testing.T) {
		infos, err := backend.GlobInfo(ctx, &einofs.GlobInfoRequest{Path: "target", Pattern: "**/*.tsx"})
		if err != nil {
			t.Fatalf("narrowed GlobInfo returned error: %v", err)
		}
		if got := einoFileInfoPaths(infos); !slices.Equal(got, []string{"target/App.tsx", "target/components/Card.tsx"}) {
			t.Fatalf("narrowed glob paths = %v", got)
		}
		if _, err := backend.GlobInfo(ctx, &einofs.GlobInfoRequest{Pattern: "**/*"}); err == nil || !strings.Contains(err.Error(), "narrow") {
			t.Fatalf("broad GlobInfo error = %v, want actionable narrowing error", err)
		}
	})

	t.Run("grep", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &einofs.GrepRequest{
			Pattern:  "needle",
			Path:     "target",
			Glob:     "*.jsx",
			FileType: "js",
		})
		if err != nil {
			t.Fatalf("narrowed GrepRaw returned error: %v", err)
		}
		if !slices.Equal(matches, []einofs.GrepMatch{{Path: "target/view.jsx", Line: 1, Content: "needle view"}}) {
			t.Fatalf("narrowed grep matches = %#v", matches)
		}
		if _, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle"}); err == nil || !strings.Contains(err.Error(), "narrow") {
			t.Fatalf("broad GrepRaw error = %v, want actionable narrowing error", err)
		}
	})
}

func TestEinoReadOnlyBackendGrep(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := store.ApplyFiles(ctx, scope, []File{
		{Path: "src/App.tsx", Content: "first\nneedle alpha\nNeedle beta\nbefore\nbegin multi\nline match\nlast\n"},
		{Path: "src/view.jsx", Content: "needle jsx\n"},
		{Path: "docs/note.txt", Content: "needle docs\n"},
		{Path: "z-last.txt", Content: "needle last\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatalf("scopeDir returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binary.bin"), []byte{'n', 0, 'e', 'e', 'd', 'l', 'e'}, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	backend, err := NewEinoReadOnlyBackend(store, scope)
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
	}

	matches, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: `needle\s+\w+`})
	if err != nil {
		t.Fatalf("GrepRaw regex returned error: %v", err)
	}
	if got := einoGrepPaths(matches); !slices.Equal(got, []string{"docs/note.txt", "src/App.tsx", "src/view.jsx", "z-last.txt"}) {
		t.Fatalf("regex paths = %v", got)
	}
	if got := matches[1]; got.Path != "src/App.tsx" || got.Line != 2 || got.Content != "needle alpha" {
		t.Fatalf("regex match = %#v, want src/App.tsx line 2", got)
	}

	caseInsensitive, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle beta", CaseInsensitive: true})
	if err != nil {
		t.Fatalf("GrepRaw case-insensitive returned error: %v", err)
	}
	if len(caseInsensitive) != 1 || caseInsensitive[0].Path != "src/App.tsx" || caseInsensitive[0].Line != 3 || caseInsensitive[0].Content != "Needle beta" {
		t.Fatalf("case-insensitive matches = %#v", caseInsensitive)
	}

	pathMatches, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle", Path: "src"})
	if err != nil {
		t.Fatalf("GrepRaw path returned error: %v", err)
	}
	if got := einoGrepPaths(pathMatches); !slices.Equal(got, []string{"src/App.tsx", "src/view.jsx"}) {
		t.Fatalf("path matches = %v", got)
	}

	globMatches, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle", Path: "src", Glob: "*.jsx"})
	if err != nil {
		t.Fatalf("GrepRaw glob returned error: %v", err)
	}
	if got := einoGrepPaths(globMatches); !slices.Equal(got, []string{"src/view.jsx"}) {
		t.Fatalf("glob matches = %v", got)
	}

	aliasMatches, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle", FileType: "js"})
	if err != nil {
		t.Fatalf("GrepRaw file type returned error: %v", err)
	}
	if got := einoGrepPaths(aliasMatches); !slices.Equal(got, []string{"src/view.jsx"}) {
		t.Fatalf("paths = %v, want Eino js alias to include jsx", got)
	}

	multiline, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "begin multi\\nline match", EnableMultiline: true})
	if err != nil {
		t.Fatalf("GrepRaw multiline returned error: %v", err)
	}
	if !slices.Equal(multiline, []einofs.GrepMatch{
		{Path: "src/App.tsx", Line: 5, Content: "begin multi"},
		{Path: "src/App.tsx", Line: 6, Content: "line match"},
	}) {
		t.Fatalf("multiline matches = %#v", multiline)
	}

	contextMatches, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle alpha", Path: "src/App.tsx", BeforeLines: 1, AfterLines: 1})
	if err != nil {
		t.Fatalf("GrepRaw context returned error: %v", err)
	}
	if !slices.Equal(contextMatches, []einofs.GrepMatch{
		{Path: "src/App.tsx", Line: 1, Content: "first"},
		{Path: "src/App.tsx", Line: 2, Content: "needle alpha"},
		{Path: "src/App.tsx", Line: 3, Content: "Needle beta"},
	}) {
		t.Fatalf("context matches = %#v", contextMatches)
	}

	if _, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "["}); err == nil {
		t.Fatal("GrepRaw invalid regex returned nil error")
	}
	if _, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle", Glob: "["}); err == nil {
		t.Fatal("GrepRaw invalid glob returned nil error")
	}
}

func TestEinoReadOnlyBackendGrepEnforcesBoundsAndCancellation(t *testing.T) {
	ctx := context.Background()
	t.Run("aggregate bytes", func(t *testing.T) {
		store := NewFileStore(t.TempDir())
		scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "aggregate"}
		content := strings.Repeat("x", MaxReadMaxBytes-len("needle\n")) + "needle\n"
		files := make([]File, 0, 65)
		for i := 0; i < 65; i++ {
			files = append(files, File{Path: fmt.Sprintf("src/file-%03d.txt", i), Content: content})
		}
		if err := store.ApplyFiles(ctx, scope, files); err != nil {
			t.Fatalf("ApplyFiles returned error: %v", err)
		}
		backend, err := NewEinoReadOnlyBackend(store, scope)
		if err != nil {
			t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
		}
		if _, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle"}); err == nil || !strings.Contains(err.Error(), "narrow request") {
			t.Fatalf("GrepRaw aggregate error = %v, want narrow request error", err)
		}
	})
	t.Run("match count", func(t *testing.T) {
		store := NewFileStore(t.TempDir())
		scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "matches"}
		if err := store.ApplyFiles(ctx, scope, []File{{Path: "many.txt", Content: strings.Repeat("needle\n", maxEinoBackendMatches+1)}}); err != nil {
			t.Fatalf("ApplyFiles returned error: %v", err)
		}
		backend, err := NewEinoReadOnlyBackend(store, scope)
		if err != nil {
			t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
		}
		if _, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle"}); err == nil || !strings.Contains(err.Error(), "narrow request") {
			t.Fatalf("GrepRaw match count error = %v, want narrow request error", err)
		}
	})
	t.Run("canceled context", func(t *testing.T) {
		store := NewFileStore(t.TempDir())
		scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "canceled"}
		if err := store.ApplyFiles(ctx, scope, []File{{Path: "src/app.txt", Content: "needle\n"}}); err != nil {
			t.Fatalf("ApplyFiles returned error: %v", err)
		}
		backend, err := NewEinoReadOnlyBackend(store, scope)
		if err != nil {
			t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := backend.GrepRaw(canceled, &einofs.GrepRequest{Pattern: "needle"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("GrepRaw canceled context error = %v, want context canceled", err)
		}
	})
}

func TestEinoReadOnlyBackendGrepStopsBeforeMaterializingDenseResults(t *testing.T) {
	_, err := boundedEinoGrepFile(context.Background(), "dense.txt", strings.Repeat("needle\n", maxEinoBackendMatches+1), &einofs.GrepRequest{Pattern: "needle"})
	if err == nil || !strings.Contains(err.Error(), "narrow request") {
		t.Fatalf("boundedEinoGrepFile dense result error = %v, want narrow request error", err)
	}
}

func TestEinoReadOnlyBackendGrepScansPastDenseMultilineContextMatches(t *testing.T) {
	content := strings.Repeat("x", maxEinoBackendMatches+1) + "\n" + strings.Repeat("needle\n", maxEinoBackendMatches+1)
	_, err := boundedEinoGrepFile(context.Background(), "dense.txt", content, &einofs.GrepRequest{
		Pattern:         "x|needle",
		EnableMultiline: true,
		BeforeLines:     1,
		AfterLines:      1,
	})
	if err == nil || !strings.Contains(err.Error(), "narrow request") {
		t.Fatalf("boundedEinoGrepFile dense multiline context error = %v, want narrow request error", err)
	}
}

func TestEinoReadOnlyBackendGrepRejectsExcessRawMultilineMatches(t *testing.T) {
	content := strings.Repeat("x", 10001) + "\nneedle\n"
	_, err := boundedEinoGrepFile(context.Background(), "dense.txt", content, &einofs.GrepRequest{
		Pattern:         "x|needle",
		EnableMultiline: true,
		BeforeLines:     1,
		AfterLines:      1,
	})
	if err == nil || !strings.Contains(err.Error(), "narrow request") {
		t.Fatalf("boundedEinoGrepFile raw match cap error = %v, want narrow request error", err)
	}
}

func TestEinoLineIndexMapsByteOffsetsLikeEino(t *testing.T) {
	content := "α\nβ\n"
	index := newEinoLineIndex(content)
	for _, test := range []struct {
		name   string
		offset int
		want   int
	}{
		{name: "start", offset: 0, want: 1},
		{name: "inside unicode rune", offset: 1, want: 1},
		{name: "on first newline", offset: 2, want: 1},
		{name: "after first newline", offset: 3, want: 2},
		{name: "inside second unicode rune", offset: 4, want: 2},
		{name: "on final newline", offset: 5, want: 2},
		{name: "after final newline", offset: 6, want: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := index.lineAtByteOffset(test.offset); got != test.want {
				t.Fatalf("lineAtByteOffset(%d) = %d, want %d", test.offset, got, test.want)
			}
		})
	}
}

func TestEinoReadOnlyBackendGrepMapsTenThousandMatchesAfterLongPrefix(t *testing.T) {
	prefix := strings.Repeat("x", 256<<10)
	matchLine := strings.Repeat("needle", maxEinoBackendRawMatches)
	content := prefix + "\n" + matchLine + "\nafter"

	matches, err := boundedEinoGrepFile(context.Background(), "adversarial.txt", content, &einofs.GrepRequest{
		Pattern:         "needle",
		EnableMultiline: true,
		BeforeLines:     1,
		AfterLines:      1,
	})
	if err != nil {
		t.Fatalf("boundedEinoGrepFile returned error: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("match count = %d, want 3 context-deduplicated lines", len(matches))
	}
	for index, want := range []struct {
		line       int
		contentLen int
	}{
		{line: 1, contentLen: len(prefix)},
		{line: 2, contentLen: len(matchLine)},
		{line: 3, contentLen: len("after")},
	} {
		if got := matches[index]; got.Path != "adversarial.txt" || got.Line != want.line || len(got.Content) != want.contentLen {
			t.Fatalf("match %d = {path:%q line:%d content bytes:%d}, want line %d with %d content bytes", index, got.Path, got.Line, len(got.Content), want.line, want.contentLen)
		}
	}
	if matches[0].Content != prefix || matches[1].Content != matchLine || matches[2].Content != "after" {
		t.Fatal("context-deduplicated match content differs from fixture")
	}
}

func TestEinoReadOnlyBackendGrepPreservesZeroWidthMultilineMatches(t *testing.T) {
	ctx := context.Background()
	content := "aa\nb\n"
	request := &einofs.GrepRequest{Pattern: "a*", EnableMultiline: true}
	reference := einofs.NewInMemoryBackend()
	if err := reference.Write(ctx, &einofs.WriteRequest{FilePath: "/zero.txt", Content: content}); err != nil {
		t.Fatalf("Write reference content returned error: %v", err)
	}
	want, err := reference.GrepRaw(ctx, request)
	if err != nil {
		t.Fatalf("reference GrepRaw returned error: %v", err)
	}
	for index := range want {
		want[index].Path = strings.TrimPrefix(want[index].Path, "/")
	}
	got, err := boundedEinoGrepFile(ctx, "zero.txt", content, request)
	if err != nil {
		t.Fatalf("boundedEinoGrepFile returned error: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("zero-width matches = %#v, want Eino %#v", got, want)
	}
}

func TestEinoReadOnlyBackendGrepMatchesEinoMultilineRegexParity(t *testing.T) {
	ctx := context.Background()
	content := "alpha\nβeta\n\nΩmega\n"
	for _, pattern := range []string{"^", "(?m)^", "$", "(?m)$", `\b`, `\B`, "β|Ω"} {
		t.Run(pattern, func(t *testing.T) {
			request := &einofs.GrepRequest{Pattern: pattern, EnableMultiline: true}
			reference := einofs.NewInMemoryBackend()
			if err := reference.Write(ctx, &einofs.WriteRequest{FilePath: "/parity.txt", Content: content}); err != nil {
				t.Fatalf("Write reference content returned error: %v", err)
			}
			want, err := reference.GrepRaw(ctx, request)
			if err != nil {
				t.Fatalf("reference GrepRaw returned error: %v", err)
			}
			for index := range want {
				want[index].Path = strings.TrimPrefix(want[index].Path, "/")
			}
			got, err := boundedEinoGrepFile(ctx, "parity.txt", content, request)
			if err != nil {
				t.Fatalf("boundedEinoGrepFile returned error: %v", err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("multiline matches = %#v, want Eino %#v", got, want)
			}
		})
	}
}

func TestEinoReadOnlyBackendGrepValidatesGlobBeforeFiltering(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(t *testing.T) *EinoReadOnlyBackend{
		"empty project": func(t *testing.T) *EinoReadOnlyBackend {
			backend, err := NewEinoReadOnlyBackend(NewFileStore(t.TempDir()), Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "empty"})
			if err != nil {
				t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
			}
			return backend
		},
		"zero path candidates": func(t *testing.T) *EinoReadOnlyBackend {
			store := NewFileStore(t.TempDir())
			scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
			if err := store.ApplyFiles(ctx, scope, []File{{Path: "src/app.go", Content: "package app\n"}}); err != nil {
				t.Fatalf("ApplyFiles returned error: %v", err)
			}
			backend, err := NewEinoReadOnlyBackend(store, scope)
			if err != nil {
				t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
			}
			return backend
		},
	} {
		t.Run(name, func(t *testing.T) {
			backend := setup(t)
			if _, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle", Path: "missing", Glob: "["}); err == nil {
				t.Fatal("GrepRaw invalid glob returned nil error")
			}
		})
	}
}

func TestEinoReadOnlyBackendGrepEnforcesFinalCapAcrossFiles(t *testing.T) {
	ctx := context.Background()
	newBackend := func(t *testing.T, secondCount int) *EinoReadOnlyBackend {
		t.Helper()
		store := NewFileStore(t.TempDir())
		scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "cap"}
		if err := store.ApplyFiles(ctx, scope, []File{
			{Path: "a.txt", Content: strings.Repeat("needle\n", 500)},
			{Path: "b.txt", Content: strings.Repeat("needle\n", secondCount)},
		}); err != nil {
			t.Fatalf("ApplyFiles returned error: %v", err)
		}
		backend, err := NewEinoReadOnlyBackend(store, scope)
		if err != nil {
			t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
		}
		return backend
	}
	t.Run("exactly 1000", func(t *testing.T) {
		matches, err := newBackend(t, 500).GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle"})
		if err != nil {
			t.Fatalf("GrepRaw returned error: %v", err)
		}
		if len(matches) != maxEinoBackendMatches || matches[0].Path != "a.txt" || matches[0].Line != 1 || matches[len(matches)-1].Path != "b.txt" || matches[len(matches)-1].Line != 500 {
			t.Fatalf("exact cap matches = first %#v last %#v len %d", matches[0], matches[len(matches)-1], len(matches))
		}
	})
	t.Run("1001", func(t *testing.T) {
		if _, err := newBackend(t, 501).GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle"}); err == nil || !strings.Contains(err.Error(), "narrow request") {
			t.Fatalf("GrepRaw 1001 error = %v, want narrow request error", err)
		}
	})
	t.Run("context overlap is deduplicated and ordered", func(t *testing.T) {
		store := NewFileStore(t.TempDir())
		scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "context"}
		if err := store.ApplyFiles(ctx, scope, []File{
			{Path: "a.txt", Content: "zero\nneedle one\nneedle two\nthree\n"},
			{Path: "b.txt", Content: "first\nneedle three\nlast\n"},
		}); err != nil {
			t.Fatalf("ApplyFiles returned error: %v", err)
		}
		backend, err := NewEinoReadOnlyBackend(store, scope)
		if err != nil {
			t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
		}
		matches, err := backend.GrepRaw(ctx, &einofs.GrepRequest{Pattern: "needle", BeforeLines: 1, AfterLines: 1})
		if err != nil {
			t.Fatalf("GrepRaw returned error: %v", err)
		}
		want := []einofs.GrepMatch{
			{Path: "a.txt", Line: 1, Content: "zero"},
			{Path: "a.txt", Line: 2, Content: "needle one"},
			{Path: "a.txt", Line: 3, Content: "needle two"},
			{Path: "a.txt", Line: 4, Content: "three"},
			{Path: "b.txt", Line: 1, Content: "first"},
			{Path: "b.txt", Line: 2, Content: "needle three"},
			{Path: "b.txt", Line: 3, Content: "last"},
		}
		if !slices.Equal(matches, want) {
			t.Fatalf("context matches = %#v, want %#v", matches, want)
		}
	})
}

func TestEinoReadOnlyBackendGrepValidatesPatternBeforeFiltering(t *testing.T) {
	ctx := context.Background()
	backend, err := NewEinoReadOnlyBackend(NewFileStore(t.TempDir()), Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "empty"})
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
	}
	for name, request := range map[string]*einofs.GrepRequest{
		"empty project": {Pattern: "["},
		"path":          {Pattern: "[", Path: "missing"},
		"glob":          {Pattern: "[", Glob: "*.does-not-exist"},
		"file type":     {Pattern: "[", FileType: "js"},
		"empty pattern": {Pattern: ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := backend.GrepRaw(ctx, request); err == nil {
				t.Fatalf("GrepRaw(%#v) returned nil error", request)
			}
		})
	}
}

func TestEinoReadOnlyBackendRejectsMutations(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "note.txt", Content: "before\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	backend, err := NewEinoReadOnlyBackend(store, scope)
	if err != nil {
		t.Fatalf("NewEinoReadOnlyBackend returned error: %v", err)
	}
	if err := backend.Write(ctx, &einofs.WriteRequest{FilePath: "note.txt", Content: "after\n"}); !errors.Is(err, errEinoReadOnlyWorkspace) {
		t.Fatalf("Write error = %v, want read-only error", err)
	}
	if err := backend.Edit(ctx, &einofs.EditRequest{FilePath: "note.txt", OldString: "before", NewString: "after"}); !errors.Is(err, errEinoReadOnlyWorkspace) {
		t.Fatalf("Edit error = %v, want read-only error", err)
	}
	read, err := store.ReadFile(ctx, scope, ReadOptions{Path: "note.txt"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "before\n" {
		t.Fatalf("content after mutation attempts = %q, want unchanged", read.Content)
	}
}

func einoFileInfoPaths(infos []einofs.FileInfo) []string {
	paths := make([]string, 0, len(infos))
	for _, info := range infos {
		paths = append(paths, info.Path)
	}
	return paths
}

func einoGrepPaths(matches []einofs.GrepMatch) []string {
	paths := make([]string, 0, len(matches))
	for _, match := range matches {
		paths = append(paths, match.Path)
	}
	return paths
}
