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
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantRunAuditIsBoundedAndSanitized(t *testing.T) {
	started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	run := &store.AssistantRun{ID: "run-1"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{
		LLM: projectLLMSettings{
			Provider: "google-ai-studio",
			Model:    "google/gemini-3.5-flash",
			APIKey:   "secret-api-key",
			BaseURL:  "https://secret.example.test",
		},
		TurnProfile: projectAssistantTurnProfileImplementation,
	}, run, started)

	recorder.recordPhaseAt(projectEinoAssistantPhaseApproval, started)
	recorder.recordPhaseAt(projectEinoAssistantPhaseApproval, started.Add(time.Millisecond))
	for i := 0; i < projectAssistantAuditMaxPhases+10; i++ {
		phase := projectEinoAssistantPhaseMutate
		if i%2 == 1 {
			phase = projectEinoAssistantPhaseVerify
		}
		recorder.recordPhaseAt(phase, started.Add(time.Duration(i+1)*time.Millisecond))
	}
	recorder.recordToolAt(projectToolCallStreamEvent{
		ID:        "call-write",
		Name:      projectToolWriteFile,
		Status:    "requested",
		Arguments: "path src/App.tsx; 123 bytes",
		Summary:   "source=do-not-store",
	}, started.Add(time.Second))
	recorder.recordToolAt(projectToolCallStreamEvent{
		ID:        "call-write",
		Name:      projectToolWriteFile,
		Status:    "succeeded",
		Arguments: "path src/App.tsx; 123 bytes",
		Summary:   "source=do-not-store",
	}, started.Add(2*time.Second))
	recorder.recordToolAt(projectToolCallStreamEvent{
		ID:        "call-search",
		Name:      projectToolGrep,
		Status:    "succeeded",
		Arguments: "pattern secret-search-term; path src; glob **/*.go; output_mode content; head_limit 20",
		Summary:   "1 result line(s)",
	}, started.Add(3*time.Second))
	recorder.recordToolAt(projectToolCallStreamEvent{
		ID:        "call-env",
		Name:      projectToolSetRuntimeEnv,
		Status:    "succeeded",
		Arguments: "2 variable(s): API_TOKEN, PASSWORD",
		Summary:   "secret-env-result",
	}, started.Add(4*time.Second))
	for i := 0; i < projectAssistantAuditMaxTools+20; i++ {
		recorder.recordToolAt(projectToolCallStreamEvent{
			ID:        "call-extra-" + strconv.Itoa(i),
			Name:      "unknown__tool",
			Status:    "succeeded",
			Arguments: `{"content":"do-not-store","apiKey":"secret-api-key"}`,
			Summary:   "secret-result",
		}, started.Add(time.Duration(i+5)*time.Second))
	}
	recorder.finalizeAt(projectAssistantAuditOutcomeSucceeded, started.Add(10*time.Minute))

	raw := string(run.Audit)
	for _, secret := range []string{
		"secret-api-key",
		"secret.example.test",
		"do-not-store",
		"secret-search-term",
		"secret-result",
		"API_TOKEN",
		"PASSWORD",
	} {
		if strings.Contains(raw, secret) {
			t.Fatalf("audit contains sensitive value %q: %s", secret, raw)
		}
	}

	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if audit.Version != projectAssistantAuditVersion || audit.Provider != "google-ai-studio" || audit.Model != "google/gemini-3.5-flash" {
		t.Fatalf("audit identity = %#v", audit)
	}
	if audit.Outcome != projectAssistantAuditOutcomeSucceeded || audit.DurationMS != 600000 {
		t.Fatalf("audit completion = %#v", audit)
	}
	if len(audit.PhaseTransitions) > projectAssistantAuditMaxPhases {
		t.Fatalf("phase transitions = %d, want <= %d", len(audit.PhaseTransitions), projectAssistantAuditMaxPhases)
	}
	if len(audit.Tools) > projectAssistantAuditMaxTools {
		t.Fatalf("tools = %d, want <= %d", len(audit.Tools), projectAssistantAuditMaxTools)
	}
	var writeEntries int
	for _, entry := range audit.Tools {
		if entry.ID == "call-write" {
			writeEntries++
			if entry.Path != "src/App.tsx" || entry.Status != "succeeded" {
				t.Fatalf("write audit entry = %#v", entry)
			}
		}
	}
	if writeEntries != 1 {
		t.Fatalf("write audit entries = %d, want one upserted entry", writeEntries)
	}
}

