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

// A successful proxy call must preserve numeric JSON tokens, including nested
// values. Rounding an ID or a filter can select different data downstream.
func TestFederationPreservesExactNumericArguments(t *testing.T) {
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
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"received"}],"structuredContent":{"id":9007199254740993,"nested":{"decimal":9007199254740993.000000000001},"exponent":1.234567890123456789e+400}}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	t.Cleanup(provider.Close)
	h := New(Options{Providers: func(context.Context, Caller) []ProviderTarget {
		return []ProviderTarget{{Name: "example", MCPURL: provider.URL}}
	}, Verifier: allowAll})
	result, code := jsonrpc(t, h, "tools/call", `{"name":"example__echo","arguments":{"id":9007199254740993,"filters":[{"value":9007199254740993.000000000001}],"text":"9007199254740993"}}`)
	if code != http.StatusOK {
		t.Fatalf("aggregate call status = %d", code)
	}
	for _, token := range []string{"9007199254740993", "9007199254740993.000000000001", "1.234567890123456789e+400"} {
		if !strings.Contains(string(result), token) {
			t.Fatalf("result changed numeric token %s: %s", token, result)
		}
	}
	select {
	case raw := <-forwarded:
		var args struct {
			ID      json.Number `json:"id"`
			Filters []struct {
				Value json.Number `json:"value"`
			} `json:"filters"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			t.Fatal(err)
		}
		if args.ID.String() != "9007199254740993" || len(args.Filters) != 1 || args.Filters[0].Value.String() != "9007199254740993.000000000001" || args.Text != "9007199254740993" {
			t.Fatalf("proxy changed numeric or string arguments: %s", raw)
		}
	default:
		t.Fatal("aggregate never forwarded the call")
	}
}
