// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package session

import (
	"errors"
	"fmt"
	"time"
)

// SessionState is the fold of a session's event log. It is never persisted as
// authority — rebuild it with Fold whenever needed; stores may cache it.
type SessionState struct {
	ID          string
	ProjectName string
	Phase       Phase

	// PendingInput is user text awaiting an engine turn (initial prompt,
	// adjust feedback, or a studio chat message).
	PendingInput string
	// PendingAnswers are wizard answers awaiting a re-propose turn.
	PendingAnswers map[string]string
	// PendingQuestions is what the user currently sees as a form.
	PendingQuestions []Question

	// PreviewURL is the development instance's public URL once observed.
	PreviewURL string
	// Blueprint is the latest proposal (questions stripped once answered).
	Blueprint *Blueprint
	// ProposeIterations counts blueprint.proposed events (convergence cap).
	ProposeIterations int
	Approved          bool

	// ActiveTurnID is non-empty while an engine turn runs; commands that
	// start turns are rejected until it completes or fails.
	ActiveTurnID string

	Checkpoints map[CheckpointName]Checkpoint

	// LastOrdinal is the highest folded event ordinal — the optimistic
	// concurrency token for appends.
	LastOrdinal int64
	// LastEventAt is the newest folded event's timestamp — used to detect
	// orphaned turns (a replica died mid-turn and nothing will complete it).
	LastEventAt time.Time
}

// Commands. Apply validates each against the current state and returns the
// events it produces; it never mutates state and performs no I/O.

type Command interface{ isCommand() }

// CmdCreate opens a session with the user's initial free-form input.
type CmdCreate struct {
	SessionID    string
	SubmissionID string
	Input        string
}

// CmdUserInput is user text: adjust feedback in review, chat in studio.
type CmdUserInput struct {
	SubmissionID string
	Text         string
}

// CmdWizardAnswers resolves the pending question form.
type CmdWizardAnswers struct {
	SubmissionID string
	Answers      map[string]string
}

// CmdApproveBlueprint accepts the blueprint and starts provisioning.
type CmdApproveBlueprint struct {
	SubmissionID string
}

// CmdTurnStarted / CmdTurnCompleted / CmdTurnFailed bracket one engine turn.
type CmdTurnStarted struct {
	SubmissionID string
	TurnID       string
}
type CmdTurnCompleted struct {
	TurnID string
}
type CmdTurnFailed struct {
	TurnID string
	Reason string
}

// CmdAssistantMessage records completed assistant prose for the transcript.
type CmdAssistantMessage struct {
	SubmissionID string
	Text         string
}

// CmdBlueprintProposed is the engine's propose-tool result.
type CmdBlueprintProposed struct {
	SubmissionID string
	Blueprint    Blueprint
}

// CmdCheckpointUpdated reports provisioning/lifecycle progress.
type CmdCheckpointUpdated struct {
	Checkpoint Checkpoint
}

// CmdProjectCreated records the tenant-workspace Project CR provisioning made.
type CmdProjectCreated struct {
	Name string
}

// CmdPreviewReady records the development instance's observed public URL.
type CmdPreviewReady struct {
	URL string
}

// CmdToolActivity records one completed tool call of the active turn.
type CmdToolActivity struct {
	Activity ToolActivityData
}

// CmdProvisionCompleted moves the session into studio.
type CmdProvisionCompleted struct{}

func (CmdCreate) isCommand()             {}
func (CmdUserInput) isCommand()          {}
func (CmdWizardAnswers) isCommand()      {}
func (CmdApproveBlueprint) isCommand()   {}
func (CmdTurnStarted) isCommand()        {}
func (CmdTurnCompleted) isCommand()      {}
func (CmdTurnFailed) isCommand()         {}
func (CmdAssistantMessage) isCommand()   {}
func (CmdBlueprintProposed) isCommand()  {}
func (CmdCheckpointUpdated) isCommand()  {}
func (CmdProjectCreated) isCommand()     {}
func (CmdPreviewReady) isCommand()       {}
func (CmdToolActivity) isCommand()       {}
func (CmdProvisionCompleted) isCommand() {}

