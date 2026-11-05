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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

const (
	testMCPToken = "mcp-attempt-bearer-value"
	testMCPURL   = "https://hub.example/services/providers/agents/config/mcp"
)

func withRemoteServer(l *harness.Launch) {
	l.Credential.MCPServers = []harness.MCPServer{{Name: "agent-config", URL: testMCPURL, Token: testMCPToken}}
}

// copyMCPConfig is the fake child's half of the mcp-config-copy scenario: it
// copies the --mcp-config document beside the argv recording and notes the
// file's mode.
func copyMCPConfig(argvFile string) {
	argv, err := os.ReadFile(argvFile) //nolint:gosec // test-controlled path
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(argv), "\n"), "\n")
	for i, line := range lines {
		if line != "--mcp-config" || i+1 >= len(lines) {
			continue
		}
		dir := filepath.Dir(argvFile)
		_ = os.WriteFile(filepath.Join(dir, "mcp-config-path"), []byte(lines[i+1]), 0o600)
		if info, err := os.Stat(lines[i+1]); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "mcp-config-mode"), []byte(fmt.Sprintf("%o", info.Mode().Perm())), 0o600)
		}
		if body, err := os.ReadFile(lines[i+1]); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "mcp-config"), body, 0o600)
		}
		return
	}
}

type mcpConfigEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func readMCPConfigCopy(t *testing.T, dir string) map[string]mcpConfigEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "mcp-config")) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("the child was given no readable --mcp-config: %v", err)
	}
	var config struct {
		MCPServers map[string]mcpConfigEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("decoding the --mcp-config document: %v", err)
	}
	if mode, _ := os.ReadFile(filepath.Join(dir, "mcp-config-mode")); string(mode) != "600" { //nolint:gosec // test-controlled path
		t.Errorf("--mcp-config mode = %s, want 600", mode)
	}
	path, _ := os.ReadFile(filepath.Join(dir, "mcp-config-path")) //nolint:gosec // test-controlled path
	if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
		t.Errorf("the --mcp-config document %s outlived the launch", path)
	}
	return config.MCPServers
}

// TestRemoteMCPServerWithoutPermissions: a launch with nobody to ask but a
// remote server to reach drops --safe-mode, which would disable that server,
// gives the child a one-entry document, and pre-approves the server's tools
// so the call is not denied for want of somebody to ask. The bearer is in the
// document and nowhere on the command line.
func TestRemoteMCPServerWithoutPermissions(t *testing.T) {
	binary, dir := fakeClaude(t, "mcp-config-copy")
	adapter := testAdapter(t, binary)
	result, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir() }, withRemoteServer), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "completed" {
		t.Fatalf("result = %+v, want completed", result)
	}

	argv := readLines(t, filepath.Join(dir, "argv"))
	for _, want := range []string{"--permission-prompts", "none", "--mcp-config", "--strict-mcp-config", "mcp__agent-config"} {
		if !contains(argv, want) {
			t.Errorf("argv %v lacks %q", argv, want)
		}
	}
	if i := indexOf(argv, "mcp__agent-config"); i < 1 || argv[i-1] != "--allowedTools" {
		t.Errorf("the remote server's tools are not pre-approved with --allowedTools: %v", argv)
	}
	for _, never := range []string{"--safe-mode", "--permission-prompt-tool"} {
		if contains(argv, never) {
			t.Errorf("argv must not contain %q with only a remote server: %v", never, argv)
		}
	}
	for _, arg := range argv {
		if strings.Contains(arg, testMCPToken) || strings.Contains(arg, "mcpServers") {
			t.Errorf("the remote server's bearer reached argv: %q", arg)
		}
	}

	servers := readMCPConfigCopy(t, dir)
	want := mcpConfigEntry{Type: "http", URL: testMCPURL, Headers: map[string]string{"Authorization": "Bearer " + testMCPToken}}
	if len(servers) != 1 || fmt.Sprint(servers["agent-config"]) != fmt.Sprint(want) {
		t.Fatalf("--mcp-config servers = %+v, want only %+v", servers, want)
	}
}

