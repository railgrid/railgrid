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

// RepositorySource tells the runner where to get a repository it does not
// keep a local checkout of. The coordinator sends it with each start; the
// runner clones into a cache it owns under its state directory.
//
// It is dispatch data, not approved attempt input: Start strips it before the
// request is fingerprinted or persisted, so the credential never reaches the
// runner's durable state and a retry carrying a freshly minted one is still
// the same request.
type RepositorySource struct {
	// RemoteURL is the Git URL to clone from. HTTPS and SSH are accepted;
	// the URL itself must never carry embedded credentials.
	RemoteURL string `json:"remoteURL"`
	// Username accompanies Token for HTTPS remotes. Empty means the usual
	// token-bearing placeholder.
	Username string `json:"username,omitempty"`
	// Token is a short-lived read-only credential. It is used for one fetch
	// and is never written to disk, logged, or passed on the command line.
	Token string `json:"token,omitempty"`
}

// HarnessCredential is the caller's harness identity for one attempt.
//
// Like RepositorySource it is DISPATCH DATA, not approved attempt input: Start
// and Resume strip it before the request is fingerprinted or persisted, so it
// never reaches the runner's durable journal and a retry carrying a freshly
// minted value is still the same request. The runner holds it in memory for the
// life of the attempt and hands it to the adapter for each launch.
//
// A runner has no identity of its own. Every start must carry one, which is
// what keeps a machine that hosts a harness from being a shared model account.
type HarnessCredential struct {
	// Kind is one of the harness.Credential kinds: claude-oauth,
	// claude-apikey, codex-auth.
	Kind string `json:"kind"`
	// Value is the credential itself. It is never logged, never written to
	// disk by the runner, and never placed on a command line.
	Value string `json:"value"`
	// Environment is what else the identity brings: further credentials the
	// turn runs with, exported into the harness child's environment under the
	// names given — a GitHub token as GH_TOKEN is the case this exists for. It
	// is dispatch data like Value: stripped with it, held in memory for the
	// attempt, and sent again on every resume. The runner refuses a name an
	// adapter sets itself (HOME, PATH, the harness's own variables, git's
	// configuration and transport), so nothing here can redirect the harness.
	Environment []EnvironmentVariable `json:"environment,omitempty"`
}

