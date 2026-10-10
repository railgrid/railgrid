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

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
)

type discoveryTestToolPort struct {
	mcpTools            []projectAssistantTool
	browserTools        []projectAssistantTool
	mcpErr              error
	browserErr          error
	browserCalls        atomic.Int32
	browserWithRefCalls atomic.Int32
	lastBrowserRef      dataPlaneRef
	delegateWithRef     projectAssistantBrowserToolDiscovererWithRef
}

func (p *discoveryTestToolPort) DiscoverMCP(context.Context, identity, *aiv1alpha1.Project, projectLLMSettings) ([]projectAssistantTool, bool, error) {
	return p.mcpTools, false, p.mcpErr
}

func (p *discoveryTestToolPort) Invoke(context.Context, projectAssistantTool, projectAssistantToolCallRequest) (string, error) {
	return "{}", nil
}

func (p *discoveryTestToolPort) DiscoverBrowser(context.Context, identity, projectLLMSettings) ([]projectAssistantTool, error) {
	p.browserCalls.Add(1)
	return p.browserTools, p.browserErr
}

func (p *discoveryTestToolPort) DiscoverBrowserWithRef(ctx context.Context, id identity, settings projectLLMSettings, ref dataPlaneRef) ([]projectAssistantTool, error) {
	p.browserWithRefCalls.Add(1)
	p.lastBrowserRef = ref
	if p.delegateWithRef != nil {
		return p.delegateWithRef.DiscoverBrowserWithRef(ctx, id, settings, ref)
	}
	return p.browserTools, p.browserErr
}

type blockingDiscoveryToolPort struct {
	mcpStarted     chan struct{}
	browserStarted chan struct{}
	release        chan struct{}
	mcpTools       []projectAssistantTool
	browserTools   []projectAssistantTool
}

