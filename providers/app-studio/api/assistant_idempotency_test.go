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

import (
	"errors"
	"testing"

	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectAssistantStartRequestBinding(t *testing.T) {
	run := store.AssistantRun{Mode: store.AssistantRunModeContinue, WorkItemID: "work-1"}
	if err := bindProjectAssistantStartRequest(&run, "actor-1", " continue ", "work-1", 4); err != nil {
		t.Fatal(err)
	}
	if err := validateProjectAssistantStartReplay(run, "actor-1", "continue", store.AssistantRunModeContinue, "work-1", 4); err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	for name, validate := range map[string]func() error{
		"actor": func() error {
			return validateProjectAssistantStartReplay(run, "actor-2", "continue", store.AssistantRunModeContinue, "work-1", 4)
		},
		"content": func() error {
			return validateProjectAssistantStartReplay(run, "actor-1", "different", store.AssistantRunModeContinue, "work-1", 4)
		},
		"mode": func() error {
			return validateProjectAssistantStartReplay(run, "actor-1", "continue", store.AssistantRunModeNew, "work-1", 4)
		},
		"workItem": func() error {
			return validateProjectAssistantStartReplay(run, "actor-1", "continue", store.AssistantRunModeContinue, "work-2", 4)
		},
		"revision": func() error {
			return validateProjectAssistantStartReplay(run, "actor-1", "continue", store.AssistantRunModeContinue, "work-1", 5)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate(); !errors.Is(err, store.ErrAssistantRunConflict) {
				t.Fatalf("error = %v, want %v", err, store.ErrAssistantRunConflict)
			}
		})
	}
}

func TestProjectAssistantInitialBootstrapIsPartOfStartIdentity(t *testing.T) {
	run := store.AssistantRun{Mode: store.AssistantRunModeNew}
	if err := bindProjectAssistantStartRequest(&run, "actor-1", "build it", "", 0, true); err != nil {
		t.Fatal(err)
	}
	if err := validateProjectAssistantStartReplay(run, "actor-1", "build it", store.AssistantRunModeNew, "", 0, true); err != nil {
		t.Fatalf("identical initial bootstrap replay: %v", err)
	}
	if err := validateProjectAssistantStartReplay(run, "actor-1", "build it", store.AssistantRunModeNew, "", 0, false); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("toggled initial bootstrap error = %v, want %v", err, store.ErrAssistantRunConflict)
	}
}

func TestProjectAssistantStopRequestBinding(t *testing.T) {
	run := store.AssistantRun{ID: "run-1"}
	if err := bindProjectAssistantStopRequest(&run, "actor-1", "stop-1"); err != nil {
		t.Fatal(err)
	}
	if err := bindProjectAssistantStopRequest(&run, "actor-1", "stop-1"); err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if err := bindProjectAssistantStopRequest(&run, "actor-1", "stop-2"); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("changed request error = %v, want %v", err, store.ErrAssistantRunConflict)
	}
}

func TestProjectAssistantCancelRequestReceipt(t *testing.T) {
	receipt, err := encodeProjectAssistantCancelReceipt(projectAssistantCancelRequestReceipt("actor-1", "work-1", "cancel-1", 3))
	if err != nil {
		t.Fatal(err)
	}
	item := store.AssistantWorkItem{ID: "work-1", CancellationReceipt: receipt}
	if err := validateProjectAssistantCancelReplay(item, "actor-1", "cancel-1", 3); err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if err := validateProjectAssistantCancelReplay(item, "actor-1", "cancel-2", 3); !errors.Is(err, store.ErrAssistantWorkItemConflict) {
		t.Fatalf("changed request error = %v, want %v", err, store.ErrAssistantWorkItemConflict)
	}
}