// EnvironmentVariable is one name=value an identity brings to a launch.
type EnvironmentVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// StartRequest is the approved execution envelope accepted by Start.
//
// There are two shapes. A REPOSITORY attempt names RepositoryID and BaseCommit
// and runs in a fresh task clone at that commit; this is what a coding
// coordinator sends. A WORKSPACE attempt names WorkspaceID instead and runs in a
// directory the runner keeps across attempts; this is what a conversational
// agent sends, where there is no commit to pin and the working files are the
// point. The two are mutually exclusive.
type StartRequest struct {
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	RequestID       string `json:"requestID"`
	TaskID          string `json:"taskID"`
	AttemptID       string `json:"attemptID"`
	AttemptEpoch    uint64 `json:"attemptEpoch"`
	// RepositoryID and BaseCommit are required unless WorkspaceID is set.
	RepositoryID string `json:"repositoryID,omitempty"`
	BaseCommit   string `json:"baseCommit,omitempty"`
	// WorkspaceID names a directory the runner owns and keeps across
	// attempts, under its managed worktree root. Mutually exclusive with
	// RepositoryID: a workspace has no approved commit to verify.
	WorkspaceID string `json:"workspaceID,omitempty"`
	// SessionID continues an existing harness session in a NEW attempt, which
	// is how consecutive turns of one conversation stay one session. Resuming
	// a parked attempt uses ResumeRequest instead.
	SessionID string `json:"sessionID,omitempty"`
	// HarnessCredential is the identity this attempt runs as. Required, and
	// stripped before anything durable is derived from the request.
	HarnessCredential      *HarnessCredential `json:"harnessCredential,omitempty"`
	Instructions           string             `json:"instructions"`
	Model                  string             `json:"model,omitempty"`
	ApprovedInput          json.RawMessage    `json:"approvedInput,omitempty"`
	RequiredCapabilities   []string           `json:"requiredCapabilities,omitempty"`
	RequiredToolchains     []string           `json:"requiredToolchains,omitempty"`
	RequiredEnvironment    []string           `json:"requiredEnvironment,omitempty"`
	RequiredHarness        string             `json:"requiredHarness,omitempty"`
	RequiredHarnessVersion string             `json:"requiredHarnessVersion,omitempty"`
	// AskPermission opts this attempt into the permission round-trip: a tool
	// call the harness's permission mode does not pre-approve becomes a
	// needs_input on the attempt, carrying Receipt.Permission, instead of being
	// denied where nobody can see it. The resume answers it with a verdict.
	//
	// It is opt-in because answering one is work a coordinator has to do. A
	// coordinator that does not send it — or an older one that has never heard
	// of the field — gets exactly the behaviour it had before: anything that
	// would have prompted is denied and the turn carries on.
	AskPermission   bool `json:"askPermission,omitempty"`
	ExportGitResult bool `json:"exportGitResult,omitempty"`
	// CommitMessage is the subject the exported git result's snapshot commit
	// is stamped with: one line of coordinator-authored public text, such as
	// the ticket's title or "Address review on #7". It is the coordinator's
	// to choose and the coordinator's to verify downstream — nothing the model
	// wrote reaches git metadata. Empty keeps the canonical message.
	CommitMessage string                   `json:"commitMessage,omitempty"`
	Limits        ExecutionLimits          `json:"limits,omitempty"`
	Verification  VerificationRequirements `json:"verification,omitempty"`
	Resources     []ResourceRequest        `json:"resources,omitempty"`
	Artifacts     []ArtifactSpec           `json:"artifacts,omitempty"`
	Repository    *RepositorySource        `json:"repository,omitempty"`
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
//
// It carries the harness credential again rather than relying on one the runner
// kept: a resume may arrive after a runner restart, and nothing about the
// caller's identity survives on the host.
type ResumeRequest struct {
	ProtocolVersion   string             `json:"protocolVersion,omitempty"`
	RequestID         string             `json:"requestID"`
	TaskID            string             `json:"taskID"`
	AttemptID         string             `json:"attemptID"`
	AttemptEpoch      uint64             `json:"attemptEpoch"`
	SessionID         string             `json:"sessionID,omitempty"`
	ClarificationID   string             `json:"clarificationID,omitempty"`
	ApprovedInput     json.RawMessage    `json:"approvedInput,omitempty"`
	Resolution        string             `json:"resolution,omitempty"`
	Instructions      string             `json:"instructions,omitempty"`
	HarnessCredential *HarnessCredential `json:"harnessCredential,omitempty"`
	// PermissionID answers an outstanding Receipt.Permission, and must equal
	// its ID. It is the fence: a verdict that names a different request is
	// refused rather than applied to whatever the harness is waiting on now.
	PermissionID string `json:"permissionID,omitempty"`
	// PermissionDecision is the verdict, PermissionAllow or PermissionDeny. It
	// is required whenever PermissionID is set, and forbidden otherwise.
	//
	// A permission resume does NOT relaunch the harness: the tool call is still
	// open on a child that never stopped, so the verdict is handed to it and
	// the SAME turn continues. Resolution, when present, is the human's own
	// words and is passed to the harness as the reason for a denial.
	PermissionDecision string `json:"permissionDecision,omitempty"`
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

// Clarification is a bounded product question captured from a genuine
// request-user-input harness interaction. Its ID is stable for one session,
// turn, and interaction item and must be echoed by a matching resume.
type Clarification struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// PermissionDecision values are the verdicts a resume may carry. They are
// spelled out rather than being a bool so that "no decision" is a distinct,
// refusable state: a resume that forgot to say must not silently allow.
const (
	PermissionAllow = "allow"
	PermissionDeny  = "deny"
)

// PermissionRequest is a harness permission prompt waiting on a human.
//
// It is NOT a Clarification, and a caller must not treat it as one. A
// clarification is a product question answered with text, which becomes the
// next turn's instructions. A permission request is a named tool call, with the
// input the harness is holding, waiting on a verdict that is delivered back
// into the call that is still open — so the same turn continues.
//
// Its ID is stable for one session and one tool call and must be echoed by the
// matching resume, exactly as a Clarification's is.
type PermissionRequest struct {
	ID   string `json:"id"`
	Tool string `json:"tool"`
	// Input is a bounded JSON rendering of the tool's input, and it is what the
	// verdict authorizes: that call, those arguments.
	Input string `json:"input,omitempty"`
}

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
	ProtocolVersion string         `json:"protocolVersion"`
	TaskID          string         `json:"taskID"`
	AttemptID       string         `json:"attemptID"`
	AttemptEpoch    uint64         `json:"attemptEpoch"`
	Phase           Phase          `json:"phase"`
	SessionID       string         `json:"sessionID,omitempty"`
	Workdir         string         `json:"workdir,omitempty"`
	Cursor          uint64         `json:"cursor"`
	Blocker         string         `json:"blocker,omitempty"`
	Clarification   *Clarification `json:"clarification,omitempty"`
	// Permission is set when the attempt is parked on a permission prompt
	// rather than on a question. At most one of Clarification and Permission is
	// ever set: they are answered differently and a caller has to know which.
	Permission *PermissionRequest `json:"permission,omitempty"`
	Resources  []string           `json:"resources,omitempty"`
	LastError  *Error             `json:"lastError,omitempty"`
	Artifacts  []Artifact         `json:"artifacts,omitempty"`
	AcceptedAt time.Time          `json:"acceptedAt"`
	UpdatedAt  time.Time          `json:"updatedAt"`
}

// ArtifactResponse contains immutable artifact metadata and bytes are served
// by the artifact endpoint. It is kept separate so callers never receive a
// filesystem path.
type ArtifactResponse struct {
	Artifact
}
