// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// step applies cmd, folds the produced events, and returns the new state.
func step(t *testing.T, state SessionState, cmd Command) SessionState {
	t.Helper()
	events, err := Apply(state, cmd, t0)
	if err != nil {
		t.Fatalf("Apply(%T): %v", cmd, err)
	}
	for _, e := range events {
		state = Evolve(state, e)
	}
	return state
}

// mustConflict asserts Apply rejects cmd with ErrConflict.
func mustConflict(t *testing.T, state SessionState, cmd Command) {
	t.Helper()
	_, err := Apply(state, cmd, t0)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Apply(%T) = %v, want ErrConflict", cmd, err)
	}
}

func TestWizardHappyPath(t *testing.T) {
	var s SessionState
	s = step(t, s, CmdCreate{SessionID: "s1", SubmissionID: "sub1", Input: "a todo app for my team"})
	if s.Phase != PhaseIntake || s.PendingInput == "" {
		t.Fatalf("after create: phase=%s pendingInput=%q", s.Phase, s.PendingInput)
	}
	if got := NextAction(s); got != ActionRunIntakeTurn {
		t.Fatalf("NextAction = %s, want %s", got, ActionRunIntakeTurn)
	}

	// Turn 1: engine proposes with a question.
	s = step(t, s, CmdTurnStarted{SubmissionID: "sub1", TurnID: "t1"})
	if s.PendingInput != "" {
		t.Fatalf("turn start must consume pending input")
	}
	if got := NextAction(s); got != ActionAwaitTurn {
		t.Fatalf("NextAction during turn = %s", got)
	}
	bp := Blueprint{
		Title:    "Todo app",
		Template: BlueprintTemplate{Name: "application"},
		Questions: []Question{{
			ID:   "template",
			Text: "Which template?",
			Options: []QuestionOption{
				{Label: "application", Recommended: true},
				{Label: "simple-webapp"},
			},
		}},
	}
	s = step(t, s, CmdBlueprintProposed{SubmissionID: "sub1", Blueprint: bp})
	s = step(t, s, CmdTurnCompleted{TurnID: "t1"})
	if s.Phase != PhaseIntake || len(s.PendingQuestions) != 1 {
		t.Fatalf("after propose w/ questions: phase=%s questions=%d", s.Phase, len(s.PendingQuestions))
	}
	if got := NextAction(s); got != ActionAwaitAnswers {
		t.Fatalf("NextAction = %s, want %s", got, ActionAwaitAnswers)
	}

	// Answers come back; turn 2 converges.
	s = step(t, s, CmdWizardAnswers{SubmissionID: "sub2", Answers: map[string]string{"template": "application"}})
	if got := NextAction(s); got != ActionRunIntakeTurn {
		t.Fatalf("NextAction after answers = %s", got)
	}
	s = step(t, s, CmdTurnStarted{SubmissionID: "sub2", TurnID: "t2"})
	converged := bp
	converged.Questions = nil
	s = step(t, s, CmdBlueprintProposed{SubmissionID: "sub2", Blueprint: converged})
	s = step(t, s, CmdTurnCompleted{TurnID: "t2"})
	if s.Phase != PhaseReview {
		t.Fatalf("after converged propose: phase=%s", s.Phase)
	}
	if got := NextAction(s); got != ActionAwaitApproval {
		t.Fatalf("NextAction = %s, want %s", got, ActionAwaitApproval)
	}

	// Approve → provisioning → studio.
	s = step(t, s, CmdApproveBlueprint{SubmissionID: "sub3"})
	if s.Phase != PhaseProvisioning || !s.Approved {
		t.Fatalf("after approve: phase=%s approved=%v", s.Phase, s.Approved)
	}
	if got := NextAction(s); got != ActionRunProvision {
		t.Fatalf("NextAction = %s, want %s", got, ActionRunProvision)
	}
	s = step(t, s, CmdCheckpointUpdated{Checkpoint: Checkpoint{Name: CheckpointTemplate, State: CheckpointDone}})
	if s.Checkpoints[CheckpointTemplate].State != CheckpointDone {
		t.Fatalf("checkpoint not folded: %+v", s.Checkpoints)
	}
	s = step(t, s, CmdProvisionCompleted{})
	if s.Phase != PhaseStudio {
		t.Fatalf("after provision: phase=%s", s.Phase)
	}
	// Entering studio starts building on its own: the wizard already
	// collected the intent and the user already approved it.
	if got := NextAction(s); got != ActionRunStudioTurn {
		t.Fatalf("NextAction on entering studio = %s, want %s", got, ActionRunStudioTurn)
	}
	if !strings.Contains(s.PendingInput, "Build the first working version") {
		t.Fatalf("kickoff input = %q, want the blueprint restated", s.PendingInput)
	}
	// Once that first turn has run, studio idles waiting for the user.
	s = step(t, s, CmdTurnStarted{SubmissionID: "kickoff", TurnID: "turn-kickoff"})
	s = step(t, s, CmdTurnCompleted{TurnID: "turn-kickoff"})
	if got := NextAction(s); got != ActionAwaitUser {
		t.Fatalf("NextAction in idle studio = %s", got)
	}

	// Studio chat.
	s = step(t, s, CmdUserInput{SubmissionID: "sub4", Text: "add a dark mode"})
	if got := NextAction(s); got != ActionRunStudioTurn {
		t.Fatalf("NextAction with studio input = %s", got)
	}
}