func TestProjectAssistantRunAuditRecordsAdaptiveRoutingAndPromotion(t *testing.T) {
	run := &store.AssistantRun{ID: "run-adaptive"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{
		TurnProfile:              projectAssistantTurnProfileAdaptive,
		RequestedAction:          string(projectAssistantActionAuto),
		ResolvedAction:           string(projectAssistantTurnProfileAdaptive),
		ClassificationReason:     "adaptive_auto_policy",
		ClassificationConfidence: projectAssistantTurnConfidenceMedium,
	}, run, time.Now().UTC())
	recorder.recordPromotion("work-item-1")

	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.RequestedAction != "auto" ||
		audit.ResolvedAction != "build" ||
		audit.ClassificationReason != "adaptive_auto_policy" ||
		audit.ClassificationConfidence != projectAssistantTurnConfidenceMedium ||
		audit.ResolutionReason != "plan_approval_requested" ||
		audit.PromotedWorkItemID != "work-item-1" {
		t.Fatalf("adaptive routing audit = %#v", audit)
	}
}

func TestProjectAssistantRunAuditRecordsModelCallShapeWithoutPayloads(t *testing.T) {
	started := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	run := &store.AssistantRun{ID: "run-model-call"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, started)
	recorder.recordModelCall(
		context.Background(),
		projectEinoAssistantPhaseMutate,
		2,
		7,
		5,
		true,
		[]*schema.ToolInfo{
			{Name: projectToolWriteFile},
			{Name: projectEinoAssistantWriteTodosTool},
		},
		[]*schema.ToolInfo{{Name: projectToolApplyPatch}},
	)
	recorder.recordModelResult(
		context.Background(),
		projectEinoAssistantPhaseMutate,
		2,
		schema.AssistantMessage("top-secret-model-content", []schema.ToolCall{{
			ID:   "call-todos",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectEinoAssistantWriteTodosTool,
				Arguments: `{"todos":[{"content":"top-secret-todo-content","status":"in_progress"}]}`,
			},
		}}),
	)

	raw := string(run.Audit)
	for _, forbidden := range []string{
		"top-secret-model-content",
		"top-secret-todo-content",
		`"arguments"`,
		`"todos"`,
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("model-call audit leaked %q: %s", forbidden, raw)
		}
	}

	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.ModelCalls) != 1 {
		t.Fatalf("model calls = %#v, want one entry", audit.ModelCalls)
	}
	call := audit.ModelCalls[0]
	if call.Phase != projectEinoAssistantPhaseMutate ||
		call.Ordinal != 2 ||
		call.SourceRevision != 7 ||
		call.VerifiedRevision != 5 ||
		!call.WarningInjected ||
		call.Outcome != "tool_calls" {
		t.Fatalf("model call = %#v", call)
	}
	wantVisible := []string{projectToolWriteFile, projectEinoAssistantWriteTodosTool, projectToolApplyPatch}
	if strings.Join(call.VisibleTools, ",") != strings.Join(wantVisible, ",") {
		t.Fatalf("visible tools = %#v, want %#v", call.VisibleTools, wantVisible)
	}
	if len(call.RequestedTools) != 1 || call.RequestedTools[0] != projectEinoAssistantWriteTodosTool {
		t.Fatalf("requested tools = %#v, want write_todos", call.RequestedTools)
	}
}

func TestProjectAssistantRunAuditRecordsDuplicateReadAsSkipped(t *testing.T) {
	run := &store.AssistantRun{ID: "run-skipped-read"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	recorder.recordTool(projectToolCallStreamEvent{
		ID:        "call-read",
		Name:      projectToolReadFile,
		Status:    "skipped",
		Arguments: "path src/App.tsx",
		Summary:   "untrusted summary must not persist",
	})
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit.Tools) != 1 ||
		audit.Tools[0].Status != "skipped" ||
		audit.Tools[0].Summary != "Skipped an unchanged duplicate read." {
		t.Fatalf("skipped read audit = %#v", audit.Tools)
	}
	if strings.Contains(string(run.Audit), "untrusted summary") {
		t.Fatalf("skipped read audit persisted caller summary: %s", run.Audit)
	}
}

