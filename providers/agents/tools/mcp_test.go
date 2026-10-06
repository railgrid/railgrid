// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/railgrid/provider-agents/engine"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// fakeVerbCaller renders the export virtual-workspace URL *dataplane.Callers
// would, against a fixed endpoint, and hands out a marked HTTP client.
type fakeVerbCaller struct{ endpoint string }

func (f fakeVerbCaller) ExportVerbURL(_ context.Context, gvr schema.GroupVersionResource, r dataplane.Request) (string, error) {
	return dataplane.SubresourceURL(f.endpoint, gvr.Group, gvr.Version, r)
}

func (f fakeVerbCaller) ProviderHTTPClient() (*http.Client, error) { return &http.Client{}, nil }

const testVW = "https://hub.example.com/services/apiexport/prov/agents.railgrid.ai"

// An mcp connection naming an instance is addressed over the infrastructure
// provider's instances/proxy verb through THIS provider's export virtual
// workspace, exactly like a self-hosted search connection — the user names the
// instance, never a URL carrying a cluster ID, and no caller credential is
// involved.
func TestConnectMCPInstanceAddressing(t *testing.T) {
	dp := DataPlane{ClusterID: "23qp2e0jwjeqwp2i", Callers: fakeVerbCaller{endpoint: testVW + "/"}}

	t.Run("composes the verb root and appends nothing", func(t *testing.T) {
		// The browser template pins /mcp as the endpoint's upstreamPath, so the
		// verb root is the MCP endpoint. Appending /mcp here would double it.
		got, err := dp.ProxyURL(context.Background(), "mcp", "browser", browserResource, "browser", "")
		if err != nil {
			t.Fatal(err)
		}
		want := testVW + "/clusters/23qp2e0jwjeqwp2i/apis/infrastructure.railgrid.ai/v1alpha1/instances/browser/proxy"
		if got != want {
			t.Fatalf("endpoint = %s\nwant %s", got, want)
		}
	})

	t.Run("a provider with no provider-scoped config is told why", func(t *testing.T) {
		// The call is made as the provider; without its kubeconfig there is
		// nothing to make it with, and the message has to say so rather than
		// point at the instance.
		_, err := DataPlane{ClusterID: dp.ClusterID}.ProxyURL(context.Background(), "mcp", "browser", browserResource, "browser", "")
		if err == nil || !strings.Contains(err.Error(), "provider-scoped kubeconfig") {
			t.Fatalf("want the missing provider config named, got %v", err)
		}
	})

	t.Run("no instance and no baseURL names both options", func(t *testing.T) {
		conn := &agentsv1alpha1.Connection{}
		conn.Name = "browser"
		conn.Spec.Type = agentsv1alpha1.ConnectionTypeMCP
		_, err := ConnectMCP(context.Background(), Deps{DataPlane: dp}, conn)
		if err == nil || !strings.Contains(err.Error(), "neither an instance nor a baseURL") {
			t.Fatalf("want an error naming both options, got %v", err)
		}
	})
}

