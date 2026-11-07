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
	"fmt"
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// Claude's documented assistant stream distinguishes subagent output with
// parent_tool_use_id, rather than Codex's child thread lifecycle notifications.
// See https://code.claude.com/docs/en/agent-sdk/subagents#detect-subagent-invocation.
func TestSubagentMessagesCannotSupplyRootAnswerOrClarification(t *testing.T) {
	for _, rootText := range []string{"parent answer", ""} {
		t.Run("root="+rootText, func(t *testing.T) {
			var events []harness.Event
			s := runState{emit: func(e harness.Event) error { events = append(events, e); return nil }}
			stream := fmt.Sprintf(`
{"type":"system","subtype":"init","session_id":"root"}
{"type":"assistant","session_id":"root","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"text","text":%q}]}}
{"type":"assistant","session_id":"root","parent_tool_use_id":"agent-call","message":{"role":"assistant","content":[{"type":"text","text":%q},{"type":"tool_use","name":"Bash","id":"child-tool"}]}}
{"type":"assistant","session_id":"root","parent_tool_use_id":"nested-agent-call","message":{"role":"assistant","content":[{"type":"text","text":"nested answer"}]}}
{"type":"result","subtype":"success","session_id":"root","result":""}
`, rootText, clarificationOpen+"\nChild question?\n"+clarificationClose)
			if err := s.consume(strings.NewReader(stream)); err != nil {
				t.Fatal(err)
			}
			if s.result == nil || s.result.Phase != "completed" || s.result.SessionID != "root" || s.result.Clarification != nil || s.lastText != rootText {
				t.Fatalf("child changed root outcome: %+v", s)
			}
			for _, e := range events {
				if e.SessionID != "root" || (e.Type == "message" && e.Message != rootText) || e.Type == "tool_use" || e.Type == "needs_input" {
					t.Fatalf("child escaped as root event: %+v", e)
				}
			}
			last := events[len(events)-1]
			if last.Type != "result" || last.Message != rootText {
				t.Fatalf("root final=%+v", last)
			}
		})
	}
}

func TestSubagentAttributionDoesNotAuthorizeForeignClaudeSession(t *testing.T) {
	s := runState{sessionID: "root"}
	err := s.consume(strings.NewReader(`{"type":"assistant","session_id":"foreign","parent_tool_use_id":"agent-call","message":{"content":[{"type":"text","text":"foreign answer"}]}}`))
	if err == nil || !strings.Contains(err.Error(), "foreign session") {
		t.Fatalf("foreign session accepted: %v", err)
	}
	if s.sessionID != "root" || s.result != nil || s.lastText != "" {
		t.Fatalf("foreign session changed state: %+v", s)
	}
}
