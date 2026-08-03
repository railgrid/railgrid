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
	"reflect"
	"sort"
	"testing"
)

// unifiedPatchScenario keeps parser/matcher fixtures close to the Codex
// apply-patch scenario shape: seed a complete input tree, apply one envelope,
// and compare the complete resulting snapshot rather than one touched file.
type unifiedPatchScenario struct {
	name        string
	initial     []File
	patch       string
	want        map[string]string
	wantCode    PatchErrorCode
	wantResults int
}

func TestUnifiedPatchCodexScenarioFixtures(t *testing.T) {
	const scopeProject = "scenario-project"
	scenarios := []unifiedPatchScenario{
		{
			name: "add_file",
			patch: `*** Begin Patch
*** Add File: src/new.txt
+created
*** End Patch`,
			want:        map[string]string{"src/new.txt": "created\n"},
			wantResults: 1,
		},
		{
			name:    "update_file",
			initial: []File{{Path: "src/app.txt", Content: "before\nkeep\n"}},
			patch: `*** Begin Patch
*** Update File: src/app.txt
@@
-before
+after
*** End Patch`,
			want:        map[string]string{"src/app.txt": "after\nkeep\n"},
			wantResults: 1,
		},
		{
			name:    "delete_file_only",
			initial: []File{{Path: "obsolete.txt", Content: "remove me\n"}},
			patch: `*** Begin Patch
*** Delete File: obsolete.txt
*** End Patch`,
			want:        map[string]string{},
			wantResults: 1,
		},
		{
			name:    "move_file",
			initial: []File{{Path: "old/name.txt", Content: "old content\n"}},
			patch: `*** Begin Patch
*** Update File: old/name.txt
*** Move to: new/name.txt
@@
-old content
+new content
*** End Patch`,
			want:        map[string]string{"new/name.txt": "new content\n"},
			wantResults: 1,
		},
		{
			name:    "end_of_file_insertion",
			initial: []File{{Path: "README.md", Content: "first line\n"}},
			patch: `*** Begin Patch
*** Update File: README.md
@@
+second line
*** End of File
*** End Patch`,
			want:        map[string]string{"README.md": "first line\nsecond line\n"},
			wantResults: 1,
		},
		{
			name:    "whitespace_and_unicode_matching",
			initial: []File{{Path: "src/labels.txt", Content: "label: “old”\nvalue: 1\u00a0\n"}},
			patch: `*** Begin Patch
*** Update File: src/labels.txt
@@
-label: "old"
+label: "new"
-value: 1
+value: 2
*** End Patch`,
			want:        map[string]string{"src/labels.txt": "label: \"new\"\nvalue: 2\n"},
			wantResults: 1,
		},
		{
			name:    "whitespace_padded_markers",
			initial: []File{{Path: "obsolete.txt", Content: "old\n"}},
			patch: `*** Begin Patch
  *** Add File: padded.txt   
+new
  *** Delete File: obsolete.txt  
  *** End Patch  `,
			want:        map[string]string{"padded.txt": "new\n"},
			wantResults: 2,
		},
		{
			name:    "whitespace_padded_update_marker",
			initial: []File{{Path: "padded-update.txt", Content: "old\n"}},
			patch: `*** Begin Patch
	*** Update File: padded-update.txt  
@@
-old
+new
*** End Patch`,
			want:        map[string]string{"padded-update.txt": "new\n"},
			wantResults: 1,
		},
	}

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: scopeProject, ProjectUID: "scenario-uid"}
			store := NewFileStore(t.TempDir())
			if err := store.ApplyFiles(context.Background(), scope, scenario.initial); err != nil {
				t.Fatalf("seed workspace: %v", err)
			}

			result, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: scenario.patch})
			if scenario.wantCode != "" {
				assertScenarioPatchError(t, err, scenario.wantCode)
				assertWorkspaceSnapshot(t, store, scope, snapshotFiles(scenario.initial))
				return
			}
			if err != nil {
				t.Fatalf("ApplyPatch: %v", err)
			}
			if result.Operation != "apply_patch" || len(result.Files) != scenario.wantResults {
				t.Fatalf("result = %#v, want apply_patch with %d file results", result, scenario.wantResults)
			}
			assertWorkspaceSnapshot(t, store, scope, scenario.want)
		})
	}
}

func TestUnifiedPatchCodexScenarioFailureCodes(t *testing.T) {
	scenarios := []unifiedPatchScenario{
		{
			name:     "add_target_exists",
			initial:  []File{{Path: "existing.txt", Content: "keep\n"}},
			patch:    "*** Begin Patch\n*** Add File: existing.txt\n+replace\n*** End Patch",
			wantCode: PatchErrorTargetExists,
		},
		{
			name:     "delete_target_missing",
			patch:    "*** Begin Patch\n*** Delete File: missing.txt\n*** End Patch",
			wantCode: PatchErrorTargetNotFound,
		},
		{
			name:     "update_context_missing",
			initial:  []File{{Path: "app.txt", Content: "present\n"}},
			patch:    "*** Begin Patch\n*** Update File: app.txt\n@@\n-missing\n+new\n*** End Patch",
			wantCode: PatchErrorContextNotFound,
		},
		{
			name:     "update_context_ambiguous",
			initial:  []File{{Path: "app.txt", Content: "same\nsame\n"}},
			patch:    "*** Begin Patch\n*** Update File: app.txt\n@@\n-same\n+new\n*** End Patch",
			wantCode: PatchErrorContextAmbiguous,
		},
	}

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "failure-project", ProjectUID: scenario.name}
			store := NewFileStore(t.TempDir())
			if err := store.ApplyFiles(context.Background(), scope, scenario.initial); err != nil {
				t.Fatalf("seed workspace: %v", err)
			}
			_, err := store.ApplyPatch(context.Background(), scope, PatchOptions{Patch: scenario.patch})
			assertScenarioPatchError(t, err, scenario.wantCode)
			assertWorkspaceSnapshot(t, store, scope, snapshotFiles(scenario.initial))
		})
	}
}

func assertScenarioPatchError(t *testing.T, err error, want PatchErrorCode) {
	t.Helper()
	var patchErr *PatchError
	if !errors.As(err, &patchErr) || patchErr.Code != want {
		t.Fatalf("error = %#v, want PatchError code %q", err, want)
	}
}

func snapshotFiles(files []File) map[string]string {
	snapshot := make(map[string]string, len(files))
	for _, file := range files {
		snapshot[file.Path] = file.Content
	}
	return snapshot
}

func assertWorkspaceSnapshot(t *testing.T, store *FileStore, scope Scope, want map[string]string) {
	t.Helper()
	list, err := store.ListFiles(context.Background(), scope, ListOptions{Limit: MaxListLimit})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	got := make(map[string]string, len(list.Files))
	for _, file := range list.Files {
		read, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: file.Path})
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", file.Path, err)
		}
		got[file.Path] = read.Content
	}
	if !reflect.DeepEqual(got, want) {
		gotPaths := make([]string, 0, len(got))
		wantPaths := make([]string, 0, len(want))
		for path := range got {
			gotPaths = append(gotPaths, path)
		}
		for path := range want {
			wantPaths = append(wantPaths, path)
		}
		sort.Strings(gotPaths)
		sort.Strings(wantPaths)
		t.Fatalf("workspace snapshot = %#v (paths=%v), want %#v (paths=%v)", got, gotPaths, want, wantPaths)
	}
}
