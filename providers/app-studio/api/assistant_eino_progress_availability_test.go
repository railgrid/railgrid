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

package api

import "testing"

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
