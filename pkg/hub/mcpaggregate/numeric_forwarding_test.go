// Copyright 2026 The Railgrid Authors.
// SPDX-License-Identifier: Apache-2.0

package mcpaggregate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Arguments a script passes to railgrid.call reach the provider as the
// script wrote them: integers stay integer literals (no float64 formatting on
// the Go side), decimals and strings are untouched. The provider's result is
// handed back twice: parsed as `data`, where JavaScript's float64 numbers
// apply, and verbatim as `raw`/`text`, where an ID above 2^53 survives for a
// script that must forward it.
func TestExecutePreservesNumericArgumentsAndRawResult(t *testing.T) {
	forwarded := make(chan json.RawMessage, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode upstream request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"echo","inputSchema":{"type":"object"}}]}}`))
		case "tools/call":
			forwarded <- req.Params.Arguments
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"id 9007199254740993"}],"structuredContent":{"id":9007199254740993,"nested":{"decimal":9007199254740993.000000000001}}}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	t.Cleanup(provider.Close)
	h := New(Options{Providers: func(context.Context, Caller) []ProviderTarget {
		return []ProviderTarget{{Name: "example", MCPURL: provider.URL}}
	}, Verifier: allowAll})

	result := callViaExecute(t, h, "test-token", "example__echo", `{id: 4294967297, filters: [{value: 0.1}], text: "9007199254740993"}`)
	var got struct {
		Text string          `json:"text"`
		Raw  string          `json:"raw"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, result)
	}
	for _, token := range []string{"9007199254740993", "9007199254740993.000000000001"} {
		if !strings.Contains(got.Raw, token) {
			t.Fatalf("raw lost numeric token %s: %s", token, got.Raw)
		}
	}
	if got.Text != "id 9007199254740993" {
		t.Fatalf("text = %q", got.Text)
	}
	if string(got.Data) != `{"id":9007199254740992,"nested":{"decimal":9007199254740994}}` {
		t.Fatalf("data = %s (JavaScript numbers are float64; raw carries the exact text)", got.Data)
	}
	select {
	case raw := <-forwarded:
		if string(raw) != `{"filters":[{"value":0.1}],"id":4294967297,"text":"9007199254740993"}` {
			t.Fatalf("provider received %s", raw)
		}
	default:
		t.Fatal("aggregate never forwarded the call")
	}
}
