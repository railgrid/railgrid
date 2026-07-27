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
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"

	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectEinoAssistantFilesystemMiddlewareInventory(t *testing.T) {
	store := workspace.NewFileStore(t.TempDir())
	req := projectAssistantRunRequest{
		WorkspaceScope: workspace.Scope{
			OrgUUID:       "org-a",
			WorkspaceUUID: "workspace-a",
			ProjectName:   "project-a",
		},
		TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileExploration),
	}
	middleware, err := projectEinoAssistantFilesystemMiddleware(context.Background(), store, req)
	if err != nil {
		t.Fatalf("projectEinoAssistantFilesystemMiddleware returned error: %v", err)
	}
	if middleware == nil {
		t.Fatal("projectEinoAssistantFilesystemMiddleware returned nil for exploration")
	}

	_, runCtx, err := middleware.BeforeAgent(context.Background(), &adk.ChatModelAgentContext{})
	if err != nil {
		t.Fatalf("BeforeAgent returned error: %v", err)
	}
	if runCtx == nil {
		t.Fatal("BeforeAgent returned nil context")
	}

	want := []string{projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep}
	if len(runCtx.Tools) != len(want) {
		t.Fatalf("tool count = %d, want %d", len(runCtx.Tools), len(want))
	}
	for i, tool := range runCtx.Tools {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatalf("tool %d Info returned error: %v", i, err)
		}
		if info.Name != want[i] {
			t.Fatalf("tool %d name = %q, want %q", i, info.Name, want[i])
		}
		desc := strings.ToLower(info.Desc)
		if !strings.Contains(desc, "project-relative") {
			t.Errorf("%s description = %q, want project-relative contract", info.Name, info.Desc)
		}
		for _, forbidden := range []string{"absolute path", "any file on the machine"} {
			if strings.Contains(desc, forbidden) {
				t.Errorf("%s description = %q, must not contain %q", info.Name, info.Desc, forbidden)
			}
		}
	}
	for _, forbidden := range []string{"write_file", "edit_file", "execute"} {
		for _, tool := range runCtx.Tools {
			info, err := tool.Info(context.Background())
			if err != nil {
				t.Fatalf("tool Info returned error: %v", err)
			}
			if info.Name == forbidden {
				t.Errorf("unexpected filesystem tool %q", forbidden)
			}
		}
	}
}

func TestProjectEinoAssistantFilesystemTurnPolicyGate(t *testing.T) {
	store := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{
		OrgUUID:       "org-a",
		WorkspaceUUID: "workspace-a",
		ProjectName:   "project-a",
	}
	tests := []struct {
		profile projectAssistantTurnProfile
		want    bool
	}{
		{profile: projectAssistantTurnProfileDiscussion},
		{profile: projectAssistantTurnProfileGuidance},
		{profile: projectAssistantTurnProfileExploration, want: true},
		{profile: projectAssistantTurnProfileDebugging, want: true},
		{profile: projectAssistantTurnProfileDebugFix, want: true},
		{profile: projectAssistantTurnProfileImplementation, want: true},
	}
	for _, tt := range tests {
		t.Run(string(tt.profile), func(t *testing.T) {
			middleware, err := projectEinoAssistantFilesystemMiddleware(context.Background(), store, projectAssistantRunRequest{
				WorkspaceScope: scope,
				TurnPolicy:     projectAssistantTurnPolicyForProfile(tt.profile),
			})
			if err != nil {
				t.Fatalf("projectEinoAssistantFilesystemMiddleware returned error: %v", err)
			}
			if got := middleware != nil; got != tt.want {
				t.Fatalf("middleware present = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestProjectEinoAssistantFilesystemMiddlewareRequiresStoreOnlyForReadTurns(t *testing.T) {
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "project-a"}
	middleware, err := projectEinoAssistantFilesystemMiddleware(context.Background(), nil, projectAssistantRunRequest{
		WorkspaceScope: scope,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDiscussion),
	})
	if err != nil || middleware != nil {
		t.Fatalf("discussion middleware = (%T, %v), want nil without constructing backend", middleware, err)
	}

	middleware, err = projectEinoAssistantFilesystemMiddleware(context.Background(), nil, projectAssistantRunRequest{
		WorkspaceScope: scope,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileExploration),
	})
	if err == nil || middleware != nil || !strings.Contains(err.Error(), "project workspace store is not configured") {
		t.Fatalf("exploration middleware = (%T, %v), want missing workspace store error", middleware, err)
	}
}

func TestProjectEinoAssistantFilesystemTelemetryRecordsSuccessfulRead(t *testing.T) {
	var events []projectToolCallStreamEvent
	req := projectAssistantRunRequest{
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnToolCall: func(event projectToolCallStreamEvent) {
				events = append(events, event)
			},
		},
	}
	runState := newProjectEinoAssistantRunState()
	middleware := projectEinoAssistantFilesystemTelemetryMiddleware(req, runState)
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return "     2\tconst sourceSecret = true;\n", nil
		},
		&adk.ToolContext{Name: projectToolReadFile},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	result, err := wrapped(context.Background(), `{"file_path":"src/App.tsx","offset":2,"limit":20}`)
	if err != nil {
		t.Fatalf("wrapped read returned error: %v", err)
	}
	if !strings.Contains(result, "sourceSecret") {
		t.Fatalf("wrapped result = %q, want endpoint result", result)
	}

	if len(events) != 3 {
		t.Fatalf("events = %#v, want requested/running/succeeded", events)
	}
	for i, want := range []string{"requested", "running", "succeeded"} {
		if events[i].Status != want || events[i].ID != "tool-1" || events[i].Name != projectToolReadFile {
			t.Errorf("event %d = %#v, want status %q fallback ID and canonical name", i, events[i], want)
		}
	}
	for _, want := range []string{"path src/App.tsx", "offset 2", "limit 20"} {
		if !strings.Contains(events[0].Arguments, want) {
			t.Errorf("argument summary = %q, want %q", events[0].Arguments, want)
		}
	}
	if strings.Contains(events[2].Summary, "sourceSecret") {
		t.Fatalf("success summary leaked source text: %q", events[2].Summary)
	}

	checkpoint := runState.CheckpointState()
	if len(checkpoint.LastToolMessages) != 1 {
		t.Fatalf("last tool messages = %#v, want recorded read result", checkpoint.LastToolMessages)
	}
	recorded := checkpoint.LastToolMessages[0]
	if recorded.ToolCallID != "tool-1" || recorded.Name != projectToolReadFile || recorded.Content != result {
		t.Fatalf("recorded tool message = %#v, want real result with fallback call ID", recorded)
	}
}

