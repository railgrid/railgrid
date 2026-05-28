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

// Package pkg registers the LinuxMCP "pkg" toolset: package-manager queries
// (read-only) and, when not readOnly, install/upgrade/remove operations.
//
// The toolset auto-detects which package manager is available at call time
// (apt-get vs dnf vs yum vs zypper vs apk) by probing `command -v` in the
// remote shell.  This keeps a single set of MCP tools usable across distros
// without the LinuxMCP spec having to declare the distro up front.
package pkg

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp"
	"github.com/faroshq/faros-kedge/providers/serveredges/linuxmcp/sshexec"
)

func init() {
	linuxmcp.Register("pkg", register)
}

func register(srv *mcp.Server, p *linuxmcp.Provider) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "pkg_search",
		Description: "Search the system package index (apt/dnf/yum/zypper/apk) for matches against <query>.",
	}, searchHandler(p))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "pkg_info",
		Description: "Show package metadata (apt/dnf/yum/zypper/apk) for <name>.",
	}, infoHandler(p))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "pkg_list_installed",
		Description: "List installed packages on the target edge (apt/dnf/yum/zypper/apk).",
	}, listInstalledHandler(p))

	if !p.ReadOnly() {
		mcp.AddTool(srv, &mcp.Tool{
			Name: "pkg_install",
			Description: "Install a package on the target edge (apt-get/dnf/yum/zypper/apk) non-interactively. " +
				"Disabled when LinuxMCP.spec.readOnly=true.",
		}, installHandler(p))

		mcp.AddTool(srv, &mcp.Tool{
			Name: "pkg_remove",
			Description: "Remove a package on the target edge (apt-get/dnf/yum/zypper/apk) non-interactively. " +
				"Disabled when LinuxMCP.spec.readOnly=true.",
		}, removeHandler(p))
	}
}

// validPkgName matches Debian/RPM-style package names: lowercase letters,
// digits, plus, minus, dot, underscore.  Tight enough to be a safe shell arg
// without quoting (we still quote, but it short-circuits any clever
// metacharacter injection).
func validPkgName(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_' || r == '.' || r == '+' || r == ':':
		default:
			return false
		}
	}
	return true
}

// detectManagerSnippet is a shell prelude that defines $PKGMGR and the
// commands to use for each operation, by probing what's available with
// `command -v`.  Subsequent tools reference $PKG_INFO / $PKG_SEARCH / etc.
//
// Putting this in one place avoids 5×N if/elif ladders across tools.
const detectManagerSnippet = `
set -e
if command -v apt-get >/dev/null 2>&1; then
  PKGMGR=apt
  PKG_SEARCH="apt-cache search"
  PKG_INFO="apt-cache show"
  PKG_LIST="dpkg-query -W -f='\${binary:Package} \${Version}\\n'"
  PKG_INSTALL="DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends"
  PKG_REMOVE="DEBIAN_FRONTEND=noninteractive apt-get remove -y"
elif command -v dnf >/dev/null 2>&1; then
  PKGMGR=dnf
  PKG_SEARCH="dnf search"
  PKG_INFO="dnf info"
  PKG_LIST="rpm -qa"
  PKG_INSTALL="dnf install -y"
  PKG_REMOVE="dnf remove -y"
elif command -v yum >/dev/null 2>&1; then
  PKGMGR=yum
  PKG_SEARCH="yum search"
  PKG_INFO="yum info"
  PKG_LIST="rpm -qa"
  PKG_INSTALL="yum install -y"
  PKG_REMOVE="yum remove -y"
elif command -v zypper >/dev/null 2>&1; then
  PKGMGR=zypper
  PKG_SEARCH="zypper --non-interactive search"
  PKG_INFO="zypper --non-interactive info"
  PKG_LIST="rpm -qa"
  PKG_INSTALL="zypper --non-interactive install"
  PKG_REMOVE="zypper --non-interactive remove"
elif command -v apk >/dev/null 2>&1; then
  PKGMGR=apk
  PKG_SEARCH="apk search"
  PKG_INFO="apk info -a"
  PKG_LIST="apk info -v"
  PKG_INSTALL="apk add --no-progress"
  PKG_REMOVE="apk del --no-progress"
else
  echo "no supported package manager found (tried apt-get, dnf, yum, zypper, apk)" >&2
  exit 127
fi
`

// runWithManager executes `body` with the detection prelude in scope.
// The first line of stdout starts after the prelude.
func runWithManager(ctx context.Context, p *linuxmcp.Provider, target, body string) (sshexec.Result, error) {
	return sshexec.Run(ctx, p, target, detectManagerSnippet+"\n"+body)
}

// ─── shared output shape ────────────────────────────────────────────────────

type pkgResult struct {
	Target   string `json:"target"`
	ExitCode int    `json:"exitCode"`
	Manager  string `json:"manager,omitempty"`
	Output   string `json:"output"`
}

// parseManager extracts "PKGMGR=<name>" from the prelude's first echoed line
// if `echo "PKGMGR=$PKGMGR"` was appended to body.  Most tools don't need
// this, but install/remove do for reporting.
func parseManager(out string) (manager, rest string) {
	if !strings.HasPrefix(out, "PKGMGR=") {
		return "", out
	}
	first, rest, ok := strings.Cut(out, "\n")
	if !ok {
		return strings.TrimPrefix(out, "PKGMGR="), ""
	}
	return strings.TrimPrefix(first, "PKGMGR="), rest
}