func TestProjectAssistantRunAuditPersistsBoundedModelMilestones(t *testing.T) {
	run := &store.AssistantRun{ID: "run-model-milestones"}
	recorder := newProjectAssistantRunAuditRecorder(
		projectAssistantRunRequest{},
		run,
		time.Now().UTC().Add(-time.Second),
	)
	var snapshots [][]byte
	recorder.setPersister(func(_ context.Context, audit []byte) error {
		snapshots = append(snapshots, append([]byte(nil), audit...))
		return nil
	})

	if err := recorder.recordModelCall(
		context.Background(),
		projectEinoAssistantPhasePlan,
		1,
		0,
		0,
		false,
		[]*schema.ToolInfo{{Name: projectToolDefineInitialProjectPlan}},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	recorder.recordModelResponseChunk(context.Background(), true)
	recorder.recordModelResponseChunk(context.Background(), true)
	if err := recorder.recordModelResult(
		context.Background(),
		projectEinoAssistantPhasePlan,
		1,
		schema.AssistantMessage("", []schema.ToolCall{{
			Function: schema.FunctionCall{Name: projectToolDefineInitialProjectPlan},
		}}),
	); err != nil {
		t.Fatal(err)
	}

	if len(snapshots) != 3 {
		t.Fatalf("persisted snapshots = %d, want start, first/tool chunk, and result", len(snapshots))
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(snapshots[len(snapshots)-1], &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit.ModelCalls) != 1 {
		t.Fatalf("model calls = %#v", audit.ModelCalls)
	}
	call := audit.ModelCalls[0]
	if call.FirstResponseAtOffsetMS == nil ||
		call.ToolCallStartedAtOffsetMS == nil ||
		call.CompletedAtOffsetMS == nil ||
		call.Outcome != "tool_calls" {
		t.Fatalf("model milestones = %#v", call)
	}
}

func TestProjectAssistantRunAuditTransportErrorDoesNotPreemptRetryResult(t *testing.T) {
	run := &store.AssistantRun{ID: "run-model-retry"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	if err := recorder.recordModelCall(
		context.Background(),
		projectEinoAssistantPhaseApproval,
		1,
		0,
		0,
		false,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	recorder.recordModelTransportError(context.Background(), errors.New("temporary provider failure"))
	if err := recorder.recordModelResult(
		context.Background(),
		projectEinoAssistantPhaseApproval,
		1,
		schema.AssistantMessage("recovered", nil),
	); err != nil {
		t.Fatal(err)
	}

	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	call := audit.ModelCalls[0]
	if !call.TransportErrorObserved || call.Outcome != "text" || call.CompletedAtOffsetMS == nil {
		t.Fatalf("retry model call = %#v", call)
	}
	if strings.Contains(string(run.Audit), "temporary provider failure") {
		t.Fatalf("model audit leaked provider error: %s", run.Audit)
	}
}

func TestProjectAssistantRunAuditKeepsFailureAdjacentModelCalls(t *testing.T) {
	run := &store.AssistantRun{ID: "run-model-call-window"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	total := projectAssistantAuditMaxModelCalls + 3
	for ordinal := 1; ordinal <= total; ordinal++ {
		recorder.recordModelCall(context.Background(), projectEinoAssistantPhaseMutate, ordinal, 0, 0, false, nil, nil)
		recorder.recordModelResult(
			context.Background(),
			projectEinoAssistantPhaseMutate,
			ordinal,
			schema.AssistantMessage("safe", nil),
		)
	}

	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.ModelCalls) != projectAssistantAuditMaxModelCalls {
		t.Fatalf("model calls = %d, want %d", len(audit.ModelCalls), projectAssistantAuditMaxModelCalls)
	}
	if audit.ModelCalls[0].Ordinal != 4 || audit.ModelCalls[len(audit.ModelCalls)-1].Ordinal != total {
		t.Fatalf("model-call window = %d..%d, want 4..%d",
			audit.ModelCalls[0].Ordinal,
			audit.ModelCalls[len(audit.ModelCalls)-1].Ordinal,
			total,
		)
	}
}

func TestProjectAssistantRunAuditMarksUnfinishedModelCallAsError(t *testing.T) {
	run := &store.AssistantRun{ID: "run-model-call-error"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	recorder.recordModelCall(context.Background(), projectEinoAssistantPhaseMutate, 1, 0, 0, false, nil, nil)
	recorder.recordModelError()

	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.ModelCalls) != 1 || audit.ModelCalls[0].Outcome != "error" {
		t.Fatalf("model calls = %#v, want unfinished call marked error", audit.ModelCalls)
	}
}

func TestProjectAssistantRunAuditPersistsSafeStructuredFailures(t *testing.T) {
	t.Run("no progress preserves safe fields", func(t *testing.T) {
		run, err := recordProjectAssistantRunAuditFailure(store.AssistantRun{}, &projectEinoAssistantNoProgressError{
			Phase:            projectEinoAssistantPhaseMutate,
			ToolName:         "provider__read_file",
			Calls:            3,
			Limit:            3,
			SourceRevision:   9,
			VerifiedRevision: 6,
		})
		if err != nil {
			t.Fatalf("record failure: %v", err)
		}

		var audit projectAssistantRunAudit
		if err := json.Unmarshal(run.Audit, &audit); err != nil {
			t.Fatalf("decode audit: %v", err)
		}
		if audit.Failure == nil ||
			audit.Failure.Kind != "no_progress" ||
			audit.Failure.Phase != projectEinoAssistantPhaseMutate ||
			audit.Failure.ToolName != projectToolReadFile ||
			audit.Failure.Calls != 3 ||
			audit.Failure.Limit != 3 ||
			audit.Failure.SourceRevision != 9 ||
			audit.Failure.VerifiedRevision != 6 {
			t.Fatalf("failure = %#v", audit.Failure)
		}
	})

	t.Run("generic error text is not persisted", func(t *testing.T) {
		const secret = "arbitrary-secret-backend-error"
		run, err := recordProjectAssistantRunAuditFailure(
			store.AssistantRun{},
			errors.New("backend failed: "+secret),
		)
		if err != nil {
			t.Fatalf("record failure: %v", err)
		}
		if strings.Contains(string(run.Audit), secret) {
			t.Fatalf("generic failure audit leaked raw error: %s", run.Audit)
		}

		var audit projectAssistantRunAudit
		if err := json.Unmarshal(run.Audit, &audit); err != nil {
			t.Fatalf("decode audit: %v", err)
		}
		if audit.Failure == nil ||
			audit.Failure.Kind != "operation_failed" ||
			audit.Failure.Summary != "assistant operation failed" {
			t.Fatalf("failure = %#v, want safe generic summary", audit.Failure)
		}
	})
}

func TestProjectAssistantRunAuditCanonicalReadsAreSanitized(t *testing.T) {
	started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	run := &store.AssistantRun{ID: "run-canonical"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, started)
	for i, event := range []projectToolCallStreamEvent{
		{ID: "ls", Name: projectToolLS, Status: "succeeded", Arguments: "path src", Summary: "2 path(s)"},
		{ID: "read", Name: projectToolReadFile, Status: "succeeded", Arguments: "path src/App.tsx; offset 1; limit 200", Summary: "file read"},
		{ID: "glob", Name: projectToolGlob, Status: "succeeded", Arguments: "pattern **/*.tsx; path src", Summary: "2 path(s)"},
		{ID: "grep", Name: projectToolGrep, Status: "succeeded", Arguments: "pattern privateNeedle; path src; glob **/*.tsx; output_mode content", Summary: "1 result line(s): matching source must not persist"},
	} {
		recorder.recordToolAt(event, started.Add(time.Duration(i+1)*time.Second))
	}

	raw := string(run.Audit)
	for _, forbidden := range []string{"privateNeedle", "matching source must not persist", "**/*.tsx"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("canonical read audit leaked %q: %s", forbidden, raw)
		}
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.Tools) != 4 {
		t.Fatalf("audit tools = %#v, want four canonical reads", audit.Tools)
	}
	for _, tool := range audit.Tools {
		if tool.Path != "src" && tool.Path != "src/App.tsx" {
			t.Fatalf("canonical read audit path = %q for %q", tool.Path, tool.Name)
		}
	}
}

func TestProjectAssistantRunAuditCanonicalSearchArgumentsResistInjection(t *testing.T) {
	started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	run := &store.AssistantRun{ID: "run-canonical-injection"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, started)
	longPattern := strings.Repeat("x", projectToolInfoLimit*2) + "; path attacker-long"
	tests := []struct {
		id      string
		name    string
		rawArgs string
		want    string
	}{
		{
			id:      "glob",
			name:    projectToolGlob,
			rawArgs: `{"path":"src/safe-glob","pattern":"needle; path attacker-delimiter\r\n\u0000"}`,
			want:    "src/safe-glob",
		},
		{
			id:      "grep",
			name:    projectToolGrep,
			rawArgs: `{"path":"src/safe-grep","pattern":` + strconv.Quote(longPattern) + `,"output_mode":"content"}`,
			want:    "src/safe-grep",
		},
		{
			id:      "grep-glob",
			name:    projectToolGrep,
			rawArgs: `{"path":"src/safe-grep-glob","pattern":"needle","glob":"**/*.ts; path attacker-glob\r\n\u0000"}`,
			want:    "src/safe-grep-glob",
		},
	}
	for i, tt := range tests {
		arguments := summarizeProjectToolArguments(tt.name, tt.rawArgs)
		if !strings.HasPrefix(arguments, "path "+tt.want+"; ") {
			t.Fatalf("%s arguments = %q, want real path first", tt.name, arguments)
		}
		if strings.Contains(arguments, "; path attacker") {
			t.Fatalf("%s arguments contain injected path segment: %q", tt.name, arguments)
		}
		if strings.ContainsAny(arguments, "\r\n\x00") {
			t.Fatalf("%s arguments contain control characters: %q", tt.name, arguments)
		}
		recorder.recordToolAt(projectToolCallStreamEvent{
			ID:        tt.id,
			Name:      tt.name,
			Status:    "succeeded",
			Arguments: arguments,
			Summary:   "1 result line(s): attacker-match-body",
		}, started.Add(time.Duration(i+1)*time.Second))
	}

	raw := string(run.Audit)
	for _, forbidden := range []string{
		"attacker-delimiter",
		"attacker-long",
		"attacker-glob",
		"attacker-match-body",
		"needle",
		strings.Repeat("x", 32),
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("canonical search audit leaked %q: %s", forbidden, raw)
		}
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.Tools) != len(tests) {
		t.Fatalf("audit tools = %#v, want %d", audit.Tools, len(tests))
	}
	for i, tool := range audit.Tools {
		if tool.Path != tests[i].want {
			t.Fatalf("audit path = %q for %q, want %q", tool.Path, tool.Name, tests[i].want)
		}
	}
}

func TestProjectAssistantCanonicalFilesystemPathsRoundTripToAuditAndUI(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		wantSummary string
	}{
		{name: "semicolon", path: "src/a;b.ts", wantSummary: "path src/a%3Bb.ts"},
		{name: "repeated spaces", path: "src/My  File.tsx", wantSummary: "path src/My  File.tsx"},
		{name: "percent", path: "src/100% done.ts", wantSummary: "path src/100%25 done.ts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawArgs, err := json.Marshal(map[string]any{"file_path": tt.path})
			if err != nil {
				t.Fatalf("marshal arguments: %v", err)
			}
			arguments := summarizeProjectToolArguments(projectToolReadFile, string(rawArgs))
			if arguments != tt.wantSummary {
				t.Fatalf("arguments = %q, want %q", arguments, tt.wantSummary)
			}

			started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
			run := &store.AssistantRun{ID: "run-path-" + tt.name}
			recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, started)
			recorder.recordToolAt(projectToolCallStreamEvent{
				ID:        "read",
				Name:      projectToolReadFile,
				Status:    "succeeded",
				Arguments: arguments,
				Summary:   "file read",
			}, started.Add(time.Second))

			var audit projectAssistantRunAudit
			if err := json.Unmarshal(run.Audit, &audit); err != nil {
				t.Fatalf("decode audit: %v", err)
			}
			if len(audit.Tools) != 1 || audit.Tools[0].Path != tt.path {
				t.Fatalf("audit tools = %#v, want exact path %q", audit.Tools, tt.path)
			}

			action := projectAssistantActionFeedItemFromAssistantToolCall(projectAssistantToolCall{
				ID:        "read",
				Name:      projectToolReadFile,
				Status:    "succeeded",
				Arguments: arguments,
				Summary:   "file read",
			})
			if action.Title != "Read file" || action.Target != tt.path {
				t.Fatalf("action = %#v, want exact target %q", action, tt.path)
			}
		})
	}
}

