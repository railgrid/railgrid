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

package runner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// Remote MCP servers ride inside the harness credential, so they are dispatch
// data on exactly the same terms: the launch sees them, nothing the runner
// writes down holds the bearer or the URL that carries it, a retry with a
// freshly minted bearer is the same request, and a resume refreshes what the
// relaunched harness is handed.
func TestCredentialMCPServersReachTheLaunchAndNothingDurable(t *testing.T) {
	source, commit := testGitSource(t)
	seen := make(chan []harness.MCPServer, 2)
	var runs atomic.Int32
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		seen <- append([]harness.MCPServer(nil), launch.Credential.MCPServers...)
		if runs.Add(1) == 1 {
			return harness.Result{Phase: string(PhaseNeedsInput), SessionID: "session-mcp", Clarification: &harness.Clarification{ID: "clarification-mcp", Text: "Which change?"}}, nil
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: "session-mcp"}, nil
	}}
	stateDir := t.TempDir()
	runner := newTestRunnerAt(t, adapter, stateDir, source, commit)

	const (
		firstToken  = "mcp-bearer-for-the-first-launch"
		secondToken = "mcp-bearer-for-the-resume"
		serverURL   = "https://hub.example/services/agents/config/mcp-url-marker"
	)
	request := testStartRequest(commit)
	request.HarnessCredential.MCPServers = []MCPServer{{Name: "agent-config", URL: serverURL, Token: firstToken}}
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseNeedsInput)
	got := <-seen
	if len(got) != 1 || !reflect.DeepEqual(got[0], harness.MCPServer{Name: "agent-config", URL: serverURL, Token: firstToken}) {
		t.Fatalf("launch servers = %+v, want the brought server", got)
	}

	// A retry carrying a freshly minted bearer is the same request.
	request.HarnessCredential.MCPServers[0].Token = "mcp-bearer-minted-again"
	if _, err := runner.Start(context.Background(), request); err != nil {
		t.Fatalf("a retry with a different bearer was refused: %v", err)
	}

	checkpoint, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil || checkpoint.Clarification == nil {
		t.Fatalf("checkpoint = %+v, %v", checkpoint, err)
	}
	resumeCredential := testHarnessCredential()
	resumeCredential.MCPServers = []MCPServer{{Name: "agent-config", URL: serverURL, Token: secondToken}}
	if _, err := runner.Resume(context.Background(), ResumeRequest{
		HarnessCredential: resumeCredential,
		ProtocolVersion:   ProtocolVersion,
		RequestID:         "resume-mcp",
		TaskID:            checkpoint.TaskID,
		AttemptID:         checkpoint.AttemptID,
		AttemptEpoch:      checkpoint.AttemptEpoch,
		SessionID:         checkpoint.SessionID,
		ClarificationID:   checkpoint.Clarification.ID,
		Resolution:        "the answer",
	}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
	got = <-seen
	if len(got) != 1 || got[0].Token != secondToken {
		t.Fatalf("resumed launch servers = %+v, want the bearer the resume brought", got)
	}

	// Nothing durable: not the journal, not the state, not a worktree.
	if err := filepath.Walk(stateDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range []string{firstToken, secondToken, "mcp-bearer-minted-again", "mcp-url-marker"} {
			if strings.Contains(string(body), secret) {
				t.Errorf("%s holds %q", path, secret)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialMCPServersAdvertised(t *testing.T) {
	source, commit := testGitSource(t)
	runner := newTestRunner(t, &fakeAdapter{}, source, commit)
	capabilities := runner.Capabilities()
	for _, capability := range capabilities.Verification {
		if capability == RemoteMCPCapability {
			return
		}
	}
	t.Fatalf("verification capabilities = %v, want %s", capabilities.Verification, RemoteMCPCapability)
}

func TestCredentialMCPServersValidation(t *testing.T) {
	valid := func(name string) MCPServer {
		return MCPServer{Name: name, URL: "https://hub.example/mcp", Token: "tok"}
	}
	withURL := func(raw string) []MCPServer {
		server := valid("agent")
		server.URL = raw
		return []MCPServer{server}
	}
	withToken := func(token string) []MCPServer {
		server := valid("agent")
		server.Token = token
		return []MCPServer{server}
	}
	for _, test := range []struct {
		name    string
		servers []MCPServer
		wantErr string
	}{
		{"none", nil, ""},
		{"one", []MCPServer{valid("agent-config")}, ""},
		{"four", []MCPServer{valid("a"), valid("b"), valid("c"), valid("d")}, ""},
		{"too many", []MCPServer{valid("a"), valid("b"), valid("c"), valid("d"), valid("e")}, "more than"},
		{"upper-case name", []MCPServer{valid("Agent")}, "not a lower-case server name"},
		{"leading digit", []MCPServer{valid("1agent")}, "not a lower-case server name"},
		{"underscore", []MCPServer{valid("agent_config")}, "not a lower-case server name"},
		{"long name", []MCPServer{valid("a" + strings.Repeat("b", 32))}, "not a lower-case server name"},
		{"empty name", []MCPServer{valid("")}, "not a lower-case server name"},
		// The permission server's key: a caller's server under it would
		// replace the permission gate in the one config a launch writes.
		{"reserved", []MCPServer{valid("railgrid")}, "reserved"},
		{"duplicate", []MCPServer{valid("agent"), valid("agent")}, "twice"},
		{"loopback http", withURL("http://127.0.0.1:8080/mcp"), ""},
		{"localhost http", withURL("http://localhost:8080/mcp"), ""},
		{"ipv6 loopback http", withURL("http://[::1]:8080/mcp"), ""},
		{"remote http", withURL("http://hub.example/mcp"), "must be https"},
		{"other scheme", withURL("ftp://hub.example/mcp"), "not https"},
		{"no host", withURL("https:///mcp"), "no host"},
		{"relative", withURL("/mcp"), "no host"},
		{"userinfo", withURL("https://user:pass@hub.example/mcp"), "userinfo"},
		{"fragment", withURL("https://hub.example/mcp#frag"), "fragment"},
		{"unparsable", withURL("https://hub.example/%zz"), "does not parse"},
		{"empty token", withToken(""), "is empty"},
		{"space in token", withToken("a b"), "invalid whitespace"},
		{"newline in token", withToken("a\nb"), "invalid whitespace"},
		{"nul in token", withToken("a\x00b"), "invalid whitespace"},
		{"oversized token", withToken(strings.Repeat("x", maxMCPServerTokenBytes+1)), "oversized"},
	} {
		t.Run(test.name, func(t *testing.T) {
			credential, err := harnessCredentialOf(&HarnessCredential{Kind: string(harness.CredentialClaudeOAuth), Value: "tok", MCPServers: test.servers})
			switch {
			case test.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)):
				t.Fatalf("error = %v, want one containing %q", err, test.wantErr)
			case test.wantErr == "" && len(credential.MCPServers) != len(test.servers):
				t.Fatalf("servers = %+v, want %d", credential.MCPServers, len(test.servers))
			}
		})
	}
}

func TestApprovedMCPToolAllowlistIsValidatedAndCopied(t *testing.T) {
	input := []MCPServer{{Name: "agent", URL: "https://agents.example/mcp", Token: "opaque", ApprovedTools: []string{"code_pull_request"}}}
	out, err := credentialMCPServersOf(input)
	if err != nil {
		t.Fatal(err)
	}
	input[0].ApprovedTools[0] = "different"
	if out[0].ApprovedTools[0] != "code_pull_request" {
		t.Fatal("caller mutated approved authority")
	}
	for _, tools := range [][]string{{""}, {"x", "x"}, {"bad\nname"}} {
		input[0].ApprovedTools = tools
		if _, err := credentialMCPServersOf(input); err == nil {
			t.Fatal("invalid tool allowlist accepted")
		}
	}
}
