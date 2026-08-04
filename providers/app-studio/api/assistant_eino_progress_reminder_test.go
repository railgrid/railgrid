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

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type projectEinoAssistantProgressReminderCaptureModel struct {
	einomodel.BaseChatModel
	input []*schema.Message
}

func (m *projectEinoAssistantProgressReminderCaptureModel) Generate(
	_ context.Context,
	input []*schema.Message,
	_ ...einomodel.Option,
) (*schema.Message, error) {
	m.input = input
	return schema.AssistantMessage("ok", nil), nil
}

func (m *projectEinoAssistantProgressReminderCaptureModel) Stream(
	_ context.Context,
	input []*schema.Message,
	_ ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.input = input
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
}

func TestProjectEinoAssistantProgressReminderTracksAcceptedProgressSeparately(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.NextModelCallOrdinal()
	runState.RecordCompletedAction("replace_file", `{"path":"src/App.tsx"}`, false)
	if !runState.QueueProgressReminder(projectEinoAssistantProgressReminderVerification, "stale verification") {
		t.Fatal("verification reminder did not queue")
	}
	if !runState.AcceptProgressMessage("The edit failed, so I am checking the next path.") {
		t.Fatal("progress message was not accepted")
	}
	checkpoint := runState.CheckpointState()
	if checkpoint.AcceptedProgressCount != 1 || checkpoint.LastAcceptedProgressModelCall != 1 {
		t.Fatalf("accepted progress checkpoint = %#v", checkpoint)
	}
	if checkpoint.NoProgressModelCallCount != 1 {
		t.Fatalf("action-progress counter = %d, want 1", checkpoint.NoProgressModelCallCount)
	}
	if runState.progressReminderPending() {
		t.Fatal("accepted progress left a stale reminder pending")
	}
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	if restored.AcceptedProgressCount() != 1 || restored.CurrentModelCallOrdinal() != 1 {
		t.Fatalf("accepted progress was not restored: count=%d ordinal=%d", restored.AcceptedProgressCount(), restored.CurrentModelCallOrdinal())
	}
}

func TestProjectEinoAssistantProgressReminderRepeatsForBoundedAttempts(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	previous := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{
		Content: "Inspect project", ActiveForm: "Inspecting project", Status: "in_progress",
	}}}
	next := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{
		Content: "Inspect project", ActiveForm: "Inspecting project", Status: "completed",
	}, {
		Content: "Verify preview", ActiveForm: "Verifying preview", Status: "in_progress",
	}}}
	if !runState.QueuePlanProgressReminder(previous, next) {
		t.Fatal("plan phase transition did not queue a reminder")
	}
	for attempt := 1; attempt <= projectEinoAssistantProgressReminderMaxAttempts; attempt++ {
		reminder, ok := runState.TakeProgressReminder(true)
		if !ok || reminder.Kind != projectEinoAssistantProgressReminderPlan {
			t.Fatalf("attempt %d reminder = %#v, ok = %v", attempt, reminder, ok)
		}
		if !strings.Contains(projectEinoAssistantProgressReminderInstruction(reminder), "Verifying preview") {
			t.Fatalf("plan reminder omitted active phase on attempt %d: %#v", attempt, reminder)
		}
		checkpoint := runState.CheckpointState()
		if attempt < projectEinoAssistantProgressReminderMaxAttempts {
			if checkpoint.ProgressReminderKind != projectEinoAssistantProgressReminderPlan || checkpoint.ProgressReminderAttempts != attempt {
				t.Fatalf("attempt %d checkpoint = %#v", attempt, checkpoint)
			}
			if !runState.progressReminderPending() {
				t.Fatalf("attempt %d cleared the queued reminder", attempt)
			}
		} else if runState.progressReminderPending() || checkpoint.ProgressReminderKind != "" || checkpoint.ProgressReminderAttempts != 0 {
			t.Fatalf("third attempt did not clear reminder: pending=%v checkpoint=%#v", runState.progressReminderPending(), checkpoint)
		}
	}
	if _, ok := runState.TakeProgressReminder(true); ok {
		t.Fatal("fourth reminder attempt was not suppressed")
	}
}

