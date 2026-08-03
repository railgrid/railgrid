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
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFileStoreAppliesAtomicMultiFileContextualPatch(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := writeTestFiles(context.Background(), store, scope, []File{
		{Path: "src/app.go", Content: "package app\n\nfunc Theme() string {\n\treturn \"light\"\n}\n"},
		{Path: "scripts/start.sh", Content: "#!/bin/sh\necho old\n"},
		{Path: "obsolete.txt", Content: "remove me\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatalf("scopeDir returned error: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "scripts", "start.sh"), 0o755); err != nil {
		t.Fatalf("Chmod returned error: %v", err)
	}

	patch := `*** Begin Patch
*** Add File: nested/new.txt
+created
*** Update File: src/app.go
@@ func Theme() string {
-	return "light"
+	return "dark"
*** Update File: scripts/start.sh
*** Move to: scripts/run.sh
@@
-echo old
+echo new
*** Delete File: obsolete.txt
*** End Patch`
	result, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if result.Operation != "apply_patch" || len(result.Files) != 4 || result.Additions != 3 || result.Deletions != 3 {
		t.Fatalf("patch result = %#v", result)
	}
	if want := []string{"nested/new.txt", "src/app.go", "scripts/start.sh", "scripts/run.sh", "obsolete.txt"}; !reflect.DeepEqual(result.Paths, want) {
		t.Fatalf("paths = %#v, want %#v", result.Paths, want)
	}
	for _, want := range []string{"--- /dev/null", "+++ /dev/null", "--- a/scripts/start.sh", "+++ b/scripts/run.sh"} {
		if !strings.Contains(result.Patch, want) {
			t.Fatalf("patch diff missing %q:\n%s", want, result.Patch)
		}
	}
	assertWorkspaceContent(t, store, scope, "nested/new.txt", "created\n")
	assertWorkspaceContent(t, store, scope, "src/app.go", "package app\n\nfunc Theme() string {\n\treturn \"dark\"\n}\n")
	assertWorkspaceContent(t, store, scope, "scripts/run.sh", "#!/bin/sh\necho new\n")
	if _, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "scripts/start.sh"}); err == nil {
		t.Fatal("move source still exists")
	}
	if _, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "obsolete.txt"}); err == nil {
		t.Fatal("deleted file still exists")
	}
	info, err := os.Stat(filepath.Join(dir, "scripts", "run.sh"))
	if err != nil {
		t.Fatalf("Stat moved file returned error: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("moved file mode = %o, want 755", got)
	}

}

func TestFileStoreRollsBackAppliedFilesWhenLaterWriteFails(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := writeTestFiles(context.Background(), store, scope, []File{
		{Path: "a.txt", Content: "a before\n"},
		{Path: "b.txt", Content: "b before\n"},
	}); err != nil {
		t.Fatalf("seed files: %v", err)
	}

	writeFile := store.patchWriteFile
	writes := 0
	store.patchWriteFile = func(dir, target string, content []byte, mode fs.FileMode, createOnly bool) error {
		writes++
		if writes == 2 {
			return errors.New("injected second write failure")
		}
		return writeFile(dir, target, content, mode, createOnly)
	}

	result, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: `*** Begin Patch
*** Update File: a.txt
@@
-a before
+a after
*** Update File: b.txt
@@
-b before
+b after
*** End Patch`})
	if err == nil {
		t.Fatal("ApplyPatch succeeded, want injected apply failure")
	}
	var patchErr *PatchError
	if !errors.As(err, &patchErr) {
		t.Fatalf("ApplyPatch error = %T %v, want *PatchError", err, err)
	}
	if patchErr.Code != PatchErrorApplyFailed {
		t.Fatalf("patch error code = %q, want %q", patchErr.Code, PatchErrorApplyFailed)
	}
	if len(patchErr.ActualChanges) != 0 {
		t.Fatalf("actual changes = %#v, want complete rollback", patchErr.ActualChanges)
	}
	if len(result.Files) != 0 || len(result.Paths) != 0 {
		t.Fatalf("result = %#v, want no surviving mutations", result)
	}
	assertWorkspaceContent(t, store, scope, "a.txt", "a before\n")
	assertWorkspaceContent(t, store, scope, "b.txt", "b before\n")
}

