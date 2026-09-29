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

package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// askOnce is an adapter that makes exactly one permission request from inside a
// turn and reports what it was told. It is the shape a real adapter has: the
// child is alive and blocked on a tool call while AskPermission is waiting.
type askOnce struct {
	request  harness.PermissionRequest
	verdicts chan harness.PermissionVerdict
	launches chan harness.Launch
}

func newAskOnce(request harness.PermissionRequest) *askOnce {
	return &askOnce{
		request:  request,
		verdicts: make(chan harness.PermissionVerdict, 1),
		launches: make(chan harness.Launch, 4),
	}
}

func (a *askOnce) adapter(sessionID string) *fakeAdapter {
	return &fakeAdapter{run: func(ctx context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		a.launches <- launch
		if err := emit(harness.Event{Type: EventStarted, SessionID: sessionID}); err != nil {
			return harness.Result{}, err
		}
		if launch.Permissions == nil {
			return harness.Result{Phase: string(PhaseFailed), SessionID: sessionID, Blocker: "no permission asker"}, nil
		}
		verdict, err := launch.Permissions.AskPermission(ctx, a.request)
		if err != nil {
			return harness.Result{Phase: string(PhaseFailed), SessionID: sessionID, Blocker: err.Error()}, nil
		}
		a.verdicts <- verdict
		// A denial is a real answer: the turn keeps going and finishes, which
		// is exactly what the harness does with a deny result.
		return harness.Result{Phase: string(PhaseCompleted), SessionID: sessionID}, nil
	}}
}

// permissionStartRequest opts the attempt into the permission round-trip. A
// start that does not is unchanged: nothing asks, and nothing parks.
func permissionStartRequest(commit string) StartRequest {
	request := testStartRequest(commit)
	request.AskPermission = true
	return request
}

func permissionResume(receipt Receipt, requestID, decision, resolution string) ResumeRequest {
	return ResumeRequest{
		ProtocolVersion:    ProtocolVersion,
		RequestID:          "resume-" + decision + "-" + requestID,
		TaskID:             receipt.TaskID,
		AttemptID:          receipt.AttemptID,
		AttemptEpoch:       receipt.AttemptEpoch,
		SessionID:          receipt.SessionID,
		PermissionID:       requestID,
		PermissionDecision: decision,
		Resolution:         resolution,
		HarnessCredential:  testHarnessCredential(),
	}
}

func TestPermissionRequestParksAttemptWithToolAndInput(t *testing.T) {
	source, commit := testGitSource(t)
	ask := newAskOnce(harness.PermissionRequest{
		ID:    "permission-abc",
		Tool:  "Bash",
		Input: `{"command":"rm -rf /tmp/x"}`,
	})
	runner := newTestRunner(t, ask.adapter("session-permission"), source, commit)
	started, err := runner.Start(context.Background(), permissionStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseNeedsInput)

	parked, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if parked.Permission == nil {
		t.Fatalf("receipt = %+v, want a permission request", parked)
	}
	if parked.Permission.ID != "permission-abc" || parked.Permission.Tool != "Bash" {
		t.Fatalf("permission = %+v, want the tool and its id", parked.Permission)
	}
	if parked.Permission.Input != `{"command":"rm -rf /tmp/x"}` {
		t.Fatalf("permission input = %q, want the call's arguments", parked.Permission.Input)
	}
	// A permission park is not a question: a caller that renders a question
	// would show an Approve button with nothing behind it.
	if parked.Clarification != nil {
		t.Fatalf("clarification = %+v, want none on a permission park", parked.Clarification)
	}
	if !strings.Contains(parked.Blocker, "Bash") {
		t.Fatalf("blocker = %q, want it to name the tool", parked.Blocker)
	}
	if !contains(runner.Capabilities().Verification, permissionPromptCapability) {
		t.Fatalf("verification capabilities = %+v, want %q", runner.Capabilities().Verification, permissionPromptCapability)
	}

	// Let the turn finish so the runner is not closed mid-park.
	if _, err := runner.Resume(context.Background(), permissionResume(parked, "permission-abc", PermissionDeny, "")); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseCompleted)
}

