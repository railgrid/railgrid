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
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func TestRPCConnRoutesServerRequestBeforeMatchingResponseID(t *testing.T) {
	var output bytes.Buffer
	conn := &rpcConn{stdin: &output, messages: make(chan wireMessage, 2)}
	conn.messages <- wireMessage{
		ID:     json.RawMessage(`1`),
		Method: "item/commandExecution/requestApproval",
		Params: json.RawMessage(`{"threadId":"thread-1"}`),
	}
	conn.messages <- wireMessage{
		ID:     json.RawMessage(`1`),
		Result: json.RawMessage(`{"thread":{"id":"thread-1"}}`),
	}
	var handled int
	response, err := conn.call(context.Background(), "thread/start", map[string]any{}, func(msg wireMessage) error {
		if msg.Method != "item/commandExecution/requestApproval" {
			t.Fatalf("handler received method %q", msg.Method)
		}
		handled++
		return nil
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if handled != 1 || response.Method != "" || string(response.Result) != `{"thread":{"id":"thread-1"}}` {
		t.Fatalf("handled = %d, response = %+v", handled, response)
	}
	var request map[string]any
	if err := json.Unmarshal(output.Bytes(), &request); err != nil || request["method"] != "thread/start" {
		t.Fatalf("outgoing request = %s, error = %v", output.Bytes(), err)
	}
}

func TestUnsupportedApprovalRequestUsesJSONRPCError(t *testing.T) {
	var output bytes.Buffer
	state := runState{conn: &rpcConn{stdin: &output}}
	err := state.handleApprovalRequest(wireMessage{
		ID:     json.RawMessage(`"approval-1"`),
		Method: "item/permissions/requestApproval",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","permissions":{"network":{"enabled":true}}}`),
	})
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported request error = %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["id"] != "approval-1" || response["result"] != nil {
		t.Fatalf("unsupported approval response = %v, want JSON-RPC error envelope", response)
	}
	if rpcError, ok := response["error"].(map[string]any); !ok || rpcError["code"] != float64(-32601) {
		t.Fatalf("unsupported approval error = %v", response["error"])
	}
}

func TestCodexPermissionRequestUsesExactCallCoordinates(t *testing.T) {
	msg := wireMessage{
		ID:     json.RawMessage(`7`),
		Method: "item/fileChange/requestApproval",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-2","itemId":"item-3","diff":"patch"}`),
	}
	request, err := codexPermissionRequest("attempt-1", "thread-1", "turn-2", msg)
	if err != nil {
		t.Fatalf("codexPermissionRequest: %v", err)
	}
	if request.ID != stablePermissionID("attempt-1", msg.Method, "thread-1", "turn-2", "item-3", "") || request.Tool != "Edit" {
		t.Fatalf("permission request = %+v", request)
	}
	if !json.Valid([]byte(request.Input)) || !strings.Contains(request.Input, `"diff":"patch"`) {
		t.Fatalf("permission input = %q", request.Input)
	}
	legacyReplay, err := codexPermissionRequest("attempt-1", "thread-1", "turn-2", msg)
	if err != nil {
		t.Fatalf("replayed approval without approvalId: %v", err)
	}
	if request.ID != "permission-29afa1d181daf667b1f2629555282168470fe97d697f5a70292ea377358359d1" {
		t.Fatalf("approval without approvalId changed the legacy ID: %q", request.ID)
	}
	if request.ID != legacyReplay.ID {
		t.Fatalf("approval without approvalId changed across replay: %q != %q", request.ID, legacyReplay.ID)
	}
	if _, err := codexPermissionRequest("attempt-1", "thread-other", "turn-2", msg); err == nil {
		t.Fatal("accepted a request from a different session")
	}
	if _, err := codexPermissionRequest("attempt-1", "thread-1", "turn-other", msg); err == nil {
		t.Fatal("accepted a request from a different turn")
	}
	if codexApprovalTool("item/permissions/requestApproval") != "" {
		t.Fatal("native sandbox permission profiles must not be mapped to a boolean command verdict")
	}
}

func TestCodexCommandApprovalIDSeparatesSubcommandCallbacks(t *testing.T) {
	requestFor := func(approvalID string) harness.PermissionRequest {
		t.Helper()
		params, err := json.Marshal(map[string]string{
			"threadId":   "thread-1",
			"turnId":     "turn-2",
			"itemId":     "command-item-1",
			"approvalId": approvalID,
			"command":    "bash -lc 'read -r reply'",
			"cwd":        "/workspace/project",
		})
		if err != nil {
			t.Fatalf("marshal command approval: %v", err)
		}
		request, err := codexPermissionRequest("attempt-1", "thread-1", "turn-2", wireMessage{
			ID:     json.RawMessage(`7`),
			Method: "item/commandExecution/requestApproval",
			Params: params,
		})
		if err != nil {
			t.Fatalf("codexPermissionRequest: %v", err)
		}
		if request.Tool != "Bash" {
			t.Fatalf("command permission tool = %q, want Bash", request.Tool)
		}
		return request
	}

	first := requestFor("stdin-write-1")
	second := requestFor("stdin-write-2")
	if first.ID == second.ID {
		t.Fatalf("distinct Codex command callbacks share permission ID %q", first.ID)
	}
}

func TestOversizedApprovalRequestDeclinesWithoutAsking(t *testing.T) {
	params, err := json.Marshal(map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"itemId":   "item-1",
		"command":  strings.Repeat("x", harness.MaxPermissionInputBytes),
	})
	if err != nil {
		t.Fatalf("marshal approval params: %v", err)
	}

	var output bytes.Buffer
	asker := &permissionAskerSpy{}
	state := runState{
		attemptID:   "attempt-1",
		sessionID:   "thread-1",
		turnID:      "turn-1",
		permissions: asker,
		conn:        &rpcConn{stdin: &output},
	}
	err = state.handleApprovalRequest(wireMessage{
		ID:     json.RawMessage(`"approval-1"`),
		Method: "item/commandExecution/requestApproval",
		Params: params,
	})
	if err == nil {
		t.Fatal("oversized approval request was accepted as valid")
	}
	if asker.calls != 0 {
		t.Fatalf("PermissionAsker calls = %d, want 0", asker.calls)
	}

	var response struct {
		Result struct {
			Decision string `json:"decision"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); err != nil {
		t.Fatalf("decode approval response: %v", err)
	}
	if response.Result.Decision != "decline" {
		t.Fatalf("approval decision = %q, want decline", response.Result.Decision)
	}
}

type permissionAskerSpy struct {
	calls int
}

func (s *permissionAskerSpy) AskPermission(context.Context, harness.PermissionRequest) (harness.PermissionVerdict, error) {
	s.calls++
	return harness.PermissionVerdict{Allow: true}, nil
}
