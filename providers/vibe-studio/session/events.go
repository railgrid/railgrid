// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Package session is the vibe-studio conversation core: an event-sourced
// wizard/studio session. The append-only event log is the source of truth;
// SessionState is a fold over it (docs/vibe-studio-design.md §4.2). Two pure
// functions own control flow: Apply (command → events) and NextAction
// (state → what should happen next). All I/O — persistence, LLM turns,
// provisioning — lives with the callers; time and IDs are injected so every
// transition is deterministic and unit-testable.
package session

import (
	"encoding/json"
	"fmt"
	"time"
)

// Phase is the session lifecycle phase. Phase changes are host decisions
// recorded as events — the model never infers its phase from the transcript.
type Phase string

const (
	// PhaseIntake collects intent: free-form input plus wizard answers until a
	// blueprint converges. File tools are invisible here.
	PhaseIntake Phase = "intake"
	// PhaseReview shows the converged blueprint and waits for the user's
	// approve/adjust decision.
	PhaseReview Phase = "review"
	// PhaseProvisioning creates the Project, repository, sandbox, and scaffold
	// after approval; progress reports through checkpoint events.
	PhaseProvisioning Phase = "provisioning"
	// PhaseStudio is the vibe loop: chat, file tools, sync, preview.
	PhaseStudio Phase = "studio"
)

// MaxProposeIterations caps blueprint proposal rounds. When the engine still
// has questions at the cap, the questions are dropped and the blueprint is
// forced to review (the kimchi convergence guard).
const MaxProposeIterations = 3

// EventType enumerates the narrow, stable event vocabulary. This is both the
// stored log schema and the SSE projection the portal consumes — new types may
// be added, existing ones never repurposed.
type EventType string

const (
	EventSessionCreated    EventType = "session.created"
	EventPhaseChanged      EventType = "phase.changed"
	EventTurnStarted       EventType = "turn.started"
	EventTurnCompleted     EventType = "turn.completed"
	EventTurnFailed        EventType = "turn.failed"
	EventUserMessage       EventType = "message.user"
	EventAssistantDelta    EventType = "message.delta"
	EventAssistantMessage  EventType = "message.assistant"
	EventToolActivity      EventType = "turn.activity"
	EventWizardQuestions   EventType = "wizard.questions"
	EventWizardAnswered    EventType = "wizard.answered"
	EventBlueprintProposed EventType = "blueprint.proposed"
	EventBlueprintApproved EventType = "blueprint.approved"
	EventBlueprintAdjusted EventType = "blueprint.adjusted"
	EventCheckpointUpdated EventType = "checkpoint.updated"
	EventProjectCreated    EventType = "project.created"
	EventPreviewReady      EventType = "preview.ready"
	EventProvisionDone     EventType = "provision.completed"
)

// Event is one appended log record. Ordinal is assigned by the store at append
// time (0 on freshly-built events) and is the resume/pagination anchor.
// SubmissionID correlates the event back to the client submission that caused
// it; engine-internal events carry the submission that started the turn.
type Event struct {
	SessionID    string          `json:"sessionID"`
	Ordinal      int64           `json:"ordinal"`
	SubmissionID string          `json:"submissionID,omitempty"`
	Type         EventType       `json:"type"`
	At           time.Time       `json:"at"`
	Data         json.RawMessage `json:"data,omitempty"`
}

// Typed event payloads (Event.Data).

type SessionCreatedData struct {
	Input string `json:"input"`
}

type PhaseChangedData struct {
	Phase Phase `json:"phase"`
}

type TurnStartedData struct {
	TurnID string `json:"turnID"`
}

type TurnCompletedData struct {
	TurnID string `json:"turnID"`
}

type TurnFailedData struct {
	TurnID string `json:"turnID"`
	Reason string `json:"reason"`
}

type MessageData struct {
	Text string `json:"text"`
}