func TestPermissionResumeApprovesAndContinuesTheSameTurn(t *testing.T) {
	source, commit := testGitSource(t)
	ask := newAskOnce(harness.PermissionRequest{ID: "permission-allow", Tool: "WebFetch", Input: `{"url":"https://example.invalid"}`})
	adapter := ask.adapter("session-allow")
	runner := newTestRunner(t, adapter, source, commit)
	started, err := runner.Start(context.Background(), permissionStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseNeedsInput)
	parked, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	if _, err := runner.Resume(context.Background(), permissionResume(parked, "permission-allow", PermissionAllow, "yes, go ahead")); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	select {
	case verdict := <-ask.verdicts:
		if !verdict.Allow {
			t.Fatalf("verdict = %+v, want an approval", verdict)
		}
		if verdict.Message != "yes, go ahead" {
			t.Fatalf("verdict message = %q, want the person's own words", verdict.Message)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting tool call never received a verdict")
	}
	waitForPhase(t, runner, started.AttemptID, PhaseCompleted)

	// The point of answering into the open call: the harness was launched ONCE.
	// A second launch would mean the turn was restarted rather than continued.
	if launches := adapter.runs.Load(); launches != 1 {
		t.Fatalf("adapter launches = %d, want exactly 1 — the verdict must continue the same turn", launches)
	}
	final, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if final.SessionID != "session-allow" {
		t.Fatalf("session = %q, want the session the turn started in", final.SessionID)
	}
	if final.Permission != nil {
		t.Fatalf("permission = %+v, want it cleared once answered", final.Permission)
	}
}

func TestPermissionDenyIsAnAnswerAndNotAFailedTurn(t *testing.T) {
	source, commit := testGitSource(t)
	ask := newAskOnce(harness.PermissionRequest{ID: "permission-deny", Tool: "Bash", Input: `{"command":"curl evil"}`})
	runner := newTestRunner(t, ask.adapter("session-deny"), source, commit)
	started, err := runner.Start(context.Background(), permissionStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseNeedsInput)
	parked, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	if _, err := runner.Resume(context.Background(), permissionResume(parked, "permission-deny", PermissionDeny, "")); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	select {
	case verdict := <-ask.verdicts:
		if verdict.Allow {
			t.Fatalf("verdict = %+v, want a denial", verdict)
		}
		// A bare denial still has to say something: the model is told this and
		// an empty string teaches it nothing.
		if strings.TrimSpace(verdict.Message) == "" {
			t.Fatal("a denial reached the harness with no message")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting tool call never received a verdict")
	}
	// The turn continued past the denial and finished. A denial that failed the
	// turn would land here as PhaseFailed.
	waitForPhase(t, runner, started.AttemptID, PhaseCompleted)
	final, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if final.LastError != nil {
		t.Fatalf("lastError = %+v, want a denial to leave no error behind", final.LastError)
	}
}

func TestPermissionResumeFencesAVerdictForAnotherRequest(t *testing.T) {
	source, commit := testGitSource(t)
	ask := newAskOnce(harness.PermissionRequest{ID: "permission-real", Tool: "Bash", Input: `{"command":"ls"}`})
	runner := newTestRunner(t, ask.adapter("session-fence"), source, commit)
	started, err := runner.Start(context.Background(), permissionStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseNeedsInput)
	parked, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	// A verdict given for a different request is not a verdict for this one:
	// the person who gave it was shown another call.
	_, err = runner.Resume(context.Background(), permissionResume(parked, "permission-somewhere-else", PermissionAllow, ""))
	assertProtocolCode(t, err, ErrorCheckpointUnavailable)

	// A verdict with no decision is not an answer either.
	undecided := permissionResume(parked, "permission-real", "", "")
	undecided.RequestID = "resume-undecided"
	_, err = runner.Resume(context.Background(), undecided)
	assertProtocolCode(t, err, ErrorInvalidRequest)

	// Answering it as if it were a question is refused too: a clarification
	// resume would relaunch the harness and abandon the open call.
	asQuestion := permissionResume(parked, "", "", "an answer in words")
	asQuestion.RequestID = "resume-as-question"
	asQuestion.ClarificationID = "clarification-not-real"
	_, err = runner.Resume(context.Background(), asQuestion)
	assertProtocolCode(t, err, ErrorCheckpointUnavailable)

	// The attempt is still parked on the same request, untouched.
	still, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if still.Permission == nil || still.Permission.ID != "permission-real" {
		t.Fatalf("permission = %+v, want the original request still outstanding", still.Permission)
	}

	if _, err := runner.Resume(context.Background(), permissionResume(still, "permission-real", PermissionAllow, "")); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseCompleted)
}

func TestPermissionParkDoesNotSpendTheExecutionDuration(t *testing.T) {
	source, commit := testGitSource(t)
	ask := newAskOnce(harness.PermissionRequest{ID: "permission-slow", Tool: "Bash", Input: `{"command":"sleep"}`})
	runner := newTestRunner(t, ask.adapter("session-slow"), source, commit)
	request := permissionStartRequest(commit)
	// One second of WORK is plenty for this adapter, and the park below lasts
	// longer than that. A wall-clock deadline would kill the turn.
	request.Limits.MaxDurationSeconds = 1
	started, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseNeedsInput)
	time.Sleep(1500 * time.Millisecond)

	parked, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if parked.Phase != PhaseNeedsInput || parked.Permission == nil {
		t.Fatalf("receipt = %+v, want it still parked after outliving the duration limit", parked)
	}
	if _, err := runner.Resume(context.Background(), permissionResume(parked, "permission-slow", PermissionAllow, "")); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseCompleted)
}

func TestPermissionRequestIsRefusedWhenMalformed(t *testing.T) {
	source, commit := testGitSource(t)
	// No tool name: there is nothing a person could be shown, so there is
	// nothing to ask.
	ask := newAskOnce(harness.PermissionRequest{ID: "permission-empty", Tool: "   "})
	runner := newTestRunner(t, ask.adapter("session-malformed"), source, commit)
	started, err := runner.Start(context.Background(), permissionStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseFailed)
	failed, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if failed.Permission != nil {
		t.Fatalf("permission = %+v, want none from a malformed request", failed.Permission)
	}
}

// A start that did not opt in gets the behaviour it always had: nothing asks,
// so nothing parks. This is what keeps a coordinator that has never heard of
// permission prompts — Factory — entirely unaffected by them.
func TestAStartThatDidNotOptInIsNeverAsked(t *testing.T) {
	source, commit := testGitSource(t)
	ask := newAskOnce(harness.PermissionRequest{ID: "permission-unused", Tool: "Bash"})
	runner := newTestRunner(t, ask.adapter("session-optout"), source, commit)
	started, err := runner.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The fake adapter fails the turn when it is given no asker, which is how
	// this test sees that none was handed over.
	waitForPhase(t, runner, started.AttemptID, PhaseFailed)
	receipt, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if receipt.Blocker != "no permission asker" {
		t.Fatalf("blocker = %q, want the adapter to have been given no asker", receipt.Blocker)
	}
	if receipt.Permission != nil {
		t.Fatalf("permission = %+v, want none on a start that did not opt in", receipt.Permission)
	}
}
