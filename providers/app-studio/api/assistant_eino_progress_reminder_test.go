/*
Copyright 2026 The Railgrid Authors.

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
	runState.RecordCompletedAction("replace_file", `{"path":"src/App.tsx"}`)
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
	if runState.progressReminderPending() {
		t.Fatal("accepted progress left a stale reminder pending")
	}
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	if restored.AcceptedProgressCount() != 1 || restored.CurrentModelCallOrdinal() != 1 {
		t.Fatalf("accepted progress was not restored: count=%d ordinal=%d", restored.AcceptedProgressCount(), restored.CurrentModelCallOrdinal())
	}
}

func TestProjectEinoAssistantProgressReminderDeliversOnceForPhaseChange(t *testing.T) {
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
	reminder, ok := runState.TakeProgressReminder(true)
	if !ok || reminder.Kind != projectEinoAssistantProgressReminderPlan {
		t.Fatalf("phase reminder = %#v, ok = %v", reminder, ok)
	}
	if !strings.Contains(projectEinoAssistantProgressReminderInstruction(reminder), "Verifying preview") {
		t.Fatalf("plan reminder omitted active phase: %#v", reminder)
	}
	checkpoint := runState.CheckpointState()
	if runState.progressReminderPending() || checkpoint.ProgressReminderKind != "" || checkpoint.ProgressReminderAttempts != 0 {
		t.Fatalf("delivered reminder was not cleared: pending=%v checkpoint=%#v", runState.progressReminderPending(), checkpoint)
	}
	if _, ok := runState.TakeProgressReminder(true); ok {
		t.Fatal("duplicate reminder was delivered")
	}
}

func TestProjectEinoAssistantPlanReminderIgnoresInitialPlanAndChecklistEdits(t *testing.T) {
	initial := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{
		Content: "Inspect project", ActiveForm: "Inspecting project", Status: "in_progress",
	}}}
	if projectEinoAssistantPlanPhaseTransition(projectAssistantPlanSnapshot{}, initial) {
		t.Fatal("initial checklist creation counted as a completed phase")
	}
	statusEdit := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{
		Content: "Inspect project", ActiveForm: "Inspecting project", Status: "completed",
	}, {
		Content: "Implement title edit", ActiveForm: "Implementing title edit", Status: "in_progress",
	}}}
	if !projectEinoAssistantPlanPhaseTransition(initial, statusEdit) {
		t.Fatal("active work changing to the next phase was not detected")
	}
	textOnlyEdit := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{
		Content: "Inspect project", ActiveForm: "Reviewing project", Status: "in_progress",
	}}}
	if projectEinoAssistantPlanPhaseTransition(initial, textOnlyEdit) {
		t.Fatal("active-form wording edit counted as a phase change")
	}
	uncompletedPhaseChange := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{
		Content: "Inspect project", ActiveForm: "Inspecting project", Status: "pending",
	}, {
		Content: "Implement title edit", ActiveForm: "Implementing title edit", Status: "in_progress",
	}}}
	if projectEinoAssistantPlanPhaseTransition(initial, uncompletedPhaseChange) {
		t.Fatal("moving to a new active step without completing the prior phase counted as progress")
	}
}

// Exercise both model APIs and inputs with/without a leading system message
// through the same preservation, delivery, and checkpoint assertions.
func TestProjectEinoAssistantProgressReminderInjectsEphemeralSystemMessage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
		system bool
		kind   string
	}{
		{"generate", false, true, projectEinoAssistantProgressReminderVerification},
		{"stream", true, true, projectEinoAssistantProgressReminderPlan},
		{"without system", false, false, projectEinoAssistantProgressReminderPlan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runState := newProjectEinoAssistantRunState()
			policy := projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
			runState.SetTurnPolicy(policy)
			if !runState.QueueProgressReminder(tc.kind, "implementing the next phase") {
				t.Fatal("reminder did not queue")
			}
			base := &projectEinoAssistantProgressReminderCaptureModel{}
			model := &projectEinoAssistantProgressReminderModel{
				BaseChatModel: base,
				req:           projectAssistantRunRequest{TurnPolicy: policy, StreamCallbacks: projectAssistantStreamCallbacks{OnProgress: func(string) {}}},
				runState:      runState,
			}
			original := []*schema.Message{schema.UserMessage("continue")}
			if tc.system {
				original = append([]*schema.Message{schema.SystemMessage("canonical system instruction")}, original...)
			}
			before := append([]*schema.Message(nil), original...)
			if tc.stream {
				stream, err := model.Stream(context.Background(), original)
				if err != nil {
					t.Fatal(err)
				}
				if stream == nil {
					t.Fatal("nil stream reader")
				}
				defer stream.Close()
				if _, err := stream.Recv(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := model.Generate(context.Background(), original); err != nil {
				t.Fatal(err)
			}
			if len(original) != len(before) || len(base.input) != len(before)+1 {
				t.Fatalf("input mutation: original=%#v captured=%#v", original, base.input)
			}
			for i, message := range before {
				if original[i] != message || base.input[i] != message {
					t.Fatal("canonical input was cloned or reordered")
				}
			}
			if tc.system && (original[0].Role != schema.System || original[0].Content != "canonical system instruction") {
				t.Fatal("canonical system instruction changed")
			}
			reminder := base.input[len(before)]
			if reminder.Role != schema.System {
				t.Fatalf("reminder role = %s", reminder.Role)
			}
			for _, text := range []string{
				"A user update may be useful for this substantial work",
				"use report_progress once with a concise outcome",
				"otherwise continue without adding a tool call",
				"advisory and non-blocking; do not force a tool choice",
			} {
				if !strings.Contains(reminder.Content, text) {
					t.Fatalf("reminder missing %q: %s", text, reminder.Content)
				}
			}
			checkpoint := runState.CheckpointState()
			if runState.progressReminderPending() || checkpoint.ProgressReminderKind != "" || checkpoint.ProgressReminderAttempts != 0 {
				t.Fatalf("one-shot reminder was not cleared: %#v", checkpoint)
			}
		})
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

func TestProjectEinoAssistantProgressReminderVerificationTriggerDeliversOnce(t *testing.T) {
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

func TestProjectEinoAssistantProgressReminderAcceptedProgressClearsOneShotAndAllowsNext(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	if !runState.QueueProgressReminder(projectEinoAssistantProgressReminderPlan, "phase one") {
		t.Fatal("plan reminder did not queue")
	}
	if _, ok := runState.TakeProgressReminder(true); !ok {
		t.Fatal("first plan reminder attempt was not delivered")
	}
	if runState.progressReminderPending() {
		t.Fatal("one-shot plan reminder remained pending after delivery")
	}
	if checkpoint := runState.CheckpointState(); checkpoint.ProgressReminderKind != "" || checkpoint.ProgressReminderAttempts != 0 {
		t.Fatalf("delivered one-shot reminder was not cleared: %#v", checkpoint)
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
	if runState.progressReminderPending() {
		t.Fatal("new phase one-shot reminder remained pending after delivery")
	}
	if checkpoint := runState.CheckpointState(); checkpoint.ProgressReminderKind != "" || checkpoint.ProgressReminderAttempts != 0 {
		t.Fatalf("new phase one-shot reminder was not cleared: %#v", checkpoint)
	}
}

func TestProjectEinoAssistantProgressReminderCheckpointRestoresAttemptsAndSanitizes(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	if !runState.QueueProgressReminder(projectEinoAssistantProgressReminderVerification, "resume verification") {
		t.Fatal("verification reminder did not queue")
	}
	checkpoint := runState.CheckpointState()
	if checkpoint.ProgressReminderAttempts != 0 {
		t.Fatalf("checkpoint attempts = %#v", checkpoint)
	}
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	if restored.CheckpointState().ProgressReminderAttempts != 0 {
		t.Fatalf("restored attempts = %#v", restored.CheckpointState())
	}
	if _, ok := restored.TakeProgressReminder(true); !ok {
		t.Fatal("restored one-shot reminder was not delivered")
	}
	if restored.progressReminderPending() {
		t.Fatal("restored one-shot reminder was not cleared")
	}
	if _, ok := restored.TakeProgressReminder(true); ok {
		t.Fatal("restored duplicate reminder was delivered")
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
	if projectEinoAssistantProgressReminderSilenceModelCalls < 18 {
		t.Fatalf("silence threshold = %d model calls, want at least 18 to avoid reminders on short tasks", projectEinoAssistantProgressReminderSilenceModelCalls)
	}
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
