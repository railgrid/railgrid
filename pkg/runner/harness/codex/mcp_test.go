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

package codex

import (
	"context"
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

func mcpCredential() harness.Credential {
	credential := testCredential()
	credential.MCPServers = []harness.MCPServer{{Name: "agent-config", URL: testMCPURL, Token: testMCPToken}}
	return credential
}

func readArgv(t *testing.T, binary string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "argv"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// TestRunAttachesRemoteMCPServersAsOverrides: a launch's remote server reaches
// the app-server as root-level -c overrides ahead of the subcommand, the
// bearer only in the environment variable the override names, and a probe of
// the same adapter reaches nobody's server.
func TestRunAttachesRemoteMCPServersAsOverrides(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env.json")
	binary := fakeCodexBinaryWithEnvFile(t, "success", envFile)
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.155.1"})

	result, err := adapter.Run(context.Background(), harness.Launch{AttemptID: "attempt-1", Workdir: t.TempDir(), Instructions: "approved", Credential: mcpCredential()}, nil)
	if err != nil || result.Phase != "completed" {
		t.Fatalf("run=%+v err=%v", result, err)
	}
	argv := readArgv(t, binary)
	want := []string{
		"-c", `mcp_servers.agent-config.url="` + testMCPURL + `"`,
		"-c", `mcp_servers.agent-config.bearer_token_env_var="RAILGRID_MCP_TOKEN_AGENT_CONFIG"`,
		"app-server",
	}
	if len(argv) < len(want) || strings.Join(argv[:len(want)], "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv = %v, want it to start with %v", argv, want)
	}
	for _, arg := range argv {
		if strings.Contains(arg, testMCPToken) {
			t.Fatalf("the bearer reached argv: %q", arg)
		}
	}
	if env := readJSONFile(t, envFile); env["RAILGRID_MCP_TOKEN_AGENT_CONFIG"] != testMCPToken {
		t.Fatalf("app-server environment = %v, want the bearer under the override's variable", env)
	}

	if info, err := adapter.Probe(context.Background()); err != nil || !info.Ready {
		t.Fatalf("probe=%+v err=%v", info, err)
	}
	if argv := readArgv(t, binary); argv[0] != "app-server" || strings.Contains(strings.Join(argv, "\n"), "mcp_servers") {
		t.Fatalf("probe argv = %v, want no remote server", argv)
	}
	if env := readJSONFile(t, envFile); env["RAILGRID_MCP_TOKEN_AGENT_CONFIG"] != "" {
		t.Fatalf("probe environment carries a bearer: %v", env)
	}
}

// TestRunWithoutRemoteMCPServersIsUnchanged pins the argv every launch had
// before remote servers existed.
func TestRunWithoutRemoteMCPServersIsUnchanged(t *testing.T) {
	binary := fakeCodexBinary(t, "success")
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.155.1"})
	if result, err := adapter.Run(context.Background(), harness.Launch{Workdir: t.TempDir(), Instructions: "approved", Credential: testCredential()}, nil); err != nil || result.Phase != "completed" {
		t.Fatalf("run=%+v err=%v", result, err)
	}
	want := "app-server --listen stdio:// --enable default_mode_request_user_input --disable apps --disable plugins --disable hooks"
	if got := strings.Join(readArgv(t, binary), " "); got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// TestRunRedactsRemoteMCPBearer: whatever the app-server echoes, the bearer
// does not survive into an event or the result.
func TestRunRedactsRemoteMCPBearer(t *testing.T) {
	binary := fakeCodexBinary(t, "leak-mcp-token")
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.155.1"})
	var events []harness.Event
	result, err := adapter.Run(context.Background(), harness.Launch{AttemptID: "attempt-1", Workdir: t.TempDir(), Instructions: "approved", Credential: mcpCredential()}, func(event harness.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "failed" || strings.Contains(result.Blocker, testMCPToken) {
		t.Fatalf("result = %+v", result)
	}
	sawRedaction := false
	for _, event := range events {
		if strings.Contains(event.Message, testMCPToken) || strings.Contains(string(event.Data), testMCPToken) {
			t.Errorf("event %q leaked the bearer: %q %s", event.Type, event.Message, event.Data)
		}
		if strings.Contains(event.Message, "[redacted]") {
			sawRedaction = true
		}
	}
	if !sawRedaction {
		t.Fatalf("no event shows the echoed bearer was redacted; the fake may not have seen it: %+v", events)
	}
}

func TestMCPOverridesRefuseAKeyThatChangesTheirShape(t *testing.T) {
	for _, servers := range [][]harness.MCPServer{
		{{Name: "a.b", URL: testMCPURL, Token: "t"}},
		{{Name: `a"`, URL: testMCPURL, Token: "t"}},
		{{Name: "A", URL: testMCPURL, Token: "t"}},
		{{Name: "a", URL: testMCPURL, Token: "t"}, {Name: "a", URL: testMCPURL, Token: "u"}},
		{{Name: "a", URL: testMCPURL, Token: "t\nINJECTED=1"}},
		{{Name: "a", URL: "", Token: "t"}},
	} {
		if _, _, err := mcpOverrides(servers); err == nil {
			t.Errorf("mcpOverrides accepted %+v", servers)
		}
	}
	args, _, err := mcpOverrides([]harness.MCPServer{{Name: "a", URL: `https://h.example/x"y`, Token: "t"}})
	if err != nil || args[1] != `mcp_servers.a.url="https://h.example/x\"y"` {
		t.Fatalf("a quote in the url was not escaped: %v %v", args, err)
	}
}
