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

package tunnel

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/railgrid/provider-sdk/dataplane/conformance"
)

func TestRootMCPListEdges(t *testing.T) {
	const token = "caller-token"
	s := testServer()
	s.edgeConnManager = NewConnManager()
	callers := &conformance.FakeCallers{
		Cluster: testCluster,
		Token:   token,
		ListKinds: map[schema.GroupVersionResource]string{
			{Group: s.group, Version: s.version, Resource: serviceResource}: "ServiceList",
		},
	}
	s.callers = callers
	for resource, kind := range s.kinds {
		callers.ListKinds[kind.GVR] = kind.Kind + "List"
		edge := edgeObject(resource, kind.Kind, "connected")
		edge.Object["status"] = map[string]any{"connected": true}
		callers.Objects = append(callers.Objects, edge, edgeObject(resource, kind.Kind, "pending"))
	}
	offline := edgeObject(macOSServerResource, "MacOSServer", "offline")
	offline.Object["status"] = map[string]any{"connected": false}
	callers.Objects = append(callers.Objects, offline)

	ctx := t.Context()
	handler := s.RootMCPHandler()
	tools, err := s.kubeListTools(ctx, handler, token, testCluster)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(tools, func(tool kubeTool) bool { return tool.Name == "list_edges" })
	if index < 0 || tools[index].Annotations == nil || !tools[index].Annotations.ReadOnlyHint {
		t.Fatalf("expected a read-only list_edges tool, got %+v", tools)
	}

	call := func(t *testing.T, bearer string) *mcp.CallToolResult {
		t.Helper()
		result, err := s.kubeCallTool(ctx, handler, bearer, testCluster, "list_edges", nil)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	checkEdges := func(t *testing.T, result *mcp.CallToolResult, want []connectedEdge) {
		t.Helper()
		if result.IsError {
			t.Fatalf("list_edges failed: %+v", result.Content)
		}
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var out listEdgesOutput
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		if out.Edges == nil || !slices.Equal(out.Edges, want) {
			t.Fatalf("list_edges = %+v, want %+v", out.Edges, want)
		}
	}

	t.Run("all connected kinds", func(t *testing.T) {
		checkEdges(t, call(t, token), []connectedEdge{
			{Name: "connected", Kind: "KubernetesCluster"},
			{Name: "connected", Kind: "LinuxServer"},
			{Name: "connected", Kind: "MacOSServer"},
		})
	})
	t.Run("caller scope", func(t *testing.T) {
		// The fake exposes workspace objects only to the configured caller.
		checkEdges(t, call(t, "other-token"), []connectedEdge{})
	})
	t.Run("forbidden list surfaces as an error", func(t *testing.T) {
		client, err := callers.For(testCluster, token)
		if err != nil {
			t.Fatal(err)
		}
		client.(*dynamicfake.FakeDynamicClient).PrependReactor("list", macOSServerResource, func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(action.GetResource().GroupResource(), "", nil)
		})
		result := call(t, token)
		if !result.IsError || len(result.Content) != 1 {
			t.Fatalf("expected a tool error, got %+v", result)
		}
		content, ok := result.Content[0].(*mcp.TextContent)
		if !ok || !strings.Contains(content.Text, "list macosservers") || !strings.Contains(content.Text, "forbidden") {
			t.Fatalf("expected a macOS list permission error, got %+v", result.Content[0])
		}
	})
}
