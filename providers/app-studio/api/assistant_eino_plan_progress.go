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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type projectEinoAssistantWriteTodosArguments struct {
	Todos []projectEinoAssistantWriteTodo `json:"todos"`
}

type projectEinoAssistantWriteTodo struct {
	Content    *string `json:"content"`
	ActiveForm *string `json:"activeForm"`
	Status     *string `json:"status"`
}

// projectEinoAssistantPlanProgressFromWriteTodos adapts Eino's model-authored
// checklist to App Studio's durable, provider-neutral plan snapshot. This is
// the equivalent of Codex turning each update_plan call into a PlanUpdate
// event; action execution never guesses which model step has advanced.
func projectEinoAssistantPlanProgressFromWriteTodos(argumentsInJSON string) (projectAssistantPlanSnapshot, error) {
	if len(argumentsInJSON) > projectEinoAssistantTodoProgressMaxInputBytes {
		return projectAssistantPlanSnapshot{}, errors.New("plan update is too large")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(argumentsInJSON))
	decoder.DisallowUnknownFields()
	var arguments projectEinoAssistantWriteTodosArguments
	if err := decoder.Decode(&arguments); err != nil {
		return projectAssistantPlanSnapshot{}, fmt.Errorf("invalid plan update: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return projectAssistantPlanSnapshot{}, errors.New("invalid plan update: trailing JSON value")
	}
	if len(arguments.Todos) == 0 || len(arguments.Todos) > projectEinoAssistantTodoProgressMaxItems {
		return projectAssistantPlanSnapshot{}, fmt.Errorf("plan update must contain between 1 and %d steps", projectEinoAssistantTodoProgressMaxItems)
	}

	plan := projectAssistantPlanSnapshot{Steps: make([]projectAssistantPlanStep, 0, len(arguments.Todos))}
	for _, todo := range arguments.Todos {
		if todo.Content == nil || todo.ActiveForm == nil || todo.Status == nil {
			return projectAssistantPlanSnapshot{}, errors.New("plan steps require content, activeForm, and status")
		}
		content := projectEinoAssistantTodoProgressLabel(*todo.Content)
		if strings.TrimSpace(content) == "" {
			return projectAssistantPlanSnapshot{}, errors.New("plan steps require content")
		}
		activeForm := projectEinoAssistantTodoProgressLabel(*todo.ActiveForm)
		if strings.TrimSpace(activeForm) == "" {
			return projectAssistantPlanSnapshot{}, errors.New("plan steps require activeForm")
		}
		plan.Steps = append(plan.Steps, projectAssistantPlanStep{
			Content:    content,
			ActiveForm: activeForm,
			Status:     strings.TrimSpace(*todo.Status),
		})
	}
	if !projectAssistantPlanSnapshotValid(plan) {
		return projectAssistantPlanSnapshot{}, errors.New("plan update has invalid step statuses")
	}
	return plan, nil
}

func projectEinoAssistantPublishPlanProgress(
	runState *projectEinoAssistantRunState,
	callbacks projectAssistantStreamCallbacks,
	plan projectAssistantPlanSnapshot,
) {
	if runState == nil || len(plan.Steps) == 0 {
		return
	}
	runState.SetPlanProgress(plan)
	if callbacks.OnPlan != nil {
		callbacks.OnPlan(plan)
	} else if callbacks.OnStatus != nil {
		// Durable App Studio consumers update the plan and its derived status
		// atomically in OnPlan. Retain status-only callback support for focused
		// engines without publishing a second, temporarily redundant snapshot.
		callbacks.OnStatus(projectEinoAssistantPlanProgressStatus(plan))
	}
}

// projectEinoAssistantPublishAcceptedPlanProgress projects a plan snapshot
// only after the App-owned tool ledger has accepted its successful result.
// OnPlan remains the durable metadata/state projection; the typed event is a
// live notification for consumers that need the same accepted snapshot before
// the next model boundary. Keep the event here rather than in the generic
// publisher so checkpoint hydration and the initial execution-plan projection
// do not masquerade as newly accepted write_todos updates.
func projectEinoAssistantPublishAcceptedPlanProgress(
	runState *projectEinoAssistantRunState,
	callbacks projectAssistantStreamCallbacks,
	plan projectAssistantPlanSnapshot,
) {
	if runState == nil || len(plan.Steps) == 0 {
		return
	}
	projectEinoAssistantPublishPlanProgress(runState, callbacks, plan)
	if callbacks.OnAssistantEvent == nil {
		return
	}
	snapshot := cloneProjectAssistantPlanSnapshot(plan)
	callbacks.OnAssistantEvent(projectAssistantEvent{
		Type: projectAssistantEventPlanUpdated,
		Plan: &snapshot,
	})
}

// projectEinoAssistantHydratePlanProgress restores the latest successful
// App-owned checklist projection after a process restart. The durable ledger
// is authoritative over a stale checkpoint or message projection; when no
// typed snapshot exists (including legacy ledgers), the caller's restored
// run-state remains untouched.
func projectEinoAssistantHydratePlanProgress(
	ctx context.Context,
	ledger *projectAssistantRunEventLedger,
	runState *projectEinoAssistantRunState,
	callbacks projectAssistantStreamCallbacks,
) error {
	if ledger == nil {
		return nil
	}
	plan, ok, err := ledger.LatestPlanSnapshot(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	projectEinoAssistantPublishPlanProgress(runState, callbacks, plan)
	return nil
}

func projectEinoAssistantPlanProgressStatus(plan projectAssistantPlanSnapshot) string {
	completed := 0
	for _, step := range plan.Steps {
		if step.Status == "completed" {
			completed++
		}
	}
	return fmt.Sprintf("Building · %d of %d steps", completed, len(plan.Steps))
}
