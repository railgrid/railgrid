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
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectEinoAssistantWriteTodosPublishesCodexStylePlanUpdate(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	var published []projectAssistantPlanSnapshot
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnPlan: func(plan projectAssistantPlanSnapshot) { published = append(published, plan) },
		},
	}, runState).(*projectEinoAssistantLifecycle)
	wrapped, err := lifecycle.WrapInvokableToolCall(
		context.Background(),
		func(_ context.Context, _ string, _ ...einotool.Option) (string, error) {
			return `Updated todo list`, nil
		},
		&adk.ToolContext{Name: projectEinoAssistantWriteTodosTool},
	)
	if err != nil {
		t.Fatal(err)
	}
	arguments := `{"todos":[{"content":"Inspect current app","activeForm":"Inspecting current app","status":"completed"},{"content":"Implement the fix","activeForm":"Implementing the fix","status":"in_progress"},{"content":"Verify behavior","activeForm":"Verifying behavior","status":"pending"}]}`
	if _, err := wrapped(context.Background(), arguments); err != nil {
		t.Fatal(err)
	}

	if len(published) != 1 || len(published[0].Steps) != 3 {
		t.Fatalf("published plans = %#v, want one three-step update", published)
	}
	if got := runState.PlanProgress().Steps[1]; got.Status != "in_progress" || got.Content != "Implement the fix" {
		t.Fatalf("current plan step = %#v", got)
	}
	if got := projectEinoAssistantPlanProgressStatus(published[0]); got != "Building · 1 of 3 steps" {
		t.Fatalf("derived status = %q", got)
	}
}

func TestProjectEinoAssistantWriteTodosPublishesDerivedStatusWithoutPlanCallback(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	var status string
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnStatus: func(next string) { status = next },
		},
	}, runState).(*projectEinoAssistantLifecycle)
	wrapped, err := lifecycle.WrapInvokableToolCall(
		context.Background(),
		func(_ context.Context, _ string, _ ...einotool.Option) (string, error) {
			return `Updated todo list`, nil
		},
		&adk.ToolContext{Name: projectEinoAssistantWriteTodosTool},
	)
	if err != nil {
		t.Fatal(err)
	}
	arguments := `{"todos":[{"content":"Inspect current app","activeForm":"Inspecting current app","status":"completed"},{"content":"Implement the fix","activeForm":"Implementing the fix","status":"in_progress"},{"content":"Verify behavior","activeForm":"Verifying behavior","status":"pending"}]}`
	if _, err := wrapped(context.Background(), arguments); err != nil {
		t.Fatal(err)
	}
	if status != "Building · 1 of 3 steps" {
		t.Fatalf("derived status = %q, want model-authored checklist status", status)
	}
}

func TestProjectEinoAssistantWriteTodosPublishesCompletedPlanSnapshot(t *testing.T) {
	plan := projectAssistantApprovedPlan{Steps: []string{"Implement the change", "Verify the result"}}
	runState := newProjectEinoAssistantRunState()
	runState.SetExecutionPlan(plan)
	var published []projectAssistantPlanSnapshot
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnPlan: func(next projectAssistantPlanSnapshot) { published = append(published, next) },
		},
	}, runState).(*projectEinoAssistantLifecycle)
	wrapper, err := lifecycle.WrapInvokableToolCall(
		context.Background(),
		func(_ context.Context, _ string, _ ...einotool.Option) (string, error) {
			return `Updated todo list`, nil
		},
		&adk.ToolContext{Name: projectEinoAssistantWriteTodosTool},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapper(context.Background(), `{"todos":[{"content":"Implement the change","activeForm":"Implementing the change","status":"completed"},{"content":"Verify the result","activeForm":"Verifying the result","status":"completed"}]}`); err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || !runState.ExecutionPlanComplete() {
		t.Fatalf("write_todos terminal snapshot = %#v, execution plan complete = %t", published, runState.ExecutionPlanComplete())
	}
	if got := projectEinoAssistantPlanProgressStatus(runState.PlanProgress()); got != "Building · 2 of 2 steps" {
		t.Fatalf("terminal status = %q", got)
	}
}

func TestProjectEinoAssistantWriteTodosPublishesTerminalStatusWithoutPlanCallback(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	var status string
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnStatus: func(next string) { status = next },
		},
	}, runState).(*projectEinoAssistantLifecycle)
	wrapper, err := lifecycle.WrapInvokableToolCall(
		context.Background(),
		func(_ context.Context, _ string, _ ...einotool.Option) (string, error) {
			return `Updated todo list`, nil
		},
		&adk.ToolContext{Name: projectEinoAssistantWriteTodosTool},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapper(context.Background(), `{"todos":[{"content":"Implement the change","activeForm":"Implementing the change","status":"completed"},{"content":"Verify the result","activeForm":"Verifying the result","status":"completed"}]}`); err != nil {
		t.Fatal(err)
	}
	if status != "Building · 2 of 2 steps" {
		t.Fatalf("terminal status = %q, want model-authored checklist status", status)
	}
}