// ErrConflict marks a command that is invalid in the current state (the HTTP
// layer maps it to 409).
var ErrConflict = errors.New("command conflicts with session state")

func conflictf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, args...))
}

// Apply decides what events a command produces against state. now is injected
// for determinism.
func Apply(state SessionState, cmd Command, now time.Time) ([]Event, error) {
	switch c := cmd.(type) {
	case CmdCreate:
		if state.ID != "" {
			return nil, conflictf("session already exists")
		}
		if c.Input == "" {
			return nil, conflictf("initial input is required")
		}
		return []Event{
			NewEvent(c.SessionID, c.SubmissionID, EventSessionCreated, now, SessionCreatedData{Input: c.Input}),
			NewEvent(c.SessionID, c.SubmissionID, EventPhaseChanged, now, PhaseChangedData{Phase: PhaseIntake}),
		}, nil

	case CmdUserInput:
		if c.Text == "" {
			return nil, conflictf("empty input")
		}
		if state.ActiveTurnID != "" {
			return nil, conflictf("a turn is already running")
		}
		switch state.Phase {
		case PhaseReview:
			// Adjust feedback: back to intake for another propose round.
			return []Event{
				NewEvent(state.ID, c.SubmissionID, EventUserMessage, now, MessageData{Text: c.Text}),
				NewEvent(state.ID, c.SubmissionID, EventBlueprintAdjusted, now, MessageData{Text: c.Text}),
				NewEvent(state.ID, c.SubmissionID, EventPhaseChanged, now, PhaseChangedData{Phase: PhaseIntake}),
			}, nil
		case PhaseStudio:
			return []Event{
				NewEvent(state.ID, c.SubmissionID, EventUserMessage, now, MessageData{Text: c.Text}),
			}, nil
		default:
			return nil, conflictf("user input is not accepted in phase %s", state.Phase)
		}

	case CmdWizardAnswers:
		if state.Phase != PhaseIntake {
			return nil, conflictf("no wizard form in phase %s", state.Phase)
		}
		if len(state.PendingQuestions) == 0 {
			return nil, conflictf("no questions pending")
		}
		if state.ActiveTurnID != "" {
			return nil, conflictf("a turn is already running")
		}
		if len(c.Answers) == 0 {
			return nil, conflictf("answers are required")
		}
		return []Event{
			NewEvent(state.ID, c.SubmissionID, EventWizardAnswered, now, WizardAnsweredData{Answers: c.Answers}),
		}, nil

	case CmdApproveBlueprint:
		if state.Phase != PhaseReview {
			return nil, conflictf("no blueprint awaiting approval in phase %s", state.Phase)
		}
		return []Event{
			NewEvent(state.ID, c.SubmissionID, EventBlueprintApproved, now, nil),
			NewEvent(state.ID, c.SubmissionID, EventPhaseChanged, now, PhaseChangedData{Phase: PhaseProvisioning}),
		}, nil

	case CmdTurnStarted:
		if state.ActiveTurnID != "" {
			return nil, conflictf("turn %s is already running", state.ActiveTurnID)
		}
		if c.TurnID == "" {
			return nil, conflictf("turn id is required")
		}
		return []Event{
			NewEvent(state.ID, c.SubmissionID, EventTurnStarted, now, TurnStartedData{TurnID: c.TurnID}),
		}, nil

	case CmdTurnCompleted:
		if state.ActiveTurnID == "" || state.ActiveTurnID != c.TurnID {
			return nil, conflictf("turn %s is not the active turn", c.TurnID)
		}
		return []Event{
			NewEvent(state.ID, "", EventTurnCompleted, now, TurnCompletedData{TurnID: c.TurnID}),
		}, nil

	case CmdTurnFailed:
		if state.ActiveTurnID == "" || state.ActiveTurnID != c.TurnID {
			return nil, conflictf("turn %s is not the active turn", c.TurnID)
		}
		return []Event{
			NewEvent(state.ID, "", EventTurnFailed, now, TurnFailedData{TurnID: c.TurnID, Reason: c.Reason}),
		}, nil

	case CmdAssistantMessage:
		if c.Text == "" {
			return nil, conflictf("empty assistant message")
		}
		return []Event{
			NewEvent(state.ID, c.SubmissionID, EventAssistantMessage, now, MessageData{Text: c.Text}),
		}, nil

	case CmdBlueprintProposed:
		if state.Phase != PhaseIntake {
			return nil, conflictf("blueprint proposals only arrive in intake, not %s", state.Phase)
		}
		bp := c.Blueprint
		if bp.Template.Name == "" {
			return nil, fmt.Errorf("blueprint names no template")
		}
		// Convergence guard: at the cap, outstanding questions are dropped and
		// the blueprint goes to review regardless.
		iteration := state.ProposeIterations + 1
		if iteration >= MaxProposeIterations {
			bp.Questions = nil
		}
		events := []Event{
			NewEvent(state.ID, c.SubmissionID, EventBlueprintProposed, now, bp),
		}
		if len(bp.Questions) == 0 {
			events = append(events,
				NewEvent(state.ID, c.SubmissionID, EventPhaseChanged, now, PhaseChangedData{Phase: PhaseReview}))
		} else {
			events = append(events,
				NewEvent(state.ID, c.SubmissionID, EventWizardQuestions, now, bp.Questions))
		}
		return events, nil

	case CmdCheckpointUpdated:
		if c.Checkpoint.Name == "" || c.Checkpoint.State == "" {
			return nil, fmt.Errorf("checkpoint name and state are required")
		}
		return []Event{
			NewEvent(state.ID, "", EventCheckpointUpdated, now, CheckpointUpdatedData{Checkpoint: c.Checkpoint}),
		}, nil

	case CmdProjectCreated:
		if state.Phase != PhaseProvisioning {
			return nil, conflictf("project creation only happens during provisioning")
		}
		if c.Name == "" {
			return nil, fmt.Errorf("project name is required")
		}
		return []Event{
			NewEvent(state.ID, "", EventProjectCreated, now, ProjectCreatedData{Name: c.Name}),
		}, nil

	case CmdToolActivity:
		if state.ActiveTurnID == "" {
			return nil, conflictf("no turn is running")
		}
		if c.Activity.Tool == "" {
			return nil, fmt.Errorf("tool name is required")
		}
		return []Event{
			NewEvent(state.ID, "", EventToolActivity, now, c.Activity),
		}, nil

	case CmdPreviewReady:
		if c.URL == "" {
			return nil, fmt.Errorf("preview url is required")
		}
		return []Event{
			NewEvent(state.ID, "", EventPreviewReady, now, PreviewReadyData{URL: c.URL}),
		}, nil

	case CmdProvisionCompleted:
		if state.Phase != PhaseProvisioning {
			return nil, conflictf("provisioning is not in progress")
		}
		events := []Event{
			NewEvent(state.ID, "", EventProvisionDone, now, nil),
			NewEvent(state.ID, "", EventPhaseChanged, now, PhaseChangedData{Phase: PhaseStudio}),
		}
		// Start building immediately. The wizard already asked what to build
		// and the user already approved the answer, so waiting for them to
		// type "build it" asks for the same decision twice — and until they
		// do, the sandbox serves the scaffold's hello-world. Emitting the
		// opening instruction as an event (rather than as engine-side special
		// casing) keeps it in the transcript and replayable like any turn.
		if kickoff := KickoffInput(state.Blueprint); kickoff != "" {
			events = append(events, NewEvent(state.ID, "", EventUserMessage, now, MessageData{Text: kickoff}))
		}
		return events, nil

	default:
		return nil, fmt.Errorf("unknown command %T", cmd)
	}
}