type WizardAnsweredData struct {
	// Answers maps question ID → chosen option label or free text.
	Answers map[string]string `json:"answers"`
}

type CheckpointUpdatedData struct {
	Checkpoint Checkpoint `json:"checkpoint"`
}

type ProjectCreatedData struct {
	// Name is the Project CR's metadata.name in the tenant workspace.
	Name string `json:"name"`
}

type PreviewReadyData struct {
	// URL is the development instance's public URL (instance status.url).
	URL string `json:"url"`
}

// ToolActivityData records one completed tool call inside a turn — the
// durable "what is the model doing" trail the portal renders live and keeps
// in history.
type ToolActivityData struct {
	Tool string `json:"tool"`
	// Detail is a short human hint (file path, component, template name).
	Detail     string `json:"detail,omitempty"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"durationMS,omitempty"`
}

// Blueprint is the wizard's converged output: what will be created and why.
// It is the payload of blueprint.proposed events and the contract of the
// engine's propose tool (docs/vibe-studio-design.md §5).
type Blueprint struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	// Template names an infrastructure Template that must exist in the
	// tenant's catalog right now; hallucinated names fail validation upstream.
	Template BlueprintTemplate `json:"template"`
	// Values are template input values derived from answers.
	Values map[string]any `json:"values,omitempty"`
	// Integrations the blueprint needs (github connection, secrets).
	Integrations []BlueprintIntegration `json:"integrations,omitempty"`
	Assumptions  []string               `json:"assumptions,omitempty"`
	// SuccessCriteria are user-visible, testable statements.
	SuccessCriteria []string `json:"successCriteria,omitempty"`
	// Questions non-empty keeps the session in intake: the host renders the
	// form, answers loop back as one wizard.answered submission.
	Questions []Question `json:"questions,omitempty"`
}

type BlueprintTemplate struct {
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
}

type BlueprintIntegration struct {
	Kind string `json:"kind"`
	// Status is "connected" or "needed".
	Status string `json:"status,omitempty"`
}

// Question is one decision-blocking clarification. Options are single concrete
// choices; exactly one is recommended. The host appends the free-text escape
// hatch — the engine must not.
type Question struct {
	ID      string           `json:"id"`
	Text    string           `json:"text"`
	Options []QuestionOption `json:"options"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Recommended bool   `json:"recommended,omitempty"`
}

// CheckpointName enumerates the four lifecycle checkpoints (inherited from
// app-studio's project_checkpoints model — the product's progress UI).
type CheckpointName string

const (
	CheckpointTemplate   CheckpointName = "template"
	CheckpointGit        CheckpointName = "git"
	CheckpointCI         CheckpointName = "ci"
	CheckpointProduction CheckpointName = "production"
)

// CheckpointState is one checkpoint's observed condition.
type CheckpointState string

const (
	CheckpointPending CheckpointState = "pending"
	CheckpointDone    CheckpointState = "done"
	CheckpointBlocked CheckpointState = "blocked"
	CheckpointError   CheckpointState = "error"
)

type Checkpoint struct {
	Name   CheckpointName  `json:"name"`
	State  CheckpointState `json:"state"`
	Reason string          `json:"reason,omitempty"`
}

// NewEvent builds an unappended event with a marshaled payload. Panics only on
// unmarshalable payloads, which are programmer errors (all payload types above
// are plain data).
func NewEvent(sessionID, submissionID string, t EventType, at time.Time, payload any) Event {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			panic(fmt.Sprintf("session: marshal %s payload: %v", t, err))
		}
		raw = b
	}
	return Event{SessionID: sessionID, SubmissionID: submissionID, Type: t, At: at.UTC(), Data: raw}
}

// DecodeData unmarshals an event payload into out.
func DecodeData(e Event, out any) error {
	if len(e.Data) == 0 {
		return fmt.Errorf("event %s has no data", e.Type)
	}
	return json.Unmarshal(e.Data, out)
}
