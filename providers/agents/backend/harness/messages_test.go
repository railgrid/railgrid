// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package harness

import (
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"

	"github.com/railgrid/provider-agents/backend"
)

func TestNativeCommentaryIsClassifiedBeforeToolsAndFinalTiming(t *testing.T) {
	f := &fakeRunner{receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, SessionID: "session"}, events: []runner.Event{
		event(1, runner.EventProgress, "Inspecting.", `{"itemId":"a","delta":"Inspecting."}`),
		event(2, runner.EventProgress, "\n\nChecking tests.", `{"itemId":"a","delta":"\n\nChecking tests."}`),
		event(3, runner.EventProgress, "", `{"item":{"id":"a","type":"agentMessage","phase":"commentary","text":"Inspecting.\n\nChecking tests."}}`),
		event(4, runner.EventProgress, "", `{"item":{"id":"cmd","type":"commandExecution","command":"go test","status":"inProgress"}}`),
		event(5, runner.EventProgress, "", `{"item":{"id":"cmd","type":"commandExecution","command":"go test","status":"completed","durationMs":100}}`),
		event(6, runner.EventProgress, "Fixed.", `{"itemId":"b","delta":"Fixed."}`),
		event(7, runner.EventProgress, "", `{"item":{"id":"b","type":"agentMessage","phase":"final_answer","text":"Fixed."}}`),
		event(8, runner.EventProgress, "completed", `{"turn":{"status":"completed","durationMs":500}}`),
		event(9, runner.EventCompleted, "", ""),
	}}
	sink := &recordingSink{}
	out, err := New(testConfig(f, 1, "")).Turn(t.Context(), testRun(), backend.Input{Messages: []backend.Message{{Role: "user", Content: "fix"}}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if out.Final != "Fixed." || len(sink.assistants) != 2 {
		t.Fatalf("outcome %+v, messages %+v", out, sink.assistants)
	}
	commentary, final := sink.assistants[0], sink.assistants[1]
	if !commentary.IsCommentary() || commentary.HasToolCalls || !commentary.PresentationOnly || commentary.Content != "Inspecting.\n\nChecking tests." {
		t.Fatalf("commentary = %+v", commentary)
	}
	if final.IsCommentary() || final.Content != "Fixed." || final.Duration != 400*time.Millisecond {
		t.Fatalf("final = %+v", final)
	}
}