func TestProjectEinoAssistantProgressReminderInjectsEphemeralSystemMessage(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	if !runState.QueueProgressReminder(projectEinoAssistantProgressReminderVerification, "preview is not ready") {
		t.Fatal("verification reminder did not queue")
	}
	base := &projectEinoAssistantProgressReminderCaptureModel{}
	model := &projectEinoAssistantProgressReminderModel{
		BaseChatModel: base,
		req: projectAssistantRunRequest{
			TurnPolicy:      projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
			StreamCallbacks: projectAssistantStreamCallbacks{OnProgress: func(string) {}},
		},
		runState: runState,
	}
	canonicalSystem := "canonical system instruction"
	original := []*schema.Message{schema.SystemMessage(canonicalSystem), schema.UserMessage("continue")}
	if _, err := model.Generate(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	if len(original) != 2 || original[0].Role != schema.System || original[0].Content != canonicalSystem || len(base.input) != 3 {
		t.Fatalf("input mutation: original=%#v captured=%#v", original, base.input)
	}
	if base.input[0] != original[0] || base.input[1] != original[1] {
		t.Fatalf("canonical input was cloned or reordered: original=%#v captured=%#v", original, base.input)
	}
	reminder := base.input[2]
	if reminder.Role != schema.System || !strings.Contains(reminder.Content, "User-visible progress is overdue") ||
		!strings.Contains(reminder.Content, "report_progress is available") {
		t.Fatalf("captured reminder = %#v", reminder)
	}
	if !strings.Contains(reminder.Content, "Call it now with one concise completed outcome and your next direction or blocker, then continue working.") {
		t.Fatalf("reminder did not require an immediate update: %#v", reminder)
	}
	if !strings.Contains(reminder.Content, "advisory and non-blocking; do not force a tool choice") {
		t.Fatalf("reminder is not advisory/non-blocking: %#v", reminder)
	}
	if !runState.progressReminderPending() {
		t.Fatal("reminder was not retained after first ignored model invocation")
	}
	checkpoint := runState.CheckpointState()
	if checkpoint.ProgressReminderKind != projectEinoAssistantProgressReminderVerification || checkpoint.ProgressReminderAttempts != 1 {
		t.Fatalf("reminder attempt was not checkpointed: %#v", checkpoint)
	}
}

func TestProjectEinoAssistantProgressReminderInjectsEphemeralSystemMessageForStream(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	if !runState.QueueProgressReminder(projectEinoAssistantProgressReminderPlan, "implementing the next phase") {
		t.Fatal("plan reminder did not queue")
	}
	base := &projectEinoAssistantProgressReminderCaptureModel{}
	model := &projectEinoAssistantProgressReminderModel{
		BaseChatModel: base,
		req: projectAssistantRunRequest{
			TurnPolicy:      projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
			StreamCallbacks: projectAssistantStreamCallbacks{OnProgress: func(string) {}},
		},
		runState: runState,
	}
	canonicalSystem := "canonical stream instruction"
	original := []*schema.Message{schema.SystemMessage(canonicalSystem), schema.UserMessage("continue")}
	stream, err := model.Stream(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	if stream == nil {
		t.Fatal("stream model returned nil reader")
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	stream.Close()
	if len(original) != 2 || original[0].Role != schema.System || original[0].Content != canonicalSystem || len(base.input) != 3 {
		t.Fatalf("stream input mutation: original=%#v captured=%#v", original, base.input)
	}
	if base.input[0] != original[0] || base.input[1] != original[1] || base.input[2].Role != schema.System ||
		!strings.Contains(base.input[2].Content, "report_progress") {
		t.Fatalf("captured stream reminder = %#v", base.input)
	}
	if !runState.progressReminderPending() {
		t.Fatal("stream reminder was not retained after first ignored model invocation")
	}
}

func TestProjectEinoAssistantProgressReminderAppendsWithoutLeadingSystem(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	if !runState.QueueProgressReminder(projectEinoAssistantProgressReminderPlan, "implementing the next phase") {
		t.Fatal("plan reminder did not queue")
	}
	base := &projectEinoAssistantProgressReminderCaptureModel{}
	model := &projectEinoAssistantProgressReminderModel{
		BaseChatModel: base,
		req: projectAssistantRunRequest{
			TurnPolicy:      projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
			StreamCallbacks: projectAssistantStreamCallbacks{OnProgress: func(string) {}},
		},
		runState: runState,
	}
	original := []*schema.Message{schema.UserMessage("continue")}
	if _, err := model.Generate(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	if len(original) != 1 || len(base.input) != 2 || base.input[0] != original[0] || base.input[1].Role != schema.System {
		t.Fatalf("append mutated or reordered input: original=%#v captured=%#v", original, base.input)
	}
	if !strings.Contains(base.input[1].Content, "report_progress") {
		t.Fatalf("appended reminder omitted report_progress: %#v", base.input[1])
	}
}

func TestProjectEinoAssistantProgressReminderSuppressesUnavailableProgressTool(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	if !runState.QueueProgressReminder(projectEinoAssistantProgressReminderSilence, "silence") {
		t.Fatal("silence reminder did not queue")
	}
	base := &projectEinoAssistantProgressReminderCaptureModel{}
	model := &projectEinoAssistantProgressReminderModel{
		BaseChatModel: base,
		req:           projectAssistantRunRequest{},
		runState:      runState,
	}
	if _, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("continue")}); err != nil {
		t.Fatal(err)
	}
	if len(base.input) != 1 || base.input[0].Role != schema.User {
		t.Fatalf("unavailable progress reminder was injected: %#v", base.input)
	}
	if runState.progressReminderPending() {
		t.Fatal("unavailable reminder was not suppressed")
	}
	if checkpoint := runState.CheckpointState(); checkpoint.ProgressReminderAttempts != 0 {
		t.Fatalf("unavailable reminder attempts were retained: %#v", checkpoint)
	}
}

func TestProjectEinoAssistantProgressReminderSuppressesPermissionBarrier(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	if !runState.QueueProgressReminder(projectEinoAssistantProgressReminderVerification, "approval is pending") {
		t.Fatal("verification reminder did not queue")
	}
	if !runState.TryStartPermissionBarrier() {
		t.Fatal("permission barrier did not start")
	}
	base := &projectEinoAssistantProgressReminderCaptureModel{}
	model := &projectEinoAssistantProgressReminderModel{
		BaseChatModel: base,
		req: projectAssistantRunRequest{
			TurnPolicy:      projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
			StreamCallbacks: projectAssistantStreamCallbacks{OnProgress: func(string) {}},
		},
		runState: runState,
	}
	if _, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("continue")}); err != nil {
		t.Fatal(err)
	}
	if len(base.input) != 1 || base.input[0].Role != schema.User {
		t.Fatalf("permission-barrier reminder was injected: %#v", base.input)
	}
	if runState.progressReminderPending() {
		t.Fatal("permission-barrier reminder was not suppressed")
	}
	if checkpoint := runState.CheckpointState(); checkpoint.ProgressReminderAttempts != 0 {
		t.Fatalf("permission-barrier reminder attempts were retained: %#v", checkpoint)
	}
}

