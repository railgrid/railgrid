/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/faroshq/provider-app-studio/hubmcp"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectSandboxSyncDigestHashesDecodedBytes(t *testing.T) {
	image := testPNG(300)
	encoded := []projectSandboxSyncFile{
		{Path: "a.txt", Content: "hello"},
		{Path: "b.png", Content: base64.StdEncoding.EncodeToString(image), Encoding: "base64"},
	}
	hash := sha256.New()
	for _, entry := range []struct {
		path string
		data []byte
	}{{"a.txt", []byte("hello")}, {"b.png", image}} {
		_, _ = hash.Write([]byte(entry.path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(entry.data)
		_, _ = hash.Write([]byte{0})
	}
	if got, want := projectSandboxSyncDigest(encoded), hex.EncodeToString(hash.Sum(nil)); got != want {
		t.Fatalf("digest = %s, want %s over decoded bytes", got, want)
	}
	// Text-only digests are unchanged by the encoding field.
	if projectSandboxSyncDigest(encoded[:1]) != projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "a.txt", Content: "hello", Encoding: "utf-8"}}) {
		t.Fatal("utf-8 encoding changed the text digest")
	}
}

func TestDevelopmentAgentBase64CapabilityIsReadFromStatusAndCached(t *testing.T) {
	var probes atomic.Int32
	encodings := []string{"utf-8", "base64"}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/process") {
			http.NotFound(w, r)
			return
		}
		probes.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"running": true, "syncEncodings": encodings})
	}))
	defer hub.Close()
	server := &Server{hubBase: hub.URL}
	id := identity{clusterID: "cluster-a", orgUUID: "org-a", workspaceUUID: "ws-a"}
	web := dataPlaneRef{Resource: "instances", Name: "demo-dev", Component: "web"}
	if !server.developmentAgentSupportsBase64(context.Background(), id, web) {
		t.Fatal("agent advertising base64 reported unsupported")
	}
	if !server.developmentAgentSupportsBase64(context.Background(), id, web) || probes.Load() != 1 {
		t.Fatalf("capability not cached: %d probes", probes.Load())
	}
	encodings = []string{"utf-8"}
	api := dataPlaneRef{Resource: "instances", Name: "demo-dev", Component: "api"}
	if server.developmentAgentSupportsBase64(context.Background(), id, api) {
		t.Fatal("agent without base64 reported supported")
	}
}

func TestAppendProjectSyncBinariesRespectsBundleBounds(t *testing.T) {
	text := []projectSandboxSyncFile{{Path: "index.js", Content: "ok"}}
	large := base64.StdEncoding.EncodeToString(make([]byte, hubmcp.BinaryFileMaxBytes))
	binaries := []projectSandboxSyncFile{
		{Path: "a.glb", Content: large, Encoding: "base64"},
		{Path: "b.glb", Content: large, Encoding: "base64"},
		{Path: "c.png", Content: base64.StdEncoding.EncodeToString([]byte{0x89, 'P'}), Encoding: "base64"},
	}
	out, dropped := appendProjectSyncBinaries("demo", "web", text, binaries)
	paths := make([]string, 0, len(out))
	for _, file := range out {
		paths = append(paths, file.Path)
	}
	if strings.Join(paths, ",") != "index.js,a.glb,c.png" {
		t.Fatalf("bounded sync files = %v, want the second 25 MiB binary dropped", paths)
	}
	if strings.Join(dropped, ",") != "b.glb" {
		t.Fatalf("dropped = %v, want [b.glb]", dropped)
	}
}

func TestProjectWorkspaceSyncFilesReadsBinariesOnlyWhenAsked(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid"}
	image := testPNG(workspace.MaxWriteBytes + 10)
	for _, file := range []workspace.PutOptions{{Path: "web/index.html", Data: []byte("<html></html>")}, {Path: "web/public/jeep.glb", Data: image}} {
		if _, err := workspaces.PutFile(ctx, scope, file); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{workspaces: workspaces}
	textOnly, err := server.projectWorkspaceSyncFiles(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(textOnly.Files) != 1 || len(textOnly.BinaryFiles) != 0 || strings.Join(textOnly.BinaryPaths, ",") != "web/public/jeep.glb" {
		t.Fatalf("text-only snapshot = %+v", textOnly)
	}
	withBinaries, err := server.projectWorkspaceSyncFilesWithBinaries(ctx, scope, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withBinaries.BinaryFiles) != 1 || withBinaries.BinaryFiles[0].Encoding != "base64" {
		t.Fatalf("binary snapshot = %+v", withBinaries.BinaryFiles)
	}
	routed := routeProjectSyncFiles(withBinaries.BinaryFiles, map[string]projectTemplateComponent{"web": {WorkspacePath: "web"}})
	if len(routed["web"]) != 1 || routed["web"][0].Path != "public/jeep.glb" || routed["web"][0].Encoding != "base64" {
		t.Fatalf("routed binary = %+v, want prefix stripped and encoding kept", routed["web"])
	}
	decoded, err := base64.StdEncoding.DecodeString(routed["web"][0].Content)
	if err != nil || string(decoded) != string(image) {
		t.Fatalf("routed binary bytes mismatch: %v", err)
	}
}
