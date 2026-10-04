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
	if request.ID != stablePermissionID("attempt-1", msg.Method, "thread-1", "turn-2", "item-3") || request.Tool != "Edit" {
		t.Fatalf("permission request = %+v", request)
	}
	if !json.Valid([]byte(request.Input)) || !strings.Contains(request.Input, `"diff":"patch"`) {
		t.Fatalf("permission input = %q", request.Input)
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
