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
	"encoding/json"
	"sync"
	"time"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	appskills "github.com/faroshq/provider-app-studio/skills"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

// projectAssistantEngine is App Studio's private boundary around assistant
// execution. Eino implementations plug in behind this contract; REST payloads,
// project APIs, and portal state stay App Studio-owned.
type projectAssistantEngine interface {
	StreamProjectAssistant(
		context.Context,
		projectAssistantRunRequest,
	) (projectAssistantRunResult, error)
	ResumeProjectAssistant(
		context.Context,
		projectAssistantRunRequest,
		projectAssistantResumeRequest,
		projectAssistantCheckpointState,
	) (projectAssistantRunResult, error)
}

type projectAssistantRunRequest struct {
	Identity                 identity
	ToolPort                 projectAssistantToolPort
	Client                   *asclient.Client
	Project                  *aiv1alpha1.Project
	Repository               *ProjectRepositoryView
	WorkspaceScope           workspace.Scope
	Workspace                *workspace.FileStore
	MessageScope             store.Scope
	LLM                      projectLLMSettings
	History                  []store.Message
	Conversation             []chatMessage
	ConversationCheckpointed bool
	MCPBaseURL               string
	MCPInsecureSkipTLSVerify bool
	ApprovalMode             store.AssistantApprovalMode
	StreamCallbacks          projectAssistantStreamCallbacks
	CollaborationMode        projectAssistantCollaborationMode
	TurnProfile              projectAssistantTurnProfile
	TurnPolicy               projectAssistantTurnPolicy
	SkillSnapshot            *appskills.Snapshot
	SelectedSkills           []projectAssistantSkillReceipt
	// InitialApprovedPlan is a run-local grant derived from the explicit
	// prompt that created a fresh Project. It is never saved as a cross-turn
	// plan grant; checkpoints retain it only while this initial run is active.
	InitialApprovedPlan *projectAssistantApprovedPlan
	Continuation        *projectAssistantCheckpointState
	AssistantRun        *store.AssistantRun
	// executionAuthority is an App Studio-internal seam for focused engine
	// tests. Production requests leave it nil; it is deliberately not part of
	// any HTTP, provider, or Eino contract.
	executionAuthority projectAssistantExecutionAuthority
	auditRecorder      *projectAssistantRunAuditRecorder
	eventLedger        *projectAssistantRunEventLedger
	// executionContext is the single request-scoped source used by both the
	// model-facing context builder and executable tool wrappers. It prevents a
	// refreshed Project/Repository view from being shown to the model while an
	// older request copy is still used for dispatch.
	executionContext *projectAssistantExecutionContext
	// Steering carries user messages admitted into this durable run while the
	// Eino loop is active. The supervisor persists each message before making it
	// visible here; the loop drains it only at model-safe boundaries.
	Steering     <-chan projectAssistantSteeringInput
	SealSteering func() bool
	// ActivateSteering rotates the public assistant segment only when the Eino
	// loop has reached the model-safe boundary that consumes these queued inputs.
	ActivateSteering func(context.Context, []projectAssistantSteeringInput) error
}

// projectAssistantExecutionContext binds one run's current sampling snapshot
// to its executable tools. snapshotMu protects replacement between model-safe
// boundaries; toolMu is a Codex-style parallel-safety gate where proven reads
// share the read lock and every other call takes exclusive ownership.
type projectAssistantExecutionContext struct {
	snapshotMu sync.RWMutex
	toolMu     sync.RWMutex
	req        projectAssistantRunRequest
}

func projectAssistantRunRequestWithExecutionContext(req projectAssistantRunRequest) projectAssistantRunRequest {
	if req.executionContext != nil {
		return req
	}
	executionContext := &projectAssistantExecutionContext{}
	req.executionContext = executionContext
	executionContext.req = req
	return req
}

func (r projectAssistantRunRequest) currentExecutionRequest() projectAssistantRunRequest {
	if r.executionContext == nil {
		return r
	}
	r.executionContext.snapshotMu.RLock()
	defer r.executionContext.snapshotMu.RUnlock()
	return r.executionContext.req
}

func (r projectAssistantRunRequest) publishExecutionRequest() {
	if r.executionContext == nil {
		return
	}
	r.executionContext.snapshotMu.Lock()
	r.executionContext.req = r
	r.executionContext.snapshotMu.Unlock()
}

