/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

// codeBinaryHub fakes the tenant MCP aggregate for CHECKOUT, which is still a
// tool: tools/list optionally advertises the binaryEncoding opt-in, and
// tools/call records the arguments it receives. Commit is not here any more —
// it is the repositories/commit/v1 action (commit_action_test.go).
type codeBinaryHub struct {
	advertise bool
	calls     []map[string]any
	checkout  string
}

func (h *codeBinaryHub) serve(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode MCP request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		result := map[string]any{}
		switch req.Method {
		case "tools/list":
			checkoutProps := map[string]any{"repositoryRef": map[string]any{}}
			if h.advertise {
				checkoutProps["binaryEncoding"] = map[string]any{"type": "string"}
			}
			result["tools"] = []any{
				map[string]any{"name": projectToolCodeCheckoutRepository, "inputSchema": map[string]any{"properties": checkoutProps}},
			}
		case "tools/call":
			h.calls = append(h.calls, map[string]any{"name": req.Params.Name, "arguments": req.Params.Arguments})
			result["content"] = []any{map[string]any{"type": "text", "text": h.checkout}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestHydrateRequestsAndWritesBase64Binaries(t *testing.T) {
	image := testPNG(2048)
	checkout, _ := json.Marshal(checkoutToolResult{Ref: "main", CommitSHA: "sha", Files: []checkoutToolFile{
		{Path: "public/logo.png", Content: base64.StdEncoding.EncodeToString(image), Encoding: "base64"},
		{Path: "index.html", Content: "<html></html>\n"},
	}})
	hub := &codeBinaryHub{advertise: true, checkout: string(checkout)}
	upstream := hub.serve(t)
	f := newProjectFilesFixture(t)
	f.server.hubBase = upstream.URL
	f.server.developmentSyncAfterMutation = func(identity, *aiv1alpha1.Project, string) error { return nil }
	resp, err := f.server.hydrateWorkspaceFromRepository(context.Background(), identity{orgUUID: "org-a", workspaceUUID: "workspace-a", clusterID: "cluster-a"}, f.project, httptest.NewRequest(http.MethodPost, "/", nil), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Written) != 2 || len(resp.Skipped) != 0 {
		t.Fatalf("hydrate = %#v", resp)
	}
	args := hub.calls[len(hub.calls)-1]["arguments"].(map[string]any)
	if args["binaryEncoding"] != "base64" {
		t.Fatalf("checkout args = %v, want binaryEncoding opt-in", args)
	}
	got, err := f.workspaces.ReadFileBytes(context.Background(), f.scope, "public/logo.png", 0)
	if err != nil || !bytes.Equal(got, image) {
		t.Fatalf("hydrated binary mismatch: %v", err)
	}
}
