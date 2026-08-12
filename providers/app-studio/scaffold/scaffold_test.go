/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package scaffold

import (
	"testing"

	"github.com/faroshq/provider-app-studio/workspace"
)

func TestArchiveURLsGitHub(t *testing.T) {
	urls, err := ArchiveURLs("github.com/faroshq/faros-scaffold-application", "v0.3.0")
	if err != nil {
		t.Fatalf("ArchiveURLs: %v", err)
	}
	want := []string{
		"https://codeload.github.com/faroshq/faros-scaffold-application/tar.gz/refs/tags/v0.3.0",
		"https://codeload.github.com/faroshq/faros-scaffold-application/tar.gz/refs/heads/v0.3.0",
		"https://codeload.github.com/faroshq/faros-scaffold-application/tar.gz/v0.3.0",
	}
	if len(urls) != len(want) {
		t.Fatalf("urls = %v, want %v", urls, want)
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Fatalf("urls[%d] = %q, want %q", i, urls[i], want[i])
		}
	}
}

func TestArchiveURLsGitHubDefaultRef(t *testing.T) {
	urls, err := ArchiveURLs("https://github.com/owner/repo.git", "")
	if err != nil {
		t.Fatalf("ArchiveURLs: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://codeload.github.com/owner/repo/tar.gz/refs/heads/main" {
		t.Fatalf("default-ref urls = %v", urls)
	}
}

func TestArchiveURLsOtherHost(t *testing.T) {
	urls, err := ArchiveURLs("git.example.com/team/starter", "main")
	if err != nil {
		t.Fatalf("ArchiveURLs: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://git.example.com/team/starter/archive/main.tar.gz" {
		t.Fatalf("gitea-style urls = %v", urls)
	}
}

func TestArchiveURLsInvalid(t *testing.T) {
	if _, err := ArchiveURLs("github.com/too/many/parts", "v1"); err == nil {
		t.Fatal("expected error for non owner/repo github path")
	}
}

func TestSkippedPath(t *testing.T) {
	cases := map[string]bool{
		"LICENSE":                  true,
		"README.md":                true,
		".git/config":              true,
		".github/workflows/ci.yml": false, // CI is deliberately kept
		"web/index.html":           false,
		"api/src/server.js":        false,
	}
	for p, want := range cases {
		if got := skippedPath(p); got != want {
			t.Errorf("skippedPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestCheckLayout(t *testing.T) {
	components := map[string]string{"web": "web", "api": "api"}

	// Matching layout passes.
	ok := []workspace.File{{Path: "web/index.html"}, {Path: "api/src/server.js"}}
	if err := CheckLayout(components, ok); err != nil {
		t.Fatalf("matching layout rejected: %v", err)
	}

	// No file under any component directory fails with a helpful message.
	bad := []workspace.File{{Path: "src/main.go"}, {Path: "README.md"}}
	if err := CheckLayout(components, bad); err == nil {
		t.Fatal("mismatched layout accepted")
	}

	// A root component ("." claims the whole workspace) always passes.
	if err := CheckLayout(map[string]string{"app": "."}, bad); err != nil {
		t.Fatalf("root component rejected a flat layout: %v", err)
	}

	// Empty inputs are no-ops.
	if err := CheckLayout(components, nil); err != nil {
		t.Fatalf("empty files rejected: %v", err)
	}
	if err := CheckLayout(nil, ok); err != nil {
		t.Fatalf("empty components rejected: %v", err)
	}
}
