/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroshq/provider-app-studio/workspace"
)

// binaryCommitHub is a fake hub aggregate that records commit_files
// arguments and optionally advertises base64 file items in tools/list.
type binaryCommitHub struct {
	advertiseEncoding bool
	listCalls         int
	commits           []map[string]any
}

func (h *binaryCommitHub) serve(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode MCP request: %v", err)
			return
		}
		result := map[string]any{}
		switch req.Method {
		case "tools/list":
			h.listCalls++
			item := map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{}, "content": map[string]any{}}}
			if h.advertiseEncoding {
				item["properties"].(map[string]any)["encoding"] = map[string]any{"type": "string"}
			}
			result = map[string]any{"tools": []any{map[string]any{
				"name":        "code__commit_files",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"files": map[string]any{"type": "array", "items": item}}},
			}}}
		case "tools/call":
			var params struct {
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			h.commits = append(h.commits, params.Arguments)
			text := commitToolText(map[string]any{"repositoryRef": "demo-repo", "name": "commit-1", "phase": "Succeeded", "commitSHA": "0123456789abcdef", "branch": "main"})
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return server
}

func commitTestPNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 13}, bytes.Repeat([]byte{0xff, 0x00}, 64)...)
}

func (env *commitTestEnv) writeBytes(path string, data []byte) {
	env.t.Helper()
	if _, err := env.files.PutFile(env.ctx, env.scope, workspace.PutOptions{Path: path, Data: data}); err != nil {
		env.t.Fatal(err)
	}
	if _, err := env.files.AddUncommittedPaths(env.ctx, env.scope, []string{path}); err != nil {
		env.t.Fatal(err)
	}
}

func TestCommitWorkspaceSendsBinariesAsBase64WhenSupported(t *testing.T) {
	env := newCommitTestEnv(t, nil)
	hub := &binaryCommitHub{advertiseEncoding: true}
	env.r.HubBase = hub.serve(t).URL
	image := commitTestPNG()
	env.writeBytes("public/logo.png", image)

	dirty, err := env.commit()
	if err != nil || dirty {
		t.Fatalf("commit = dirty %t, err %v", dirty, err)
	}
	if len(hub.commits) != 1 {
		t.Fatalf("commit calls = %d", len(hub.commits))
	}
	files, _ := hub.commits[0]["files"].([]any)
	var found bool
	for _, raw := range files {
		file := raw.(map[string]any)
		if file["path"] != "public/logo.png" {
			if _, hasEncoding := file["encoding"]; hasEncoding {
				t.Fatalf("text file carries an encoding: %v", file)
			}
			continue
		}
		found = true
		decoded, err := base64.StdEncoding.DecodeString(file["content"].(string))
		if file["encoding"] != "base64" || err != nil || !bytes.Equal(decoded, image) {
			t.Fatalf("binary entry = %v (decode err %v)", file, err)
		}
	}
	if !found {
		t.Fatalf("binary missing from commit: %v", files)
	}
	if pending := env.pending(); len(pending) != 0 {
		t.Fatalf("uncommitted after binary commit = %v", pending)
	}
}

func TestCommitWorkspaceSkipsBinariesWithoutRequeueWhenUnsupported(t *testing.T) {
	env := newCommitTestEnv(t, nil)
	hub := &binaryCommitHub{}
	env.r.HubBase = hub.serve(t).URL
	env.writeBytes("public/logo.png", commitTestPNG())

	dirty, err := env.commit()
	if err != nil || dirty {
		t.Fatalf("first commit = dirty %t, err %v; want text committed without requeue", dirty, err)
	}
	if len(hub.commits) != 1 {
		t.Fatalf("commit calls = %d, want the text commit", len(hub.commits))
	}
	if raw, _ := json.Marshal(hub.commits[0]["files"]); strings.Contains(string(raw), "logo.png") || strings.Contains(string(raw), "base64") {
		t.Fatalf("unsupported provider received a binary: %s", raw)
	}
	if pending := env.pending(); strings.Join(pending, ",") != "public/logo.png" {
		t.Fatalf("uncommitted = %v, want only the skipped binary", pending)
	}

	// A later pass with only the skipped binary neither calls commit_files
	// nor asks for a requeue; the capability answer is cached.
	dirty, err = env.commit()
	if err != nil || dirty {
		t.Fatalf("second commit = dirty %t, err %v", dirty, err)
	}
	if len(hub.commits) != 1 || hub.listCalls != 1 {
		t.Fatalf("second pass calls: commits %d, tools/list %d", len(hub.commits), hub.listCalls)
	}
}
