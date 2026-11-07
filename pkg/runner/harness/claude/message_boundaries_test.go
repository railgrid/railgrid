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
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func TestAssistantBlocksPreserveParagraphsWithoutPrivatePayloads(t *testing.T) {
	var events []harness.Event
	state := &runState{credential: credential{value: "secret-value"}, emit: func(e harness.Event) error { events = append(events, e); return nil }}
	record := streamRecord{Message: json.RawMessage(`{"id":"message-1","content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"First paragraph.\n\nSecond secret-value paragraph."},{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"private input"}}]}`)}
	if err := state.handleAssistant(record); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	var message struct {
		Type  string `json:"type"`
		Phase string `json:"phase"`
		Text  string `json:"text"`
		ID    string `json:"id"`
	}
	if err := json.Unmarshal(events[0].Data, &message); err != nil {
		t.Fatal(err)
	}
	if message.Phase != "commentary" || message.ID != "message-1:1" || !strings.Contains(message.Text, "\n\n") {
		t.Fatalf("lost message structure: %+v", message)
	}
	raw, _ := json.Marshal(events)
	for _, forbidden := range []string{"secret-value", "private reasoning", "private input"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("forwarded private payload %q", forbidden)
		}
	}
}
