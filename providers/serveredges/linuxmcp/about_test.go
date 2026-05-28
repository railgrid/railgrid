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

package linuxmcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectInMemory wires a freshly-built linux MCP server to an in-memory
// client transport so tests exercise the resource path without HTTP.
func connectInMemory(t *testing.T, p *Provider, enabled []string, meta Meta) (*mcp.ClientSession, func()) {
	t.Helper()
	srv := newServer(p, enabled, meta)
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

// readAbout fetches kedge://about over the given client session.
func readAbout(t *testing.T, cs *mcp.ClientSession) AboutDoc {
	t.Helper()
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: aboutURI})
	if err != nil {
		t.Fatalf("read about: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("expected exactly 1 content block, got %d", len(res.Contents))
	}
	var doc AboutDoc
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &doc); err != nil {
		t.Fatalf("unmarshal about: %v", err)
	}
	return doc
}

func TestLinuxMCPAbout_Defaults(t *testing.T) {
	// A Provider with no OpenSession is fine for the about path — it
	// doesn't dial SSH.
	p := NewProvider(Config{Cluster: "root", EdgeNames: []string{"a", "b"}})
	cs, cleanup := connectInMemory(t, p, nil, Meta{})
	defer cleanup()

	got := readAbout(t, cs)

	if got.SchemaVersion != "kedge.faros.sh/about/v1" {
		t.Errorf("SchemaVersion: got %q", got.SchemaVersion)
	}
	if got.Role != "linux" {
		t.Errorf("Role: got %q, want %q", got.Role, "linux")
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "linux" || got.Capabilities[1] != "ssh" {
		t.Errorf("Capabilities: got %v, want [linux ssh]", got.Capabilities)
	}
	if len(got.Toolsets) != 1 || got.Toolsets[0] != "core" {
		t.Errorf("Toolsets default: got %v, want [core]", got.Toolsets)
	}
	if got.ConnectedEdges["linux"] != 2 {
		t.Errorf("ConnectedEdges[linux]: got %d, want 2", got.ConnectedEdges["linux"])
	}
	if got.HumanReadme == "" {
		t.Errorf("HumanReadme: expected non-empty fallback")
	}
}

func TestLinuxMCPAbout_RespectsCallerFields(t *testing.T) {
	p := NewProvider(Config{Cluster: "root", EdgeNames: []string{"only"}})
	cs, cleanup := connectInMemory(t, p, []string{"core", "systemd"}, Meta{
		About: AboutDoc{
			Tenant:      "root:kedge:tenants:xyz",
			LinuxMCP:    "prod-linux",
			EndpointURL: "https://hub.example.com/services/linux-mcp/xyz/.../prod-linux/mcp",
			ReadOnly:    true,
			HumanReadme: "## Prod servers — confirm before destructive ops.",
		},
	})
	defer cleanup()

	got := readAbout(t, cs)

	if got.Tenant != "root:kedge:tenants:xyz" {
		t.Errorf("Tenant: got %q", got.Tenant)
	}
	if got.LinuxMCP != "prod-linux" {
		t.Errorf("LinuxMCP: got %q", got.LinuxMCP)
	}
	if !got.ReadOnly {
		t.Errorf("ReadOnly: expected true")
	}
	// Toolsets list should come from the enabled slice passed to newServer
	// (overrides the default "core" only when the caller picked others).
	if len(got.Toolsets) != 2 || got.Toolsets[1] != "systemd" {
		t.Errorf("Toolsets: got %v, want [core systemd]", got.Toolsets)
	}
	if got.HumanReadme != "## Prod servers — confirm before destructive ops." {
		t.Errorf("HumanReadme override: got %q", got.HumanReadme)
	}
}
