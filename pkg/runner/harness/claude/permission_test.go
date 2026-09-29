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
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// recordingAsker stands in for the runner. It records what it was asked and
// answers with a fixed verdict.
type recordingAsker struct {
	verdict  harness.PermissionVerdict
	asked    atomic.Int32
	requests chan harness.PermissionRequest
}

func newRecordingAsker(verdict harness.PermissionVerdict) *recordingAsker {
	return &recordingAsker{verdict: verdict, requests: make(chan harness.PermissionRequest, 8)}
}

func (a *recordingAsker) AskPermission(_ context.Context, request harness.PermissionRequest) (harness.PermissionVerdict, error) {
	a.asked.Add(1)
	a.requests <- request
	return a.verdict, nil
}

// askPermissionAsClaudeCodeWould is the fake child's half of the round trip. It
// reads the --mcp-config file the adapter wrote and speaks the wire shape that
// was captured from the real 2.1.281 binary: initialize, tools/list, then a
// tools/call whose arguments are a flat tool_name / input / tool_use_id object.
// It returns the permission result text, or a description of what went wrong.
func askPermissionAsClaudeCodeWould(argvFile string) string {
	argv, err := os.ReadFile(argvFile) //nolint:gosec // test-controlled path
	if err != nil {
		return "no argv: " + err.Error()
	}
	lines := strings.Split(strings.TrimRight(string(argv), "\n"), "\n")
	configPath, toolRef := "", ""
	for i, line := range lines {
		switch line {
		case "--mcp-config":
			if i+1 < len(lines) {
				configPath = lines[i+1]
			}
		case "--permission-prompt-tool":
			if i+1 < len(lines) {
				toolRef = lines[i+1]
			}
		}
	}
	if configPath == "" || toolRef == "" {
		return "no permission prompt configured"
	}
	raw, err := os.ReadFile(configPath) //nolint:gosec // path came from our own argv
	if err != nil {
		return "no config: " + err.Error()
	}
	var config struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return "bad config: " + err.Error()
	}
	// The tool reference must be mcp__<server>__<tool>; the binary refuses
	// anything else.
	parts := strings.SplitN(strings.TrimPrefix(toolRef, "mcp__"), "__", 2)
	if len(parts) != 2 {
		return "bad tool reference " + toolRef
	}
	server, present := config.MCPServers[parts[0]]
	if !present {
		return "config has no server " + parts[0]
	}
	if len(config.MCPServers) != 1 {
		return fmt.Sprintf("config exposes %d servers, want exactly one", len(config.MCPServers))
	}

	post := func(body string) (string, int) {
		request, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(body))
		if err != nil {
			return err.Error(), 0
		}
		request.Header.Set("Content-Type", "application/json")
		for key, value := range server.Headers {
			request.Header.Set(key, value)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return err.Error(), 0
		}
		defer func() { _ = response.Body.Close() }()
		answer, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return string(answer), response.StatusCode
	}

	if _, status := post(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.1.281"}}}`); status != http.StatusOK {
		return fmt.Sprintf("initialize status %d", status)
	}
	listed, status := post(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if status != http.StatusOK || !strings.Contains(listed, parts[1]) {
		return "tools/list did not offer " + parts[1]
	}
	called, status := post(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":{"tool_name":"Bash","input":{"command":"rm -rf /tmp/x","description":"remove it"},"tool_use_id":"toolu_01FAKE"},"_meta":{"claudecode/toolUseId":"toolu_01FAKE"}}}`,
		parts[1]))
	if status != http.StatusOK {
		return fmt.Sprintf("tools/call status %d", status)
	}
	var answer struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(called), &answer); err != nil {
		return "bad tools/call answer: " + err.Error()
	}
	// Claude Code validates this exactly: ONE block, type "text", string text.
	if len(answer.Result.Content) != 1 || answer.Result.Content[0].Type != "text" {
		return "tools/call answer was not a single text block: " + called
	}
	return answer.Result.Content[0].Text
}