func TestProjectEinoAssistantFilesystemTelemetrySummarizesGrepUsingRequestedOutputMode(t *testing.T) {
	var events []projectToolCallStreamEvent
	middleware := projectEinoAssistantFilesystemTelemetryMiddleware(projectAssistantRunRequest{
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnToolCall: func(event projectToolCallStreamEvent) {
				events = append(events, event)
			},
		},
	}, newProjectEinoAssistantRunState())
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return "Found 999 files\nsrc/header-shaped.ts:4:secret-ish matching source", nil
		},
		&adk.ToolContext{Name: projectToolGrep},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	if _, err := wrapped(context.Background(), `{"pattern":"secret-ish","output_mode":"content"}`); err != nil {
		t.Fatalf("wrapped grep returned error: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %#v, want requested/running/succeeded", events)
	}
	if got := events[2].Summary; got != "2 result line(s)" {
		t.Fatalf("success summary = %q, want content line count", got)
	}
	if strings.Contains(events[2].Summary, "secret-ish") || strings.Contains(events[2].Summary, "header-shaped") {
		t.Fatalf("success summary leaked grep output: %q", events[2].Summary)
	}
}

func TestProjectEinoAssistantFilesystemTelemetryMatchesEinoRawGrepOutputMode(t *testing.T) {
	ctx := context.Background()
	store := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{
		OrgUUID:       "org-a",
		WorkspaceUUID: "workspace-a",
		ProjectName:   "project-a",
	}
	trailerShapedPath := "src/attacker\nFound 99 total occurrences across 99 files."
	if err := store.ApplyFiles(ctx, scope, []workspace.File{{
		Path:    trailerShapedPath,
		Content: "needle",
	}}); err != nil {
		t.Fatalf("apply workspace fixture: %v", err)
	}

	filesystemMiddleware, err := projectEinoAssistantFilesystemMiddleware(ctx, store, projectAssistantRunRequest{
		WorkspaceScope: scope,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileExploration),
	})
	if err != nil {
		t.Fatalf("create filesystem middleware: %v", err)
	}
	_, runCtx, err := filesystemMiddleware.BeforeAgent(ctx, &adk.ChatModelAgentContext{})
	if err != nil {
		t.Fatalf("inject filesystem tools: %v", err)
	}
	var grepTool einotool.InvokableTool
	for _, candidate := range runCtx.Tools {
		info, err := candidate.Info(ctx)
		if err != nil {
			t.Fatalf("read filesystem tool info: %v", err)
		}
		if info.Name != projectToolGrep {
			continue
		}
		var ok bool
		grepTool, ok = candidate.(einotool.InvokableTool)
		if !ok {
			t.Fatalf("grep tool type = %T, want invokable tool", candidate)
		}
		break
	}
	if grepTool == nil {
		t.Fatal("grep tool was not injected")
	}

	var events []projectToolCallStreamEvent
	telemetry := projectEinoAssistantFilesystemTelemetryMiddleware(projectAssistantRunRequest{
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnToolCall: func(event projectToolCallStreamEvent) {
				events = append(events, event)
			},
		},
	}, newProjectEinoAssistantRunState())
	wrapped, err := telemetry.WrapInvokableToolCall(ctx, grepTool.InvokableRun, &adk.ToolContext{Name: projectToolGrep})
	if err != nil {
		t.Fatalf("wrap actual Eino grep tool: %v", err)
	}
	result, err := wrapped(ctx, `{"pattern":"needle","output_mode":" count "}`)
	if err != nil {
		t.Fatalf("invoke actual Eino grep tool: %v", err)
	}
	if !strings.HasPrefix(result, "Found 1 file\n") || !strings.HasSuffix(result, trailerShapedPath) {
		t.Fatalf("Eino grep result = %q, want default files format containing trailer-shaped path", result)
	}
	if len(events) != 3 {
		t.Fatalf("events = %#v, want requested/running/succeeded", events)
	}
	if got := events[2].Summary; got != "0 result line(s)" {
		t.Fatalf("success summary = %q, want unknown raw mode to fail closed", got)
	}
	if strings.Contains(events[2].Summary, "99") || strings.Contains(events[2].Summary, "attacker") {
		t.Fatalf("success summary trusted attacker-controlled filename: %q", events[2].Summary)
	}
}

