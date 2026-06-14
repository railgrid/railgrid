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

	gogithub "github.com/google/go-github/v66/github"

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

func TestShouldReuseHeadCommitWhenDesiredTreeAlreadyAtHead(t *testing.T) {
	parent := &gogithub.Commit{SHA: gogithub.String("abc123")}
	tree := &gogithub.Tree{SHA: gogithub.String("tree123")}
	if !shouldReuseHeadCommit(parent, tree, "tree123") {
		t.Fatal("shouldReuseHeadCommit returned false for matching head tree")
	}
	if shouldReuseHeadCommit(parent, tree, "other") {
		t.Fatal("shouldReuseHeadCommit returned true for a different tree")
	}
	if shouldReuseHeadCommit(nil, tree, "tree123") {
		t.Fatal("shouldReuseHeadCommit returned true without a parent commit")
	}
}

func TestCommitMessageWithIdempotencyKey(t *testing.T) {
	got := commitMessageWithIdempotencyKey("Initial app", "root:acme/demo")
	if got != "Initial app\n\nKedge-RepositoryCommit: root:acme/demo" {
		t.Fatalf("message = %q", got)
	}
	if got := commitMessageWithIdempotencyKey("Initial app", ""); got != "Initial app" {
		t.Fatalf("message with empty key = %q, want unchanged", got)
	}
}

func TestCommitMessageHasIdempotencyKey(t *testing.T) {
	message := "Initial app\n\nKedge-RepositoryCommit: root:acme/demo"
	if !commitMessageHasIdempotencyKey(message, "root:acme/demo") {
		t.Fatal("commitMessageHasIdempotencyKey returned false for matching trailer")
	}
	if commitMessageHasIdempotencyKey(message, "root:acme/other") {
		t.Fatal("commitMessageHasIdempotencyKey returned true for a different key")
	}
	if commitMessageHasIdempotencyKey("Initial app Kedge-RepositoryCommit: root:acme/demo", "root:acme/demo") {
		t.Fatal("commitMessageHasIdempotencyKey returned true for an inline substring")
	}
}
