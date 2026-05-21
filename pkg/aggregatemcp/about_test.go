/*
Copyright 2026 The Faros Authors.

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

package aggregatemcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeEnumerate returns the supplied edge lists every time it's invoked.
// Tests use it to make the "about resource counts live edges" assertion
// independent of any real edge inventory.
func fakeEnumerate(kube, linux []TargetInfo) TargetEnumerator {
	return func(_ context.Context) ([]TargetInfo, []TargetInfo, error) {
		return kube, linux, nil
	}
}

// connectInMemory wires the aggregate server to a client over the SDK's
// in-memory transport pair so tests don't need an HTTP listener.
func connectInMemory(t *testing.T, cfg Config) (*mcp.ClientSession, func()) {
	t.Helper()
	srv := newServer(cfg)
	t1, t2 := mcp.NewInMemoryTransports()

	serverSession, err := srv.Connect(context.Background(), t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	clientSession, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	return clientSession, func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	}
}

// TestAboutResource_Defaults verifies the kedge://about handler fills in
// sensible defaults (schemaVersion / role=aggregate / discoveryTool /
// human-readable readme) when the caller leaves Config.About zero.
func TestAboutResource_Defaults(t *testing.T) {
	cs, cleanup := connectInMemory(t, Config{
		Enumerate: fakeEnumerate(nil, nil),
	})
	defer cleanup()

	got, err := readAbout(t, cs)
	if err != nil {
		t.Fatalf("read about: %v", err)
	}

	if got.SchemaVersion != "kedge.faros.sh/about/v1" {
		t.Errorf("SchemaVersion: got %q, want %q", got.SchemaVersion, "kedge.faros.sh/about/v1")
	}
	if got.Role != "aggregate" {
		t.Errorf("Role: got %q, want %q", got.Role, "aggregate")
	}
	if got.DiscoveryTool != "list_targets" {
		t.Errorf("DiscoveryTool: got %q, want %q", got.DiscoveryTool, "list_targets")
	}
	if got.HumanReadme == "" {
		t.Errorf("HumanReadme: expected non-empty fallback")
	}
}

// TestAboutResource_RespectsCallerSuppliedFields verifies that whatever the
// caller writes into Config.About is preserved through the handler.
func TestAboutResource_RespectsCallerSuppliedFields(t *testing.T) {
	cs, cleanup := connectInMemory(t, Config{
		Enumerate: fakeEnumerate(nil, nil),
		About: AboutDoc{
			Tenant:      "root:kedge:tenants:abc123",
			MCPServer:   "production",
			EndpointURL: "https://hub.example.com/services/mcpserver/abc123/.../production/mcp",
			ReadOnly:    true,
			Toolsets: AboutToolsets{
				Kubernetes: []string{"core", "helm"},
				Linux:      []string{"core", "systemd"},
			},
			HumanReadme: "## Operator note\nProduction tenant — confirm before destructive ops.",
		},
	})
	defer cleanup()

	got, err := readAbout(t, cs)
	if err != nil {
		t.Fatalf("read about: %v", err)
	}

	if got.Tenant != "root:kedge:tenants:abc123" {
		t.Errorf("Tenant: got %q", got.Tenant)
	}
	if got.MCPServer != "production" {
		t.Errorf("MCPServer: got %q", got.MCPServer)
	}
	if !got.ReadOnly {
		t.Errorf("ReadOnly: expected true")
	}
	if len(got.Toolsets.Kubernetes) != 2 || got.Toolsets.Kubernetes[0] != "core" {
		t.Errorf("Toolsets.Kubernetes: got %v", got.Toolsets.Kubernetes)
	}
	if got.HumanReadme != "## Operator note\nProduction tenant — confirm before destructive ops." {
		t.Errorf("HumanReadme: got %q", got.HumanReadme)
	}
}

// TestAboutResource_ReflectsLiveEdgeCount asserts that ConnectedEdges in the
// about payload is computed at read time from the Enumerator output, not
// snapshotted at server-construction time.
func TestAboutResource_ReflectsLiveEdgeCount(t *testing.T) {
	cs, cleanup := connectInMemory(t, Config{
		Enumerate: fakeEnumerate(
			[]TargetInfo{
				{Name: "kube-a", Type: "kubernetes", Connected: true},
				{Name: "kube-b", Type: "kubernetes", Connected: false}, // disconnected — should NOT count
				{Name: "kube-c", Type: "kubernetes", Connected: true},
			},
			[]TargetInfo{
				{Name: "linux-a", Type: "linux", Connected: true},
				{Name: "linux-b", Type: "linux", Connected: true},
			},
		),
	})
	defer cleanup()

	got, err := readAbout(t, cs)
	if err != nil {
		t.Fatalf("read about: %v", err)
	}

	if got.ConnectedEdges == nil {
		t.Fatalf("ConnectedEdges: expected non-nil map")
	}
	if got.ConnectedEdges["kubernetes"] != 2 {
		t.Errorf("ConnectedEdges[kubernetes]: got %d, want 2", got.ConnectedEdges["kubernetes"])
	}
	if got.ConnectedEdges["linux"] != 2 {
		t.Errorf("ConnectedEdges[linux]: got %d, want 2", got.ConnectedEdges["linux"])
	}
}

// readAbout fetches kedge://about over the supplied MCP client session and
// unmarshals it into an AboutDoc.  Encapsulates the MCP protocol boilerplate
// so individual tests stay focused on the assertion they care about.
func readAbout(t *testing.T, cs *mcp.ClientSession) (AboutDoc, error) {
	t.Helper()
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "kedge://about"})
	if err != nil {
		return AboutDoc{}, err
	}
	if len(res.Contents) != 1 {
		t.Fatalf("expected exactly 1 content block, got %d", len(res.Contents))
	}
	var doc AboutDoc
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &doc); err != nil {
		return AboutDoc{}, err
	}
	return doc, nil
}