func TestProjectAssistantCanonicalFilesystemControlPathsAreOmittedFromAuditAndUI(t *testing.T) {
	for _, path := range []string{"src/bad\tname.tsx", "src/bad\nname.tsx"} {
		t.Run(strconv.Quote(path), func(t *testing.T) {
			rawArgs, err := json.Marshal(map[string]any{"file_path": path})
			if err != nil {
				t.Fatalf("marshal arguments: %v", err)
			}
			arguments := summarizeProjectToolArguments(projectToolReadFile, string(rawArgs))

			started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
			run := &store.AssistantRun{ID: "run-control-path"}
			recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, started)
			recorder.recordToolAt(projectToolCallStreamEvent{
				ID:        "read",
				Name:      projectToolReadFile,
				Status:    "succeeded",
				Arguments: arguments,
				Summary:   "file read",
			}, started.Add(time.Second))

			var audit projectAssistantRunAudit
			if err := json.Unmarshal(run.Audit, &audit); err != nil {
				t.Fatalf("decode audit: %v", err)
			}
			if len(audit.Tools) != 1 || audit.Tools[0].Path != "" {
				t.Fatalf("audit tools = %#v, want unsafe canonical path omitted", audit.Tools)
			}

			action := projectAssistantActionFeedItemFromAssistantToolCall(projectAssistantToolCall{
				ID:        "read",
				Name:      projectToolReadFile,
				Status:    "succeeded",
				Arguments: arguments,
				Summary:   "file read",
			})
			if action.Title != "Read file" || action.Target != "" {
				t.Fatalf("action = %#v, want unsafe target omitted", action)
			}
		})
	}
}

