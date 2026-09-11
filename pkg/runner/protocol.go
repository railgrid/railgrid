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

package runner

import (
	"encoding/json"
	"time"
)

// ProtocolVersion is the wire protocol implemented by the local runner.
const ProtocolVersion = "runner/v1"

// Phase describes the durable lifecycle of an attempt.
type Phase string

const (
	PhaseAccepted   Phase = "accepted"
	PhaseStarting   Phase = "starting"
	PhaseRunning    Phase = "running"
	PhaseNeedsInput Phase = "needs_input"
	PhaseCancelling Phase = "cancelling"
	PhaseCancelled  Phase = "cancelled"
	PhaseCompleted  Phase = "completed"
	PhaseFailed     Phase = "failed"
)

// IsTerminal reports whether p no longer has a live execution.
func (p Phase) IsTerminal() bool {
	switch p {
	case PhaseCancelled, PhaseCompleted, PhaseFailed:
		return true
	default:
		return false
	}
}

// EventType values are the bounded, replayable events exposed by the runner.
const (
	EventAccepted   = "accepted"
	EventStarted    = "started"
	EventProgress   = "progress"
	EventCheckpoint = "checkpoint"
	EventNeedsInput = "needs_input"
	EventArtifact   = "artifact"
	EventCompleted  = "completed"
	EventFailed     = "failed"
	EventCancelled  = "cancelled"
)

// ErrorCode values are stable protocol errors. Callers should branch on Code,
// not on Message.
const (
	ErrorInvalidRequest        = "invalid_request"
	ErrorUnauthorized          = "unauthorized"
	ErrorForbidden             = "forbidden"
	ErrorUnsupportedVersion    = "unsupported_version"
	ErrorUnsupportedCapability = "unsupported_capability"
	ErrorBusy                  = "busy"
	ErrorStaleAttempt          = "stale_attempt"
	ErrorIdempotencyConflict   = "idempotency_conflict"
	ErrorCheckpointUnavailable = "checkpoint_unavailable"
	ErrorCursorExpired         = "cursor_expired"
	ErrorUnavailable           = "unavailable"
)

// HarnessCapability describes an installed harness and its readiness.
type HarnessCapability struct {
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Ready   bool     `json:"ready"`
	Reasons []string `json:"reasons,omitempty"`
}

// Capacity is the execution capacity advertised by a runner.
type Capacity struct {
	Maximum int `json:"maximum"`
	Used    int `json:"used"`
}

// Capabilities is returned by GET /runner/v1/capabilities.
type Capabilities struct {
	ProtocolVersion string              `json:"protocolVersion"`
	RunnerID        string              `json:"runnerID"`
	Version         string              `json:"version"`
	OS              string              `json:"os"`
	Architecture    string              `json:"architecture"`
	Harnesses       []HarnessCapability `json:"harnesses,omitempty"`
	Toolchains      []string            `json:"toolchains,omitempty"`
	Environment     []string            `json:"environment,omitempty"`
	Verification    []string            `json:"verificationCapabilities,omitempty"`
	Capacity        Capacity            `json:"capacity"`
	Ready           bool                `json:"ready"`
	Reasons         []string            `json:"reasons,omitempty"`
}

// ExecutionLimits bounds adapter execution. The runner enforces duration and
// emitted-output limits; MaxTurns is limited to one because one adapter launch
// represents one turn in this protocol revision.
type ExecutionLimits struct {
	MaxDurationSeconds int `json:"maxDurationSeconds,omitempty"`
	MaxOutputBytes     int `json:"maxOutputBytes,omitempty"`
	MaxTurns           int `json:"maxTurns,omitempty"`
}

// VerificationRequirements records the checks approved for an attempt.
type VerificationRequirements struct {
	Names    []string `json:"names,omitempty"`
	Commands []string `json:"commands,omitempty"`
}

// ResourceRequest identifies a preconfigured shared resource. Resource values
// are names only: the runner never provisions ports, containers, or clusters.
type ResourceRequest struct {
	Name string `json:"name"`
}

// ArtifactSpec is an approved logical artifact. Path is relative to the task
// worktree and is only used when the adapter reports an artifact event.
type ArtifactSpec struct {
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	Digest    string `json:"digest,omitempty"`
}

