/*
Copyright 2026 The Railgrid Authors.

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

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/railgrid/provider-sdk/mcpcode"
	"github.com/railgrid/provider-sdk/mcpcode/mcpcodetest"
)

func TestMCPCallAndList(t *testing.T) {
	inner := func(result string) func(json.RawMessage) (json.RawMessage, error) {
		return func(json.RawMessage) (json.RawMessage, error) { return json.RawMessage(result), nil }
	}
	fake := &mcpcodetest.Server{Tools: []mcpcodetest.Tool{
		{CatalogTool: mcpcode.CatalogTool{Name: "code__build_status", Description: "Read the latest run.\nMore."}},
		{CatalogTool: mcpcode.CatalogTool{Name: "infrastructure__dev_sync", Description: "Push files."}},
		{CatalogTool: mcpcode.CatalogTool{Name: "structured"}, Call: inner(`{"content":[{"type":"text","text":"ignored"}],"structuredContent":{"found":true,"conclusion":"success"}}`)},
		{CatalogTool: mcpcode.CatalogTool{Name: "texty"}, Call: inner(`{"content":[{"type":"text","text":"plain words"}]}`)},
		{CatalogTool: mcpcode.CatalogTool{Name: "broken"}, Call: inner(`{"isError":true,"content":[{"type":"text","text":"template \"database\" not found"}]}`)},
		{CatalogTool: mcpcode.CatalogTool{Name: "code__branches", ConsentRequired: true}, Call: inner(`{"content":[{"type":"text","text":"main"}]}`)},
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	resolve := staticTarget(srv)
	oneShot := func(request any) []byte {
		var out, errOut bytes.Buffer
		p := newMCPProxy(resolve, &out, &errOut)
		line, _ := json.Marshal(request)
		if err := p.run(context.Background(), bytes.NewReader(append(line, '\n'))); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	// call mirrors runMCPCall: execute first, the direct tool only on a
	// consent refusal.
	call := func(tool string, args string) (string, error) {
		params, err := mcpcode.CallParams(tool, json.RawMessage(args))
		if err != nil {
			return "", err
		}
		reply, err := mcpReplyResult(oneShot(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params}))
		if err != nil {
			return "", err
		}
		res, err := mcpcode.DecodeCallResult(reply)
		var refused *mcpcode.CallError
		if errors.As(err, &refused) && refused.ConsentRequired() {
			direct, err := parseMCPToolResult(oneShot(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": json.RawMessage(args)}}))
			return string(direct), err
		}
		if err != nil {
			return "", err
		}
		result, err := toolResultOutput(res.ToolResultJSON())
		return string(result), err
	}
	res, err := call("structured", `{"repositoryRef":"shop"}`)
	if err != nil || !strings.Contains(res, `"conclusion":"success"`) {
		t.Fatalf("structured: res=%s err=%v", res, err)
	}
	if inv := fake.Invocations(); len(inv) != 1 || inv[0].Method != "execute" || inv[0].Tool != "structured" || string(inv[0].Args) != `{"repositoryRef":"shop"}` {
		t.Fatalf("aggregate saw %+v, want one execute of structured with the arguments", inv)
	}
	res, err = call("texty", `{}`)
	if err != nil || res != `"plain words"` {
		t.Fatalf("texty: res=%s err=%v", res, err)
	}
	if _, err := call("broken", `{}`); err == nil || !strings.Contains(err.Error(), `template "database" not found`) {
		t.Fatalf("isError not surfaced: %v", err)
	}
	res, err = call("code__branches", `{}`)
	if err != nil || res != `"main"` {
		t.Fatalf("consent-gated tool via direct fallback: res=%s err=%v", res, err)
	}
	if inv := fake.Invocations(); inv[len(inv)-1].Method != "direct" || inv[len(inv)-1].Tool != "code__branches" {
		t.Fatalf("consent fallback did not go direct: %+v", inv[len(inv)-1])
	}

	// The catalog through the fake endpoint (never the kubeconfig's hub).
	out := oneShot(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": mcpcode.CatalogParams()})
	if !strings.Contains(string(out), `code__build_status`) {
		t.Fatalf("catalog reply = %q", out)
	}
}