func TestProjectEinoAssistantProgressReminderVerificationTriggerRepeatsForBoundedAttempts(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.RecordDevelopmentVerification(false)
	for attempt := 1; attempt <= projectEinoAssistantProgressReminderMaxAttempts; attempt++ {
		reminder, ok := runState.TakeProgressReminder(true)
		if !ok || reminder.Kind != projectEinoAssistantProgressReminderVerification {
			t.Fatalf("attempt %d verification reminder = %#v, ok = %v", attempt, reminder, ok)
		}
	}
	if _, ok := runState.TakeProgressReminder(true); ok {
		t.Fatal("verification reminder remained available after the bounded attempts")
	}
}

func TestProjectEinoAssistantProgressReminderAcceptedProgressResetsAttempts(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	if !runState.QueueProgressReminder(projectEinoAssistantProgressReminderPlan, "phase one") {
		t.Fatal("plan reminder did not queue")
	}
	if _, ok := runState.TakeProgressReminder(true); !ok {
		t.Fatal("first plan reminder attempt was not delivered")
	}
	if checkpoint := runState.CheckpointState(); checkpoint.ProgressReminderAttempts != 1 {
		t.Fatalf("first attempt checkpoint = %#v", checkpoint)
	}
	if !runState.AcceptProgressMessage("I completed that phase and am moving to verification.") {
		t.Fatal("progress message was not accepted")
	}
	if runState.progressReminderPending() {
		t.Fatal("accepted progress left the reminder pending")
	}
	if checkpoint := runState.CheckpointState(); checkpoint.ProgressReminderKind != "" || checkpoint.ProgressReminderAttempts != 0 {
		t.Fatalf("accepted progress did not clear reminder metadata: %#v", checkpoint)
	}
	if !runState.QueueProgressReminder(projectEinoAssistantProgressReminderPlan, "phase two") {
		t.Fatal("new phase reminder did not queue after accepted progress")
	}
	if _, ok := runState.TakeProgressReminder(true); !ok {
		t.Fatal("new phase reminder was not delivered")
	}
	if checkpoint := runState.CheckpointState(); checkpoint.ProgressReminderAttempts != 1 {
		t.Fatalf("new phase did not reset attempts: %#v", checkpoint)
	}
}

