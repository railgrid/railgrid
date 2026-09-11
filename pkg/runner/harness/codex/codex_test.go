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

package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroshq/faros/pkg/runner/harness"
)

func TestProbeUsesVersionAndAccountReadWithoutModelCall(t *testing.T) {
	binary := fakeCodexBinary(t, "probe")
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.147.0"})

	info, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !info.Ready || info.Version != "0.147.0" {
		t.Fatalf("unexpected probe info: %+v", info)
	}
	methods := readMethods(t, filepath.Join(filepath.Dir(binary), "methods"))
	want := []string{"initialize", "initialized", "account/read"}
	if strings.Join(methods, ",") != strings.Join(want, ",") {
		t.Fatalf("probe methods = %v, want %v", methods, want)
	}
	for _, method := range methods {
		if method == "thread/start" || method == "thread/resume" || method == "turn/start" {
			t.Fatalf("probe unexpectedly called model method %q", method)
		}
	}
}

func TestProbeAccountReadAuthenticationState(t *testing.T) {
	tests := []struct {
		name       string
		scenario   string
		wantReady  bool
		wantReason bool
	}{
		{name: "chatgpt account", scenario: "auth-chatgpt", wantReady: true},
		{name: "api key account", scenario: "auth-api-key", wantReady: true},
		{name: "missing account", scenario: "auth-missing", wantReason: true},
		{name: "null account", scenario: "auth-null", wantReason: true},
		{name: "malformed account", scenario: "auth-malformed", wantReason: true},
		{name: "malformed account without auth requirement", scenario: "auth-malformed-noauth", wantReason: true},
		{name: "missing auth requirement", scenario: "auth-missing-flag", wantReason: true},
		{name: "malformed auth requirement", scenario: "auth-wrong-type-flag", wantReason: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binary := fakeCodexBinary(t, tc.scenario)
			adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.147.0"})

			info, err := adapter.Probe(context.Background())
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if info.Ready != tc.wantReady {
				t.Fatalf("ready = %v, want %v (info = %+v)", info.Ready, tc.wantReady, info)
			}
			if (len(info.Reasons) > 0) != tc.wantReason {
				t.Fatalf("reasons = %v, want reason presence %v", info.Reasons, tc.wantReason)
			}
			infoData, err := json.Marshal(info)
			if err != nil {
				t.Fatalf("marshal probe info: %v", err)
			}
			for _, secret := range []string{"user@example.com", "sk-test-secret", "access-token-secret"} {
				if strings.Contains(string(infoData), secret) {
					t.Fatalf("probe info exposed account data %q: %+v", secret, info)
				}
			}
		})
	}
}

func TestRunLifecycleStartsSessionBeforeTurnAndSanitizesEnvironment(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env.json")
	binary := fakeCodexBinaryWithEnvFile(t, "success", envFile)
	oldGH := os.Getenv("GITHUB_TOKEN")
	oldGitHub := os.Getenv("GITHUB_ACTIONS")
	t.Setenv("GITHUB_TOKEN", "secret-github-token")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/shared-gitconfig")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/shared-agent.sock")
	t.Cleanup(func() {
		_ = os.Setenv("GITHUB_TOKEN", oldGH)
		_ = os.Setenv("GITHUB_ACTIONS", oldGitHub)
	})
	adapter := New(Config{Binary: binary, Home: t.TempDir(), Model: "model-147", ExpectedVersion: "0.147.0"})
	var events []harness.Event
	result, err := adapter.Run(context.Background(), harness.Launch{
		AttemptID:    "attempt-1",
		Workdir:      t.TempDir(),
		Instructions: "say hello",
	}, func(event harness.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "completed" || result.SessionID != "thread-1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(events) < 3 || events[0].Type != "session" || events[1].Type != "message" || events[2].Type != "turn_completed" {
		t.Fatalf("unexpected event order: %+v", events)
	}
	if events[1].Message != "hello" {
		t.Fatalf("message event = %+v", events[1])
	}
	envData := readJSONFile(t, envFile)
	if envData["GITHUB_TOKEN"] != "" || envData["GITHUB_ACTIONS"] != "" {
		t.Fatalf("github credentials leaked into app-server environment: %v", envData)
	}
	if got, _ := envData["CODEX_HOME"].(string); got == "" {
		t.Fatalf("CODEX_HOME was not set: %v", envData)
	}
	if envData["HOME"] != envData["CODEX_HOME"] || envData["XDG_CONFIG_HOME"] != envData["CODEX_HOME"] {
		t.Fatalf("worker home was not isolated: %v", envData)
	}
	if envData["GIT_CONFIG_GLOBAL"] == "" || envData["SSH_AUTH_SOCK"] != "" {
		t.Fatalf("git/ssh configuration was not sanitized: %v", envData)
	}
	methods := readMethods(t, filepath.Join(filepath.Dir(binary), "methods"))
	if strings.Join(methods, ",") != "initialize,initialized,thread/start,turn/start" {
		t.Fatalf("run methods = %v", methods)
	}
	requests := readJSONLines(t, filepath.Join(filepath.Dir(binary), "requests"))
	thread := requests[2]
	if thread["method"] != "thread/start" {
		t.Fatalf("thread request = %v", thread)
	}
	threadParams := thread["params"].(map[string]any)
	if threadParams["sandbox"] != "workspace-write" || threadParams["cwd"] == nil {
		t.Fatalf("unsafe thread params = %v", threadParams)
	}
	turn := requests[3]
	turnParams := turn["params"].(map[string]any)
	policy := turnParams["sandboxPolicy"].(map[string]any)
	if policy["type"] != "workspaceWrite" || policy["networkAccess"] != false {
		t.Fatalf("unsafe turn policy = %v", policy)
	}
}

