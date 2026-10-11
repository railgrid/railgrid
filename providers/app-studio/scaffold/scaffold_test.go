/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package scaffold

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/railgrid/provider-app-studio/workspace"
)

func TestArchiveURLsGitHub(t *testing.T) {
	urls, err := ArchiveURLs("github.com/railgrid/scaffold-application", "v0.3.0")
	if err != nil {
		t.Fatalf("ArchiveURLs: %v", err)
	}
	want := []string{
		"https://codeload.github.com/railgrid/scaffold-application/tar.gz/refs/tags/v0.3.0",
		"https://codeload.github.com/railgrid/scaffold-application/tar.gz/refs/heads/v0.3.0",
		"https://codeload.github.com/railgrid/scaffold-application/tar.gz/v0.3.0",
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

func TestFetchKeepsTotalContentWithinTransactionBound(t *testing.T) {
	type archiveFile struct {
		name string
		data []byte
	}
	files := make([]archiveFile, 0, 10)
	for index := range 7 {
		files = append(files, archiveFile{name: fmt.Sprintf("starter-main/web/file-%d.txt", index), data: []byte(strings.Repeat("x", maxFileBytes))})
	}
	files = append(files,
		archiveFile{name: "starter-main/web/partial.txt", data: []byte(strings.Repeat("p", maxFileBytes/2))},
		archiveFile{name: "starter-main/web/overflow.txt", data: []byte(strings.Repeat("o", maxFileBytes))},
		archiveFile{name: "starter-main/web/fits.txt", data: []byte("fits in remaining budget")},
	)
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	writer := tar.NewWriter(compressed)
	for _, file := range files {
		if err := writer.WriteHeader(&tar.Header{Name: file.name, Mode: 0o644, Size: int64(len(file.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	archiveBytes := append([]byte(nil), archive.Bytes()...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archiveBytes)
	}))
	defer server.Close()

	got, err := Fetch(context.Background(), server.URL+"/team/starter", "main")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	paths := make(map[string]bool, len(got))
	total := 0
	for _, file := range got {
		paths[file.Path] = true
		total += len(file.Content)
	}
	if total > maxTotalBytes {
		t.Fatalf("fetched content totals %d bytes, exceeds transaction bound %d", total, maxTotalBytes)
	}
	if paths["web/overflow.txt"] || !paths["web/fits.txt"] {
		t.Fatalf("archive budget paths = %v; oversized file must be skipped and a later fitting file retained", paths)
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