func TestProjectEinoAssistantRuntimeVerificationDoesNotAdvancePlanProgress(t *testing.T) {
	plan := projectAssistantApprovedPlan{Steps: []string{"Implement the change", "Verify the result"}}
	runState := newProjectEinoAssistantRunState()
	runState.SetExecutionPlan(plan)
	runState.SetPlanProgress(projectAssistantInitialPlanProgress(plan))
	runState.RecordSourceMutation()
	before := runState.PlanProgress()
	approved := plan
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		InitialApprovedPlan: &approved,
		Workspace:           workspace.NewFileStore(t.TempDir()),
	}, runState).(*projectEinoAssistantLifecycle)
	wrapper, err := lifecycle.WrapInvokableToolCall(
		context.Background(),
		func(_ context.Context, _ string, _ ...einotool.Option) (string, error) {
			return `{"status":"ready","checkedMutationRevision":1}`, nil
		},
		&adk.ToolContext{Name: projectToolVerifyDevelopmentRuntime},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapper(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
	if got := runState.PlanProgress(); !reflect.DeepEqual(got, before) {
		t.Fatalf("runtime verification advanced plan: got %#v, before %#v", got, before)
	}
}

func TestProjectEinoAssistantPreviewInspectionDoesNotAdvancePlanProgress(t *testing.T) {
	plan := projectAssistantApprovedPlan{Steps: []string{"Implement the change", "Verify the result"}}
	runState := newProjectEinoAssistantRunState()
	runState.SetExecutionPlan(plan)
	runState.SetPlanProgress(projectAssistantInitialPlanProgress(plan))
	runState.RecordSourceMutation()
	before := runState.PlanProgress()
	approved := plan
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		InitialApprovedPlan: &approved,
	}, runState).(*projectEinoAssistantLifecycle)
	wrapper, err := lifecycle.WrapInvokableToolCall(
		context.Background(),
		func(_ context.Context, _ string, _ ...einotool.Option) (string, error) {
			return `{}`, nil
		},
		&adk.ToolContext{Name: projectToolInspectDevelopmentPreview},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapper(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
	if got := runState.PlanProgress(); !reflect.DeepEqual(got, before) {
		t.Fatalf("preview inspection advanced plan: got %#v, before %#v", got, before)
	}
}

func TestProjectEinoAssistantWriteTodosRejectsUnpublishablePlan(t *testing.T) {
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{}, newProjectEinoAssistantRunState()).(*projectEinoAssistantLifecycle)
	called := false
	wrapped, err := lifecycle.WrapInvokableToolCall(
		context.Background(),
		func(_ context.Context, _ string, _ ...einotool.Option) (string, error) {
			called = true
			return `Updated todo list`, nil
		},
		&adk.ToolContext{Name: projectEinoAssistantWriteTodosTool},
	)
	if err != nil {
		t.Fatal(err)
	}
	arguments := `{"todos":[{"content":"First","activeForm":"First","status":"in_progress"},{"content":"Second","activeForm":"Second","status":"in_progress"}]}`
	if _, err := wrapped(context.Background(), arguments); err == nil {
		t.Fatal("write_todos accepted a plan the durable UI cannot represent")
	}
	if called {
		t.Fatal("invalid plan reached Eino's session-state writer")
	}
}

func TestProjectEinoAssistantWriteTodosRejectsTrailingPayload(t *testing.T) {
	if _, err := projectEinoAssistantPlanProgressFromWriteTodos(
		`{"todos":[{"content":"Inspect","activeForm":"Inspecting","status":"in_progress"}]} {}`,
	); err == nil {
		t.Fatal("write_todos accepted a trailing JSON value")
	}
}

func TestProjectEinoAssistantPlanProgressBoundsModelLabels(t *testing.T) {
	long := strings.Repeat("é", projectEinoAssistantTodoProgressMaxLabelBytes)
	plan, err := projectEinoAssistantPlanProgressFromWriteTodos(`{"todos":[{"content":"` + long + `","activeForm":"` + long + `","status":"in_progress"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Steps[0]; len(got.Content) > projectEinoAssistantTodoProgressMaxLabelBytes || len(got.ActiveForm) > projectEinoAssistantTodoProgressMaxLabelBytes {
		t.Fatalf("bounded step = %#v", got)
	}
}