// ─── pkg_search ──────────────────────────────────────────────────────────────

type searchInput struct {
	Query  string `json:"query" jsonschema:"package search query"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

func searchHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[searchInput, pkgResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, pkgResult, error) {
		if in.Query == "" {
			return nil, pkgResult{}, fmt.Errorf("pkg_search: \"query\" is required")
		}
		if !validPkgName(in.Query) {
			return nil, pkgResult{}, fmt.Errorf("pkg_search: invalid query %q (alphanumeric + -._+: only)", in.Query)
		}
		body := `echo "PKGMGR=$PKGMGR"; $PKG_SEARCH ` + sshexec.ShellQuote(in.Query)
		res, err := runWithManager(ctx, p, in.Target, body)
		if err != nil {
			return nil, pkgResult{Target: in.Target}, fmt.Errorf("pkg_search: %w", err)
		}
		mgr, output := parseManager(res.Stdout)
		return nil, pkgResult{Target: res.Target, ExitCode: res.ExitCode, Manager: mgr, Output: output}, nil
	}
}

// ─── pkg_info ────────────────────────────────────────────────────────────────

type infoInput struct {
	Name   string `json:"name" jsonschema:"package name"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

func infoHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[infoInput, pkgResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in infoInput) (*mcp.CallToolResult, pkgResult, error) {
		if !validPkgName(in.Name) {
			return nil, pkgResult{}, fmt.Errorf("pkg_info: invalid name %q", in.Name)
		}
		body := `echo "PKGMGR=$PKGMGR"; $PKG_INFO ` + sshexec.ShellQuote(in.Name)
		res, err := runWithManager(ctx, p, in.Target, body)
		if err != nil {
			return nil, pkgResult{Target: in.Target}, fmt.Errorf("pkg_info: %w", err)
		}
		mgr, output := parseManager(res.Stdout)
		return nil, pkgResult{Target: res.Target, ExitCode: res.ExitCode, Manager: mgr, Output: output}, nil
	}
}

// ─── pkg_list_installed ──────────────────────────────────────────────────────

type listInstalledInput struct {
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

func listInstalledHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[listInstalledInput, pkgResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listInstalledInput) (*mcp.CallToolResult, pkgResult, error) {
		body := `echo "PKGMGR=$PKGMGR"; sh -c "$PKG_LIST"`
		res, err := runWithManager(ctx, p, in.Target, body)
		if err != nil {
			return nil, pkgResult{Target: in.Target}, fmt.Errorf("pkg_list_installed: %w", err)
		}
		mgr, output := parseManager(res.Stdout)
		return nil, pkgResult{Target: res.Target, ExitCode: res.ExitCode, Manager: mgr, Output: output}, nil
	}
}

// ─── pkg_install ─────────────────────────────────────────────────────────────

type installInput struct {
	Name   string `json:"name" jsonschema:"package name to install"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

func installHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[installInput, pkgResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in installInput) (*mcp.CallToolResult, pkgResult, error) {
		if !validPkgName(in.Name) {
			return nil, pkgResult{}, fmt.Errorf("pkg_install: invalid name %q", in.Name)
		}
		body := `echo "PKGMGR=$PKGMGR"; sh -c "$PKG_INSTALL ` + sshexec.ShellQuote(in.Name) + `"`
		res, err := runWithManager(ctx, p, in.Target, body)
		if err != nil {
			return nil, pkgResult{Target: in.Target}, fmt.Errorf("pkg_install: %w", err)
		}
		mgr, output := parseManager(res.Stdout)
		if output == "" {
			output = res.Stderr
		}
		return nil, pkgResult{Target: res.Target, ExitCode: res.ExitCode, Manager: mgr, Output: output}, nil
	}
}

// ─── pkg_remove ──────────────────────────────────────────────────────────────

type removeInput struct {
	Name   string `json:"name" jsonschema:"package name to remove"`
	Target string `json:"target,omitempty" jsonschema:"edge name (defaults to first connected edge)"`
}

func removeHandler(p *linuxmcp.Provider) mcp.ToolHandlerFor[removeInput, pkgResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in removeInput) (*mcp.CallToolResult, pkgResult, error) {
		if !validPkgName(in.Name) {
			return nil, pkgResult{}, fmt.Errorf("pkg_remove: invalid name %q", in.Name)
		}
		body := `echo "PKGMGR=$PKGMGR"; sh -c "$PKG_REMOVE ` + sshexec.ShellQuote(in.Name) + `"`
		res, err := runWithManager(ctx, p, in.Target, body)
		if err != nil {
			return nil, pkgResult{Target: in.Target}, fmt.Errorf("pkg_remove: %w", err)
		}
		mgr, output := parseManager(res.Stdout)
		if output == "" {
			output = res.Stderr
		}
		return nil, pkgResult{Target: res.Target, ExitCode: res.ExitCode, Manager: mgr, Output: output}, nil
	}
}
