// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcpserver

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestHandlerListsToolsWithoutPanic is the regression guard for the kuery
// federation EOF: registerTools used to register kuery_query with a typed Out
// (queryOutput, whose status is the self-recursive v1alpha1.ObjectResult). The
// SDK's schema reflector panics with "cycle detected" on recursive types, and
// because the server is built per request, that panic propagated out of
// ServeHTTP and every /mcp request died mid-flight — the aggregator's tools/list
// saw an EOF and dropped kuery entirely.
//
// This drives the real streamable-HTTP handler the exact way the aggregator
// does (initialize + tools/list) and asserts both tools come back. Deps.Engine
// is nil on purpose: registration must not touch it (the engine is only used
// inside the tool handlers at call time), so a tools/list never dereferences it.
func TestHandlerListsToolsWithoutPanic(t *testing.T) {
	srv := httptest.NewServer(NewHandler(Deps{}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect to kuery MCP handler: %v", err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list against kuery MCP handler: %v", err)
	}

	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{"kuery_query", "kuery_impact"} {
		if !got[want] {
			t.Errorf("tools/list missing %q; got %v", want, keys(got))
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
