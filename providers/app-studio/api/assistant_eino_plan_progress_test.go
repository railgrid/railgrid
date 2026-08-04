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
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/faroshq/provider-app-studio/workspace"
)

func newProjectEinoAssistantWriteTodosNode(
	t *testing.T,
	h projectAssistantV2ToolHarness,
	runState *projectEinoAssistantRunState,
	callbacks projectAssistantStreamCallbacks,
) *compose.ToolsNode {
	t.Helper()
	req := h.req
	req.StreamCallbacks = callbacks
	return newProjectEinoAssistantToolNode(t, req, runState, newProjectEinoAssistantWriteTodosTool(h.server, req, runState))
}

func newProjectEinoAssistantToolNode(
	t *testing.T,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	tool einotool.BaseTool,
) *compose.ToolsNode {
	t.Helper()
	lifecycle := projectEinoAssistantLifecycleMiddleware(req, runState).(*projectEinoAssistantLifecycle)
	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{
		Tools: []einotool.BaseTool{tool},
		ToolCallMiddlewares: []compose.ToolMiddleware{{
			Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
				wrapped, wrapErr := lifecycle.WrapInvokableToolCall(context.Background(), func(ctx context.Context, arguments string, opts ...einotool.Option) (string, error) {
					output, err := next(ctx, &compose.ToolInput{Name: projectEinoAssistantWriteTodosTool, Arguments: arguments, CallOptions: opts})
					if err != nil {
						return "", err
					}
					return output.Result, nil
				}, &adk.ToolContext{Name: projectEinoAssistantWriteTodosTool})
				if wrapErr != nil {
					t.Fatalf("wrap lifecycle: %v", wrapErr)
				}
				return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
					result, err := wrapped(ctx, input.Arguments, input.CallOptions...)
					if err != nil {
						return nil, err
					}
					return &compose.ToolOutput{Result: result}, nil
				}
			},
		}},
		ExecuteSequentially: true,
	})
	if err != nil {
		t.Fatalf("create write_todos node: %v", err)
	}
	return node
}

func invokeProjectEinoAssistantWriteTodos(t *testing.T, node *compose.ToolsNode, callID, arguments string) error {
	t.Helper()
	_, err := node.Invoke(context.Background(), schema.AssistantMessage("", []schema.ToolCall{{
		ID:       callID,
		Function: schema.FunctionCall{Name: projectEinoAssistantWriteTodosTool, Arguments: arguments},
	}}))
	return err
}

func TestProjectEinoAssistantWriteTodosDurablyProjectsInitialIncrementalAndTerminalPlans(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "run-write-todos-projection")
	runState := newProjectEinoAssistantRunState()
	var published []projectAssistantPlanSnapshot
	node := newProjectEinoAssistantWriteTodosNode(t, h, runState, projectAssistantStreamCallbacks{
		OnPlan: func(plan projectAssistantPlanSnapshot) { published = append(published, plan) },
	})
	plans := []string{
		`{"todos":[{"content":"Inspect current app","activeForm":"Inspecting current app","status":"in_progress"},{"content":"Implement the fix","activeForm":"Implementing the fix","status":"pending"},{"content":"Verify behavior","activeForm":"Verifying behavior","status":"pending"}]}`,
		`{"todos":[{"content":"Inspect current app","activeForm":"Inspecting current app","status":"completed"},{"content":"Implement the fix","activeForm":"Implementing the fix","status":"in_progress"},{"content":"Verify behavior","activeForm":"Verifying behavior","status":"pending"}]}`,
		`{"todos":[{"content":"Inspect current app","activeForm":"Inspecting current app","status":"completed"},{"content":"Implement the fix","activeForm":"Implementing the fix","status":"completed"},{"content":"Verify behavior","activeForm":"Verifying behavior","status":"completed"}]}`,
	}
	for i, arguments := range plans {
		if err := invokeProjectEinoAssistantWriteTodos(t, node, fmt.Sprintf("todo-%d", i+1), arguments); err != nil {
			t.Fatalf("plan %d: %v", i+1, err)
		}
	}
	if len(published) != 3 || len(published[0].Steps) != 3 || published[0].Steps[0].Status != "in_progress" || published[1].Steps[0].Status != "completed" || published[2].Steps[2].Status != "completed" {
		t.Fatalf("published plans = %#v", published)
	}
	if got := projectEinoAssistantPlanProgressStatus(runState.PlanProgress()); got != "Building · 3 of 3 steps" {
		t.Fatalf("terminal status = %q", got)
	}
	events := listAssistantRunEventLedgerEvents(t, h.messages, h.scope, h.req.AssistantRun.ID)
	if len(events) != 9 {
		t.Fatalf("durable tool events = %#v, want request/call/result for each update", events)
	}
	for i := 0; i < len(events); i += 3 {
		if events[i].Type != projectAssistantRunToolRequestEventType || events[i+1].Type != projectAssistantRunToolCallEventType || events[i+2].Type != projectAssistantRunToolResultEventType {
			t.Fatalf("event triplet %d = %#v, want request/call/result ordering", i/3, events[i:i+3])
		}
		var payload projectAssistantRunToolResultPayload
		if err := json.Unmarshal(events[i+2].Payload, &payload); err != nil {
			t.Fatalf("decode settled plan %d: %v", i/3, err)
		}
		if payload.PlanSnapshot == nil || !projectAssistantPlanSnapshotValid(*payload.PlanSnapshot) {
			t.Fatalf("settled plan payload %d = %#v, want sanitized snapshot", i/3, payload)
		}
	}
}

