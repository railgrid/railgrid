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

// Package net registers the LinuxMCP "net" toolset: read-only network
// inspection (ss listening sockets, ip address listing, ping, DNS lookup).
package net

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp"
	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp/sshexec"
)

func init() {
	linuxmcp.Register("net", register)
}

func register(srv *mcp.Server, p *linuxmcp.Provider) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "net_listening_sockets",
		Description: "Run `ss -tulpn` to list listening TCP/UDP sockets (PID/program where readable).",
	}, listeningSocketsHandler(p))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "net_ip_addr",
		Description: "Run `ip -br addr` to list configured network interface addresses.",
	}, ipAddrHandler(p))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "net_ip_route",
		Description: "Run `ip route` to dump the routing table.",
	}, ipRouteHandler(p))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "net_ping",
		Description: "Run `ping -c <count> <host>` from the edge. Count is bounded [1, 20]; host is host/IP only.",
	}, pingHandler(p))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "net_dns_lookup",
		Description: "Resolve a hostname on the edge via `getent ahosts <host>` (preferred) or `nslookup`. " +
			"Host is host/IP only.",
	}, dnsLookupHandler(p))
}

// validHost is a defensive whitelist for host/IP arguments passed to
// network tools.  Allows DNS hostnames and IPv4/IPv6 literals; rejects
// anything resembling a shell metacharacter.
func validHost(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.' || r == '-' || r == ':' || r == '_':
		default:
			return false
		}
	}
	return true
}

// ─── shared shapes ───────────────────────────────────────────────────────────

type targetOnlyInput struct {
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

type textOutput struct {
	Target string `json:"target"`
	Output string `json:"output"`
}

func makeTextHandler(p *linuxmcp.Provider, toolName, cmd string) mcp.ToolHandlerFor[targetOnlyInput, textOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in targetOnlyInput) (*mcp.CallToolResult, textOutput, error) {
		res, err := sshexec.Run(ctx, p, in.Target, cmd)
		if err != nil {
			return nil, textOutput{Target: in.Target}, fmt.Errorf("%s: %w", toolName, err)
		}
		return nil, textOutput{Target: res.Target, Output: res.Stdout}, nil
	}
}

func listeningSocketsHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[targetOnlyInput, textOutput] {
	// `ss` may need root to show PIDs; we still return what we can if not.
	return makeTextHandler(p, "net_listening_sockets", "ss -tulpn 2>/dev/null || ss -tuln")
}

func ipAddrHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[targetOnlyInput, textOutput] {
	return makeTextHandler(p, "net_ip_addr", "ip -br addr")
}

func ipRouteHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[targetOnlyInput, textOutput] {
	return makeTextHandler(p, "net_ip_route", "ip route")
}

// ─── net_ping ────────────────────────────────────────────────────────────────

type pingInput struct {
	Host   string `json:"host" jsonschema:"DNS hostname or IP address to ping"`
	Count  int    `json:"count,omitempty" jsonschema:"number of ICMP probes (default 4, max 20)"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

type pingOutput struct {
	Target   string `json:"target"`
	Host     string `json:"host"`
	Count    int    `json:"count"`
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
}

func pingHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[pingInput, pingOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in pingInput) (*mcp.CallToolResult, pingOutput, error) {
		if !validHost(in.Host) {
			return nil, pingOutput{}, fmt.Errorf("net_ping: invalid host %q", in.Host)
		}
		n := in.Count
		if n <= 0 {
			n = 4
		}
		if n > 20 {
			n = 20
		}
		cmd := "ping -c " + strconv.Itoa(n) + " -W 5 -- " + sshexec.ShellQuote(in.Host)
		res, err := sshexec.Run(ctx, p, in.Target, cmd)
		if err != nil {
			return nil, pingOutput{Target: in.Target, Host: in.Host, Count: n},
				fmt.Errorf("net_ping: %w", err)
		}
		out := pingOutput{
			Target:   res.Target,
			Host:     in.Host,
			Count:    n,
			ExitCode: res.ExitCode,
			Output:   res.Stdout,
		}
		if out.Output == "" {
			out.Output = res.Stderr
		}
		return nil, out, nil
	}
}

// ─── net_dns_lookup ──────────────────────────────────────────────────────────

type dnsLookupInput struct {
	Host   string `json:"host" jsonschema:"DNS hostname to resolve"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

type dnsLookupOutput struct {
	Target string `json:"target"`
	Host   string `json:"host"`
	Tool   string `json:"tool"` // "getent" or "nslookup"
	Output string `json:"output"`
}

func dnsLookupHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[dnsLookupInput, dnsLookupOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in dnsLookupInput) (*mcp.CallToolResult, dnsLookupOutput, error) {
		if !validHost(in.Host) {
			return nil, dnsLookupOutput{}, fmt.Errorf("net_dns_lookup: invalid host %q", in.Host)
		}
		q := sshexec.ShellQuote(in.Host)
		// Try `getent ahosts` first (works on glibc systems with nsswitch
		// configured); fall back to nslookup on musl/minimal images.
		cmd := "if command -v getent >/dev/null 2>&1; then getent ahosts -- " + q +
			"; else nslookup " + q + "; fi"
		res, err := sshexec.Run(ctx, p, in.Target, cmd)
		if err != nil {
			return nil, dnsLookupOutput{Target: in.Target, Host: in.Host}, fmt.Errorf("net_dns_lookup: %w", err)
		}
		tool := "getent"
		if strings.Contains(res.Stdout, "Server:") || strings.Contains(res.Stdout, "Address:") {
			tool = "nslookup"
		}
		return nil, dnsLookupOutput{
			Target: res.Target,
			Host:   in.Host,
			Tool:   tool,
			Output: res.Stdout,
		}, nil
	}
}