type projectAssistantSteeringInput struct {
	MessageID       string
	ClientRequestID string
	Content         string
}

type projectAssistantRunResult struct {
	Content            string
	InitialPlan        *projectAssistantApprovedPlan
	InitialBuild       bool
	CompletionEvidence projectAssistantCompletionEvidence
}

type projectAssistantCompletionEvidence struct {
	PlanDefined               bool     `json:"planDefined"`
	PlanComplete              bool     `json:"planComplete"`
	SourceMutationRevision    uint64   `json:"sourceMutationRevision,omitempty"`
	VerifiedMutationRevision  uint64   `json:"verifiedMutationRevision,omitempty"`
	LatestMutationVerified    bool     `json:"latestMutationVerified"`
	CommitRequired            bool     `json:"commitRequired,omitempty"`
	CommittedMutationRevision uint64   `json:"committedMutationRevision,omitempty"`
	LatestMutationCommitted   bool     `json:"latestMutationCommitted,omitempty"`
	VerificationOutcome       string   `json:"verificationOutcome,omitempty"`
	VerificationSummary       string   `json:"verificationSummary,omitempty"`
	Blockers                  []string `json:"blockers,omitempty"`
}

type projectAssistantEvent struct {
	Type         projectAssistantEventType   `json:"type"`
	ToolCall     *projectAssistantToolCall   `json:"toolCall,omitempty"`
	Permission   *projectAssistantPermission `json:"permission,omitempty"`
	FollowUp     *projectAssistantFollowUp   `json:"followUp,omitempty"`
	Checkpoint   *projectAssistantCheckpoint `json:"checkpoint,omitempty"`
	BuilderEvent *projectBuilderEventView    `json:"builderEvent,omitempty"`
	Delta        string                      `json:"delta,omitempty"`
	Status       string                      `json:"status,omitempty"`
	Error        string                      `json:"error,omitempty"`
}

type projectAssistantEventType string

const (
	projectAssistantEventMessageDelta     projectAssistantEventType = "message_delta"
	projectAssistantEventStatus           projectAssistantEventType = "status"
	projectAssistantEventToolCallStarted  projectAssistantEventType = "tool_call_started"
	projectAssistantEventToolCallFinished projectAssistantEventType = "tool_call_finished"
	projectAssistantEventPermissionNeeded projectAssistantEventType = "permission_required"
	projectAssistantEventInputNeeded      projectAssistantEventType = "input_required"
	projectAssistantEventCheckpointSaved  projectAssistantEventType = "checkpoint_saved"
	projectAssistantEventBuilderEvent     projectAssistantEventType = "builder_event"
	projectAssistantEventRunFailed        projectAssistantEventType = "run_failed"
	projectAssistantEventRunFinished      projectAssistantEventType = "run_finished"
)

type projectAssistantToolCall struct {
	ID         string                        `json:"id"`
	Name       string                        `json:"name"`
	Status     string                        `json:"status,omitempty"`
	Arguments  string                        `json:"arguments,omitempty"`
	Summary    string                        `json:"summary,omitempty"`
	Error      string                        `json:"error,omitempty"`
	Input      json.RawMessage               `json:"input,omitempty"`
	Result     json.RawMessage               `json:"result,omitempty"`
	Exec       *projectAssistantExecMetadata `json:"exec,omitempty"`
	RecoveryOf string                        `json:"recoveryOf,omitempty"`
}

type projectAssistantPermission struct {
	ID         string                        `json:"id"`
	ToolCallID string                        `json:"toolCallID,omitempty"`
	ToolName   string                        `json:"toolName,omitempty"`
	Reason     string                        `json:"reason,omitempty"`
	Input      json.RawMessage               `json:"input,omitempty"`
	Exec       *projectAssistantExecMetadata `json:"exec,omitempty"`
}

type projectAssistantFollowUp struct {
	ID         string                             `json:"id"`
	ToolCallID string                             `json:"toolCallID,omitempty"`
	Questions  []projectAssistantFollowUpQuestion `json:"questions,omitempty"`
	Prompt     string                             `json:"prompt,omitempty"`
}

type projectAssistantCheckpoint struct {
	ID        string     `json:"id"`
	Reason    string     `json:"reason,omitempty"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
}
