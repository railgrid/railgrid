// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcpserver

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerAllowsHubFederationHostHeaderWhenConfigured(t *testing.T) {
	srv := httptest.NewServer(NewHandler(Deps{DisableLocalhostMCPProtection: true}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		srv.URL,
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "host.docker.internal:8086"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST tools/list: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), `"list_tables"`) {
		t.Fatalf("tools/list body missing list_tables tool: %s", string(body))
	}
	if strings.Contains(string(body), "databricks__list_tables") {
		t.Fatalf("provider-local tools should not be provider-prefixed: %s", string(body))
	}
	if strings.Contains(string(body), `"query_table"`) {
		t.Fatalf("tools/list exposed runtime data query tool: %s", string(body))
	}
}
