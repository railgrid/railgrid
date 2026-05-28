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
	"fmt"
	"slices"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// Target identifies a single MCP-callable edge inside a LinuxMCP set.
type Target struct {
	Cluster  string
	EdgeName string
}

// OpenSessionFunc opens a fresh SSH client to the given edge.  Callers must
// Close the returned client.  The hub-side implementation (in
// pkg/virtual/builder/linux_mcp_builder.go) wires this up using the existing
// ConnManager + SSH helpers; keeping it as a function pointer lets this
// package stay independent of hub-internal types and easily unit-testable
// with an in-memory SSH server.
type OpenSessionFunc func(ctx context.Context, edgeName string) (*gossh.Client, error)

// Provider holds per-MCP-session state: which edges are in scope, how to dial
// them, and the policy limits that apply to every tool call.  One Provider is
// created per inbound HTTP request (the LinuxMCP server is stateless across
// requests, matching the kube-mcp design).
type Provider struct {
	cluster    string
	edges      []string
	open       OpenSessionFunc
	cmdTimeout time.Duration
	maxOutputB int
	readOnly   bool
}

// Config bundles the wiring for a Provider.
type Config struct {
	// Cluster is the kcp cluster path that owns the LinuxMCP object.
	Cluster string
	// EdgeNames are the names of the connected server-type edges that the
	// LinuxMCP's edgeSelector resolves to.  Already filtered by tunnel state.
	EdgeNames []string
	// OpenSession is invoked by tool handlers to open an SSH client.
	OpenSession OpenSessionFunc
	// CommandTimeout caps each tool's wall-clock execution time.  Default 30s.
	CommandTimeout time.Duration
	// MaxOutputBytes caps stdout+stderr returned per tool call.  Default 1 MiB.
	MaxOutputBytes int
	// ReadOnly disables every mutating tool when true.
	ReadOnly bool
}

// NewProvider creates a Provider with the given configuration.
func NewProvider(cfg Config) *Provider {
	if cfg.OpenSession == nil {
		// We accept a nil OpenSession in tests that exercise the schema-only
		// path (tools/list, etc.).  Tool calls will return an error.
		cfg.OpenSession = func(_ context.Context, edge string) (*gossh.Client, error) {
			return nil, fmt.Errorf("linuxmcp: no OpenSession configured (edge=%s)", edge)
		}
	}
	timeout := cfg.CommandTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	maxOut := cfg.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = 1 << 20 // 1 MiB
	}
	return &Provider{
		cluster:    cfg.Cluster,
		edges:      append([]string(nil), cfg.EdgeNames...),
		open:       cfg.OpenSession,
		cmdTimeout: timeout,
		maxOutputB: maxOut,
		readOnly:   cfg.ReadOnly,
	}
}

// Cluster returns the kcp cluster path this provider serves.
func (p *Provider) Cluster() string { return p.cluster }

// Targets returns the resolved candidate edges for this session.
func (p *Provider) Targets() []Target {
	out := make([]Target, 0, len(p.edges))
	for _, e := range p.edges {
		out = append(out, Target{Cluster: p.cluster, EdgeName: e})
	}
	return out
}

// DefaultTarget returns the first connected edge, or "" if none.  Tools omit
// the "target" parameter to mean "the default".
func (p *Provider) DefaultTarget() string {
	if len(p.edges) == 0 {
		return ""
	}
	return p.edges[0]
}

// HasTarget reports whether the named edge is in this provider's resolved set.
func (p *Provider) HasTarget(edgeName string) bool {
	return slices.Contains(p.edges, edgeName)
}

// CommandTimeout returns the per-tool wall-clock cap.
func (p *Provider) CommandTimeout() time.Duration { return p.cmdTimeout }

// MaxOutputBytes returns the cap on combined stdout+stderr per tool call.
func (p *Provider) MaxOutputBytes() int { return p.maxOutputB }

// ReadOnly reports whether mutating tools should be refused.
func (p *Provider) ReadOnly() bool { return p.readOnly }

// OpenSession opens an SSH client for the given edge.  An empty edgeName uses
// the default target.  Callers must Close the returned client.
func (p *Provider) OpenSession(ctx context.Context, edgeName string) (*gossh.Client, error) {
	if edgeName == "" {
		edgeName = p.DefaultTarget()
	}
	if edgeName == "" {
		return nil, fmt.Errorf("linuxmcp: no connected edges available")
	}
	if !p.HasTarget(edgeName) {
		return nil, fmt.Errorf("linuxmcp: edge %q is not in this LinuxMCP's resolved set", edgeName)
	}
	return p.open(ctx, edgeName)
}
