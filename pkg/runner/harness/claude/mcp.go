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

package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// writeMCPConfig writes the ONE --mcp-config document a launch is given: the
// permission prompt server when there is one, and every remote server the
// identity brought. It returns "" when there is neither, which is the case
// that keeps --safe-mode.
//
// One document rather than one per server because --strict-mcp-config makes
// that document the complete list of what the child may reach, and a single
// file is what lets a test, or a reviewer, see the whole list at once. It is a
// 0600 file in the runner-owned home and never argv, because both the
// permission secret and every remote bearer are in it. The caller removes it
// when the launch ends.
func writeMCPConfig(home string, permissions *permissionServer, remote []harness.MCPServer) (string, error) {
	if permissions == nil && len(remote) == 0 {
		return "", nil
	}
	servers := make(map[string]any, len(remote)+1)
	if permissions != nil {
		servers[permissionServerName] = permissions.mcpServerEntry()
	}
	for _, server := range remote {
		// The runner has validated these already. The two checks repeated here
		// are the ones whose failure would be silent: a duplicate or reserved
		// key would quietly replace another entry in the map, and replacing the
		// permission server's entry would hand its tool to somebody else.
		if server.Name == "" || server.Name == permissionServerName {
			return "", fmt.Errorf("remote MCP server name %q is reserved", server.Name)
		}
		if _, dup := servers[server.Name]; dup {
			return "", fmt.Errorf("remote MCP server %q is named twice", server.Name)
		}
		if server.URL == "" || server.Token == "" {
			return "", fmt.Errorf("remote MCP server %q has no url or token", server.Name)
		}
		servers[server.Name] = map[string]any{
			"type":    "http",
			"url":     server.URL,
			"headers": map[string]string{"Authorization": "Bearer " + server.Token},
		}
	}
	encoded, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return "", fmt.Errorf("encoding the MCP configuration: %w", err)
	}
	// The name pattern is outside unsafeHomeEntries on purpose: a file the
	// adapter leaves behind after a crash must not make the home refuse every
	// later launch.
	file, err := os.CreateTemp(home, "launch-mcp-*.json")
	if err != nil {
		return "", fmt.Errorf("creating the MCP configuration: %w", err)
	}
	path := file.Name()
	fail := func(step string, err error) (string, error) {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("%s the MCP configuration: %w", step, err)
	}
	if err := file.Chmod(0o600); err != nil {
		return fail("protecting", err)
	}
	if _, err := file.Write(encoded); err != nil {
		return fail("writing", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", errors.Join(errors.New("closing the MCP configuration"), err)
	}
	return path, nil
}

// remoteToolPattern is the --allowedTools entry that pre-approves every tool a
// remote server offers. Claude Code reads a bare mcp__<server> as "any tool on
// that server". The servers are pre-approved because the one they exist for,
// the agent's own config endpoint, enforces its own approval gate server-side;
// a host prompt for each call would ask twice, and with nobody to ask
// (--permission-prompts none) would deny every call instead.
func remoteToolPattern(server harness.MCPServer) string {
	return "mcp__" + server.Name
}