func TestProjectAssistantMutationPercentPathsAreNotCanonicalDecoded(t *testing.T) {
	const path = "src/literal%3Bsegment.tsx"
	const arguments = "path " + path

	if got := projectAssistantAuditToolPath(projectToolWriteFile, arguments); got != path {
		t.Fatalf("mutation audit path = %q, want literal %q", got, path)
	}
	action := projectAssistantActionFeedItemFromAssistantToolCall(projectAssistantToolCall{
		ID:        "write",
		Name:      projectToolWriteFile,
		Status:    "succeeded",
		Arguments: arguments,
		Summary:   "write_file",
	})
	if action.Title != "Updated file" || action.Target != path {
		t.Fatalf("mutation action = %#v, want literal percent target", action)
	}
}

func TestProjectAssistantNamespacedReadFilePercentPathIsNotCanonicalDecoded(t *testing.T) {
	const name = "provider__read_file"
	const path = "src/literal%2Fsegment.tsx"
	const arguments = "path " + path

	if got := projectAssistantAuditToolPath(name, arguments); got != path {
		t.Fatalf("namespaced audit path = %q, want literal %q", got, path)
	}
	action := projectAssistantActionFeedItemFromAssistantToolCall(projectAssistantToolCall{
		ID:        "read",
		Name:      name,
		Status:    "succeeded",
		Arguments: arguments,
		Summary:   "file read",
	})
	if action.Title != "Read file" || action.Target != path {
		t.Fatalf("namespaced action = %#v, want literal percent target", action)
	}
}

