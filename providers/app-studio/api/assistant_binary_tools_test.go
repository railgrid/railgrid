/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/faroshq/provider-app-studio/workspace"
)

func binaryToolFixture(t *testing.T) (*Server, workspace.Scope, projectAssistantToolRegistry) {
	t.Helper()
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, workspaces: workspace.NewFileStore(t.TempDir())}
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid"}
	return server, scope, projectAssistantLocalToolRegistry(server)
}

func callBinaryTool(t *testing.T, registry projectAssistantToolRegistry, name string, req projectAssistantToolCallRequest) (string, error) {
	t.Helper()
	tool, ok := registry.Get(name)
	if !ok {
		t.Fatalf("tool %s is not registered", name)
	}
	if spec := tool.Spec(); spec.Risk != projectAssistantToolRiskWrite || projectAssistantToolBundleForSpec(spec) != projectAssistantToolBundleEdit {
		t.Fatalf("%s spec = %+v, want a write-risk edit tool", name, spec)
	}
	if err := projectAssistantValidateWorkspaceMutationArguments(name, req.Arguments); err != nil {
		t.Fatalf("argument validation: %v", err)
	}
	return tool.Call(context.Background(), req)
}

func TestImportAttachmentPlacesFileAttachmentInWorkspace(t *testing.T) {
	server, scope, registry := binaryToolFixture(t)
	model := testPNG(workspace.MaxWriteBytes * 3)
	receipt := attachmentReceiptForTest("att-jeep", "jeep.glb", "model/gltf-binary", model)
	state := newProjectEinoAssistantRunState()
	state.SetContentParts([]projectAssistantContentPart{projectAssistantContentPartAttachment(receipt)})
	req := projectAssistantToolCallRequest{
		WorkspaceScope:   scope,
		RunState:         state,
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{"att-jeep": model}},
		Arguments:        map[string]any{"attachmentID": "att-jeep", "path": "public/assets/jeep.glb"},
	}
	result, err := callBinaryTool(t, registry, projectToolImportAttachment, req)
	if err != nil {
		t.Fatal(err)
	}
	mutation := projectAssistantMutationFromResult(projectToolImportAttachment, result)
	if mutation == nil || !mutation.Changed || mutation.Path != "public/assets/jeep.glb" {
		t.Fatalf("mutation = %#v from %s", mutation, result)
	}
	var decoded map[string]any
	_ = json.Unmarshal([]byte(result), &decoded)
	if decoded["binary"] != true || decoded["sha256"] != receipt.SHA256 || decoded["attachmentID"] != "att-jeep" {
		t.Fatalf("result = %s", result)
	}
	got, err := server.workspaces.ReadFileBytes(context.Background(), scope, "public/assets/jeep.glb", 0)
	if err != nil || !bytes.Equal(got, model) {
		t.Fatalf("placed bytes mismatch: %v", err)
	}
	if paths, err := projectAssistantWriteTargetPaths(projectToolImportAttachment, req.Arguments); err != nil || strings.Join(paths, ",") != "public/assets/jeep.glb" {
		t.Fatalf("write target paths = %v, %v", paths, err)
	}
	if _, err := callBinaryTool(t, registry, projectToolImportAttachment, req); err == nil || !strings.Contains(err.Error(), "overwrite=true") {
		t.Fatalf("second import error = %v, want exists guidance", err)
	}
	req.Arguments["overwrite"] = true
	if _, err := callBinaryTool(t, registry, projectToolImportAttachment, req); err != nil {
		t.Fatalf("overwrite import: %v", err)
	}
	req.Arguments["attachmentID"] = "att-unknown"
	if _, err := callBinaryTool(t, registry, projectToolImportAttachment, req); err == nil || !strings.Contains(err.Error(), "not selected") {
		t.Fatalf("unknown attachment error = %v", err)
	}
}

