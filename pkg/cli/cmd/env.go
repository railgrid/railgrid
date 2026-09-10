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

package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// envValues is what `faros env` resolves. JSON keys are for --json; the shell
// variable names match skills/faros/scripts/faros-env.sh.
type envValues struct {
	Hub          string `json:"hub"`
	Cluster      string `json:"cluster"`
	Org          string `json:"org"`
	Workspace    string `json:"workspace"`
	Token        string `json:"token"`
	AppStudioURL string `json:"appStudioURL"`
	MCPURL       string `json:"mcpURL,omitempty"`
	MCPToken     string `json:"mcpToken,omitempty"`
}

func newEnvCommand() *cobra.Command {
	var target hubTarget
	var asJSON, noMCP bool

	cmd := &cobra.Command{
		Use:   "env",
		Short: "Print shell exports for calling the hub as you",
		Long: `Resolve every identifier a shell session or AI agent needs to call the hub
REST and MCP APIs as you, and print them as shell exports:

  HUB        hub base URL
  CLUSTER    kcp cluster of the active workspace
  ORG, WS    org and workspace UUIDs (the X-Faros-Org / X-Faros-Workspace headers)
  TOKEN      your bearer token (OIDC tokens expire; re-run to refresh)
  AS         App Studio REST base ($HUB/services/providers/app-studio)
  MCP_URL    the workspace's aggregate MCP endpoint
  MCP_TOKEN  a long-lived token for MCP_URL

Load them with:

  eval "$(faros env)"
  curl -s -H "Authorization: Bearer $TOKEN" -H "X-Faros-Org: $ORG" \
    -H "X-Faros-Workspace: $WS" "$AS/api/projects"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnv(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), target, asJSON, noMCP)
		},
	}
	target.addFlags(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print a JSON object instead of shell exports")
	cmd.Flags().BoolVar(&noMCP, "no-mcp", false, "Skip the MCP connect call (no MCP_URL / MCP_TOKEN)")
	return cmd
}

func runEnv(ctx context.Context, out, errOut io.Writer, target hubTarget, asJSON, noMCP bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := newHubSession(ctx, target)
	if err != nil {
		return err
	}
	token, err := s.bearerToken(ctx)
	if err != nil {
		return err
	}
	v := envValues{
		Hub:          s.Hub,
		Cluster:      s.Cluster,
		Org:          s.Org.UUID,
		Workspace:    s.WS.UUID,
		Token:        token,
		AppStudioURL: s.appStudioURL(),
	}
	mcpState := "skipped"
	if !noMCP {
		info, err := s.mcpConnect(ctx, defaultMCPServerName)
		switch {
		case err != nil:
			mcpState = "unavailable"
			_, _ = fmt.Fprintf(errOut, "faros env: warning: %v\n", err)
		case info.Token == "":
			mcpState = "token not ready"
			v.MCPURL = info.EndpointURL
		default:
			mcpState = "ready"
			v.MCPURL, v.MCPToken = info.EndpointURL, info.Token
		}
	}
	if asJSON {
		return printJSON(out, v)
	}
	if _, err := io.WriteString(out, renderEnvExports(v, !noMCP)); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(errOut, "faros env: hub=%s org=%s (%s) ws=%s (%s) mcp=%s\n",
		v.Hub, s.Org.DisplayName, v.Org, displayLabel(s.WS.DisplayName, v.Workspace), v.Workspace, mcpState)
	return nil
}

// renderEnvExports renders one shell-quoted export per variable, in the
// order faros-env.sh defines them. MCP variables are emitted only when
// requested and resolved.
func renderEnvExports(v envValues, includeMCP bool) string {
	vars := [][2]string{
		{"HUB", v.Hub},
		{"CLUSTER", v.Cluster},
		{"ORG", v.Org},
		{"WS", v.Workspace},
		{"TOKEN", v.Token},
		{"AS", v.AppStudioURL},
	}
	if includeMCP && v.MCPURL != "" {
		vars = append(vars, [2]string{"MCP_URL", v.MCPURL})
		if v.MCPToken != "" {
			vars = append(vars, [2]string{"MCP_TOKEN", v.MCPToken})
		}
	}
	var b strings.Builder
	for _, kv := range vars {
		fmt.Fprintf(&b, "export %s=%s\n", kv[0], shellSingleQuote(kv[1]))
	}
	return b.String()
}