func TestProjectAssistantCanonicalSummaryUnescapeFailsClosed(t *testing.T) {
	for _, encoded := range []string{
		"src/incomplete%",
		"src/incomplete%2",
		"src/bad%GGhex",
		"src/invalid%FFutf8",
		"src/control%00byte",
		"src/control%C2%85rune",
	} {
		t.Run(encoded, func(t *testing.T) {
			if decoded, ok := unescapeProjectCanonicalToolSummaryValue(encoded); ok {
				t.Fatalf("unescape(%q) = %q, true; want fail closed", encoded, decoded)
			}
		})
	}
}

func TestProjectAssistantCanonicalSummaryUnicodeRoundTrip(t *testing.T) {
	const path = "src/日本語;100%.tsx"
	escaped := escapeProjectCanonicalToolSummaryValue(path)
	decoded, ok := unescapeProjectCanonicalToolSummaryValue(escaped)
	if !ok || decoded != path {
		t.Fatalf("roundtrip %q -> %q -> %q, %t", path, escaped, decoded, ok)
	}
}

func TestProjectAssistantEveryCanonicalReadPathRoundTripsToAudit(t *testing.T) {
	const path = "src/日本語;a.ts"
	for _, name := range []string{projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep} {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{"path": path}
			if name == projectToolReadFile {
				args = map[string]any{"file_path": path}
			}
			if name == projectToolGlob || name == projectToolGrep {
				args["pattern"] = "*.ts"
			}
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatalf("marshal arguments: %v", err)
			}
			summary := summarizeProjectToolArguments(name, string(raw))
			if got := projectAssistantAuditToolPath(name, summary); got != path {
				t.Fatalf("%s audit path = %q from %q, want %q", name, got, summary, path)
			}
		})
	}
}

