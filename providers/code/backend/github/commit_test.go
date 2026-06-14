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

package github

import (
	"testing"

	"github.com/faroshq/provider-code/backend"
)

func TestCleanRepositoryPath(t *testing.T) {
	got, err := cleanRepositoryPath("src\\main.go")
	if err != nil {
		t.Fatalf("cleanRepositoryPath returned error: %v", err)
	}
	if got != "src/main.go" {
		t.Fatalf("clean path = %q, want src/main.go", got)
	}

	for _, path := range []string{"", "/etc/passwd", "../secret", "src/../secret"} {
		if _, err := cleanRepositoryPath(path); err == nil {
			t.Fatalf("cleanRepositoryPath(%q) returned nil error", path)
		}
	}
}

func TestGitTreeEntries(t *testing.T) {
	entries, paths, err := gitTreeEntries([]backend.RepositoryCommitFile{
		{Path: "b.txt", Content: "b"},
		{Path: "a.txt", Content: "a"},
	})
	if err != nil {
		t.Fatalf("gitTreeEntries returned error: %v", err)
	}
	if len(entries) != 2 || len(paths) != 2 {
		t.Fatalf("entry count = %d paths = %d, want 2/2", len(entries), len(paths))
	}
	if paths[0] != "a.txt" || paths[1] != "b.txt" {
		t.Fatalf("paths = %#v, want sorted paths", paths)
	}
	if entries[0].GetContent() != "a" || entries[1].GetContent() != "b" {
		t.Fatalf("entries were not ordered with their content")
	}
}

func TestGitTreeEntriesRejectsDuplicatePaths(t *testing.T) {
	_, _, err := gitTreeEntries([]backend.RepositoryCommitFile{
		{Path: "src/../app.go", Content: "a"},
		{Path: "app.go", Content: "b"},
	})
	if err == nil {
		t.Fatal("gitTreeEntries returned nil error for duplicate normalized paths")
	}
}