// Evolve folds one event into state. It must accept any event Apply can emit
// and must never fail: unknown event types are ignored so old logs replay
// under newer code.
func Evolve(state SessionState, e Event) SessionState {
	switch e.Type {
	case EventSessionCreated:
		var d SessionCreatedData
		_ = DecodeData(e, &d)
		state.ID = e.SessionID
		state.PendingInput = d.Input
	case EventPhaseChanged:
		var d PhaseChangedData
		_ = DecodeData(e, &d)
		state.Phase = d.Phase
	case EventTurnStarted:
		var d TurnStartedData
		_ = DecodeData(e, &d)
		state.ActiveTurnID = d.TurnID
		// The turn consumes whatever input was pending.
		state.PendingInput = ""
		state.PendingAnswers = nil
	case EventTurnCompleted, EventTurnFailed:
		state.ActiveTurnID = ""
	case EventUserMessage:
		var d MessageData
		_ = DecodeData(e, &d)
		state.PendingInput = d.Text
	case EventWizardAnswered:
		var d WizardAnsweredData
		_ = DecodeData(e, &d)
		state.PendingAnswers = d.Answers
		state.PendingQuestions = nil
	case EventWizardQuestions:
		var qs []Question
		_ = DecodeData(e, &qs)
		state.PendingQuestions = qs
	case EventBlueprintProposed:
		var bp Blueprint
		_ = DecodeData(e, &bp)
		state.Blueprint = &bp
		state.ProposeIterations++
	case EventBlueprintApproved:
		state.Approved = true
	case EventBlueprintAdjusted:
		// PendingInput already set by the paired message.user event.
	case EventCheckpointUpdated:
		var d CheckpointUpdatedData
		_ = DecodeData(e, &d)
		if state.Checkpoints == nil {
			state.Checkpoints = map[CheckpointName]Checkpoint{}
		}
		state.Checkpoints[d.Checkpoint.Name] = d.Checkpoint
	case EventProjectCreated:
		var d ProjectCreatedData
		_ = DecodeData(e, &d)
		state.ProjectName = d.Name
	case EventPreviewReady:
		var d PreviewReadyData
		_ = DecodeData(e, &d)
		state.PreviewURL = d.URL
	case EventProvisionDone:
		// Phase change rides the paired phase.changed event.
	}
	if e.Ordinal > state.LastOrdinal {
		state.LastOrdinal = e.Ordinal
	}
	if e.At.After(state.LastEventAt) {
		state.LastEventAt = e.At
	}
	return state
}