func TestProjectEinoAssistantWriteTodosHydratesLatestPlanAfterFreshProcess(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "run-write-todos-hydrate")
	runState := newProjectEinoAssistantRunState()
	node := newProjectEinoAssistantWriteTodosNode(t, h, runState, projectAssistantStreamCallbacks{})
	updates := []string{
		`{"todos":[{"content":"Inspect app","activeForm":"Inspecting app","status":"in_progress"},{"content":"Verify app","activeForm":"Verifying app","status":"pending"}]}`,
		`{"todos":[{"content":"Inspect app","activeForm":"Inspecting app","status":"completed"},{"content":"Verify app","activeForm":"Verifying app","status":"in_progress"}]}`,
	}
	for i, arguments := range updates {
		if err := invokeProjectEinoAssistantWriteTodos(t, node, fmt.Sprintf("hydrate-%d", i+1), arguments); err != nil {
			t.Fatalf("persist plan %d: %v", i+1, err)
		}
	}
	eventsBefore := listAssistantRunEventLedgerEvents(t, h.messages, h.scope, h.req.AssistantRun.ID)
	freshLedger := newProjectAssistantRunEventLedger(h.messages, h.scope, h.req.AssistantRun.ID)
	freshState := newProjectEinoAssistantRunState()
	var hydrated []projectAssistantPlanSnapshot
	if err := projectEinoAssistantHydratePlanProgress(context.Background(), freshLedger, freshState, projectAssistantStreamCallbacks{
		OnPlan: func(plan projectAssistantPlanSnapshot) { hydrated = append(hydrated, plan) },
	}); err != nil {
		t.Fatal(err)
	}
	want := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Inspect app", ActiveForm: "Inspecting app", Status: "completed"},
		{Content: "Verify app", ActiveForm: "Verifying app", Status: "in_progress"},
	}}
	if len(hydrated) != 1 || !reflect.DeepEqual(hydrated[0], want) || !reflect.DeepEqual(freshState.PlanProgress(), want) {
		t.Fatalf("hydrated plans = %#v state = %#v, want latest %#v", hydrated, freshState.PlanProgress(), want)
	}
	if eventsAfter := listAssistantRunEventLedgerEvents(t, h.messages, h.scope, h.req.AssistantRun.ID); len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("hydration redispatched or appended events: before=%d after=%d", len(eventsBefore), len(eventsAfter))
	}
}

func TestProjectAssistantRunEventLedgerLegacyResultsHaveNoPlanSnapshot(t *testing.T) {
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-write-todos-legacy")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-write-todos-legacy")
	spec := projectAssistantToolSpec{Name: projectToolReadFile, Risk: projectAssistantToolRiskRead}
	decision, err := ledger.BeginToolCall(context.Background(), "legacy-read", spec, map[string]any{"path": "README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.FinishToolCall(context.Background(), decision.Token, "contents", nil); err != nil {
		t.Fatal(err)
	}
	fresh := newProjectAssistantRunEventLedger(messageStore, scope, "run-write-todos-legacy")
	if plan, ok, err := fresh.LatestPlanSnapshot(context.Background()); err != nil || ok || len(plan.Steps) != 0 {
		t.Fatalf("legacy latest plan = %#v ok=%v err=%v, want no snapshot", plan, ok, err)
	}
}