func TestConvergenceCapForcesReview(t *testing.T) {
	var s SessionState
	s = step(t, s, CmdCreate{SessionID: "s1", SubmissionID: "sub1", Input: "an app"})

	withQuestions := Blueprint{
		Title:    "App",
		Template: BlueprintTemplate{Name: "application"},
		Questions: []Question{{
			ID: "q", Text: "?",
			Options: []QuestionOption{{Label: "a", Recommended: true}, {Label: "b"}},
		}},
	}
	// Rounds 1 and 2 keep asking; round 3 hits the cap and must force review.
	for i := 1; i <= MaxProposeIterations; i++ {
		turn := CmdTurnStarted{SubmissionID: "sub", TurnID: "t"}
		s = step(t, s, turn)
		s = step(t, s, CmdBlueprintProposed{SubmissionID: "sub", Blueprint: withQuestions})
		s = step(t, s, CmdTurnCompleted{TurnID: "t"})
		if i < MaxProposeIterations {
			if s.Phase != PhaseIntake {
				t.Fatalf("round %d: phase=%s, want intake", i, s.Phase)
			}
			// Simulate answers so the next round is legal.
			s = step(t, s, CmdWizardAnswers{SubmissionID: "sub", Answers: map[string]string{"q": "a"}})
		}
	}
	if s.Phase != PhaseReview {
		t.Fatalf("after %d rounds: phase=%s, want review (forced)", MaxProposeIterations, s.Phase)
	}
	if len(s.Blueprint.Questions) != 0 {
		t.Fatalf("forced blueprint still carries questions")
	}
}

func TestApplyRejections(t *testing.T) {
	var empty SessionState
	mustConflict(t, empty, CmdCreate{SessionID: "s", SubmissionID: "x", Input: ""})
	mustConflict(t, empty, CmdApproveBlueprint{SubmissionID: "x"})
	mustConflict(t, empty, CmdWizardAnswers{SubmissionID: "x", Answers: map[string]string{"a": "b"}})

	var s SessionState
	s = step(t, s, CmdCreate{SessionID: "s1", SubmissionID: "sub1", Input: "an app"})
	// No form yet.
	mustConflict(t, s, CmdWizardAnswers{SubmissionID: "x", Answers: map[string]string{"a": "b"}})
	// Double create.
	mustConflict(t, s, CmdCreate{SessionID: "s1", SubmissionID: "x", Input: "again"})
	// Intake rejects free chat (initial input already captured at create).
	mustConflict(t, s, CmdUserInput{SubmissionID: "x", Text: "hello"})

	s = step(t, s, CmdTurnStarted{SubmissionID: "sub1", TurnID: "t1"})
	// Only one active turn.
	mustConflict(t, s, CmdTurnStarted{SubmissionID: "x", TurnID: "t2"})
	// Completing the wrong turn.
	mustConflict(t, s, CmdTurnCompleted{TurnID: "not-active"})
	// Turn failure clears the active turn.
	s = step(t, s, CmdTurnFailed{TurnID: "t1", Reason: "model unavailable"})
	if s.ActiveTurnID != "" {
		t.Fatalf("failed turn must clear ActiveTurnID")
	}
}

