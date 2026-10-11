/*
Copyright 2026 The Railgrid Authors.

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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/workspace"
)

// giteaStyleArchive serves a gzip tarball with a <root>/ prefix, the
// convention the non-github scaffold path expects.
func giteaStyleArchive(t *testing.T, files map[string]string) *httptest.Server {
	return giteaStyleArchiveWithHook(t, files, nil)
}

func giteaStyleArchiveWithHook(t *testing.T, files map[string]string, onRequest func()) *httptest.Server {
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
	var requested bool
	var requestMu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestMu.Lock()
		firstRequest := !requested
		requested = true
		requestMu.Unlock()
		if firstRequest && onRequest != nil {
			onRequest()
		}
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
	s := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, workspaces: store}
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
	s := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, workspaces: workspace.NewFileStore(t.TempDir())}
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
	s := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, workspaces: store}
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

// A prompt-only project has no template at creation; by the time the assistant
// selects one the reconciler has hydrated the git host's autoInit README into
// the workspace. That boilerplate must not count as content, or the scaffold —
// and with it the build workflow promotion depends on — is never seeded.
func TestSeedProjectScaffoldSeedsOverRepositoryBoilerplate(t *testing.T) {
	srv := giteaStyleArchive(t, map[string]string{
		"web/index.html":               "<!doctype html>",
		".github/workflows/build.yaml": "on: push",
		".gitignore":                   "node_modules\n",
		"README.md":                    "scaffold readme (skipped)",
	})
	defer srv.Close()
	store := workspace.NewFileStore(t.TempDir())
	s := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, workspaces: store}
	id := identity{orgUUID: "org-1", workspaceUUID: "ws-1"}
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-1")}}
	scope := projectWorkspaceScope(id, p)
	if err := store.ApplyFiles(context.Background(), scope, []workspace.File{
		{Path: "README.md", Content: "Created by App Studio for Demo"},
		{Path: "LICENSE", Content: "MIT"},
		{Path: ".gitignore", Content: "generated\n"},
	}); err != nil {
		t.Fatal(err)
	}
	info := projectTemplateInfo{Name: "simple-webapp", ScaffoldRepo: srv.URL + "/team/starter", Components: map[string]projectTemplateComponent{"app": {WorkspacePath: "."}}}
	seeded, err := s.seedProjectScaffold(context.Background(), id, p, info)
	if err != nil {
		t.Fatalf("seedProjectScaffold: %v", err)
	}
	if seeded != 3 {
		t.Fatalf("seeded = %d, want 3 (index, workflow, .gitignore; scaffold README skipped)", seeded)
	}
	got, err := store.ListFiles(context.Background(), scope, workspace.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, f := range got.Files {
		paths[f.Path] = true
	}
	for _, want := range []string{"README.md", "LICENSE", ".gitignore", "web/index.html", ".github/workflows/build.yaml"} {
		if !paths[want] {
			t.Errorf("workspace missing %s after seed: %v", want, paths)
		}
	}
	readme, err := store.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "README.md"})
	if err != nil || readme.Content != "Created by App Studio for Demo" {
		t.Fatalf("README after seed = %#v, err=%v; the git host's README must survive", readme, err)
	}
	ignore, err := store.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: ".gitignore"})
	if err != nil || ignore.Content != "node_modules\n" {
		t.Fatalf(".gitignore after seed = %#v, err=%v; the scaffold's copy wins", ignore, err)
	}
}

func TestSeedProjectScaffoldDoesNotReplaceConcurrentBoilerplateEdit(t *testing.T) {
	ctx := context.Background()
	store := workspace.NewFileStore(t.TempDir())
	s := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, workspaces: store}
	id := identity{orgUUID: "org-1", workspaceUUID: "ws-1"}
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-1")}}
	scope := projectWorkspaceScope(id, p)
	if err := store.ApplyFiles(ctx, scope, []workspace.File{{Path: ".gitignore", Content: "generated\n"}}); err != nil {
		t.Fatal(err)
	}
	srv := giteaStyleArchiveWithHook(t, map[string]string{
		"web/index.html": "scaffold source",
		".gitignore":     "scaffold ignore\n",
	}, func() {
		if err := store.ApplyFiles(ctx, scope, []workspace.File{{Path: ".gitignore", Content: "user edit\n"}}); err != nil {
			t.Errorf("concurrent .gitignore edit: %v", err)
		}
	})
	defer srv.Close()
	info := projectTemplateInfo{Name: "x", ScaffoldRepo: srv.URL + "/team/starter", Components: map[string]projectTemplateComponent{"web": {WorkspacePath: "web"}}}
	if _, err := s.seedProjectScaffold(ctx, id, p, info); err == nil {
		t.Fatal("stale scaffold overwrite succeeded; want version conflict")
	} else {
		var conflict *workspace.MutationError
		if !errors.As(err, &conflict) || conflict.Code != workspace.MutationErrorStale || len(conflict.ChangedFiles) != 1 || conflict.ChangedFiles[0] != ".gitignore" {
			t.Fatalf("scaffold conflict = %#v, want structured stale .gitignore conflict", err)
		}
	}
	ignore, err := store.ReadFile(ctx, scope, workspace.ReadOptions{Path: ".gitignore"})
	if err != nil || ignore.Content != "user edit\n" {
		t.Fatalf("concurrent .gitignore after seed = %#v err=%v", ignore, err)
	}
	if _, err := store.ReadFile(ctx, scope, workspace.ReadOptions{Path: "web/index.html"}); err == nil {
		t.Fatal("partial scaffold survived an all-or-nothing conflict")
	}
}

func TestSeedProjectScaffoldKeepsSupportedFileCountAtomic(t *testing.T) {
	files := make(map[string]string, 129)
	for index := range 129 {
		files[fmt.Sprintf("web/file-%03d.txt", index)] = "source"
	}
	srv := giteaStyleArchive(t, files)
	defer srv.Close()
	store := workspace.NewFileStore(t.TempDir())
	s := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, workspaces: store}
	id := identity{orgUUID: "org-1", workspaceUUID: "ws-1"}
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-1")}}
	info := projectTemplateInfo{Name: "x", ScaffoldRepo: srv.URL + "/team/starter", Components: map[string]projectTemplateComponent{"app": {WorkspacePath: "."}}}
	seeded, err := s.seedProjectScaffold(context.Background(), id, p, info)
	if err != nil || seeded != len(files) {
		t.Fatalf("seedProjectScaffold seeded=%d err=%v; want all %d files in one transaction", seeded, err, len(files))
	}
}

func TestSeedMissingBuildWorkflowDoesNotReplaceConcurrentFile(t *testing.T) {
	ctx := context.Background()
	store := workspace.NewFileStore(t.TempDir())
	s := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, workspaces: store}
	id := identity{orgUUID: "org-1", workspaceUUID: "ws-1"}
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-1")}}
	scope := projectWorkspaceScope(id, p)
	want := ".github/workflows/build.yaml"
	if err := store.ApplyFiles(ctx, scope, []workspace.File{{Path: "src/app.js", Content: "keep"}}); err != nil {
		t.Fatal(err)
	}
	srv := giteaStyleArchiveWithHook(t, map[string]string{want: "scaffold workflow\n"}, func() {
		if err := store.ApplyFiles(ctx, scope, []workspace.File{{Path: want, Content: "user workflow\n"}}); err != nil {
			t.Errorf("concurrent workflow create: %v", err)
		}
	})
	defer srv.Close()
	info := projectTemplateInfo{Name: "x", ScaffoldRepo: srv.URL + "/team/starter", BuildWorkflowPath: want}
	seeded, err := s.seedMissingBuildWorkflow(ctx, id, p, info)
	if err != nil || seeded != "" {
		t.Fatalf("seedMissingBuildWorkflow = %q, %v; concurrent file should make it a no-op", seeded, err)
	}
	workflow, err := store.ReadFile(ctx, scope, workspace.ReadOptions{Path: want})
	if err != nil || workflow.Content != "user workflow\n" {
		t.Fatalf("workflow after seed = %#v err=%v", workflow, err)
	}
}

func TestWorkspaceHoldsOnlyRepositoryBoilerplate(t *testing.T) {
	for _, test := range []struct {
		name  string
		files []workspace.FileInfo
		want  bool
	}{
		{name: "empty", want: true},
		{name: "readme only", files: []workspace.FileInfo{{Path: "README.md"}}, want: true},
		{name: "all boilerplate", files: []workspace.FileInfo{{Path: "README.md"}, {Path: "LICENSE"}, {Path: ".gitignore"}}, want: true},
		{name: "readme plus source", files: []workspace.FileInfo{{Path: "README.md"}, {Path: "server.js"}}, want: false},
		{name: "nested readme is content", files: []workspace.FileInfo{{Path: "docs/README.md"}}, want: false},
		{name: "workflow is content", files: []workspace.FileInfo{{Path: ".github/workflows/build.yaml"}}, want: false},
	} {
		if got := workspaceHoldsOnlyRepositoryBoilerplate(test.files); got != test.want {
			t.Errorf("%s: workspaceHoldsOnlyRepositoryBoilerplate = %v, want %v", test.name, got, test.want)
		}
	}
}
