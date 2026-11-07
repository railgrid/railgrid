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
	"encoding/json"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func TestItemNotificationPreservesStartAndCompletion(t *testing.T) {
	var events []harness.Event
	state := &runState{sessionID: "thread", emit: func(e harness.Event) error { events = append(events, e); return nil }}
	for _, method := range []string{"item/started", "item/completed"} {
		err := state.handle(wireMessage{Method: method, Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","item":{"id":"a","type":"agentMessage","phase":"commentary","text":"Hello."}}`)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	for i, method := range []string{"item/started", "item/completed"} {
		var data struct {
			Event string `json:"_railgridEvent"`
			Item  struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if err := json.Unmarshal(events[i].Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Event != method || data.Item.ID != "a" || data.Item.Text != "Hello." {
			t.Fatalf("lost native boundary: %+v", data)
		}
	}
}