// TestRemoteMCPServerBesidePermissions: with somebody to ask, the permission
// server and the remote server share the one document, and the permission
// prompt tool is still the railgrid entry's.
func TestRemoteMCPServerBesidePermissions(t *testing.T) {
	binary, dir := fakeClaude(t, "mcp-config-copy")
	adapter := testAdapter(t, binary)
	asker := newRecordingAsker(harness.PermissionVerdict{Allow: true})
	result, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir(); l.Permissions = asker }, withRemoteServer), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "completed" {
		t.Fatalf("result = %+v, want completed", result)
	}

	argv := readLines(t, filepath.Join(dir, "argv"))
	if strings.Count(strings.Join(argv, "\n"), "--mcp-config") != 1 {
		t.Errorf("argv passes more than one --mcp-config: %v", argv)
	}
	for _, want := range []string{"--permission-prompts", "host", "--permission-prompt-tool", permissionToolRef, "--strict-mcp-config", "mcp__agent-config"} {
		if !contains(argv, want) {
			t.Errorf("argv %v lacks %q", argv, want)
		}
	}
	if contains(argv, "--safe-mode") {
		t.Errorf("argv passes --safe-mode, which disables both servers: %v", argv)
	}

	servers := readMCPConfigCopy(t, dir)
	if len(servers) != 2 {
		t.Fatalf("--mcp-config servers = %+v, want the permission server and the remote one", servers)
	}
	if permission := servers[permissionServerName]; permission.Type != "http" || !strings.HasPrefix(permission.URL, "http://127.0.0.1:") || permission.Headers[permissionSecretHeader] == "" {
		t.Errorf("permission entry = %+v, want the loopback server with its secret", permission)
	}
	if remote := servers["agent-config"]; remote.URL != testMCPURL || remote.Headers["Authorization"] != "Bearer "+testMCPToken {
		t.Errorf("remote entry = %+v", remote)
	}
}

// TestRemoteMCPServerTokenIsRedacted: the child can print a bearer back, and
// it must not survive into an event, a blocker or an error, whether it comes
// back bare or as the header value it was sent as.
func TestRemoteMCPServerTokenIsRedacted(t *testing.T) {
	binary, _ := fakeClaude(t, "leak-secret")
	t.Setenv(fakeSecretEnv, "Bearer "+testMCPToken)
	adapter := testAdapter(t, binary)

	var events []harness.Event
	result, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir() }, withRemoteServer), collect(&events))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(result.Blocker, testMCPToken) {
		t.Fatalf("the blocker leaked the bearer: %q", result.Blocker)
	}
	for _, e := range events {
		if strings.Contains(e.Message, testMCPToken) || strings.Contains(string(e.Data), testMCPToken) {
			t.Errorf("event %q leaked the bearer", e.Type)
		}
	}
	cred, err := credentialFor(launch(withRemoteServer))
	if err != nil {
		t.Fatalf("credentialFor: %v", err)
	}
	if got := cred.redactError(fmt.Errorf("dial with %s failed", testMCPToken)).Error(); strings.Contains(got, testMCPToken) {
		t.Errorf("redactError kept the bearer: %q", got)
	}
}

// TestWriteMCPConfigRefusesAKeyThatWouldReplaceAnother: the map would quietly
// let a later entry win, and the entry that matters most to lose is the
// permission server's.
func TestWriteMCPConfigRefusesAKeyThatWouldReplaceAnother(t *testing.T) {
	home := t.TempDir()
	for _, servers := range [][]harness.MCPServer{
		{{Name: permissionServerName, URL: testMCPURL, Token: "t"}},
		{{Name: "a", URL: testMCPURL, Token: "t"}, {Name: "a", URL: testMCPURL, Token: "u"}},
	} {
		if path, err := writeMCPConfig(home, nil, servers); err == nil {
			_ = os.Remove(path)
			t.Errorf("writeMCPConfig accepted %+v", servers)
		}
	}
	if path, err := writeMCPConfig(home, nil, nil); err != nil || path != "" {
		t.Errorf("writeMCPConfig with nothing to write = %q, %v; want no document", path, err)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Errorf("a refused document was left in the home: %v", entries)
	}
}