func TestContextualPatchUsesUniqueTieredMatchingAndPreservesLineEndings(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	before := "package demo\r\n\r\nfunc first() {\r\n  theme := \"light\"  \r\n}\r\n\r\nfunc second() {\r\n  label := “old”\r\n}"
	if err := writeTestFiles(context.Background(), store, scope, []File{{Path: "app.go", Content: before}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	patch := `*** Begin Patch
*** Update File: app.go
@@ func first() {
-  theme := "light"
+  theme := "dark"
@@ func second() {
-  label := "old"
+  label := "new"
 }
*** End of File
*** End Patch`
	result, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if result.Replacements != 2 {
		t.Fatalf("replacements = %d, want 2", result.Replacements)
	}
	read, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "app.go"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	want := "package demo\r\n\r\nfunc first() {\r\n  theme := \"dark\"\r\n}\r\n\r\nfunc second() {\r\n  label := \"new\"\r\n}"
	if read.Content != want {
		t.Fatalf("content = %q, want %q", read.Content, want)
	}
	if strings.HasSuffix(read.Content, "\r\n") {
		t.Fatal("patch added a final newline to a file that did not have one")
	}
}

func TestContextualPatchNormalizesIndependentHunksIntoSourceOrder(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := writeTestFiles(context.Background(), store, scope, []File{{
		Path:    "app.txt",
		Content: "header\nfirst old\nmiddle\nsecond old\nfooter\n",
	}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}

	patch := `*** Begin Patch
*** Update File: app.txt
@@ middle
-second old
+second new
@@ header
-first old
+first new
*** End Patch`
	result, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if result.Replacements != 2 {
		t.Fatalf("replacements = %d, want 2", result.Replacements)
	}
	assertWorkspaceContent(t, store, scope, "app.txt", "header\nfirst new\nmiddle\nsecond new\nfooter\n")
}

func TestContextualPatchNormalizesRepeatedAnchorAgainstOriginalSnapshot(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := writeTestFiles(context.Background(), store, scope, []File{{
		Path:    "app.jsx",
		Content: "<header>\n  <h1>Operations Dashboard</h1>\n  <p>Old subtitle</p>\n</header>\n",
	}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}

	patch := `*** Begin Patch
*** Update File: app.jsx
@@   <h1>Operations Dashboard</h1>
-  <p>Old subtitle</p>
+  <p>New subtitle</p>
@@   <h1>Operations Dashboard</h1>
+  <h1>Fleet Pulse</h1>
-  <h1>Operations Dashboard</h1>
*** End Patch`
	result, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if result.Replacements != 2 {
		t.Fatalf("replacements = %d, want 2", result.Replacements)
	}
	assertWorkspaceContent(t, store, scope, "app.jsx", "<header>\n  <h1>Fleet Pulse</h1>\n  <p>New subtitle</p>\n</header>\n")
}

func TestContextualPatchNormalizesRepeatedAnchorInSingleHunk(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := writeTestFiles(context.Background(), store, scope, []File{{
		Path:    "app.jsx",
		Content: "<header>\n  <h1>Fleet Pulse</h1>\n  <p>Old subtitle</p>\n</header>\n",
	}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}

	patch := `*** Begin Patch
*** Update File: app.jsx
@@   <h1>Fleet Pulse</h1>
-  <h1>Fleet Pulse</h1>
-  <p>Old subtitle</p>
+  <h1>Fleet Command</h1>
+  <p>New subtitle</p>
*** End Patch`
	result, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if result.Replacements != 1 {
		t.Fatalf("replacements = %d, want 1", result.Replacements)
	}
	assertWorkspaceContent(t, store, scope, "app.jsx", "<header>\n  <h1>Fleet Command</h1>\n  <p>New subtitle</p>\n</header>\n")
}

func TestContextualPatchAcceptsUnprefixedExactContextLine(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := writeTestFiles(context.Background(), store, scope, []File{{
		Path:    "app.css",
		Content: ".header-sub {\n  color: gray;\n  margin-top: 4px;\n}\n\n.kpi {\n  display: grid;\n}\n",
	}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}

	patch := `*** Begin Patch
*** Update File: app.css
@@ .header-sub {
   color: gray;
   margin-top: 4px;
}
+
+.header-badge {
+  color: blue;
+}
*** End Patch`
	if _, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch}); err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	assertWorkspaceContent(t, store, scope, "app.css", ".header-sub {\n  color: gray;\n  margin-top: 4px;\n}\n\n.header-badge {\n  color: blue;\n}\n\n.kpi {\n  display: grid;\n}\n")
}

func TestContextualPatchRejectsAmbiguousAndMissingContextWithTypedErrors(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := writeTestFiles(context.Background(), store, scope, []File{{Path: "app.txt", Content: "same\nsame\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	tests := []struct {
		name      string
		patch     string
		wantCode  PatchErrorCode
		wantMatch int
	}{
		{
			name: "ambiguous",
			patch: `*** Begin Patch
*** Update File: app.txt
@@
-same
+changed
*** End Patch`,
			wantCode:  PatchErrorContextAmbiguous,
			wantMatch: 2,
		},
		{
			name: "missing",
			patch: `*** Begin Patch
*** Update File: app.txt
@@
-missing
+changed
*** End Patch`,
			wantCode: PatchErrorContextNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: tt.patch})
			var patchErr *PatchError
			if !errors.As(err, &patchErr) || patchErr.Code != tt.wantCode || patchErr.Path != "app.txt" || patchErr.Matches != tt.wantMatch {
				t.Fatalf("error = %#v, want code=%q path=app.txt matches=%d", err, tt.wantCode, tt.wantMatch)
			}
			if tt.wantCode == PatchErrorContextNotFound && !strings.Contains(patchErr.Message, "failed to find the expected lines after line 0:\nmissing") {
				t.Fatalf("missing-context error did not preserve exact expected lines: %q", patchErr.Message)
			}
			if patchErr.ExpectedContext == "" || patchErr.ActualContext == "" {
				t.Fatalf("patch context = expected %q actual %q, want bounded expected and current excerpts", patchErr.ExpectedContext, patchErr.ActualContext)
			}
			if len([]rune(patchErr.ExpectedContext)) > 2_001 || len([]rune(patchErr.ActualContext)) > 2_001 {
				t.Fatalf("patch context was not bounded: expected=%d actual=%d", len([]rune(patchErr.ExpectedContext)), len([]rune(patchErr.ActualContext)))
			}
			assertWorkspaceContent(t, store, scope, "app.txt", "same\nsame\n")
		})
	}
}

func TestContextualPatchBodyDisambiguatesRepeatedLiteralAnchor(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := writeTestFiles(context.Background(), store, scope, []File{{Path: "app.txt", Content: "section\nfirst\nsection\nunique target\nafter\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}

	patch := `*** Begin Patch
*** Update File: app.txt
@@ section
 unique target
+inserted
 after
*** End Patch`
	if _, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch}); err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	assertWorkspaceContent(t, store, scope, "app.txt", "section\nfirst\nsection\nunique target\ninserted\nafter\n")
}

func TestContextualPatchAppliesAllHunksWhenLaterAnchorRepeats(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	source := `function App() {
  const toggleTheme = () => setDark((d) => !d);

  const showStreak = streak.length >= 3;

  return (
    <main>
      <section className="counter-section">
        <span>Counter</span>
      </section>

      <section className="summary-section">
        <span>Summary</span>
      </section>

      <section className="controls-section">
        <button>Increment</button>
      </section>
    </main>
  );
}
`
	if err := writeTestFiles(context.Background(), store, scope, []File{{Path: "App.jsx", Content: source}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}

	patch := `*** Begin Patch
*** Update File: App.jsx
@@   const toggleTheme = () => setDark((d) => !d);
+  useEffect(() => {
+    document.addEventListener("keydown", handleKeyDown);
+    return () => document.removeEventListener("keydown", handleKeyDown);
+  }, [handleKeyDown]);
+
   const showStreak = streak.length >= 3;
@@         </section>
+      <div className="keyboard-hints">Keyboard shortcuts</div>
+
       <section className="controls-section">
*** End Patch`
	if _, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch}); err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	expected := `function App() {
  const toggleTheme = () => setDark((d) => !d);

  useEffect(() => {
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [handleKeyDown]);

  const showStreak = streak.length >= 3;

  return (
    <main>
      <section className="counter-section">
        <span>Counter</span>
      </section>

      <section className="summary-section">
        <span>Summary</span>
      </section>

      <div className="keyboard-hints">Keyboard shortcuts</div>

      <section className="controls-section">
        <button>Increment</button>
      </section>
    </main>
  );
}
`
	assertWorkspaceContent(t, store, scope, "App.jsx", expected)
}

func TestContextualPatchRejectsRepeatedLiteralAnchorWithoutBodyContext(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := writeTestFiles(context.Background(), store, scope, []File{{Path: "app.txt", Content: "section\nfirst\nsection\nsecond\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}

	patch := `*** Begin Patch
*** Update File: app.txt
@@ section
+inserted
*** End Patch`
	_, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch})
	var patchErr *PatchError
	if !errors.As(err, &patchErr) || patchErr.Code != PatchErrorContextAmbiguous {
		t.Fatalf("error = %#v, want repeated-anchor ambiguity", err)
	}
	assertWorkspaceContent(t, store, scope, "app.txt", "section\nfirst\nsection\nsecond\n")
}

func TestContextualPatchRejectsNumericUnifiedDiffHunkHeader(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := writeTestFiles(context.Background(), store, scope, []File{{Path: "app.txt", Content: "old\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}

	patch := `*** Begin Patch
*** Update File: app.txt
@@ -1,1 +1,1 @@
-old
+new
*** End Patch`
	_, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch})
	var patchErr *PatchError
	if !errors.As(err, &patchErr) || patchErr.Code != PatchErrorInvalidPatch {
		t.Fatalf("error = %#v, want invalid_patch", err)
	}
	if !strings.Contains(err.Error(), "numeric unified-diff hunk headers are not supported") {
		t.Fatalf("error = %q, want targeted numeric-header guidance", err)
	}
	assertWorkspaceContent(t, store, scope, "app.txt", "old\n")
}

func TestContextualPatchPreflightsEveryOperationBeforeMutation(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	patch := `*** Begin Patch
*** Add File: first.txt
+must not survive
*** Update File: missing.txt
@@
-old
+new
*** End Patch`
	_, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch})
	var patchErr *PatchError
	if !errors.As(err, &patchErr) || patchErr.Code != PatchErrorTargetNotFound || patchErr.Path != "missing.txt" {
		t.Fatalf("error = %#v, want target_not_found for missing.txt", err)
	}
	if _, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "first.txt"}); err == nil {
		t.Fatal("an earlier Add File was applied before preflight completed")
	}
}

func TestContextualPatchRejectsUnsafeAndConflictingPaths(t *testing.T) {
	tests := []struct {
		name  string
		patch string
		code  PatchErrorCode
	}{
		{
			name: "traversal",
			patch: `*** Begin Patch
*** Add File: ../escape.txt
+bad
*** End Patch`,
			code: PatchErrorInvalidPatch,
		},
		{
			name: "same path twice",
			patch: `*** Begin Patch
*** Add File: duplicate.txt
+first
*** Delete File: duplicate.txt
*** End Patch`,
			code: PatchErrorInvalidPatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PatchPaths(tt.patch)
			var patchErr *PatchError
			if !errors.As(err, &patchErr) || patchErr.Code != tt.code {
				t.Fatalf("error = %#v, want %q", err, tt.code)
			}
		})
	}
}