func TestConnectMCPEndpointWithClientPreservesBoundedResults(t *testing.T) {
	const exactID = uint64(9_007_199_254_740_993)
	largeFirst := `{"exactID":9007199254740993,"padding":"` + strings.Repeat("x", 23_050) + `"`
	largeTail := `,"records":["recorded_sales"],"metrics":{"hits":50,"total":53},"pagination":{"continueToken":"next-page-token"}}`
	largeWant := largeFirst + "\n" + largeTail
	if at := strings.Index(largeWant, "recorded_sales"); at <= webFetchMaxReturn {
		t.Fatalf("test result should put recorded_sales beyond the former %d-byte cap, got offset %d", webFetchMaxReturn, at)
	}

	underLimitUTF8 := strings.Repeat("界", (mcpResultMaxReturn-1)/len("界"))
	overLimitUTF8 := strings.Repeat("界", mcpResultMaxReturn/len("界")+1)
	imageData := []byte{0x89, 'P', 'N', 'G'}

	server := mcp.NewServer(&mcp.Implementation{Name: "result-test", Version: "1.0.0"}, nil)
	addResultTool := func(name string, result *mcp.CallToolResult) {
		server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return result, nil
		})
	}
	addResultTool("large_semantic_search", &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: largeFirst},
			&mcp.TextContent{Text: largeTail},
		},
		// MCP's decoded StructuredContent uses any and can round integers
		// above 2^53; TextContent must retain the exact value.
		StructuredContent: map[string]any{"exactID": exactID, "roundedID": float64(exactID)},
	})
	addResultTool("exact_limit", &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", mcpResultMaxReturn)}},
	})
	addResultTool("under_limit_utf8", &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: underLimitUTF8}},
	})
	addResultTool("over_limit_utf8", &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: overLimitUTF8}},
	})
	addResultTool("image_result", &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "camera snapshot"},
			&mcp.ImageContent{MIMEType: "image/png", Data: imageData},
		},
	})
	addResultTool("error_result", &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "backend unavailable"}},
		IsError: true,
	})

	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil))
	t.Cleanup(httpServer.Close)

	session, err := ConnectMCPEndpointWithClient(context.Background(), httpServer.URL, httpServer.Client(), "results")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	byName := make(map[string]engine.Tool, len(session.Tools))
	for _, tool := range session.Tools {
		byName[tool.Name] = tool
	}

	tests := []struct {
		name           string
		wantText       string
		wantErrParts   []string
		wantImageMIME  string
		wantImageBytes []byte
	}{
		{name: "large_semantic_search", wantText: largeWant},
		{name: "exact_limit", wantText: strings.Repeat("x", mcpResultMaxReturn)},
		{name: "under_limit_utf8", wantText: underLimitUTF8},
		{
			name:         "over_limit_utf8",
			wantErrParts: []string{"65538 bytes", "not truncated", "not passed to the model", "already executed", "do not repeat side-effecting calls", "read-only queries", "smaller page"},
		},
		{name: "image_result", wantText: "camera snapshot", wantImageMIME: "image/png", wantImageBytes: imageData},
		{name: "error_result", wantErrParts: []string{"backend unavailable"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := byName["results__"+tc.name]
			if !ok || tool.ExecRich == nil {
				t.Fatalf("connected MCP tool %q is missing", tc.name)
			}
			obs, err := tool.ExecRich(context.Background(), `{}`)
			if len(tc.wantErrParts) > 0 {
				if err == nil {
					t.Fatalf("ExecRich returned no error; observation text length=%d", len(obs.Text))
				}
				for _, part := range tc.wantErrParts {
					if !strings.Contains(err.Error(), part) {
						t.Errorf("error %q does not contain %q", err, part)
					}
				}
				if obs.Text != "" || len(obs.Images) != 0 {
					t.Fatalf("error returned partial observation: text length=%d, images=%d", len(obs.Text), len(obs.Images))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if obs.Text != tc.wantText {
				t.Fatalf("text length=%d, want %d", len(obs.Text), len(tc.wantText))
			}
			if tc.name == "large_semantic_search" {
				if strings.Contains(obs.Text, `"roundedID"`) || !strings.Contains(obs.Text, `"exactID":9007199254740993`) {
					t.Fatalf("text result did not preserve the exact integer without appending decoded StructuredContent")
				}
				if !strings.HasSuffix(obs.Text, `"continueToken":"next-page-token"}}`) {
					t.Fatalf("tail pagination data is missing from the complete result")
				}
			}
			if tc.wantImageMIME != "" {
				if len(obs.Images) != 1 || obs.Images[0].MIMEType != tc.wantImageMIME || string(obs.Images[0].Data) != string(tc.wantImageBytes) {
					t.Fatalf("image result = %#v, want one %s image with original bytes", obs.Images, tc.wantImageMIME)
				}
			} else if len(obs.Images) != 0 {
				t.Fatalf("got %d unexpected images", len(obs.Images))
			}
		})
	}
}