func TestProjectEinoAssistantProgressReminderCheckpointRestoresAttemptsAndSanitizes(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	if !runState.QueueProgressReminder(projectEinoAssistantProgressReminderVerification, "resume verification") {
		t.Fatal("verification reminder did not queue")
	}
	if _, ok := runState.TakeProgressReminder(true); !ok {
		t.Fatal("first reminder attempt was not delivered")
	}
	checkpoint := runState.CheckpointState()
	if checkpoint.ProgressReminderAttempts != 1 {
		t.Fatalf("checkpoint attempts = %#v", checkpoint)
	}
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	if restored.CheckpointState().ProgressReminderAttempts != 1 {
		t.Fatalf("restored attempts = %#v", restored.CheckpointState())
	}
	if _, ok := restored.TakeProgressReminder(true); !ok {
		t.Fatal("restored second reminder attempt was not delivered")
	}
	if _, ok := restored.TakeProgressReminder(true); !ok {
		t.Fatal("restored third reminder attempt was not delivered")
	}
	if restored.progressReminderPending() {
		t.Fatal("restored third attempt did not clear reminder")
	}
	if _, ok := restored.TakeProgressReminder(true); ok {
		t.Fatal("restored fourth reminder attempt was delivered")
	}
	high := newProjectEinoAssistantRunState()
	high.RestoreCheckpointState(projectAssistantCheckpointState{
		ProgressReminderKind:     projectEinoAssistantProgressReminderPlan,
		ProgressReminderAttempts: projectEinoAssistantProgressReminderMaxAttempts + 100,
	})
	if high.progressReminderPending() || high.CheckpointState().ProgressReminderAttempts != 0 {
		t.Fatalf("over-limit attempts were not sanitized: %#v", high.CheckpointState())
	}
	negative := newProjectEinoAssistantRunState()
	negative.RestoreCheckpointState(projectAssistantCheckpointState{
		ProgressReminderKind:     projectEinoAssistantProgressReminderPlan,
		ProgressReminderAttempts: -10,
	})
	if !negative.progressReminderPending() || negative.CheckpointState().ProgressReminderAttempts != 0 {
		t.Fatalf("negative attempts were not sanitized: %#v", negative.CheckpointState())
	}
	invalid := newProjectEinoAssistantRunState()
	invalid.RestoreCheckpointState(projectAssistantCheckpointState{
		ProgressReminderKind:             "unknown-reminder",
		ProgressReminderAttempts:         2,
		ProgressReminderSilenceTriggered: true,
	})
	invalidCheckpoint := invalid.CheckpointState()
	if invalid.progressReminderPending() || invalidCheckpoint.ProgressReminderAttempts != 0 || invalidCheckpoint.ProgressReminderSilenceTriggered {
		t.Fatalf("invalid reminder checkpoint was not sanitized: %#v", invalidCheckpoint)
	}
}

func TestProjectEinoAssistantProgressReminderAcceptedUpdateSuppressesSameCallVerification(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.NextModelCallOrdinal()
	if !runState.AcceptProgressMessage("The previous verification failed; I am adjusting the implementation.") {
		t.Fatal("progress message was not accepted")
	}
	runState.RecordDevelopmentVerification(false)
	if runState.progressReminderPending() {
		t.Fatal("verification queued a stale reminder after accepted progress in the same model call")
	}
}

func TestProjectEinoAssistantProgressReminderSilenceIsBoundedAndCheckpointed(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	for i := 0; i < projectEinoAssistantProgressReminderSilenceModelCalls-1; i++ {
		runState.NextModelCallOrdinal()
	}
	if runState.progressReminderPending() {
		t.Fatal("silence reminder queued before bounded threshold")
	}
	runState.NextModelCallOrdinal()
	checkpoint := runState.CheckpointState()
	if checkpoint.ProgressReminderKind != projectEinoAssistantProgressReminderSilence || !checkpoint.ProgressReminderSilenceTriggered {
		t.Fatalf("silence checkpoint = %#v", checkpoint)
	}
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	if _, ok := restored.TakeProgressReminder(true); !ok {
		t.Fatal("restored silence reminder was not available")
	}
	if restored.NextModelCallOrdinal() != projectEinoAssistantProgressReminderSilenceModelCalls+1 {
		t.Fatalf("restored model call ordinal = %d", restored.CurrentModelCallOrdinal())
	}
}