func TestRunResumeMissingThreadNeedsInputWithoutStartingNewThread(t *testing.T) {
	binary := fakeCodexBinary(t, "missing-resume")
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.147.0"})
	result, err := adapter.Run(context.Background(), harness.Launch{
		Workdir:      t.TempDir(),
		SessionID:    "missing-thread",
		Instructions: "continue",
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "needs_input" || !strings.Contains(result.Blocker, "not found") {
		t.Fatalf("unexpected result: %+v", result)
	}
	methods := readMethods(t, filepath.Join(filepath.Dir(binary), "methods"))
	if strings.Contains(strings.Join(methods, ","), "thread/start") {
		t.Fatalf("resume fallback started a new thread: %v", methods)
	}
}

func TestRunApprovalRequestNeedsInputWithoutAutoApproval(t *testing.T) {
	decisionFile := filepath.Join(t.TempDir(), "decision")
	binary := fakeCodexBinaryWithDecisionFile(t, "approval", decisionFile)
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.147.0"})
	var events []harness.Event
	result, err := adapter.Run(context.Background(), harness.Launch{
		Workdir:      t.TempDir(),
		Instructions: "run command",
	}, func(event harness.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "needs_input" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(events) != 2 || events[1].Type != "item/commandExecution/requestApproval" {
		t.Fatalf("approval events = %+v", events)
	}
	var approval map[string]any
	if err := json.Unmarshal(events[1].Data, &approval); err != nil {
		t.Fatalf("decode approval data: %v", err)
	}
	wantApproval := map[string]any{"itemId": "item-1", "threadId": "thread-1", "turnId": "turn-1", "command": "uname"}
	if !reflect.DeepEqual(approval, wantApproval) {
		t.Fatalf("approval data = %s", events[1].Data)
	}
	if data, err := os.ReadFile(decisionFile); err == nil && len(data) != 0 {
		t.Fatalf("adapter auto-approved request: %q", data)
	}
}

func TestRunAuthRequestNeedsInputWithoutAnswering(t *testing.T) {
	binary := fakeCodexBinary(t, "auth-request")
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.147.0"})
	var events []harness.Event
	result, err := adapter.Run(context.Background(), harness.Launch{
		Workdir:      t.TempDir(),
		Instructions: "authenticate",
	}, func(event harness.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "needs_input" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(events) != 2 || events[1].Type != "auth_failure" {
		t.Fatalf("auth events = %+v", events)
	}
	var params map[string]any
	if err := json.Unmarshal(events[1].Data, &params); err != nil || params["reason"] != "expired" {
		t.Fatalf("auth data = %s", events[1].Data)
	}
}

func TestRunCancellationInterruptsAndStopsProcess(t *testing.T) {
	binary := fakeCodexBinary(t, "cancel")
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.147.0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	resultCh := make(chan harness.Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := adapter.Run(ctx, harness.Launch{
			Workdir:      t.TempDir(),
			Instructions: "wait",
		}, func(event harness.Event) error {
			if event.Type == "turn_started" {
				close(started)
			}
			return nil
		})
		resultCh <- result
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not start")
	}
	cancel()
	select {
	case result := <-resultCh:
		if result.Phase != "cancelled" {
			t.Fatalf("result = %+v", result)
		}
		if err := <-errCh; err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	methods := readMethods(t, filepath.Join(filepath.Dir(binary), "methods"))
	if !strings.Contains(strings.Join(methods, ","), "turn/interrupt") {
		t.Fatalf("methods = %v, want turn/interrupt", methods)
	}
}

func fakeCodexBinary(t *testing.T, scenario string) string {
	return fakeCodexBinaryWithEnvFile(t, scenario, "")
}

func fakeCodexBinaryWithDecisionFile(t *testing.T, scenario, decisionFile string) string {
	binary := fakeCodexBinary(t, scenario)
	t.Setenv("FAROS_FAKE_CODEX_DECISION_FILE", decisionFile)
	return binary
}

func fakeCodexBinaryWithEnvFile(t *testing.T, scenario, envFile string) string {
	t.Helper()
	dir := t.TempDir()
	methodsFile := filepath.Join(dir, "methods")
	requestsFile := filepath.Join(dir, "requests")
	script := filepath.Join(dir, "codex")
	content := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo codex-cli 0.147.0; exit 0; fi\nprintf '%%s\\n' \"$@\" > %q\nFAROS_FAKE_CODEX_CHILD=1 exec %q -test.run=TestFakeAppServerProcess\n", filepath.Join(dir, "argv"), os.Args[0])
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAROS_FAKE_CODEX_SCENARIO", scenario)
	t.Setenv("FAROS_FAKE_CODEX_METHODS", methodsFile)
	t.Setenv("FAROS_FAKE_CODEX_REQUESTS", requestsFile)
	if envFile != "" {
		t.Setenv("FAROS_FAKE_CODEX_ENV_FILE", envFile)
	} else {
		t.Setenv("FAROS_FAKE_CODEX_ENV_FILE", "")
	}
	t.Setenv("FAROS_FAKE_CODEX_DECISION_FILE", "")
	return script
}

func TestFakeAppServerProcess(t *testing.T) {
	if os.Getenv("FAROS_FAKE_CODEX_CHILD") != "1" {
		return
	}
	scenario := os.Getenv("FAROS_FAKE_CODEX_SCENARIO")
	methodsFile := os.Getenv("FAROS_FAKE_CODEX_METHODS")
	requestsFile := os.Getenv("FAROS_FAKE_CODEX_REQUESTS")
	if envFile := os.Getenv("FAROS_FAKE_CODEX_ENV_FILE"); envFile != "" {
		env := map[string]string{}
		for _, name := range []string{"GITHUB_TOKEN", "GITHUB_ACTIONS", "CODEX_HOME", "HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL", "SSH_AUTH_SOCK"} {
			env[name] = os.Getenv(name)
		}
		writeJSONFile(envFile, env)
	}
	methods, methodsErr := os.OpenFile(methodsFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	requests, requestsErr := os.OpenFile(requestsFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if methodsErr != nil || requestsErr != nil {
		os.Exit(2)
	}
	defer func() { _ = methods.Close() }()
	defer func() { _ = requests.Close() }()
	scanner := bufio.NewScanner(os.Stdin)
	approvalPending := false
	sawApprovalDecision := false
	for scanner.Scan() {
		var request map[string]any
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		method, _ := request["method"].(string)
		if approvalPending {
			sawApprovalDecision = true
		}
		_, _ = fmt.Fprintln(methods, method)
		writeJSONLine(requests, request)
		id := request["id"]
		switch method {
		case "initialize":
			writeResponse(map[string]any{"id": id, "result": map[string]any{"userAgent": "fake", "codexHome": os.Getenv("CODEX_HOME"), "platformFamily": "unix", "platformOs": "linux"}})
		case "account/read":
			switch scenario {
			case "auth":
				writeResponse(map[string]any{"id": id, "error": map[string]any{"code": 401, "message": "authentication required"}})
			case "auth-chatgpt":
				writeResponse(map[string]any{"id": id, "result": map[string]any{
					"account":            map[string]any{"type": "chatgpt", "email": "user@example.com", "planType": "pro"},
					"requiresOpenaiAuth": true,
				}})
			case "auth-api-key":
				writeResponse(map[string]any{"id": id, "result": map[string]any{
					"account":            map[string]any{"type": "apiKey"},
					"requiresOpenaiAuth": true,
				}})
			case "auth-missing":
				writeResponse(map[string]any{"id": id, "result": map[string]any{"requiresOpenaiAuth": true}})
			case "auth-null":
				writeResponse(map[string]any{"id": id, "result": map[string]any{"account": nil, "requiresOpenaiAuth": true}})
			case "auth-malformed":
				writeResponse(map[string]any{"id": id, "result": map[string]any{
					"account":            map[string]any{"type": []string{"chatgpt"}, "token": "access-token-secret", "apiKey": "sk-test-secret"},
					"requiresOpenaiAuth": true,
				}})
			case "auth-missing-flag":
				writeResponse(map[string]any{"id": id, "result": map[string]any{
					"account": map[string]any{"type": "chatgpt", "email": "user@example.com"},
				}})
			case "auth-wrong-type-flag":
				writeResponse(map[string]any{"id": id, "result": map[string]any{
					"account":            map[string]any{"type": "chatgpt", "email": "user@example.com"},
					"requiresOpenaiAuth": "true",
				}})
			case "auth-malformed-noauth":
				writeResponse(map[string]any{"id": id, "result": map[string]any{
					"account":            []string{"not-an-account"},
					"requiresOpenaiAuth": false,
				}})
			default:
				writeResponse(map[string]any{"id": id, "result": map[string]any{"account": nil, "requiresOpenaiAuth": false}})
			}
		case "thread/start":
			writeResponse(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "thread-1"}}})
		case "thread/resume":
			if scenario == "missing-resume" {
				writeResponse(map[string]any{"id": id, "error": map[string]any{"code": -32602, "message": "thread not found"}})
			} else {
				params, _ := request["params"].(map[string]any)
				resumeID, _ := params["threadId"].(string)
				if scenario == "foreign-resume" {
					resumeID = "thread-foreign"
				}
				writeResponse(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": resumeID}}})
			}
		case "turn/start":
			writeResponse(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress"}}})
			params, _ := request["params"].(map[string]any)
			threadID, _ := params["threadId"].(string)
			if threadID == "" {
				threadID = "thread-1"
			}
			switch scenario {
			case "success", "foreign-completion":
				writeNotification("item/agentMessage/delta", map[string]any{"threadId": threadID, "turnId": "turn-1", "itemId": "item-1", "delta": "hello"})
				completionThreadID := threadID
				if scenario == "foreign-completion" {
					completionThreadID = "thread-foreign"
				}
				writeNotification("turn/completed", map[string]any{"threadId": completionThreadID, "turn": map[string]any{"id": "turn-1", "status": "completed", "error": nil}})
			case "approval":
				writeRequest("item/commandExecution/requestApproval", "approval-1", map[string]any{"itemId": "item-1", "threadId": "thread-1", "turnId": "turn-1", "command": "uname"})
				approvalPending = true
			case "auth-request":
				writeRequest("account/chatgptAuthTokens/refresh", "auth-1", map[string]any{"reason": "expired"})
			case "cancel":
				writeNotification("turn/started", map[string]any{"threadId": threadID, "turn": map[string]any{"id": "turn-1", "status": "inProgress"}})
			}
		case "turn/interrupt":
			writeResponse(map[string]any{"id": id, "result": map[string]any{}})
			writeNotification("turn/completed", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "interrupted", "error": nil}})
		default:
			if id != nil {
				writeResponse(map[string]any{"id": id, "result": map[string]any{}})
			}
		}
	}
	if scenario == "approval" && sawApprovalDecision {
		// Any response after the approval request proves the adapter auto-approved it.
		_ = os.WriteFile(os.Getenv("FAROS_FAKE_CODEX_DECISION_FILE"), []byte("decision"), 0600)
	}
	os.Exit(0)
}

var fakeWriteMu sync.Mutex

func writeResponse(value map[string]any) {
	writeJSONLine(os.Stdout, value)
}

func writeNotification(method string, params map[string]any) {
	writeJSONLine(os.Stdout, map[string]any{"method": method, "params": params})
}

func writeRequest(method, id string, params map[string]any) {
	writeJSONLine(os.Stdout, map[string]any{"id": id, "method": method, "params": params})
}

func writeJSONLine(w interface{ Write([]byte) (int, error) }, value any) {
	fakeWriteMu.Lock()
	defer fakeWriteMu.Unlock()
	data, _ := json.Marshal(value)
	_, _ = w.Write(append(data, '\n'))
}

func writeJSONFile(path string, value any) {
	data, _ := json.Marshal(value)
	_ = os.WriteFile(path, data, 0600)
}

func readMethods(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read methods: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func readJSONLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read requests: %v", err)
	}
	var result []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("decode request %q: %v", line, err)
		}
		result = append(result, value)
	}
	return result
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read json file: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode json file: %v", err)
	}
	return value
}