func (p *blockingDiscoveryToolPort) DiscoverMCP(ctx context.Context, _ identity, _ *aiv1alpha1.Project, _ projectLLMSettings) ([]projectAssistantTool, bool, error) {
	p.mcpStarted <- struct{}{}
	select {
	case <-p.release:
		return p.mcpTools, false, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

func (p *blockingDiscoveryToolPort) Invoke(context.Context, projectAssistantTool, projectAssistantToolCallRequest) (string, error) {
	return "{}", nil
}

func (p *blockingDiscoveryToolPort) DiscoverBrowser(ctx context.Context, _ identity, _ projectLLMSettings) ([]projectAssistantTool, error) {
	p.browserStarted <- struct{}{}
	select {
	case <-p.release:
		return p.browserTools, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func discoveryTestTool(name string) projectAssistantTool {
	return projectAssistantToolFunc{spec: projectAssistantToolSpec{
		Name:        name,
		Description: "test discovery tool",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Risk:        projectAssistantToolRiskRead,
	}}
}

func discoveryBrowserCatalog(description string) []projectMCPTool {
	return []projectMCPTool{
		{Name: "browser_navigate", Description: description, InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_snapshot", Description: "snapshot", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_console_messages", Description: "console", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_click", Description: "click", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_fill_form", Description: "fill", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
}

func discoveryTestServer(inspector projectAssistantPreviewInspector) *Server {
	return &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		tenantProviders:  defaultTestProviders,
		previewInspector: inspector,
	}
}

func TestProjectEinoAssistantDiscoveryRunsMCPAndBrowserConcurrently(t *testing.T) {
	port := &blockingDiscoveryToolPort{
		mcpStarted:     make(chan struct{}, 1),
		browserStarted: make(chan struct{}, 1),
		release:        make(chan struct{}),
		mcpTools:       []projectAssistantTool{discoveryTestTool("mcp_concurrent_test")},
		browserTools:   []projectAssistantTool{discoveryTestTool("browser_concurrent_test")},
	}
	server := discoveryTestServer(&fakeProjectAssistantPreviewInspector{})
	req := projectAssistantRunRequest{
		ToolPort:   port,
		TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	done := make(chan projectEinoAssistantToolDiscovery, 1)
	go func() { done <- projectEinoAssistantDiscoverTools(context.Background(), server, req) }()

	deadline := time.After(2 * time.Second)
	started := map[string]bool{}
	for len(started) < 2 {
		select {
		case <-port.mcpStarted:
			started["MCP"] = true
		case <-port.browserStarted:
			started["browser"] = true
		case <-deadline:
			close(port.release)
			t.Fatalf("discovery did not start both independent requests before either completed: started=%v", started)
		}
	}
	close(port.release)
	select {
	case discovery := <-done:
		if len(discovery.MCPTools) != 1 || discovery.MCPTools[0].Spec().Name != "mcp_concurrent_test" {
			t.Fatalf("MCP tools = %#v", discovery.MCPTools)
		}
		if len(discovery.BrowserTools) != 1 || discovery.BrowserTools[0].Spec().Name != "browser_concurrent_test" {
			t.Fatalf("browser tools = %#v", discovery.BrowserTools)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tool discovery did not finish after both requests were released")
	}
}

func TestProjectEinoAssistantDiscoveryKeepsIndependentFailuresAndGatesUnavailableBrowser(t *testing.T) {
	t.Run("browser failure keeps MCP tools", func(t *testing.T) {
		port := &discoveryTestToolPort{
			mcpTools:   []projectAssistantTool{discoveryTestTool("mcp_survives_browser_error")},
			browserErr: errors.New("catalog unavailable"),
		}
		discovery := projectEinoAssistantDiscoverTools(context.Background(), discoveryTestServer(&fakeProjectAssistantPreviewInspector{}), projectAssistantRunRequest{
			ToolPort: port, TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
		})
		if len(discovery.MCPTools) != 1 || len(discovery.BrowserTools) != 0 {
			t.Fatalf("discovery lost independent MCP result: MCP=%d browser=%d", len(discovery.MCPTools), len(discovery.BrowserTools))
		}
		if !strings.Contains(discovery.Prompt, "Playwright capability discovery failed") {
			t.Fatalf("browser failure was not explained in the prompt: %q", discovery.Prompt)
		}
	})

	t.Run("MCP failure keeps browser tools", func(t *testing.T) {
		port := &discoveryTestToolPort{
			mcpErr:       errors.New("MCP unavailable"),
			browserTools: []projectAssistantTool{discoveryTestTool("browser_survives_mcp_error")},
		}
		discovery := projectEinoAssistantDiscoverTools(context.Background(), discoveryTestServer(&fakeProjectAssistantPreviewInspector{}), projectAssistantRunRequest{
			ToolPort: port, TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
		})
		if len(discovery.MCPTools) != 0 || len(discovery.BrowserTools) != 1 {
			t.Fatalf("discovery lost independent browser result: MCP=%d browser=%d", len(discovery.MCPTools), len(discovery.BrowserTools))
		}
	})

	t.Run("unavailable browser is not discovered or exposed", func(t *testing.T) {
		port := &discoveryTestToolPort{browserTools: []projectAssistantTool{discoveryTestTool("stale_browser_tool")}}
		discovery := projectEinoAssistantDiscoverTools(context.Background(), discoveryTestServer(&fakeProjectAssistantPreviewInspector{healthErr: errors.New("not ready")}), projectAssistantRunRequest{
			ToolPort: port, TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
		})
		if discovery.IncludePreviewInspection || len(discovery.BrowserTools) != 0 {
			t.Fatalf("unavailable browser capability was exposed: %#v", discovery)
		}
		if got := port.browserCalls.Load(); got != 0 {
			t.Fatalf("browser discoverer called %d times while unavailable", got)
		}
	})
}

func TestProjectEinoAssistantDiscoveryUsesOneFreshReadyBrowserRef(t *testing.T) {
	studio := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Studio",
		"metadata":   map[string]any{"name": aiv1alpha1.StudioName},
		"status": map[string]any{"browser": map[string]any{
			"phase": aiv1alpha1.StudioServiceReady, "resource": "instances", "instance": "browser-a",
		}},
	}}
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(), studio)
	var studioGets atomic.Int32
	dynamicClient.PrependReactor("get", studioResource.GVR.Resource, func(action ktesting.Action) (bool, runtime.Object, error) {
		studioGets.Add(1)
		return false, nil, nil
	})
	id := identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"}
	server := discoveryTestServer(nil)
	server.projectClientFor = func(identity) (*asclient.Client, error) { return asclient.NewFromDynamic(dynamicClient), nil }
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	defer server.browserSessions.closeAll()
	ref := dataPlaneRef{Resource: "instances", Name: "browser-a"}
	server.browserSessions.setBrowserCatalog(id, ref, discoveryBrowserCatalog("catalog for current Ready ref"))
	httpPort := projectAssistantHTTPToolPort{server: server, request: httptest.NewRequest(http.MethodPost, "/", nil)}
	port := &discoveryTestToolPort{delegateWithRef: httpPort}
	state := newProjectEinoAssistantRunState()
	state.SetToolDiscovery(projectEinoAssistantToolDiscovery{
		IncludePreviewInspection: true,
		BrowserTools:             projectAssistantNativeBrowserToolsForSpecs(server, discoveryBrowserCatalog("stale checkpoint catalog")),
		BrowserCatalogCached:     true,
	})
	discovery := projectEinoAssistantRefreshToolDiscovery(context.Background(), server, projectAssistantRunRequest{
		Identity: id, ToolPort: port, TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}, state)
	if got := studioGets.Load(); got != 1 {
		t.Fatalf("Ready Studio GET count = %d, want one; browser ref should be passed to discovery", got)
	}
	if port.browserWithRefCalls.Load() != 1 || port.browserCalls.Load() != 0 || port.lastBrowserRef != ref {
		t.Fatalf("browser discovery calls/ref = withRef %d, legacy %d, ref %#v; want one ref-bound call for %#v", port.browserWithRefCalls.Load(), port.browserCalls.Load(), port.lastBrowserRef, ref)
	}
	if len(discovery.BrowserTools) != 5 {
		t.Fatalf("browser tools = %d, want the validated identity+ref catalog", len(discovery.BrowserTools))
	}
	var navigateDescription string
	for _, tool := range discovery.BrowserTools {
		if tool.Spec().Name == "browser_navigate" {
			navigateDescription = tool.Spec().Description
		}
	}
	if navigateDescription != "catalog for current Ready ref" {
		t.Fatalf("browser catalog description = %q, want the catalog bound to the freshly resolved ref", navigateDescription)
	}
}