// StartRequest is the approved execution envelope accepted by Start.
type StartRequest struct {
	ProtocolVersion        string                   `json:"protocolVersion,omitempty"`
	RequestID              string                   `json:"requestID"`
	TaskID                 string                   `json:"taskID"`
	AttemptID              string                   `json:"attemptID"`
	AttemptEpoch           uint64                   `json:"attemptEpoch"`
	RepositoryID           string                   `json:"repositoryID"`
	BaseCommit             string                   `json:"baseCommit"`
	Instructions           string                   `json:"instructions"`
	Model                  string                   `json:"model,omitempty"`
	ApprovedInput          json.RawMessage          `json:"approvedInput,omitempty"`
	RequiredCapabilities   []string                 `json:"requiredCapabilities,omitempty"`
	RequiredToolchains     []string                 `json:"requiredToolchains,omitempty"`
	RequiredEnvironment    []string                 `json:"requiredEnvironment,omitempty"`
	RequiredHarness        string                   `json:"requiredHarness,omitempty"`
	RequiredHarnessVersion string                   `json:"requiredHarnessVersion,omitempty"`
	Limits                 ExecutionLimits          `json:"limits,omitempty"`
	Verification           VerificationRequirements `json:"verification,omitempty"`
	Resources              []ResourceRequest        `json:"resources,omitempty"`
	Artifacts              []ArtifactSpec           `json:"artifacts,omitempty"`
}

// CancelRequest requests cancellation of an active attempt.
type CancelRequest struct {
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	RequestID       string `json:"requestID"`
	TaskID          string `json:"taskID"`
	AttemptID       string `json:"attemptID"`
	AttemptEpoch    uint64 `json:"attemptEpoch"`
}

// ResumeRequest continues a needs-input attempt in its existing session.
type ResumeRequest struct {
	ProtocolVersion string          `json:"protocolVersion,omitempty"`
	RequestID       string          `json:"requestID"`
	TaskID          string          `json:"taskID"`
	AttemptID       string          `json:"attemptID"`
	AttemptEpoch    uint64          `json:"attemptEpoch"`
	SessionID       string          `json:"sessionID,omitempty"`
	ApprovedInput   json.RawMessage `json:"approvedInput,omitempty"`
	Resolution      string          `json:"resolution,omitempty"`
	Instructions    string          `json:"instructions,omitempty"`
}

// Error describes a structured protocol failure.
type Error struct {
	Code             string   `json:"code"`
	Retryable        bool     `json:"retryable"`
	Message          string   `json:"message"`
	Receipt          *Receipt `json:"receipt,omitempty"`
	SnapshotRequired bool     `json:"snapshotRequired,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Event is an ordered, durable attempt event. Data is bounded by the runner
// before the event is persisted.
type Event struct {
	Cursor       uint64          `json:"cursor"`
	Timestamp    time.Time       `json:"timestamp"`
	AttemptEpoch uint64          `json:"attemptEpoch"`
	Type         string          `json:"type"`
	Message      string          `json:"message,omitempty"`
	Data         json.RawMessage `json:"data,omitempty"`
}

// Artifact is an immutable, named result reference.
type Artifact struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Digest    string    `json:"digest"`
	Length    int64     `json:"length"`
	MediaType string    `json:"mediaType"`
	CreatedAt time.Time `json:"createdAt"`
}

// Receipt is the durable view of an attempt returned by start and inspect.
type Receipt struct {
	ProtocolVersion string     `json:"protocolVersion"`
	TaskID          string     `json:"taskID"`
	AttemptID       string     `json:"attemptID"`
	AttemptEpoch    uint64     `json:"attemptEpoch"`
	Phase           Phase      `json:"phase"`
	SessionID       string     `json:"sessionID,omitempty"`
	Workdir         string     `json:"workdir,omitempty"`
	Cursor          uint64     `json:"cursor"`
	Blocker         string     `json:"blocker,omitempty"`
	Resources       []string   `json:"resources,omitempty"`
	LastError       *Error     `json:"lastError,omitempty"`
	Artifacts       []Artifact `json:"artifacts,omitempty"`
	AcceptedAt      time.Time  `json:"acceptedAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// ArtifactResponse contains immutable artifact metadata and bytes are served
// by the artifact endpoint. It is kept separate so callers never receive a
// filesystem path.
type ArtifactResponse struct {
	Artifact
}
