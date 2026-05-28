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

// Package diag registers the LinuxMCP "diag" toolset: read-only diagnostic
// snapshots (df, free, uptime, ps, dmesg tail).  Every tool here is safe to
// expose under readOnly because none of them mutate state.
package diag

import (
	"context"
	"fmt"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp"
	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp/sshexec"
)

func init() {
	linuxmcp.Register("diag", register)
}

func register(srv *mcp.Server, p *linuxmcp.Provider) {
	mcp.AddTool(srv, &mcp.Tool{Name: "diag_df", Description: "Run `df -h` on the target edge."}, dfHandler(p))
	mcp.AddTool(srv, &mcp.Tool{Name: "diag_free", Description: "Run `free -h` on the target edge."}, freeHandler(p))
	mcp.AddTool(srv, &mcp.Tool{Name: "diag_uptime", Description: "Run `uptime` on the target edge."}, uptimeHandler(p))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "diag_ps",
		Description: "Run `ps -eo pid,user,pcpu,pmem,comm --sort=-pcpu | head -n <count+1>` on the target edge.",
	}, psHandler(p))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "diag_dmesg_tail",
		Description: "Tail recent kernel messages via `dmesg --ctime | tail -n <lines>`. Requires CAP_SYSLOG.",
	}, dmesgTailHandler(p))
}

// noInput is used by tools that take only an optional target.
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

func dfHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[targetOnlyInput, textOutput] {
	return makeTextHandler(p, "diag_df", "df -h")
}
func freeHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[targetOnlyInput, textOutput] {
	return makeTextHandler(p, "diag_free", "free -h")
}
func uptimeHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[targetOnlyInput, textOutput] {
	return makeTextHandler(p, "diag_uptime", "uptime")
}

// ─── diag_ps ─────────────────────────────────────────────────────────────────

type psInput struct {
	Count  int    `json:"count,omitempty" jsonschema:"number of processes to return (default 20, max 200)"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

func psHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[psInput, textOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in psInput) (*mcp.CallToolResult, textOutput, error) {
		n := in.Count
		if n <= 0 {
			n = 20
		}
		if n > 200 {
			n = 200
		}
		cmd := "ps -eo pid,user,pcpu,pmem,comm --sort=-pcpu | head -n " + strconv.Itoa(n+1)
		res, err := sshexec.Run(ctx, p, in.Target, cmd)
		if err != nil {
			return nil, textOutput{Target: in.Target}, fmt.Errorf("diag_ps: %w", err)
		}
		return nil, textOutput{Target: res.Target, Output: res.Stdout}, nil
	}
}

// ─── diag_dmesg_tail ─────────────────────────────────────────────────────────

type dmesgTailInput struct {
	Lines  int    `json:"lines,omitempty" jsonschema:"number of lines to tail (default 100, max 5000)"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

func dmesgTailHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[dmesgTailInput, textOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in dmesgTailInput) (*mcp.CallToolResult, textOutput, error) {
		n := in.Lines
		if n <= 0 {
			n = 100
		}
		if n > 5000 {
			n = 5000
		}
		cmd := "dmesg --ctime 2>/dev/null | tail -n " + strconv.Itoa(n)
		res, err := sshexec.Run(ctx, p, in.Target, cmd)
		if err != nil {
			return nil, textOutput{Target: in.Target}, fmt.Errorf("diag_dmesg_tail: %w", err)
		}
		return nil, textOutput{Target: res.Target, Output: res.Stdout}, nil
	}
}