// Fold rebuilds state from an ordered event log.
func Fold(events []Event) SessionState {
	var s SessionState
	for _, e := range events {
		s = Evolve(s, e)
	}
	return s
}

// Action names what should happen next. Every tool/HTTP result can surface it,
// and the coordinator executes it — the model never decides what phase it is
// in (docs/vibe-studio-design.md §4.3).
type Action string

const (
	// ActionRunIntakeTurn: pending input or answers await an engine propose turn.
	ActionRunIntakeTurn Action = "run-intake-turn"
	// ActionAwaitAnswers: a question form is in front of the user.
	ActionAwaitAnswers Action = "await-answers"
	// ActionAwaitApproval: the blueprint card is in front of the user.
	ActionAwaitApproval Action = "await-approval"
	// ActionRunProvision: blueprint approved, provisioning work is owed.
	ActionRunProvision Action = "run-provision"
	// ActionRunStudioTurn: studio chat input awaits an engine turn.
	ActionRunStudioTurn Action = "run-studio-turn"
	// ActionAwaitUser: nothing owed; waiting for the user.
	ActionAwaitUser Action = "await-user"
	// ActionAwaitTurn: an engine turn is running; wait for it.
	ActionAwaitTurn Action = "await-turn"
)

// NextAction reads state and names the next move. Priority-ordered, pure.
func NextAction(state SessionState) Action {
	if state.ActiveTurnID != "" {
		return ActionAwaitTurn
	}
	switch state.Phase {
	case PhaseIntake:
		if len(state.PendingAnswers) > 0 || state.PendingInput != "" {
			return ActionRunIntakeTurn
		}
		if len(state.PendingQuestions) > 0 {
			return ActionAwaitAnswers
		}
		return ActionAwaitUser
	case PhaseReview:
		return ActionAwaitApproval
	case PhaseProvisioning:
		return ActionRunProvision
	case PhaseStudio:
		if state.PendingInput != "" {
			return ActionRunStudioTurn
		}
		return ActionAwaitUser
	default:
		return ActionAwaitUser
	}
}