// TestPermissionPromptRoundTripSpeaksTheConfirmedContract: the adapter wires
// the child to a one-tool loopback MCP server, the child's call reaches the
// asker with the tool and its input, and the verdict comes back in the shape
// Claude Code validates.
func TestPermissionPromptRoundTripSpeaksTheConfirmedContract(t *testing.T) {
	binary, dir := fakeClaude(t, "permission-ask")
	adapter := testAdapter(t, binary)
	asker := newRecordingAsker(harness.PermissionVerdict{Allow: true})

	// The fake child puts the permission result it was given into the stream's
	// terminal record, so the "result" event is what the harness was told.
	decision := ""
	emit := func(event harness.Event) error {
		if event.Type == "result" {
			decision = strings.TrimSpace(event.Message)
		}
		return nil
	}
	result, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir(); l.Permissions = asker }), emit)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "completed" {
		t.Fatalf("result = %+v, want completed", result)
	}

	select {
	case request := <-asker.requests:
		if request.Tool != "Bash" {
			t.Errorf("asked about tool %q, want Bash", request.Tool)
		}
		if !strings.Contains(request.Input, "rm -rf /tmp/x") {
			t.Errorf("the call's input did not reach the human: %q", request.Input)
		}
		if request.ID == "" {
			t.Error("the request carries no id, so a resume could not be fenced")
		}
	default:
		t.Fatal("the permission prompt never reached the asker")
	}

	// What the child was told, verbatim. An approval carries no updatedInput:
	// the call runs as it was shown.
	if strings.TrimSpace(result.Blocker) != "" {
		t.Fatalf("blocker = %q, want none", result.Blocker)
	}
	if decision != `{"behavior":"allow"}` {
		t.Fatalf("permission result = %s, want a bare allow", decision)
	}

	argv := readLines(t, filepath.Join(dir, "argv"))
	for _, want := range []string{"--permission-prompts", "host", "--permission-prompt-tool", permissionToolRef, "--mcp-config", "--strict-mcp-config"} {
		if !contains(argv, want) {
			t.Errorf("argv %v lacks %q", argv, want)
		}
	}
	// --safe-mode and a permission prompt tool cannot coexist: measured against
	// 2.1.281, --safe-mode disables --mcp-config servers and the launch fails
	// with "Available MCP tools: none".
	if contains(argv, "--safe-mode") {
		t.Errorf("argv passes --safe-mode alongside a permission prompt tool, which disables it: %v", argv)
	}
	if contains(argv, "none") {
		t.Errorf("argv still denies prompts instead of asking: %v", argv)
	}
	// The secret must not be on the command line, where every process can read it.
	for _, arg := range argv {
		if strings.Contains(arg, "mcpServers") {
			t.Errorf("the permission configuration was passed inline on argv: %q", arg)
		}
	}
}

// TestBypassPermissionsNeverAsks: a machine whose owner chose bypassPermissions
// has already decided. Nothing prompts there, so no server is hosted and no
// human is ever troubled.
func TestBypassPermissionsNeverAsks(t *testing.T) {
	binary, dir := fakeClaude(t, "permission-ask")
	adapter := testAdapter(t, binary, func(c *Config) { c.PermissionMode = PermissionBypass })
	asker := newRecordingAsker(harness.PermissionVerdict{Allow: true})

	if _, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir(); l.Permissions = asker }), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if asked := asker.asked.Load(); asked != 0 {
		t.Fatalf("asked %d times under bypassPermissions, want 0", asked)
	}
	argv := readLines(t, filepath.Join(dir, "argv"))
	if contains(argv, "--mcp-config") || contains(argv, "--permission-prompt-tool") {
		t.Errorf("bypassPermissions hosted a permission prompt server: %v", argv)
	}
	for _, want := range []string{"--permission-prompts", "none", "--safe-mode", "bypassPermissions"} {
		if !contains(argv, want) {
			t.Errorf("argv %v lacks %q", argv, want)
		}
	}
}

