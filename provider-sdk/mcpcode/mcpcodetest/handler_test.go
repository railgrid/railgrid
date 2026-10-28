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

package mcpcodetest

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/railgrid/provider-sdk/mcpcode"
)

func rpc(t *testing.T, url string, params any, method string) json.RawMessage {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		return json.RawMessage(`{"rpcError":` + strconvQuote(env.Error.Message) + `}`)
	}
	return env.Result
}

func TestServerSpeaksTheAggregateWire(t *testing.T) {
	var seen json.RawMessage
	fake := &Server{Tools: []Tool{
		{CatalogTool: mcpcode.CatalogTool{Name: "infra__provision", Description: "make"}, Call: func(args json.RawMessage) (json.RawMessage, error) {
			seen = args
			return json.RawMessage(`{"content":[{"type":"text","text":"done"}],"structuredContent":{"ok":1}}`), nil
		}},
		{CatalogTool: mcpcode.CatalogTool{Name: "code__branches", ConsentRequired: true}, Call: func(json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"content":[{"type":"text","text":"branches"}]}`), nil
		}},
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	var list struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	_ = json.Unmarshal(rpc(t, srv.URL, map[string]any{}, "tools/list"), &list)
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "search,execute,code__branches" {
		t.Fatalf("tools/list = %v", names)
	}

	cat, err := mcpcode.DecodeCatalog(rpc(t, srv.URL, mcpcode.CatalogParams(), "tools/call"))
	if err != nil || len(cat) != 2 || cat[0].Name != "infra__provision" {
		t.Fatalf("catalog = %+v, %v", cat, err)
	}

	params, _ := mcpcode.CallParams("infra__provision", map[string]any{"size": 2})
	call, err := mcpcode.DecodeCallResult(rpc(t, srv.URL, params, "tools/call"))
	if err != nil || call.Text != "done" || string(call.Data) != `{"ok":1}` || string(seen) != `{"size":2}` {
		t.Fatalf("call = %+v, err = %v, seen = %s", call, err, seen)
	}

	params, _ = mcpcode.CallParams("code__branches", nil)
	var ce *mcpcode.CallError
	if _, err := mcpcode.DecodeCallResult(rpc(t, srv.URL, params, "tools/call")); !errors.As(err, &ce) || !ce.ConsentRequired() {
		t.Fatalf("consent tool through execute: err = %v", err)
	}
	params, _ = mcpcode.CallParams("nope", nil)
	if _, err := mcpcode.DecodeCallResult(rpc(t, srv.URL, params, "tools/call")); !errors.As(err, &ce) || !ce.UnknownTool() {
		t.Fatalf("unknown tool through execute: err = %v", err)
	}

	direct := rpc(t, srv.URL, map[string]any{"name": "code__branches", "arguments": map[string]any{}}, "tools/call")
	if !strings.Contains(string(direct), "branches") {
		t.Fatalf("direct consent call = %s", direct)
	}
	direct = rpc(t, srv.URL, map[string]any{"name": "infra__provision", "arguments": map[string]any{}}, "tools/call")
	if !strings.Contains(string(direct), "unknown tool") {
		t.Fatalf("direct call of a non-consent tool must be refused: %s", direct)
	}

	inv := fake.Invocations()
	if len(inv) != 7 || inv[1].Method != "search" || inv[2].Method != "execute" || inv[2].Tool != "infra__provision" || inv[5].Method != "direct" {
		t.Fatalf("invocations = %+v", inv)
	}
}

func TestUnwrapAdaptsByNameFakes(t *testing.T) {
	var sawName string
	var sawArgs json.RawMessage
	byName := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sawName, sawArgs = req.Params.Name, req.Params.Arguments
		w.Header().Set("Content-Type", "application/json")
		switch req.Params.Name {
		case "code__rebuild":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"dispatched":true}}}`))
		case "code__broken":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"unavailable"}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"` + req.Params.Name + `"}]}}`))
		}
	})
	srv := httptest.NewServer(Unwrap(byName))
	defer srv.Close()

	params, _ := mcpcode.CallParams("code__rebuild", map[string]any{"ref": "sha"})
	call, err := mcpcode.DecodeCallResult(rpc(t, srv.URL, params, "tools/call"))
	if err != nil || string(call.Data) != `{"dispatched":true}` || sawName != "code__rebuild" || string(sawArgs) != `{"ref":"sha"}` {
		t.Fatalf("call = %+v err = %v saw %s %s", call, err, sawName, sawArgs)
	}
	params, _ = mcpcode.CallParams("code__broken", nil)
	if got := string(rpc(t, srv.URL, params, "tools/call")); !strings.Contains(got, "rpcError") || !strings.Contains(got, "unavailable") {
		t.Fatalf("JSON-RPC error did not pass through: %s", got)
	}
	// Anything that is not an execute call reaches the fake untouched.
	if got := string(rpc(t, srv.URL, map[string]any{"name": "direct", "arguments": map[string]any{}}, "tools/call")); !strings.Contains(got, `"direct"`) {
		t.Fatalf("direct call was not passed through: %s", got)
	}
}
