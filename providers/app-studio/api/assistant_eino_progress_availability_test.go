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

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/railgrid/provider-app-studio/store"
)

func TestProjectEinoAssistantProgressAvailabilityUsesCallbackAndTurnProfile(t *testing.T) {
	req := projectAssistantRunRequest{
		TurnProfile: projectAssistantTurnProfileImplementation,
		TurnPolicy:  projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnProgress: func(string) {},
		},
	}
	if !projectEinoAssistantProgressEnabled(req, nil) {
		t.Fatal("report_progress unavailable for implementation turn without runtime/workspace state")
	}

	withoutCallback := req
	withoutCallback.StreamCallbacks.OnProgress = nil
	if projectEinoAssistantProgressEnabled(withoutCallback, nil) {
		t.Fatal("report_progress available without a progress callback")
	}

	for _, profile := range []projectAssistantTurnProfile{
		projectAssistantTurnProfileDebugging,
		projectAssistantTurnProfile("review"),
	} {
		readOnly := req
		readOnly.TurnProfile = profile
		readOnly.TurnPolicy = projectAssistantTurnPolicyForProfile(profile)
		if projectEinoAssistantProgressEnabled(readOnly, nil) {
			t.Fatalf("report_progress available for read-only profile %q", profile)
		}
	}

	runState := newProjectEinoAssistantRunState()
	runState.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	if !projectEinoAssistantProgressEnabled(req, runState) {
		t.Fatal("report_progress unavailable for implementation run state")
	}
	runState.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging))
	if projectEinoAssistantProgressEnabled(req, runState) {
		t.Fatal("report_progress available for read-only run state")
	}
}

func TestProjectEinoAssistantProgressToolRecordsTiming(t *testing.T) {
	timing := &projectAssistantWorkTiming{}
	ctx := context.WithValue(context.Background(), projectAssistantWorkTimingKey{}, timing)
	state := &projectAssistantDurableMetadataState{workTiming: timing}
	tool := projectEinoAssistantProgressTool{req: projectAssistantRunRequest{
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnProgress: func(message string) { state.appendProgress(message) },
		},
	}}
	if _, err := tool.InvokableRun(ctx, `{"message":"I inspected the project."}`); err != nil {
		t.Fatal(err)
	}
	run := store.AssistantRun{}
	timing.updateRun(&run)
	if run.WorkedDurationMS == nil {
		t.Fatal("report_progress did not record a timing measurement")
	}
	progress := state.progressSnapshot()
	if progress == nil || len(progress.Messages) != 1 {
		t.Fatalf("progress = %#v, want one progress message", progress)
	}
	raw, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"workedDurationMs":0`)) {
		t.Fatalf("progress = %s, want the required legacy duration placeholder", raw)
	}
}
