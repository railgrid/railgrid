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
	Content    string `json:"content"`
	ActiveForm string `json:"activeForm"`
	Status     string `json:"status"`
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
		content := projectEinoAssistantTodoProgressLabel(todo.Content)
		if strings.TrimSpace(content) == "" {
			return projectAssistantPlanSnapshot{}, errors.New("plan steps require content")
		}
		activeForm := projectEinoAssistantTodoProgressLabel(todo.ActiveForm)
		plan.Steps = append(plan.Steps, projectAssistantPlanStep{
			Content:    content,
			ActiveForm: activeForm,
			Status:     strings.TrimSpace(todo.Status),
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

func projectEinoAssistantPublishCompletedExecutionPlan(
	runState *projectEinoAssistantRunState,
	callbacks projectAssistantStreamCallbacks,
) {
	if runState == nil {
		return
	}
	runState.CompleteExecutionPlan()
	projectEinoAssistantPublishPlanProgress(runState, callbacks, runState.PlanProgress())
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
