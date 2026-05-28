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

// Package systemd registers the LinuxMCP "systemd" toolset: read-only
// queries against systemd (unit status, journal tail, unit listing) and,
// when not readOnly, lifecycle actions (start/stop/restart/enable/disable).
package systemd

import (
	"context"
	"fmt"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp"
	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp/sshexec"
)

func init() {
	linuxmcp.Register("systemd", register)
}

func register(srv *mcp.Server, p *linuxmcp.Provider) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "systemd_unit_status",
		Description: "Run `systemctl status <unit>` (no pager) and return the textual output.",
	}, unitStatusHandler(p))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "systemd_list_units",
		Description: "Return `systemctl list-units --type=service --all --no-pager` for the target.",
	}, listUnitsHandler(p))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "systemd_journal_tail",
		Description: "Tail recent journald entries for an optional unit using `journalctl -n <lines>`. " +
			"Returns plain-text log lines.",
	}, journalTailHandler(p))

	if !p.ReadOnly() {
		mcp.AddTool(srv, &mcp.Tool{
			Name: "systemd_lifecycle",
			Description: "Run a `systemctl <action> <unit>` lifecycle command. " +
				"Allowed actions: start, stop, restart, reload, enable, disable. " +
				"Disabled when LinuxMCP.spec.readOnly=true.",
		}, lifecycleHandler(p))
	}
}

// validUnitName is a defensive whitelist for the bits of a unit name we
// accept verbatim into a shell command.  Per systemd.unit(5), unit names use
// a small set of characters; we err on the strict side here.
func validUnitName(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_' || r == '.' || r == '@' || r == ':' || r == '\\':
		default:
			return false
		}
	}
	return true
}

// ─── systemd_unit_status ────────────────────────────────────────────────────

type UnitStatusInput struct {
	Unit   string `json:"unit" jsonschema:"systemd unit name (e.g. nginx.service)"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

type UnitStatusOutput struct {
	Target   string `json:"target"`
	Unit     string `json:"unit"`
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
}

func unitStatusHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[UnitStatusInput, UnitStatusOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UnitStatusInput) (*mcp.CallToolResult, UnitStatusOutput, error) {
		if !validUnitName(in.Unit) {
			return nil, UnitStatusOutput{}, fmt.Errorf("systemd_unit_status: invalid unit name %q", in.Unit)
		}
		res, err := sshexec.Run(ctx, p, in.Target,
			"systemctl status --no-pager -- "+sshexec.ShellQuote(in.Unit))
		if err != nil {
			return nil, UnitStatusOutput{Target: in.Target, Unit: in.Unit},
				fmt.Errorf("systemd_unit_status: %w", err)
		}
		out := UnitStatusOutput{
			Target:   res.Target,
			Unit:     in.Unit,
			ExitCode: res.ExitCode,
			Output:   res.Stdout,
		}
		if out.Output == "" {
			out.Output = res.Stderr
		}
		return nil, out, nil
	}
}

// ─── systemd_list_units ─────────────────────────────────────────────────────

type ListUnitsInput struct {
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

type ListUnitsOutput struct {
	Target  string `json:"target"`
	Listing string `json:"listing"`
}

func listUnitsHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[ListUnitsInput, ListUnitsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListUnitsInput) (*mcp.CallToolResult, ListUnitsOutput, error) {
		res, err := sshexec.Run(ctx, p, in.Target,
			"systemctl list-units --type=service --all --no-pager --no-legend")
		if err != nil {
			return nil, ListUnitsOutput{Target: in.Target}, fmt.Errorf("systemd_list_units: %w", err)
		}
		return nil, ListUnitsOutput{Target: res.Target, Listing: res.Stdout}, nil
	}
}

// ─── systemd_journal_tail ───────────────────────────────────────────────────

type JournalTailInput struct {
	Unit   string `json:"unit,omitempty" jsonschema:"optional systemd unit (defaults to system-wide)"`
	Lines  int    `json:"lines,omitempty" jsonschema:"number of lines to return (default 100, max 5000)"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

type JournalTailOutput struct {
	Target string `json:"target"`
	Unit   string `json:"unit,omitempty"`
	Lines  int    `json:"lines"`
	Output string `json:"output"`
}

func journalTailHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[JournalTailInput, JournalTailOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in JournalTailInput) (*mcp.CallToolResult, JournalTailOutput, error) {
		n := in.Lines
		if n <= 0 {
			n = 100
		}
		if n > 5000 {
			n = 5000
		}
		cmd := "journalctl --no-pager -n " + strconv.Itoa(n)
		if in.Unit != "" {
			if !validUnitName(in.Unit) {
				return nil, JournalTailOutput{}, fmt.Errorf("systemd_journal_tail: invalid unit name %q", in.Unit)
			}
			cmd += " -u " + sshexec.ShellQuote(in.Unit)
		}
		res, err := sshexec.Run(ctx, p, in.Target, cmd)
		if err != nil {
			return nil, JournalTailOutput{Target: in.Target, Unit: in.Unit, Lines: n},
				fmt.Errorf("systemd_journal_tail: %w", err)
		}
		return nil, JournalTailOutput{
			Target: res.Target,
			Unit:   in.Unit,
			Lines:  n,
			Output: res.Stdout,
		}, nil
	}
}

// ─── systemd_lifecycle ──────────────────────────────────────────────────────

type LifecycleInput struct {
	Action string `json:"action" jsonschema:"one of: start, stop, restart, reload, enable, disable"`
	Unit   string `json:"unit" jsonschema:"systemd unit name"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

type LifecycleOutput struct {
	Target   string `json:"target"`
	Action   string `json:"action"`
	Unit     string `json:"unit"`
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output,omitempty"`
}

var allowedLifecycleActions = map[string]struct{}{
	"start":   {},
	"stop":    {},
	"restart": {},
	"reload":  {},
	"enable":  {},
	"disable": {},
}

func lifecycleHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[LifecycleInput, LifecycleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in LifecycleInput) (*mcp.CallToolResult, LifecycleOutput, error) {
		if _, ok := allowedLifecycleActions[in.Action]; !ok {
			return nil, LifecycleOutput{},
				fmt.Errorf("systemd_lifecycle: action %q not allowed (start|stop|restart|reload|enable|disable)", in.Action)
		}
		if !validUnitName(in.Unit) {
			return nil, LifecycleOutput{}, fmt.Errorf("systemd_lifecycle: invalid unit name %q", in.Unit)
		}
		cmd := fmt.Sprintf("systemctl %s -- %s", in.Action, sshexec.ShellQuote(in.Unit))
		res, err := sshexec.Run(ctx, p, in.Target, cmd)
		if err != nil {
			return nil, LifecycleOutput{Target: in.Target, Action: in.Action, Unit: in.Unit},
				fmt.Errorf("systemd_lifecycle: %w", err)
		}
		out := LifecycleOutput{
			Target:   res.Target,
			Action:   in.Action,
			Unit:     in.Unit,
			ExitCode: res.ExitCode,
			Output:   res.Stdout,
		}
		if out.Output == "" {
			out.Output = res.Stderr
		}
		return nil, out, nil
	}
}