func TestProjectAssistantRunEventLedgerRejectsPlanSnapshotForNonWriteOrFailure(t *testing.T) {
	ctx := context.Background()
	plan := &projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{Content: "Inspect app", ActiveForm: "Inspecting app", Status: "in_progress"}}}
	for _, test := range []struct {
		name      string
		spec      projectAssistantToolSpec
		resultErr error
	}{
		{name: "non-write", spec: projectAssistantToolSpec{Name: projectToolReadFile, Risk: projectAssistantToolRiskRead}},
		{name: "failed", spec: projectAssistantToolSpec{Name: projectEinoAssistantWriteTodosTool, Risk: projectAssistantToolRiskPlan}, resultErr: errors.New("rejected")},
	} {
		t.Run(test.name, func(t *testing.T) {
			messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-plan-reject-"+test.name)
			ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-plan-reject-"+test.name)
			decision, err := ledger.BeginToolCall(ctx, "call-plan", test.spec, map[string]any{"value": "test"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.FinishToolCallWithPlan(ctx, decision.Token, "result", test.resultErr, plan); err == nil {
				t.Fatal("accepted invalid plan snapshot settlement")
			}
		})
	}
}

func TestProjectEinoAssistantWriteTodosSuppressesInvalidAndFailedProjection(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "run-write-todos-failures")
	runState := newProjectEinoAssistantRunState()
	var published []projectAssistantPlanSnapshot
	node := newProjectEinoAssistantWriteTodosNode(t, h, runState, projectAssistantStreamCallbacks{
		OnPlan: func(plan projectAssistantPlanSnapshot) { published = append(published, plan) },
	})
	invalid := `{"todos":[{"content":"first","activeForm":"First","status":"in_progress"},{"content":"second","activeForm":"Second","status":"in_progress"}]}`
	if err := invokeProjectEinoAssistantWriteTodos(t, node, "todo-invalid", invalid); err != nil {
		t.Fatalf("invalid plan invocation: %v", err)
	}
	events := listAssistantRunEventLedgerEvents(t, h.messages, h.scope, h.req.AssistantRun.ID)
	if len(events) != 2 {
		t.Fatalf("invalid plan events = %#v, want request/result without admission", events)
	}
	var invalidPayload projectAssistantRunToolResultPayload
	if err := json.Unmarshal(events[1].Payload, &invalidPayload); err != nil {
		t.Fatal(err)
	}
	if !invalidPayload.Failed || invalidPayload.PlanSnapshot != nil || len(published) != 0 {
		t.Fatalf("invalid plan settlement = %#v, published = %#v", invalidPayload, published)
	}

	failingTool := projectEinoAssistantTool{
		server: h.server,
		req:    h.req, runState: runState,
		tool: projectAssistantToolFunc{
			spec: projectAssistantToolSpec{Name: projectEinoAssistantWriteTodosTool, Parameters: projectEinoAssistantWriteTodosParameters, Risk: projectAssistantToolRiskPlan},
			call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
				return "Tool call failed: checklist was rejected", nil
			},
		},
	}
	failingReq := h.req
	failingReq.StreamCallbacks = projectAssistantStreamCallbacks{
		OnPlan: func(plan projectAssistantPlanSnapshot) { published = append(published, plan) },
	}
	failingTool.req = failingReq
	failingNode := newProjectEinoAssistantToolNode(t, failingReq, runState, failingTool)
	if err := invokeProjectEinoAssistantWriteTodos(t, failingNode, "todo-failed", `{"todos":[{"content":"first","activeForm":"First","status":"in_progress"}]}`); err != nil {
		t.Fatalf("failed plan invocation: %v", err)
	}
	events = listAssistantRunEventLedgerEvents(t, h.messages, h.scope, h.req.AssistantRun.ID)
	if len(events) != 5 {
		t.Fatalf("failed plan events = %#v, want a second request/call/result", events)
	}
	var failedPayload projectAssistantRunToolResultPayload
	if err := json.Unmarshal(events[4].Payload, &failedPayload); err != nil {
		t.Fatal(err)
	}
	if failedPayload.Disposition != projectAssistantToolDispositionFailed || failedPayload.PlanSnapshot != nil {
		t.Fatalf("failed plan settlement = %#v", failedPayload)
	}
	if len(published) != 0 {
		t.Fatalf("failed plan published = %#v", published)
	}
}