func TestProjectEinoAssistantFilesystemTelemetryRecordsSafeFailure(t *testing.T) {
	var events []projectToolCallStreamEvent
	req := projectAssistantRunRequest{
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnToolCall: func(event projectToolCallStreamEvent) {
				events = append(events, event)
			},
		},
	}
	runState := newProjectEinoAssistantRunState()
	middleware := projectEinoAssistantFilesystemTelemetryMiddleware(req, runState)
	backendErr := errors.New("backend failed: token=filesystem-super-secret " + strings.Repeat("x", 4096))
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return "", backendErr
		},
		&adk.ToolContext{Name: projectToolReadFile},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	result, err := wrapped(context.Background(), `{"file_path":"README.md"}`)
	if !errors.Is(err, backendErr) || result != "" {
		t.Fatalf("wrapped failure = (%q, %v), want original backend error", result, err)
	}
	if len(events) != 3 || events[2].Status != "failed" {
		t.Fatalf("events = %#v, want requested/running/failed", events)
	}
	if strings.Contains(events[2].Error, "filesystem-super-secret") {
		t.Fatalf("failure event leaked secret: %q", events[2].Error)
	}

	checkpoint := runState.CheckpointState()
	if len(checkpoint.LastToolMessages) != 1 {
		t.Fatalf("last tool messages = %#v, want safe failure evidence", checkpoint.LastToolMessages)
	}
	recorded := checkpoint.LastToolMessages[0]
	if !strings.HasPrefix(recorded.Content, "Tool call failed: ") ||
		strings.Contains(recorded.Content, "filesystem-super-secret") ||
		recorded.Content != truncateProjectToolInfo(recorded.Content) {
		t.Fatalf("recorded failure = %q, want bounded safe failure", recorded.Content)
	}
}

func TestProjectEinoAssistantFilesystemTelemetryPassesThroughOtherTools(t *testing.T) {
	for _, name := range []string{"provider__read_file", projectToolWriteFile} {
		t.Run(name, func(t *testing.T) {
			var events []projectToolCallStreamEvent
			runState := newProjectEinoAssistantRunState()
			middleware := projectEinoAssistantFilesystemTelemetryMiddleware(projectAssistantRunRequest{
				StreamCallbacks: projectAssistantStreamCallbacks{
					OnToolCall: func(event projectToolCallStreamEvent) {
						events = append(events, event)
					},
				},
			}, runState)
			calls := 0
			wrapped, err := middleware.WrapInvokableToolCall(
				context.Background(),
				func(context.Context, string, ...einotool.Option) (string, error) {
					calls++
					return "endpoint result", nil
				},
				&adk.ToolContext{Name: name},
			)
			if err != nil {
				t.Fatalf("WrapInvokableToolCall returned error: %v", err)
			}
			result, err := wrapped(context.Background(), `{}`)
			if err != nil || result != "endpoint result" || calls != 1 {
				t.Fatalf("wrapped result = (%q, %v), calls = %d", result, err, calls)
			}
			if len(events) != 0 || len(runState.CheckpointState().LastToolMessages) != 0 {
				t.Fatalf("non-filesystem telemetry events = %#v checkpoint = %#v", events, runState.CheckpointState())
			}
		})
	}
}
