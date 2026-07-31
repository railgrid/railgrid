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
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestProjectEinoAssistantProgressToolEmitsSanitizedDistinctProse(t *testing.T) {
	var progress []string
	toolCalls := 0
	req := projectAssistantRunRequest{
		TurnProfile: projectAssistantTurnProfileImplementation,
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnProgress: func(message string) {
				progress = append(progress, message)
			},
			OnToolCall: func(projectToolCallStreamEvent) {
				toolCalls++
			},
		},
	}
	runState := newProjectEinoAssistantRunState()
	tool := projectEinoAssistantProgressTool{req: req, runState: runState}

	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info returned error: %v", err)
	}
	if info.Name != projectEinoAssistantReportProgressTool || info.ParamsOneOf == nil {
		t.Fatalf("tool info = %#v, want report_progress with parameters", info)
	}

	result, err := tool.InvokableRun(
		context.Background(),
		`{"message":"  I found token=progress-super-secret and will continue with the approved change.  "}`,
	)
	if err != nil {
		t.Fatalf("InvokableRun returned error: %v", err)
	}
	if result != `{"status":"shown"}` {
		t.Fatalf("result = %q, want shown", result)
	}
	if len(progress) != 1 {
		t.Fatalf("progress = %#v, want one message", progress)
	}
	if strings.Contains(progress[0], "progress-super-secret") ||
		!strings.Contains(progress[0], "token=[REDACTED]") {
		t.Fatalf("progress = %q, want secret redaction", progress[0])
	}
	if toolCalls != 0 {
		t.Fatalf("action-feed tool calls = %d, want none", toolCalls)
	}

	result, err = tool.InvokableRun(
		context.Background(),
		`{"message":"I found token=progress-super-secret and will continue with the approved change."}`,
	)
	if err != nil {
		t.Fatalf("duplicate InvokableRun returned error: %v", err)
	}
	if result != `{"status":"duplicate","reason":"provide a new progress outcome"}` ||
		len(progress) != 1 {
		t.Fatalf("duplicate result = %q progress = %#v", result, progress)
	}

	result, err = tool.InvokableRun(
		context.Background(),
		`{"message":"`+strings.Repeat("界", projectEinoAssistantProgressMaxBytes)+`"}`,
	)
	if err != nil || result != `{"status":"shown"}` {
		t.Fatalf("long progress result = (%q, %v), want shown", result, err)
	}
	if got := progress[len(progress)-1]; len(got) > projectEinoAssistantProgressMaxBytes ||
		!utf8.ValidString(got) ||
		!strings.HasSuffix(got, "...") {
		t.Fatalf("long progress bytes = %d valid = %t suffix = %q", len(got), utf8.ValidString(got), got[len(got)-3:])
	}

	result, err = tool.InvokableRun(context.Background(), `{"message":"unsafe\u0000message"}`)
	if err != nil {
		t.Fatalf("control-character progress returned error: %v", err)
	}
	if result != `{"status":"rejected","reason":"progress message contains invalid text"}` {
		t.Fatalf("control-character result = %q, want rejection", result)
	}
}