// TestPermissionServerIsReachableOnlyWithItsSecret: the server is on loopback,
// but loopback is shared with everything else on the host.
func TestPermissionServerIsReachableOnlyWithItsSecret(t *testing.T) {
	home := t.TempDir()
	asker := newRecordingAsker(harness.PermissionVerdict{Allow: true})
	server, err := startPermissionServer(asker, "attempt-1", home)
	if err != nil {
		t.Fatalf("startPermissionServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	unauthenticated, err := http.Post(server.url, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = unauthenticated.Body.Close() }()
	if unauthenticated.StatusCode != http.StatusNotFound {
		t.Fatalf("status without the secret = %d, want 404", unauthenticated.StatusCode)
	}
	if asker.asked.Load() != 0 {
		t.Fatal("an unauthenticated caller reached the human")
	}

	// The configuration the child reads is not world-readable either.
	info, err := os.Stat(server.ConfigPath())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("configuration mode = %o, want 600", mode)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(server.ConfigPath()); !os.IsNotExist(err) {
		t.Fatal("the permission configuration outlived the launch")
	}
}

// TestPermissionServerExposesNothingElse: one tool, and no other method does
// anything useful.
func TestPermissionServerExposesNothingElse(t *testing.T) {
	home := t.TempDir()
	asker := newRecordingAsker(harness.PermissionVerdict{Allow: true})
	server, err := startPermissionServer(asker, "attempt-1", home)
	if err != nil {
		t.Fatalf("startPermissionServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	call := func(body string) map[string]any {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, server.url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set(permissionSecretHeader, server.secret)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var decoded map[string]any
		if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
			t.Fatalf("decoding %s: %v", body, err)
		}
		return decoded
	}

	listed := call(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools, _ := listed["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("server offers %d tools, want exactly one", len(tools))
	}

	if answer := call(`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`); answer["error"] == nil {
		t.Errorf("resources/list was answered: %v", answer)
	}
	if answer := call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"something_else","arguments":{}}}`); answer["error"] == nil {
		t.Errorf("an unknown tool was called: %v", answer)
	}
	if asker.asked.Load() != 0 {
		t.Fatal("a call that was not the permission tool reached the human")
	}
}

// TestPermissionResultRendersADenialTheHarnessCanActedOn: a denial is an
// answer with words in it, not an error and not an empty refusal.
func TestPermissionResultRendersADenialTheHarnessCanActedOn(t *testing.T) {
	denied := permissionResult(harness.PermissionVerdict{Allow: false, Message: "not on production"})
	content, _ := denied["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v, want one block", content)
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("block type = %v, want text", block["type"])
	}
	var decision map[string]any
	if err := json.Unmarshal([]byte(block["text"].(string)), &decision); err != nil {
		t.Fatalf("the decision was not JSON: %v", err)
	}
	if decision["behavior"] != "deny" || decision["message"] != "not on production" {
		t.Fatalf("decision = %v, want a deny carrying the reason", decision)
	}

	// A denial with nothing said still says something.
	silent := permissionResult(harness.PermissionVerdict{Allow: false})
	silentBlock := silent["content"].([]any)[0].(map[string]any)
	if err := json.Unmarshal([]byte(silentBlock["text"].(string)), &decision); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(decision["message"].(string)) == "" {
		t.Fatal("a silent denial reached the harness with no reason")
	}

	// An approval never rewrites the call it approved.
	allowed := permissionResult(harness.PermissionVerdict{Allow: true})
	allowedBlock := allowed["content"].([]any)[0].(map[string]any)
	if err := json.Unmarshal([]byte(allowedBlock["text"].(string)), &decision); err != nil {
		t.Fatal(err)
	}
	if decision["behavior"] != "allow" {
		t.Fatalf("decision = %v, want allow", decision)
	}
	if _, rewritten := decision["updatedInput"]; rewritten {
		t.Fatal("the approval rewrote the call's arguments")
	}
}

// TestBoundedPermissionInputStaysSmall: a tool input routinely holds a whole
// file, and this one travels to a portal and into durable state.
func TestBoundedPermissionInputStaysSmall(t *testing.T) {
	huge := json.RawMessage(`{"content":"` + strings.Repeat("a", harness.MaxPermissionInputBytes*2) + `"}`)
	got := boundedPermissionInput(huge)
	if len(got) > harness.MaxPermissionInputBytes+len("…[truncated]") {
		t.Fatalf("bounded input is %d bytes, want it capped", len(got))
	}
	if !strings.HasSuffix(got, "…[truncated]") {
		t.Fatal("a truncated input does not say so")
	}
	if boundedPermissionInput(nil) != "" || boundedPermissionInput(json.RawMessage("null")) != "" {
		t.Fatal("an absent input rendered as something")
	}
}

// TestPermissionRequestIDIsStableForOneCall: a replayed resume has to be
// recognized as answering the same request rather than a new one.
func TestPermissionRequestIDIsStableForOneCall(t *testing.T) {
	server := &permissionServer{scope: "attempt-1"}
	first := server.requestID("toolu_01", "Bash", json.RawMessage(`{"command":"ls"}`))
	second := server.requestID("toolu_01", "Bash", json.RawMessage(`{"command":"ls"}`))
	if first != second {
		t.Fatalf("ids for the same call differ: %q, %q", first, second)
	}
	other := &permissionServer{scope: "attempt-2"}
	if other.requestID("toolu_01", "Bash", json.RawMessage(`{"command":"ls"}`)) == first {
		t.Fatal("two attempts minted the same request id")
	}
	// With no tool_use_id, two identical calls are still two questions. Bound to
	// variables rather than compared inline, because requestID is deliberately
	// NOT pure in this case and a linter reading it as pure flags the comparison.
	noID := &permissionServer{scope: "attempt-1"}
	firstAnon := noID.requestID("", "Bash", json.RawMessage(`{"command":"ls"}`))
	secondAnon := noID.requestID("", "Bash", json.RawMessage(`{"command":"ls"}`))
	if firstAnon == secondAnon {
		t.Fatal("two separate calls collapsed onto one request id")
	}
}
