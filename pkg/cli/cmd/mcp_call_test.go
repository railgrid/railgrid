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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPCallAndList(t *testing.T) {
	var gotName string
	var gotArgs json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"code__build_status","description":"Read the latest run.\nMore."},{"name":"infrastructure__dev_sync","description":"Push files."}]}}`))
		case "tools/call":
			gotName, gotArgs = msg.Params.Name, msg.Params.Arguments
			switch msg.Params.Name {
			case "structured":
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ignored"}],"structuredContent":{"found":true,"conclusion":"success"}}}`))
			case "texty":
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"plain words"}]}}`))
			default:
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"template \"database\" not found"}]}}`))
			}
		}
	}))
	defer srv.Close()
	resolve := staticTarget(srv)
	call := func(tool string, args string) (string, error) {
		var out, errOut bytes.Buffer
		p := newMCPProxy(resolve, &out, &errOut)
		line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": json.RawMessage(args)}})
		if err := p.run(context.Background(), bytes.NewReader(append(line, '\n'))); err != nil {
			return "", err
		}
		result, err := parseMCPToolResult(out.Bytes())
		if err != nil {
			return "", err
		}
		return string(result), nil
	}
	res, err := call("structured", `{"repositoryRef":"shop"}`)
	if err != nil || gotName != "structured" || string(gotArgs) != `{"repositoryRef":"shop"}` || !strings.Contains(res, `"conclusion":"success"`) {
		t.Fatalf("structured: res=%s err=%v name=%s args=%s", res, err, gotName, gotArgs)
	}
	res, err = call("texty", `{}`)
	if err != nil || res != `"plain words"` {
		t.Fatalf("texty: res=%s err=%v", res, err)
	}
	if _, err := call("broken", `{}`); err == nil || !strings.Contains(err.Error(), `template "database" not found`) {
		t.Fatalf("isError not surfaced: %v", err)
	}

	// tools/list through the fake endpoint (never the kubeconfig's hub).
	var out, errOut bytes.Buffer
	p := newMCPProxy(resolve, &out, &errOut)
	line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if err := p.run(context.Background(), bytes.NewReader(append(line, '\n'))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"code__build_status"`) {
		t.Fatalf("tools/list reply = %q", out.String())
	}
}
