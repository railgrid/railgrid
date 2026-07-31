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
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
)

func TestProjectAssistantTerminalFailureContentIsVisibleAndResumable(t *testing.T) {
	content := projectAssistantTerminalFailureContent(adk.ErrExceedMaxIterations)
	if !strings.Contains(content, "bounded action limit") || !strings.Contains(content, "Send another message") {
		t.Fatalf("content = %q, want bounded resumable failure", content)
	}
}

func TestProjectAssistantTerminalFailureContentPreservesPartialOutputWithGuidance(t *testing.T) {
	content := projectAssistantContentWithTerminalFailure("I inspected the project and", adk.ErrExceedMaxIterations)
	if !strings.HasPrefix(content, "I inspected the project and") ||
		!strings.Contains(content, "bounded action limit") ||
		!strings.Contains(content, "Send another message") {
		t.Fatalf("content = %q, want partial output followed by resumable failure", content)
	}
}

func TestProjectAssistantTerminalFailureContentExplainsNoProgress(t *testing.T) {
	err := fmt.Errorf("%w: phase approval made no progress", errProjectAssistantNoProgress)
	content := projectAssistantTerminalFailureContent(err)
	if !strings.Contains(content, "did not make implementation progress") ||
		!strings.Contains(content, "Send another message") {
		t.Fatalf("content = %q, want resumable no-progress failure", content)
	}
	if !projectEinoAssistantNoProgressExceeded(err) {
		t.Fatalf("projectEinoAssistantNoProgressExceeded(%v) = false", err)
	}
	if reason := projectAssistantWorkItemFailureReason(err); reason != "no_progress" {
		t.Fatalf("WorkItem failure reason = %q, want no_progress", reason)
	}
	if reason := projectAssistantWorkItemFailureReason(errors.New("provider failed")); reason != "failed" {
		t.Fatalf("ordinary WorkItem failure reason = %q, want failed", reason)
	}
}

func TestProjectAssistantDurableTerminalContentKeepsBoundedClosingAnswerAuthoritative(t *testing.T) {
	closing := projectAssistantBoundedClosingAnswerForTest("I inspected the project.")
	for _, err := range []error{
		adk.ErrExceedMaxIterations,
		fmt.Errorf("%w: phase approval made no progress", errProjectAssistantNoProgress),
	} {
		if got := projectAssistantDurableTerminalContent(closing, "partial progress", err); got != closing {
			t.Errorf("projectAssistantDurableTerminalContent(%v) = %q, want closing answer", err, got)
		}
	}
}

func TestProjectAssistantDurableTerminalContentRejectsUnstructuredBoundedAnswer(t *testing.T) {
	got := projectAssistantDurableTerminalContent("Done.", "", adk.ErrExceedMaxIterations)
	if got == "Done." || !projectEinoAssistantBoundedClosingAnswerValid(got) {
		t.Fatalf("content = %q, want structured resumable fallback", got)
	}
}

func TestProjectAssistantDurableTerminalContentTreatsNoOutputAsIncomplete(t *testing.T) {
	got := projectAssistantDurableTerminalContent("", "", errProjectAssistantNoOutput)
	if !projectEinoAssistantBoundedClosingAnswerValid(got) ||
		!strings.Contains(got, "No usable assistant response") {
		t.Fatalf("content = %q, want structured no-output failure", got)
	}
}

func TestProjectAssistantBoundedClosingAnswerAddsIncompleteStatus(t *testing.T) {
	body := strings.TrimPrefix(
		projectAssistantBoundedClosingAnswerForTest("I inspected the project."),
		"Status: Incomplete\n\n",
	)
	got := projectEinoAssistantBoundedClosingAnswer(body)
	if !strings.HasPrefix(got, "Status: Incomplete\n\n") ||
		!projectEinoAssistantBoundedClosingAnswerValid(got) {
		t.Fatalf("content = %q, want deterministic incomplete envelope", got)
	}
}

func TestProjectAssistantDurableTerminalContentAppendsOrdinaryFailure(t *testing.T) {
	got := projectAssistantDurableTerminalContent("I inspected the project.", "", errors.New("provider failed"))
	if !strings.HasPrefix(got, "I inspected the project.") ||
		!strings.Contains(got, "stopped before it could finish") {
		t.Fatalf("content = %q, want reply followed by ordinary failure guidance", got)
	}
}

func projectAssistantBoundedClosingAnswerForTest(completed string) string {
	return "Status: Incomplete\n\nCompleted:\n- " + completed +
		"\n\nRemaining:\n- The requested task is not yet complete." +
		"\n\nBlocked:\n- The current turn paused before completion." +
		"\n\nNext:\n- Continue from the preserved project state."
}

func TestProjectAssistantPendingStatesDoNotReceiveTerminalFailureText(t *testing.T) {
	tests := []error{
		context.Canceled,
		&projectAssistantPermissionRequiredError{},
		&projectAssistantInputRequiredError{},
	}
	for _, err := range tests {
		if projectAssistantShouldPersistTerminalFailure(err) {
			t.Errorf("projectAssistantShouldPersistTerminalFailure(%T) = true", err)
		}
	}
	if !projectAssistantShouldPersistTerminalFailure(errors.New("provider failed")) {
		t.Fatal("ordinary terminal failure was not persisted")
	}
}
