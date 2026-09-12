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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectComponentSyncSkippedReasons(t *testing.T) {
	got := projectComponentSyncSkipped(
		[]string{"public/logo.png", "public/huge.glb"},
		[]string{"public/huge.glb", "data/big.json"},
		nil, false)
	want := []projectSyncSkippedFile{
		{Path: "data/big.json", Reason: projectSyncSkipTooLarge},
		// An agent without base64 refuses every binary, whatever its size.
		{Path: "public/huge.glb", Reason: projectSyncSkipBinaryUnsupported},
		{Path: "public/logo.png", Reason: projectSyncSkipBinaryUnsupported},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("without base64: skipped = %+v, want %+v", got, want)
	}

	got = projectComponentSyncSkipped(
		[]string{"public/logo.png", "public/huge.glb", "public/b.glb"},
		[]string{"public/huge.glb"},
		[]string{"public/b.glb"}, true)
	want = []projectSyncSkippedFile{
		{Path: "public/b.glb", Reason: projectSyncSkipSyncLimit},
		{Path: "public/huge.glb", Reason: projectSyncSkipTooLarge},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("with base64: skipped = %+v, want %+v", got, want)
	}

	if got := projectComponentSyncSkipped([]string{"logo.png"}, nil, nil, true); got != nil {
		t.Fatalf("nothing skipped = %+v, want nil", got)
	}
}

func TestWithProjectSyncSkippedIsAdditive(t *testing.T) {
	body := []byte(`{"phase":"Synced","changed":["index.js"],"restarted":true,"sourceRevision":7}`)
	if got := withProjectSyncSkipped(body, nil); string(got) != string(body) {
		t.Fatalf("nothing skipped changed the body: %s", got)
	}
	got := withProjectSyncSkipped(body, []projectSyncSkippedFile{{Path: "logo.png", Reason: projectSyncSkipBinaryUnsupported}})
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"phase": "Synced", "changed": []any{"index.js"}, "restarted": true, "sourceRevision": float64(7),
		"skipped": []any{map[string]any{"path": "logo.png", "reason": "binary-unsupported"}},
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("merged = %v, want %v", decoded, want)
	}
	// A non-object body is passed through rather than dropped.
	if got := withProjectSyncSkipped([]byte("null"), []projectSyncSkippedFile{{Path: "a", Reason: "x"}}); string(got) != "null" {
		t.Fatalf("non-object body = %s", got)
	}
}

// fakeSkipAgent serves per-component process status and sync behind the
// data-plane route, recording what each component received.
type fakeSkipAgent struct {
	mu        sync.Mutex
	encodings map[string][]string
	received  map[string][]projectSandboxSyncFile
}

func (a *fakeSkipAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, rest, ok := strings.Cut(r.URL.Path, "/components/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	component, verb, _ := strings.Cut(rest, "/")
	switch {
	case r.Method == http.MethodGet && verb == "process":
		_ = json.NewEncoder(w).Encode(map[string]any{"running": true, "syncEncodings": a.encodings[component]})
	case r.Method == http.MethodPost && verb == "sync":
		var req projectSandboxSyncRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		a.received[component] = req.Files
		changed := make([]string, 0, len(req.Files))
		for _, f := range req.Files {
			changed = append(changed, f.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"phase": "Synced", "changed": changed, "restarted": true, "sourceRevision": req.SourceRevision})
	default:
		http.NotFound(w, r)
	}
}

func TestSyncProjectDevelopmentTargetReportsSkippedFiles(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	project := &aiv1alpha1.Project{}
	project.Name, project.UID = "demo", "uid"
	id := identity{clusterID: "cluster-a", orgUUID: "org-a", workspaceUUID: "ws-a"}
	scope := projectWorkspaceScope(id, project)
	for _, file := range []workspace.PutOptions{
		{Path: "web/index.html", Data: []byte("<html></html>")},
		{Path: "web/public/logo.png", Data: testPNG(64)},
		{Path: "api/main.js", Data: []byte("console.log(1)\n")},
		{Path: "api/icon.png", Data: testPNG(32)},
	} {
		if _, err := workspaces.PutFile(ctx, scope, file); err != nil {
			t.Fatal(err)
		}
	}
	agent := &fakeSkipAgent{
		encodings: map[string][]string{"web": {"utf-8"}, "api": {"utf-8", "base64"}},
		received:  map[string][]projectSandboxSyncFile{},
	}
	hub := httptest.NewServer(agent)
	defer hub.Close()

	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.faros.sh/v1alpha1",
		"kind":       "Instance",
		"metadata":   map[string]any{"name": "demo-dev"},
	}}
	client := asclient.NewFromDynamic(publishingTestDynamic(instance))
	target := projectDevelopmentSyncTargetInfo{
		ResourceName: "demo-dev",
		Resource:     "instances",
		Kind:         "Instance",
		APIVersion:   "infrastructure.faros.sh/v1alpha1",
		Components:   map[string]projectTemplateComponent{"web": {WorkspacePath: "web"}, "api": {WorkspacePath: "api"}},
	}
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, hubBase: hub.URL, workspaces: workspaces}

	raw, err := server.syncProjectDevelopmentTarget(ctx, client, id, project, target)
	if err != nil {
		t.Fatalf("syncProjectDevelopmentTarget: %v", err)
	}
	var result map[string]struct {
		Phase   string                   `json:"phase"`
		Changed []string                 `json:"changed"`
		Skipped []projectSyncSkippedFile `json:"skipped"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode result %s: %v", raw, err)
	}
	web, api := result["web"], result["api"]
	if web.Phase != "Synced" || !reflect.DeepEqual(web.Changed, []string{"index.html"}) {
		t.Fatalf("web result = %+v, want the agent's own fields kept", web)
	}
	if want := []projectSyncSkippedFile{{Path: "public/logo.png", Reason: "binary-unsupported"}}; !reflect.DeepEqual(web.Skipped, want) {
		t.Fatalf("web skipped = %+v, want %+v", web.Skipped, want)
	}
	if len(api.Changed) != 2 || len(api.Skipped) != 0 {
		t.Fatalf("api result = %+v, want both files and nothing skipped", api)
	}
	var apiFields map[string]json.RawMessage
	if err := json.Unmarshal(mustRawField(t, raw, "api"), &apiFields); err != nil {
		t.Fatal(err)
	}
	if _, ok := apiFields["skipped"]; ok {
		t.Fatalf("api result carries an empty skipped field: %s", mustRawField(t, raw, "api"))
	}
	if len(agent.received["web"]) != 1 {
		t.Fatalf("web received %+v, want text only", agent.received["web"])
	}
}

func mustRawField(t *testing.T, raw json.RawMessage, key string) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields[key]
}