func TestDownloadFileFetchesDirectFileURL(t *testing.T) {
	previous := projectAssistantDownloadHTTPClient
	// The production client refuses loopback; the test server is local.
	projectAssistantDownloadHTTPClient = &http.Client{CheckRedirect: projectAssistantDownloadCheckRedirect}
	t.Cleanup(func() { projectAssistantDownloadHTTPClient = previous })

	model := testPNG(4096)
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jeep.glb":
			w.Header().Set("Content-Type", "model/gltf-binary")
			_, _ = w.Write(model)
		case "/redirect":
			http.Redirect(w, r, upstream.URL+"/jeep.glb", http.StatusFound)
		case "/listing":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html>buy this model</html>"))
		case "/huge":
			w.Header().Set("Content-Length", strconv.Itoa(workspace.MaxBinaryWriteBytes+1))
			w.WriteHeader(http.StatusOK)
		case "/loop":
			http.Redirect(w, r, upstream.URL+"/loop", http.StatusFound)
		case "/missing":
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	server, scope, registry := binaryToolFixture(t)
	req := projectAssistantToolCallRequest{WorkspaceScope: scope, Arguments: map[string]any{"url": upstream.URL + "/redirect", "path": "public/assets/jeep.glb"}}
	result, err := callBinaryTool(t, registry, projectToolDownloadFile, req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	_ = json.Unmarshal([]byte(result), &decoded)
	if decoded["operation"] != projectToolDownloadFile || decoded["contentType"] != "model/gltf-binary" || decoded["size"] != float64(len(model)) || decoded["sha256"] == "" {
		t.Fatalf("download result = %s", result)
	}
	got, err := server.workspaces.ReadFileBytes(context.Background(), scope, "public/assets/jeep.glb", 0)
	if err != nil || !bytes.Equal(got, model) {
		t.Fatalf("downloaded bytes mismatch: %v", err)
	}
	for path, want := range map[string]string{
		"/listing": "web page",
		"/huge":    "limit",
		"/loop":    "redirects",
		"/missing": "HTTP 404",
	} {
		req.Arguments = map[string]any{"url": upstream.URL + path, "path": "public/other.bin"}
		if _, err := callBinaryTool(t, registry, projectToolDownloadFile, req); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("download %s error = %v, want %q", path, err, want)
		}
	}
	req.Arguments = map[string]any{"url": "file:///etc/passwd", "path": "public/x.bin"}
	if _, err := callBinaryTool(t, registry, projectToolDownloadFile, req); err == nil {
		t.Fatal("file:// URL accepted")
	}
}

func TestDownloadClientRefusesNonPublicAddresses(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "100.127.255.254",
		"198.18.0.1", "198.19.255.255", "224.0.0.1", "0.0.0.0", "255.255.255.255", "::1", "fc00::1", "fd12::1", "fe80::1",
		"ff02::1", "::ffff:10.0.0.1", "64:ff9b::a00:1",
	} {
		if err := webDialGuard("tcp", net.JoinHostPort(address, "443"), nil); err == nil {
			t.Errorf("dial guard allowed %s", address)
		}
	}
	for _, address := range []string{"93.184.216.34", "2606:4700::6810:84e5", "100.128.0.1", "198.20.0.1"} {
		if err := webDialGuard("tcp", net.JoinHostPort(address, "443"), nil); err != nil {
			t.Errorf("dial guard refused public %s: %v", address, err)
		}
	}
	// The production download client carries the guard.
	previous := projectAssistantDownloadHTTPClient
	projectAssistantDownloadHTTPClient = newProjectAssistantDownloadClient()
	t.Cleanup(func() { projectAssistantDownloadHTTPClient = previous })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte{0}) }))
	defer upstream.Close()
	if _, err := projectAssistantDownload(context.Background(), upstream.URL, "x.bin"); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("loopback download error = %v, want the dial guard", err)
	}
}

func TestFileAttachmentsReachTheModelAsMetadataOnly(t *testing.T) {
	model := testPNG(1 << 20)
	receipt := attachmentReceiptForTest("att-jeep", "jeep.glb", "model/gltf-binary", model)
	state := newProjectEinoAssistantRunState()
	state.SetContentParts([]projectAssistantContentPart{projectAssistantContentPartAttachment(receipt)})
	// An empty reader proves no bytes are read for a file attachment.
	messages, err := projectAssistantAttachmentMessages(context.Background(), projectAssistantRunRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{}},
	}, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || len(messages[0].UserInputMultiContent) != 0 {
		t.Fatalf("file attachment messages = %#v", messages)
	}
	for _, want := range []string{"att-jeep", "jeep.glb", "model/gltf-binary", fmt.Sprint(len(model)), "import_attachment"} {
		if !strings.Contains(messages[0].Content, want) {
			t.Fatalf("file attachment notice %q is missing %q", messages[0].Content, want)
		}
	}
	placeholder := projectAssistantAttachmentPlaceholderMessage(receipt)
	if !strings.Contains(placeholder.Content, "import_attachment") {
		t.Fatalf("historical file placeholder = %q", placeholder.Content)
	}
	// A large PNG is a file, not a model image.
	large := attachmentReceiptForTest("att-hero", "hero.png", "image/png", testPNG(1024))
	large.SizeBytes = projectAssistantAttachmentImageMaxBytes + 1
	if projectAssistantAttachmentIsImage(large) || !projectAssistantAttachmentIsFile(large) {
		t.Fatal("oversized image classified as a model image")
	}
	tools := projectAssistantFilterAttachmentTools(projectAssistantLocalToolRegistry(nil).Tools(false), false)
	for _, tool := range tools {
		if name := tool.Spec().Name; name == projectToolImportAttachment || name == projectToolReadAttachment {
			t.Fatalf("%s exposed without selectable attachments", name)
		}
	}
}
