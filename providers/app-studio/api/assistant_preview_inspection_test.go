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
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

type fakeProjectAssistantPreviewInspector struct {
	healthErr error
	result    projectAssistantPreviewInspectionResult
	err       error
	request   projectAssistantPreviewInspectionRequest
	calls     int
}

func (f *fakeProjectAssistantPreviewInspector) Health(context.Context) error { return f.healthErr }

func (f *fakeProjectAssistantPreviewInspector) Inspect(_ context.Context, request projectAssistantPreviewInspectionRequest) (projectAssistantPreviewInspectionResult, error) {
	f.calls++
	f.request = request
	return f.result, f.err
}

func TestProjectAssistantPreviewInspectionTargetURLConfinesOrigin(t *testing.T) {
	got, err := projectAssistantPreviewInspectionTargetURL("https://demo.preview.example/base", "/tasks?state=open#ignored")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://demo.preview.example/tasks?state=open"; got != want {
		t.Fatalf("target URL = %q, want %q", got, want)
	}
	for _, path := range []string{"https://attacker.example/", "//attacker.example/", "relative"} {
		if _, err := projectAssistantPreviewInspectionTargetURL("https://demo.preview.example/", path); err == nil {
			t.Fatalf("path %q was accepted", path)
		}
	}
}

func TestProjectAssistantPreviewInspectionAssertionsNormalizeRoleTextAlias(t *testing.T) {
	assertions, err := projectAssistantPreviewInspectionAssertions([]any{map[string]any{
		"kind": "role_present",
		"role": "button",
		"text": "Add Habit",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(assertions) != 1 || assertions[0].Name != "Add Habit" || assertions[0].Text != "" {
		t.Fatalf("assertions = %#v, want text normalized to accessible name", assertions)
	}
}

func TestProjectAssistantPreviewInspectionAssertionsRejectInvalidShapes(t *testing.T) {
	for _, value := range []any{
		[]any{map[string]any{"kind": "role_present", "role": "button", "unknown": true}},
		[]any{map[string]any{"kind": "text_present", "text": "Ready", "role": "button"}},
		[]any{map[string]any{"kind": "role_present", "role": "button", "min": 1}},
		[]any{map[string]any{"kind": "role_count", "role": "row", "min": 2, "max": 1}},
	} {
		if _, err := projectAssistantPreviewInspectionAssertions(value); err == nil {
			t.Fatalf("invalid assertions were accepted: %#v", value)
		}
	}
}

func TestInspectProjectDevelopmentPreviewReturnsTypedFailure(t *testing.T) {
	inspector := &fakeProjectAssistantPreviewInspector{result: projectAssistantPreviewInspectionResult{
		Status:      "failed",
		FailureKind: "assertion",
		Summary:     "requested text was not rendered",
		Screenshot: &projectAssistantPreviewInspectionScreenshot{
			MIMEType: "image/png",
			Base64:   "aGVsbG8=",
			Width:    1280,
			Height:   720,
			SHA256:   "digest",
		},
	}}
	server := &Server{
		previewInspector: inspector,
		previewInspectionResolveURL: func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
			return "https://demo.preview.example/", nil
		},
	}
	raw, err := server.inspectProjectDevelopmentPreview(context.Background(), projectAssistantToolCallRequest{
		Project: &aiv1alpha1.Project{},
		Arguments: map[string]any{
			"path": "/tasks",
			"assertions": []any{map[string]any{
				"kind": "text_present",
				"text": "Tasks",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result projectAssistantPreviewInspectionResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || result.FailureKind != "assertion" {
		t.Fatalf("result = %#v", result)
	}
	if result.EvidenceScope != "rendered_state_only" || result.InteractionEvidence || len(result.Limitations) != 1 || !strings.Contains(result.Limitations[0], "did not click, type, press keys") {
		t.Fatalf("inspection evidence contract = %#v", result)
	}
	if result.Screenshot == nil || result.Screenshot.Base64 != "" || result.Screenshot.Bytes == 0 {
		t.Fatalf("persistable screenshot metadata = %#v", result.Screenshot)
	}
	if inspector.request.URL != "https://demo.preview.example/tasks" || inspector.request.IncludeScreenshot {
		t.Fatalf("worker request = %#v", inspector.request)
	}
	if got := projectAssistantToolResultDisposition(projectToolInspectDevelopmentPreview, raw, nil); got != projectAssistantToolDispositionFailed {
		t.Fatalf("disposition = %q, want failed", got)
	}
}

func TestInspectProjectDevelopmentPreviewRejectsUnsynchronizedMutation(t *testing.T) {
	inspector := &fakeProjectAssistantPreviewInspector{result: projectAssistantPreviewInspectionResult{Status: "succeeded"}}
	runState := &projectEinoAssistantRunState{}
	runState.RecordSourceMutation()
	server := &Server{
		previewInspector: inspector,
		previewInspectionResolveURL: func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
			return "https://demo.preview.example/", nil
		},
	}
	raw, err := server.inspectProjectDevelopmentPreview(context.Background(), projectAssistantToolCallRequest{
		Project:  &aiv1alpha1.Project{},
		RunState: runState,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result projectAssistantPreviewInspectionResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || result.FailureKind != "not_current" || inspector.calls != 0 {
		t.Fatalf("result = %#v; worker calls = %d", result, inspector.calls)
	}
}

func TestProjectAssistantPreviewInspectionCapabilityFollowsHealth(t *testing.T) {
	inspector := &fakeProjectAssistantPreviewInspector{}
	server := &Server{previewInspector: inspector}
	if !server.projectAssistantPreviewInspectionAvailable(context.Background()) {
		t.Fatal("healthy inspector capability was hidden")
	}
	discovery := projectEinoAssistantDiscoverTools(context.Background(), server, projectAssistantRunRequest{
		TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging),
	})
	if !discovery.IncludePreviewInspection || !strings.Contains(discovery.Prompt, "inspect_development_preview") {
		t.Fatalf("healthy discovery = %#v", discovery)
	}
	inspector.healthErr = errors.New("down")
	discovery = projectEinoAssistantDiscoverTools(context.Background(), server, projectAssistantRunRequest{
		TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging),
	})
	if discovery.IncludePreviewInspection || strings.Contains(discovery.Prompt, "inspect_development_preview") {
		t.Fatalf("unhealthy discovery = %#v", discovery)
	}
}

func TestProjectAssistantEnhancedPreviewInspectionReturnsImageWithoutPersistingBytesInText(t *testing.T) {
	inspector := &fakeProjectAssistantPreviewInspector{result: projectAssistantPreviewInspectionResult{
		Status:   "succeeded",
		Summary:  "Preview rendered.",
		Snapshot: "heading: Tasks",
		Screenshot: &projectAssistantPreviewInspectionScreenshot{
			MIMEType: "image/png",
			Base64:   "aGVsbG8=",
			Width:    1280,
			Height:   720,
			SHA256:   "digest",
		},
	}}
	server := &Server{
		previewInspector: inspector,
		previewInspectionResolveURL: func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
			return "https://demo.preview.example/", nil
		},
	}
	registered, ok := server.projectAssistantToolRegistry().Get(projectToolInspectDevelopmentPreview)
	if !ok {
		t.Fatal("preview inspection tool is not registered")
	}
	base := newProjectEinoAssistantEnhancedPreviewTool(server, registered, projectAssistantRunRequest{
		Project: &aiv1alpha1.Project{},
	}, &projectEinoAssistantRunState{})
	enhanced, ok := base.(einotool.EnhancedInvokableTool)
	if !ok {
		t.Fatalf("tool type = %T, want EnhancedInvokableTool", base)
	}
	result, err := enhanced.InvokableRun(context.Background(), &schema.ToolArgument{Text: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Parts) != 1 || result.Parts[0].Type != schema.ToolPartTypeText {
		t.Fatalf("tool result = %#v", result)
	}
	if strings.Contains(result.Parts[0].Text, "aGVsbG8=") {
		t.Fatal("screenshot bytes leaked into the durable text result")
	}
	messageParts, err := result.ToMessageInputParts()
	if err != nil {
		t.Fatal(err)
	}
	if len(messageParts) != 1 {
		t.Fatalf("durable tool message parts = %#v", messageParts)
	}
	toolMessage := schema.ToolMessage(result.Parts[0].Text, "preview-call")
	toolMessage.ToolName = projectToolInspectDevelopmentPreview
	persisted, err := json.Marshal(toolMessage)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "aGVsbG8=") {
		t.Fatal("screenshot bytes entered durable tool history")
	}
	expanded := base.(*projectEinoAssistantEnhancedPreviewTool).runState.ExpandTransientToolMessages([]*schema.Message{toolMessage})
	if len(expanded) != 1 || len(expanded[0].UserInputMultiContent) != 2 {
		t.Fatalf("transient model message = %#v", expanded)
	}
	image := expanded[0].UserInputMultiContent[1].Image
	if image == nil || image.Base64Data == nil || *image.Base64Data != "aGVsbG8=" {
		t.Fatalf("transient image = %#v", image)
	}
	checkpoint, err := json.Marshal(base.(*projectEinoAssistantEnhancedPreviewTool).runState.CheckpointState())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(checkpoint), "aGVsbG8=") {
		t.Fatal("screenshot bytes entered App Studio checkpoint state")
	}
}

func TestProjectAssistantModelCapabilitiesFailClosed(t *testing.T) {
	if projectAssistantCapabilitiesForModel(projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "unknown-model"}).VisionToolResults {
		t.Fatal("unknown model was granted image tool results")
	}
	if !projectAssistantCapabilitiesForModel(projectLLMSettings{Provider: defaultProjectLLMProvider, Model: defaultProjectLLMModel}).VisionToolResults {
		t.Fatal("default model lacks its cataloged image tool capability")
	}
}

func TestProjectAssistantLifecycleAccountsForEnhancedPreviewInspection(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.NextModelCallOrdinal()
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{}, runState).(*projectEinoAssistantLifecycle)
	endpoint, err := lifecycle.WrapEnhancedInvokableToolCall(context.Background(), func(context.Context, *schema.ToolArgument, ...einotool.Option) (*schema.ToolResult, error) {
		return projectAssistantPreviewInspectionToolResult(`{"status":"failed","failureKind":"assertion"}`, "", ""), nil
	}, &adk.ToolContext{Name: projectToolInspectDevelopmentPreview})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint(context.Background(), &schema.ToolArgument{Text: `{}`}); err != nil {
		t.Fatal(err)
	}
	if name, count := runState.RepeatedCompletedAction(); name != projectToolInspectDevelopmentPreview || count != 1 {
		t.Fatalf("completed enhanced action = %q x%d", name, count)
	}
}