func TestProjectEinoAssistantWriteTodosReplaysWithoutRedispatchAndRepairsProjection(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "run-write-todos-replay")
	runState := newProjectEinoAssistantRunState()
	req := h.req
	var calls, published int
	var plans []projectAssistantPlanSnapshot
	req.StreamCallbacks.OnPlan = func(plan projectAssistantPlanSnapshot) {
		published++
		plans = append(plans, plan)
	}
	tool := projectEinoAssistantTool{
		server: h.server,
		req:    req, runState: runState,
		tool: projectAssistantToolFunc{
			spec: projectAssistantToolSpec{Name: projectEinoAssistantWriteTodosTool, Parameters: projectEinoAssistantWriteTodosParameters, Risk: projectAssistantToolRiskPlan},
			call: func(_ context.Context, call projectAssistantToolCallRequest) (string, error) {
				calls++
				return fmt.Sprintf("Updated todo list to %v", call.Arguments), nil
			},
		},
	}
	lifecycle := projectEinoAssistantLifecycleMiddleware(req, runState).(*projectEinoAssistantLifecycle)
	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{
		Tools: []einotool.BaseTool{tool}, ExecuteSequentially: true,
		ToolCallMiddlewares: []compose.ToolMiddleware{{Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			wrapped, wrapErr := lifecycle.WrapInvokableToolCall(context.Background(), func(ctx context.Context, arguments string, opts ...einotool.Option) (string, error) {
				output, nextErr := next(ctx, &compose.ToolInput{Name: projectEinoAssistantWriteTodosTool, Arguments: arguments, CallOptions: opts})
				if nextErr != nil {
					return "", nextErr
				}
				return output.Result, nil
			}, &adk.ToolContext{Name: projectEinoAssistantWriteTodosTool})
			if wrapErr != nil {
				t.Fatalf("wrap replay lifecycle: %v", wrapErr)
			}
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				result, callErr := wrapped(ctx, input.Arguments, input.CallOptions...)
				return &compose.ToolOutput{Result: result}, callErr
			}
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	arguments := `{"todos":[{"content":"Implement","activeForm":"Implementing","status":"in_progress"}]}`
	for i := 0; i < 2; i++ {
		if err := invokeProjectEinoAssistantWriteTodos(t, node, "todo-replay", arguments); err != nil {
			t.Fatalf("replay invocation %d: %v", i+1, err)
		}
	}
	if calls != 1 || published != 2 || len(plans) != 2 || !reflect.DeepEqual(plans[0], plans[1]) {
		t.Fatalf("backend calls=%d published=%d plans=%#v, want one dispatch and two equivalent projections", calls, published, plans)
	}
	if events := listAssistantRunEventLedgerEvents(t, h.messages, h.scope, h.req.AssistantRun.ID); len(events) != 3 {
		t.Fatalf("replay appended durable events: %#v", events)
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

func TestProjectEinoAssistantWriteTodosExposesStrictAppOwnedSchema(t *testing.T) {
	tool := newProjectEinoAssistantWriteTodosTool(nil, projectAssistantRunRequest{}, newProjectEinoAssistantRunState())
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != projectEinoAssistantWriteTodosTool {
		t.Fatalf("tool name = %q", info.Name)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(info.Extra[projectEinoToolParametersExtraKey].(string)), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("top-level schema = %#v, want strict properties", schema)
	}
	properties := schema["properties"].(map[string]any)
	todos := properties["todos"].(map[string]any)
	item := todos["items"].(map[string]any)
	if item["additionalProperties"] != false {
		t.Fatalf("todo item schema = %#v, want strict properties", item)
	}
	status := item["properties"].(map[string]any)["status"].(map[string]any)
	if got := status["enum"].([]any); len(got) != 3 || got[0] != "pending" || got[1] != "in_progress" || got[2] != "completed" {
		t.Fatalf("status schema = %#v", status)
	}
}

func TestProjectEinoAssistantWriteTodosIsVisibleOnlyInDefaultMode(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "run-write-todos-modes")
	for _, mode := range []projectAssistantCollaborationMode{
		projectAssistantCollaborationModeDefault,
		projectAssistantCollaborationModePlan,
		projectAssistantCollaborationModeReview,
	} {
		req := h.req
		req.CollaborationMode = mode
		tools, err := projectEinoAssistantToolsForDiscovery(
			context.Background(),
			h.server,
			req,
			newProjectEinoAssistantRunState(),
			projectEinoAssistantToolDiscovery{},
		)
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		count := 0
		for _, tool := range tools {
			info, infoErr := tool.Info(context.Background())
			if infoErr != nil {
				t.Fatal(infoErr)
			}
			if info.Name == projectEinoAssistantWriteTodosTool {
				count++
			}
		}
		want := 0
		if mode == projectAssistantCollaborationModeDefault {
			want = 1
		}
		if count != want {
			t.Fatalf("mode %s write_todos count = %d, want %d", mode, count, want)
		}
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