func TestProjectAssistantPermissionAuditDoesNotPersistRawPayloads(t *testing.T) {
	run, err := appendProjectAssistantRunAudit(store.AssistantRun{}, projectAssistantPermissionAudit{
		RequestID:       "perm-1",
		Decision:        projectAssistantPermissionAllow,
		Actor:           "user@example.test",
		ToolCallID:      "call-1",
		ToolName:        projectToolWriteFile,
		EditedArguments: map[string]any{"path": "src/App.tsx", "content": "secret-source"},
		Result:          `{"content":"secret-result"}`,
		Error:           "secret-error",
		ResolvedAt:      time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("append audit: %v", err)
	}
	raw := string(run.Audit)
	for _, secret := range []string{"secret-source", "secret-result", "secret-error", "editedArguments", `"result"`, `"error"`} {
		if strings.Contains(raw, secret) {
			t.Fatalf("permission audit contains %q: %s", secret, raw)
		}
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.Decisions) != 1 || audit.Decisions[0].Reason != "operation_failed" {
		t.Fatalf("permission audit = %#v, want safe failure reason", audit)
	}
}

func TestProjectAssistantPermissionAuditKeepsOnlyRecentDecisions(t *testing.T) {
	run := store.AssistantRun{}
	for i := 0; i < projectAssistantAuditMaxDecisions+10; i++ {
		var err error
		run, err = appendProjectAssistantRunAudit(run, projectAssistantPermissionAudit{
			RequestID:  "perm-" + strconv.Itoa(i),
			Decision:   projectAssistantPermissionAllow,
			ToolCallID: "call-" + strconv.Itoa(i),
			ToolName:   projectToolWriteFile,
			ResolvedAt: time.Date(2026, 7, 26, 12, 0, 0, i, time.UTC),
		})
		if err != nil {
			t.Fatalf("append decision %d: %v", i, err)
		}
	}

	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.Decisions) != projectAssistantAuditMaxDecisions {
		t.Fatalf("decisions = %d, want %d", len(audit.Decisions), projectAssistantAuditMaxDecisions)
	}
	if audit.Decisions[0].RequestID != "perm-10" ||
		audit.Decisions[len(audit.Decisions)-1].RequestID != "perm-"+strconv.Itoa(projectAssistantAuditMaxDecisions+9) {
		t.Fatalf("decision window = %q..%q, want most recent decisions",
			audit.Decisions[0].RequestID,
			audit.Decisions[len(audit.Decisions)-1].RequestID,
		)
	}
}

func TestCompleteClaimedProjectAssistantRunAfterResumeErrorFinalizesAudit(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	started := time.Now().UTC().Add(-2 * time.Second)
	run := store.AssistantRun{
		ID:          "run-1",
		ProjectName: scope.ProjectName,
		Status:      store.AssistantRunStatusRunning,
		RequestID:   "perm-1",
		CreatedAt:   started,
		UpdatedAt:   started,
	}
	newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{
		LLM:         projectLLMSettings{Provider: "google-ai-studio", Model: "google/gemini-3.5-flash"},
		TurnProfile: projectAssistantTurnProfileImplementation,
	}, &run, started)
	if err := messages.SaveAssistantRun(context.Background(), scope, run); err != nil {
		t.Fatalf("save run: %v", err)
	}
	state := projectAssistantCheckpointState{
		CurrentIndex: 0,
		ToolCalls: []chatToolCall{{
			ID: "call-1",
			Function: chatToolCallFunction{
				Name: projectToolWriteFile,
			},
		}},
	}
	cause := errors.New("resume failed before Eino")
	_, err := server.completeClaimedProjectAssistantRunAfterResumeError(
		context.Background(),
		scope,
		run,
		state,
		projectAssistantResumeRequest{},
		projectAssistantPermissionAllow,
		"user@example.test",
		projectAssistantResumeResponse{},
		nil,
		cause,
	)
	if !errors.Is(err, cause) {
		t.Fatalf("resume completion error = %v, want original cause", err)
	}

	saved, err := messages.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(saved.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if saved.Status != store.AssistantRunStatusCompleted ||
		audit.Outcome != projectAssistantAuditOutcomeFailed ||
		audit.DurationMS < 1000 {
		t.Fatalf("saved run = %#v, audit = %#v; want finalized failure", saved, audit)
	}
}