func TestProjectEinoAssistantProgressContractFollowsLifecyclePhases(t *testing.T) {
	var progress []string
	req := projectAssistantRunRequest{
		TurnProfile: projectAssistantTurnProfileImplementation,
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnProgress: func(message string) {
				progress = append(progress, message)
			},
		},
	}
	runState := newProjectEinoAssistantRunState()
	progressTool := projectEinoAssistantProgressTool{req: req, runState: runState}
	progressInfo, err := progressTool.Info(context.Background())
	if err != nil {
		t.Fatalf("progress Info returned error: %v", err)
	}
	readInfo := projectEinoAssistantPhaseToolInfo(
		projectToolReadFile,
		projectAssistantToolRiskRead,
		projectAssistantToolBundleWorkspaceRead,
	)
	state := &adk.ChatModelAgentState{
		ToolInfos:         []*schema.ToolInfo{progressInfo, readInfo},
		DeferredToolInfos: []*schema.ToolInfo{progressInfo, readInfo},
	}
	middleware := projectEinoAssistantPhaseMiddleware(req, runState).(*projectEinoAssistantPhaseFilterMiddleware)

	if _, state, err = middleware.BeforeModelRewriteState(context.Background(), state, nil); err != nil {
		t.Fatalf("BeforeModelRewriteState returned error: %v", err)
	}
	if !projectEinoAssistantPhaseToolNamesContain(
		projectEinoAssistantPhaseToolNames(state.ToolInfos),
		projectEinoAssistantReportProgressTool,
	) {
		t.Fatalf("approval tools = %#v, want report_progress", projectEinoAssistantPhaseToolNames(state.ToolInfos))
	}
	if !projectEinoAssistantMessagesContainPrefix(state.Messages, projectEinoAssistantCommentaryProgressPrefix) {
		t.Fatalf("messages = %#v, want commentary requirement", state.Messages)
	}

	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		progressTool.InvokableRun,
		&adk.ToolContext{Name: projectEinoAssistantReportProgressTool},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	result, err := wrapped(
		context.Background(),
		`{"message":"I’m reviewing the existing project shape before changing it."}`,
		[]einotool.Option{}...,
	)
	if err != nil || result != `{"status":"shown"}` {
		t.Fatalf("progress result = (%q, %v), want shown", result, err)
	}
	if !runState.ProgressReported(projectEinoAssistantPhaseApproval) || len(progress) != 1 {
		t.Fatalf("approval progress reported = %t messages = %#v",
			runState.ProgressReported(projectEinoAssistantPhaseApproval), progress)
	}

	if _, state, err = middleware.BeforeModelRewriteState(context.Background(), state, nil); err != nil {
		t.Fatalf("second BeforeModelRewriteState returned error: %v", err)
	}
	if projectEinoAssistantMessagesContainPrefix(state.Messages, projectEinoAssistantCommentaryProgressPrefix) {
		t.Fatalf("messages = %#v, commentary requirement should clear after progress", state.Messages)
	}
	if projectEinoAssistantPhaseToolNamesContain(
		projectEinoAssistantPhaseToolNames(state.ToolInfos),
		projectEinoAssistantReportProgressTool,
	) {
		t.Fatalf("tools = %#v, report_progress should hide after phase progress", projectEinoAssistantPhaseToolNames(state.ToolInfos))
	}

	runState.ApprovePlan(projectAssistantApprovedPlan{
		Steps:       []string{"apply the requested change"},
		TargetPaths: []string{"src/App.jsx"},
	})
	if _, state, err = middleware.BeforeModelRewriteState(context.Background(), state, nil); err != nil {
		t.Fatalf("mutation BeforeModelRewriteState returned error: %v", err)
	}
	if middleware.phase != projectEinoAssistantPhaseMutate ||
		!projectEinoAssistantMessagesContainPrefix(state.Messages, projectEinoAssistantCommentaryProgressPrefix) {
		t.Fatalf("phase = %q messages = %#v, want fresh mutation commentary requirement", middleware.phase, state.Messages)
	}
}

func TestProjectEinoAssistantProgressPhaseReservationIsAtomic(t *testing.T) {
	req := projectAssistantRunRequest{
		TurnProfile: projectAssistantTurnProfileImplementation,
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnProgress: func(string) {},
		},
	}
	runState := newProjectEinoAssistantRunState()
	middleware := projectEinoAssistantPhaseMiddleware(req, runState).(*projectEinoAssistantPhaseFilterMiddleware)
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			calls++
			close(started)
			<-release
			return `{"status":"shown"}`, nil
		},
		&adk.ToolContext{Name: projectEinoAssistantReportProgressTool},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}

	firstResult := make(chan string, 1)
	go func() {
		result, _ := wrapped(context.Background(), `{"message":"First update."}`)
		firstResult <- result
	}()
	<-started
	secondResult, err := wrapped(context.Background(), `{"message":"Second update."}`)
	if err != nil {
		t.Fatalf("second progress returned error: %v", err)
	}
	close(release)
	if result := <-firstResult; result != `{"status":"shown"}` {
		t.Fatalf("first result = %q, want shown", result)
	}
	if secondResult != `{"status":"duplicate","reason":"progress already reported for this phase"}` {
		t.Fatalf("second result = %q, want phase duplicate", secondResult)
	}
	if calls != 1 {
		t.Fatalf("progress endpoint calls = %d, want one", calls)
	}
}

func projectEinoAssistantMessagesContainPrefix(messages []*schema.Message, prefix string) bool {
	for _, message := range messages {
		if message != nil && strings.HasPrefix(message.Content, prefix) {
			return true
		}
	}
	return false
}
