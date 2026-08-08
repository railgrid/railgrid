/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)

// giteaStyleArchive serves a gzip tarball with a <root>/ prefix, the
// convention the non-github scaffold path expects.
func giteaStyleArchive(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		full := "starter-main/" + name
		if err := tw.WriteHeader(&tar.Header{Name: full, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	payload := buf.Bytes()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(payload)
	}))
}

func TestSeedProjectScaffoldPopulatesWorkspace(t *testing.T) {
	srv := giteaStyleArchive(t, map[string]string{
		"web/index.html":    "<!doctype html><title>hi</title>",
		"api/src/server.js": "export const x = 1",
		"README.md":         "ignored",
	})
	defer srv.Close()

	store := workspace.NewFileStore(t.TempDir())
	s := &Server{workspaces: store}
	id := identity{orgUUID: "org-1", workspaceUUID: "ws-1", user: "alice"}
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-1")}}
	info := projectTemplateInfo{
		Name:         "application",
		ScaffoldRepo: srv.URL + "/team/starter", // gitea-style → /archive/main.tar.gz
		Components: map[string]projectTemplateComponent{
			"web": {WorkspacePath: "web"},
			"api": {WorkspacePath: "api"},
		},
	}

	seeded, err := s.seedProjectScaffold(context.Background(), id, p, info)
	if err != nil {
		t.Fatalf("seedProjectScaffold: %v", err)
	}
	// README.md is dropped; web + api files kept.
	if seeded != 2 {
		t.Fatalf("seeded = %d, want 2 (README skipped)", seeded)
	}

	scope := projectWorkspaceScope(id, p)
	got, err := store.ListFiles(context.Background(), scope, workspace.ListOptions{})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	paths := map[string]bool{}
	for _, f := range got.Files {
		paths[f.Path] = true
	}
	if !paths["web/index.html"] || !paths["api/src/server.js"] {
		t.Fatalf("workspace missing scaffold files: %v", paths)
	}
	if paths["README.md"] {
		t.Fatalf("README.md should have been skipped: %v", paths)
	}
}

func TestSeedProjectScaffoldSkipsWhenNoScaffold(t *testing.T) {
	s := &Server{workspaces: workspace.NewFileStore(t.TempDir())}
	id := identity{orgUUID: "org-1", workspaceUUID: "ws-1"}
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-1")}}
	seeded, err := s.seedProjectScaffold(context.Background(), id, p, projectTemplateInfo{Name: "x"})
	if err != nil || seeded != 0 {
		t.Fatalf("no-scaffold: seeded=%d err=%v, want 0, nil", seeded, err)
	}
}

func TestSeedProjectScaffoldSkipsPopulatedWorkspace(t *testing.T) {
	srv := giteaStyleArchive(t, map[string]string{"web/index.html": "x"})
	defer srv.Close()
	store := workspace.NewFileStore(t.TempDir())
	s := &Server{workspaces: store}
	id := identity{orgUUID: "org-1", workspaceUUID: "ws-1"}
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-1")}}
	scope := projectWorkspaceScope(id, p)
	if err := store.ApplyFiles(context.Background(), scope, []workspace.File{{Path: "existing.txt", Content: "keep"}}); err != nil {
		t.Fatal(err)
	}
	info := projectTemplateInfo{Name: "x", ScaffoldRepo: srv.URL + "/team/starter", Components: map[string]projectTemplateComponent{"web": {WorkspacePath: "web"}}}
	seeded, err := s.seedProjectScaffold(context.Background(), id, p, info)
	if err != nil || seeded != 0 {
		t.Fatalf("populated workspace: seeded=%d err=%v, want 0 (no clobber)", seeded, err)
	}
}