func TestEinoAssistantEnginePersistsCompletedAndFailedRunAudits(t *testing.T) {
	tests := []struct {
		name             string
		profile          projectAssistantTurnProfile
		content          string
		streamErrors     []error
		wantOutcome      projectAssistantAuditOutcome
		wantErr          bool
		wantModelOutcome string
	}{
		{
			name:        "completed discussion",
			profile:     projectAssistantTurnProfileDiscussion,
			content:     "Here is the answer.",
			wantOutcome: projectAssistantAuditOutcomeSucceeded,
		},
		{
			name:        "implementation report is accepted",
			profile:     projectAssistantTurnProfileImplementation,
			content:     "I reviewed the request.",
			wantOutcome: projectAssistantAuditOutcomeSucceeded,
		},
		{
			name:             "failed model call records error shape",
			profile:          projectAssistantTurnProfileImplementation,
			content:          "unreachable",
			streamErrors:     []error{io.ErrUnexpectedEOF, io.ErrUnexpectedEOF, io.ErrUnexpectedEOF},
			wantOutcome:      projectAssistantAuditOutcomeFailed,
			wantErr:          true,
			wantModelOutcome: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
			server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
			chatModel := &retryingEinoChatModel{content: tt.content, streamErrors: tt.streamErrors}
			engine := projectEinoAssistantEngine{
				server: server,
				newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
					return chatModel, nil
				},
				newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
					return nil, nil
				},
			}
			req := projectEinoRunRequestForProfileTest(tt.profile)
			req.LLM = projectLLMSettings{Provider: "google-ai-studio", Model: "google/gemini-3.5-flash"}
			var started projectAssistantDurableStartResult
			var err error
			attach := func(run store.AssistantRun, assistant store.Message, _ bool) error {
				_, attachErr := server.projectAssistantSupervisor().Attach(req.MessageScope, run, assistant)
				return attachErr
			}
			if tt.profile == projectAssistantTurnProfileImplementation {
				started, err = server.startProjectAssistantBuildRunDurably(context.Background(), req.MessageScope, req.Identity.user, "Implement the request", "audit-"+strings.ReplaceAll(tt.name, " ", "-"), attach)
			} else {
				started, err = server.startProjectAssistantRunDurably(context.Background(), req.MessageScope, req.Identity.user, "Answer the request", "audit-"+strings.ReplaceAll(tt.name, " ", "-"), attach)
			}
			if err != nil {
				t.Fatalf("start durable audit run: %v", err)
			}
			req.AssistantRun = &started.Run
			req.executionAuthority = nil

			_, err = engine.StreamProjectAssistant(context.Background(), req)
			if tt.wantErr && err == nil {
				t.Fatal("StreamProjectAssistant error = nil, want lifecycle failure")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("StreamProjectAssistant returned error: %v", err)
			}
			if tt.wantErr && !errors.Is(err, adk.ErrExceedMaxRetries) && !strings.Contains(err.Error(), "exceeds max retries") {
				t.Fatalf("StreamProjectAssistant error = %v, want Eino retry exhaustion", err)
			}
			if messages.lastAssistantRun == nil {
				t.Fatal("no assistant run persisted")
			}
			if messages.lastAssistantRun.Status != store.AssistantRunStatusRunning {
				t.Fatalf("run status = %q, want lifecycle-owned running row", messages.lastAssistantRun.Status)
			}
			var audit projectAssistantRunAudit
			if err := json.Unmarshal(messages.lastAssistantRun.Audit, &audit); err != nil {
				t.Fatalf("decode audit: %v", err)
			}
			if audit.Outcome != tt.wantOutcome || audit.Provider != req.LLM.Provider || audit.Model != req.LLM.Model {
				t.Fatalf("audit = %#v", audit)
			}
			if tt.wantModelOutcome != "" {
				if len(audit.ModelCalls) == 0 || audit.ModelCalls[len(audit.ModelCalls)-1].Outcome != tt.wantModelOutcome {
					t.Fatalf("model calls = %#v, want terminal outcome %q", audit.ModelCalls, tt.wantModelOutcome)
				}
			}
		})
	}
}