func TestAdjustLoopsBackToIntake(t *testing.T) {
	var s SessionState
	s = step(t, s, CmdCreate{SessionID: "s1", SubmissionID: "sub1", Input: "an app"})
	s = step(t, s, CmdTurnStarted{SubmissionID: "sub1", TurnID: "t1"})
	s = step(t, s, CmdBlueprintProposed{SubmissionID: "sub1", Blueprint: Blueprint{
		Title: "App", Template: BlueprintTemplate{Name: "application"},
	}})
	s = step(t, s, CmdTurnCompleted{TurnID: "t1"})
	if s.Phase != PhaseReview {
		t.Fatalf("phase=%s, want review", s.Phase)
	}

	s = step(t, s, CmdUserInput{SubmissionID: "sub2", Text: "make it a worker instead"})
	if s.Phase != PhaseIntake || s.PendingInput == "" {
		t.Fatalf("adjust: phase=%s pendingInput=%q", s.Phase, s.PendingInput)
	}
	if got := NextAction(s); got != ActionRunIntakeTurn {
		t.Fatalf("NextAction after adjust = %s", got)
	}
}

func TestFoldRoundTrip(t *testing.T) {
	var s SessionState
	var log []Event
	record := func(cmd Command) {
		events, err := Apply(s, cmd, t0)
		if err != nil {
			t.Fatalf("Apply(%T): %v", cmd, err)
		}
		base := int64(len(log))
		for i, e := range events {
			e.Ordinal = base + int64(i) + 1
			log = append(log, e)
			s = Evolve(s, e)
		}
	}
	record(CmdCreate{SessionID: "s1", SubmissionID: "sub1", Input: "an app"})
	record(CmdTurnStarted{SubmissionID: "sub1", TurnID: "t1"})
	record(CmdBlueprintProposed{SubmissionID: "sub1", Blueprint: Blueprint{
		Title: "App", Template: BlueprintTemplate{Name: "application"},
	}})
	record(CmdTurnCompleted{TurnID: "t1"})
	record(CmdApproveBlueprint{SubmissionID: "sub2"})

	refolded := Fold(log)
	if refolded.Phase != s.Phase || refolded.Approved != s.Approved ||
		refolded.ProposeIterations != s.ProposeIterations ||
		refolded.LastOrdinal != int64(len(log)) {
		t.Fatalf("refold mismatch:\n live=%+v\n fold=%+v", s, refolded)
	}
}

func TestScriptedEngineConverges(t *testing.T) {
	eng := &ScriptedEngine{}
	var s SessionState
	s = step(t, s, CmdCreate{SessionID: "s1", SubmissionID: "sub1", Input: "a todo app"})

	bp, err := eng.IntakeTurn(context.Background(), TurnContext{}, s, s.PendingInput, nil)
	if err != nil {
		t.Fatalf("IntakeTurn: %v", err)
	}
	if len(bp.Questions) != 1 {
		t.Fatalf("first round should ask one question, got %d", len(bp.Questions))
	}
	recommended := 0
	for _, o := range bp.Questions[0].Options {
		if o.Recommended {
			recommended++
		}
	}
	if recommended != 1 {
		t.Fatalf("exactly one option must be recommended, got %d", recommended)
	}

	bp2, err := eng.IntakeTurn(context.Background(), TurnContext{}, s, "", map[string]string{"template": "worker"})
	if err != nil {
		t.Fatalf("IntakeTurn round 2: %v", err)
	}
	if len(bp2.Questions) != 0 || bp2.Template.Name != "worker" {
		t.Fatalf("round 2 should converge on worker, got %+v", bp2)
	}

	if _, err := eng.IntakeTurn(context.Background(), TurnContext{}, s, "", map[string]string{"template": "not-in-catalog"}); err == nil {
		t.Fatalf("hallucinated template must fail validation")
	}
}