func TestContextualPatchRejectsEmptyEnvelopeAsInvalidPatch(t *testing.T) {
	store := NewFileStore(t.TempDir())
	_, err := store.ApplyPatch(context.Background(), Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}, PatchOptions{})
	var patchErr *PatchError
	if !errors.As(err, &patchErr) || patchErr.Code != PatchErrorInvalidPatch {
		t.Fatalf("error = %#v, want invalid_patch", err)
	}
}

func TestContextualPatchRejectsSymlinkTargetWithTypedConflict(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatalf("scopeDir returned error: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	external := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(external, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Symlink(external, filepath.Join(dir, "linked.txt")); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}
	_, err = store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: `*** Begin Patch
*** Update File: linked.txt
@@
-outside
+changed
*** End Patch`})
	var patchErr *PatchError
	if !errors.As(err, &patchErr) || patchErr.Code != PatchErrorWorkspaceConflict || patchErr.Path != "linked.txt" {
		t.Fatalf("error = %#v, want workspace_conflict for linked.txt", err)
	}
	raw, err := os.ReadFile(external)
	if err != nil || string(raw) != "outside\n" {
		t.Fatalf("external content = %q, err = %v", raw, err)
	}
}

func TestPatchPathsIncludesMoveSourceAndDestination(t *testing.T) {
	patch := `*** Begin Patch
*** Add File: z.txt
+z
*** Update File: a.txt
*** Move to: b.txt
@@
-a
+b
*** End Patch`
	paths, err := PatchPaths(patch)
	if err != nil {
		t.Fatalf("PatchPaths returned error: %v", err)
	}
	if want := []string{"a.txt", "b.txt", "z.txt"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
	readPaths, err := PatchReadPaths(patch)
	if err != nil {
		t.Fatalf("PatchReadPaths returned error: %v", err)
	}
	if want := []string{"a.txt"}; !reflect.DeepEqual(readPaths, want) {
		t.Fatalf("read paths = %#v, want %#v", readPaths, want)
	}
}

func TestContextualPatchTreatsIndentedMarkerTextAsFileContent(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	before := "*** Move to: literal.txt\n*** Add File: literal.txt\n*** Update File: literal.txt\n*** Delete File: literal.txt\nold\n"
	if err := writeTestFiles(context.Background(), store, scope, []File{{Path: "README.md", Content: before}}); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: README.md
@@
 *** Move to: literal.txt
 *** Add File: literal.txt
 *** Update File: literal.txt
 *** Delete File: literal.txt
-old
+new
*** End Patch`
	if _, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch}); err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	assertWorkspaceContent(t, store, scope, "README.md", strings.Replace(before, "old\n", "new\n", 1))
}

func TestContextualPatchPreservesIndentedMarkerLookingAddFileContent(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	patch := `*** Begin Patch
*** Add File: notes.txt
+first line
+ *** Update File: example
+last line
*** End Patch`

	result, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if result.Operation != "apply_patch" || len(result.Files) != 1 {
		t.Fatalf("patch result = %#v, want one Add File operation", result)
	}
	assertWorkspaceContent(t, store, scope, "notes.txt", "first line\n *** Update File: example\nlast line\n")
	if _, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "example"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("marker-looking content created an unexpected operation target: %v", err)
	}
}

func TestValidateCommittablePatchAcceptsAllWorkspaceOperations(t *testing.T) {
	for name, patch := range map[string]string{
		"delete": `*** Begin Patch
*** Delete File: old.txt
*** End Patch`,
		"move": `*** Begin Patch
*** Update File: old.txt
*** Move to: new.txt
@@
-old
+new
*** End Patch`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCommittablePatch(patch); err != nil {
				t.Fatalf("ValidateCommittablePatch returned %v", err)
			}
		})
	}
	if err := ValidateCommittablePatch(`*** Begin Patch
*** Add File: new.txt
+new
*** Update File: existing.txt
@@
-old
+new
*** End Patch`); err != nil {
		t.Fatalf("add/update patch rejected: %v", err)
	}
}

func TestContextualPatchRejectsUnprefixedAddFileContent(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	patch := `*** Begin Patch
*** Add File: src/App.css
:root {
  --surface: white;
}
*** End Patch`

	_, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch})
	var patchErr *PatchError
	if !errors.As(err, &patchErr) || patchErr.Code != PatchErrorInvalidPatch ||
		!strings.Contains(patchErr.Message, "Add File content lines must begin with '+'") {
		t.Fatalf("ApplyPatch error = %#v, want invalid unprefixed Add File content", err)
	}
	if _, readErr := store.ReadFile(context.Background(), scope, ReadOptions{Path: "src/App.css"}); !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("rejected Add File mutated workspace: %v", readErr)
	}
}

func TestContextualPatchPreservesPrefixedProtocolLookingAddFileContent(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	patch := "*** Begin Patch\n*** Add File: src/App.jsx\n" +
		"+@@ import React from 'react';\n" +
		"+*** Begin Patch\n" +
		"+*** End Patch\n" +
		"+*** Update File: example\n*** End Patch"

	result, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: patch})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if result.Operation != "apply_patch" || len(result.Files) != 1 {
		t.Fatalf("patch result = %#v, want one Add File operation", result)
	}
	assertWorkspaceContent(t, store, scope, "src/App.jsx", "@@ import React from 'react';\n*** Begin Patch\n*** End Patch\n*** Update File: example\n")
}

func assertWorkspaceContent(t *testing.T, store *FileStore, scope Scope, path, want string) {
	t.Helper()
	read, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: path})
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", path, err)
	}
	if read.Content != want {
		t.Fatalf("content of %q = %q, want %q", path, read.Content, want)
	}
}
