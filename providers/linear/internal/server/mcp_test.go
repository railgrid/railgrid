// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPDiscoveryAndTimestampSubmission(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/clusters/tenant-one/apis/linear.providers.faros.sh/v1alpha1/operations" || r.Header.Get("Authorization") != "Bearer caller-token" {
			t.Errorf("incorrect tenant request: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		spec := body["spec"].(map[string]any)
		if spec["since"] != "2026-09-13T12:00:00Z" || spec["connection"] != "main" {
			t.Errorf("unexpected spec: %v", spec)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	handler, err := (Server{HubURL: upstream.URL}).MCP()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Faros-Cluster", "tenant-one")
		r.Header.Set("Authorization", "Bearer caller-token")
		handler.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	}()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 2 {
		t.Fatalf("tools: %v", list.Tools)
	}
	// Each stateless request builds a server from the shared input schema.
	// Exercise that path concurrently under the race detector.
	var reads sync.WaitGroup
	for range 8 {
		reads.Go(func() {
			tools, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Error(err)
				return
			}
			if len(tools.Tools) != 2 {
				t.Errorf("concurrent tool count = %d", len(tools.Tools))
			}
		})
	}
	reads.Wait()
	var schema map[string]any
	for _, tool := range list.Tools {
		if tool.Name == "linear_submit_operation" {
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
		}
	}
	if schema == nil {
		t.Fatal("submit tool missing")
	}
	since := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)["since"].(map[string]any)
	if !reflect.DeepEqual(since["type"], []any{"null", "string"}) || since["format"] != "date-time" {
		t.Fatalf("timestamp schema: %v", since)
	}
	for _, value := range []string{"2026-09-13T12:00:00Z", "not-a-time"} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "linear_submit_operation", Arguments: map[string]any{"name": "op-stable", "spec": map[string]any{"connection": "main", "action": "reconcile", "since": value}}})
		if value == "not-a-time" {
			if err == nil && !result.IsError {
				t.Fatal("invalid timestamp accepted")
			}
		} else if err != nil || result.IsError {
			t.Fatalf("valid timestamp rejected: %v, %+v", err, result)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream writes = %d, want 1", calls.Load())
	}
}
