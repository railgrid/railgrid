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

package dispatch

import (
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner"
)

type messageRecorder struct {
	recorder
	messages []Message
	order    []string
}

func (r *messageRecorder) Message(message Message) {
	r.messages = append(r.messages, message)
	r.order = append(r.order, message.Phase)
}

func (r *messageRecorder) ToolStart(id, name, args string) {
	r.recorder.ToolStart(id, name, args)
	r.order = append(r.order, "tool")
}

func TestMessageBoundariesPreserveParagraphsAndToolOrdering(t *testing.T) {
	rec := &messageRecorder{}
	s := newStream(rec, 0)
	for _, e := range []runner.Event{
		event(1, runner.EventProgress, "", `{"_railgridEvent":"item/started","item":{"id":"a","type":"agentMessage","phase":"commentary","text":"provisional"}}`),
		event(2, runner.EventProgress, "First paragraph.", `{"itemId":"a","delta":"First paragraph."}`),
		event(3, runner.EventProgress, "\n\nSecond paragraph.", `{"itemId":"a","delta":"\n\nSecond paragraph."}`),
		event(4, runner.EventProgress, "", `{"_railgridEvent":"item/completed","item":{"id":"a","type":"agentMessage","phase":"commentary","text":"First paragraph.\n\nSecond paragraph."}}`),
		event(5, runner.EventProgress, "", `{"item":{"id":"command","type":"commandExecution","command":"go test","status":"inProgress"}}`),
		event(6, runner.EventProgress, "Tests passed.", `{"itemId":"b","delta":"Tests passed."}`),
		event(7, runner.EventProgress, "", `{"item":{"id":"b","type":"agentMessage","phase":"commentary","text":"Tests passed."}}`),
		event(8, runner.EventProgress, "Fixed.", `{"itemId":"c","delta":"Fixed."}`),
		event(9, runner.EventProgress, "", `{"item":{"id":"c","type":"agentMessage","phase":"final_answer","text":"Fixed."}}`),
		event(10, runner.EventProgress, "", `{"item":{"id":"c","type":"agentMessage","phase":"final_answer","text":"Fixed."}}`),
	} {
		s.observe(e)
	}
	if len(rec.messages) != 3 || rec.messages[0].Content != "First paragraph.\n\nSecond paragraph." || rec.messages[2].Phase != "final" {
		t.Fatalf("message boundaries = %+v", rec.messages)
	}
	if got := strings.Join(rec.order, ","); got != "commentary,tool,commentary,final" {
		t.Fatalf("event order = %s", got)
	}
	if s.final != "Fixed." {
		t.Fatalf("final answer = %q", s.final)
	}
	if got := s.text.String(); got != "First paragraph.\n\nSecond paragraph.\n\nTests passed.\n\nFixed." {
		t.Fatalf("saved prose = %q", got)
	}
}

func TestMessageDeduplicationSurvivesCheckpointRestore(t *testing.T) {
	s := newStream(&messageRecorder{}, 0)
	s.observe(event(1, runner.EventProgress, "", `{"item":{"id":"a","type":"agentMessage","phase":"commentary","text":"Before restart."}}`))
	rec := &messageRecorder{}
	restored, err := restoreStream(rec, s.snapshot(Position{}))
	if err != nil {
		t.Fatal(err)
	}
	restored.observe(event(2, runner.EventProgress, "", `{"item":{"id":"a","type":"agentMessage","phase":"commentary","text":"Before restart."}}`))
	restored.observe(event(3, runner.EventProgress, "", `{"item":{"id":"b","type":"agentMessage","phase":"commentary","text":"After restart."}}`))
	if len(rec.messages) != 1 || rec.messages[0].Content != "After restart." {
		t.Fatalf("replayed commentary: %+v", rec.messages)
	}
}

func TestClaudeBlocksAreSeparatedAndToolNamesAreNotProse(t *testing.T) {
	rec := &messageRecorder{}
	s := newStream(rec, 0)
	message := `{"type":"railgrid.assistant_message","id":"a","phase":"commentary","text":"Inspecting.\n\nChecking tests."}`
	s.observe(event(1, runner.EventProgress, "Inspecting.\n\nChecking tests.", message))
	s.observe(event(2, runner.EventProgress, "Inspecting.\n\nChecking tests.", message))
	s.observe(event(3, runner.EventProgress, "Bash", `{"type":"railgrid.tool_use","name":"Bash"}`))
	if got := strings.Join(rec.text, ""); got != "Inspecting.\n\nChecking tests.\n" {
		t.Fatalf("live text = %q", got)
	}
	if s.text.String() != strings.Join(rec.text, "") || len(rec.messages) != 1 {
		t.Fatalf("saved/live blocks diverged: %+v", rec.messages)
	}
}

func TestLegacyLiveProgressKeepsSavedLineBreaks(t *testing.T) {
	rec := &recorder{}
	s := newStream(rec, 0)
	s.observe(event(1, runner.EventProgress, "One paragraph.\n\nAnother paragraph.", ""))
	s.observe(event(2, runner.EventProgress, "Next message.", ""))
	if got := strings.Join(rec.text, ""); got != "One paragraph.\n\nAnother paragraph.\nNext message.\n" || got != s.text.String() {
		t.Fatalf("live/saved text = %q / %q", got, s.text.String())
	}
}

func TestUnclassifiedNativeMessagesKeepParagraphSeparators(t *testing.T) {
	rec := &messageRecorder{}
	s := newStream(rec, 0)
	// A runner may expose item IDs without completed-message phases. Merely
	// supporting MessageObserver must not remove the text fallback separators.
	s.observe(event(1, runner.EventProgress, "First.", `{"itemId":"a","delta":"First."}`))
	s.observe(event(2, runner.EventProgress, "Second.", `{"itemId":"b","delta":"Second."}`))
	if got := strings.Join(rec.text, ""); got != "First.\n\nSecond." {
		t.Fatalf("unclassified live text = %q", got)
	}
}
