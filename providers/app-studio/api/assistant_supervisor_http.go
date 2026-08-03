// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/google/uuid"
	"github.com/gorilla/mux"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

type projectAssistantRunStartResponse struct {
	Run       projectAssistantRunView    `json:"run"`
	User      *aiv1alpha1.ProjectMessage `json:"user,omitempty"`
	Assistant aiv1alpha1.ProjectMessage  `json:"assistant"`
}

// projectAssistantRunView is the public run contract. Durable execution
// records also contain checkpoint, audit, grant, and project-scoping state that
// must remain server-side.
type projectAssistantRunView struct {
	ID              string                        `json:"id"`
	Mode            store.AssistantRunMode        `json:"mode,omitempty"`
	ApprovalMode    store.AssistantApprovalMode   `json:"approvalMode,omitempty"`
	Status          store.AssistantRunStatus      `json:"status"`
	ClientRequestID string                        `json:"clientRequestID,omitempty"`
	UserMessageID   string                        `json:"userMessageID,omitempty"`
	ActiveMessageID string                        `json:"activeMessageID,omitempty"`
	Revision        int64                         `json:"revision,omitempty"`
	RequestID       string                        `json:"requestID,omitempty"`
	Error           *projectAssistantRunErrorView `json:"error,omitempty"`
	AbortReason     store.AssistantRunAbortReason `json:"abortReason,omitempty"`
	CreatedAt       time.Time                     `json:"createdAt"`
	UpdatedAt       time.Time                     `json:"updatedAt"`
}

type projectAssistantRunErrorView struct {
	Message   string `json:"message"`
	ErrorInfo string `json:"errorInfo,omitempty"`
}

type projectAssistantRunSnapshotResponse struct {
	Run     projectAssistantRunView   `json:"run"`
	Message aiv1alpha1.ProjectMessage `json:"message"`
}

func projectAssistantRunToAPI(run store.AssistantRun) projectAssistantRunView {
	var terminalError *projectAssistantRunErrorView
	if len(run.Error) > 0 && string(run.Error) != "{}" {
		var decoded projectAssistantRunErrorView
		if json.Unmarshal(run.Error, &decoded) == nil && strings.TrimSpace(decoded.Message) != "" {
			terminalError = &decoded
		}
	}
	return projectAssistantRunView{
		ID:              run.ID,
		Mode:            run.Mode,
		ApprovalMode:    run.ApprovalMode,
		Status:          run.Status,
		ClientRequestID: run.ClientRequestID,
		UserMessageID:   run.UserMessageID,
		ActiveMessageID: run.ActiveMessageID,
		Revision:        run.Revision,
		RequestID:       run.RequestID,
		Error:           terminalError,
		AbortReason:     run.AbortReason,
		CreatedAt:       run.CreatedAt,
		UpdatedAt:       run.UpdatedAt,
	}
}

func projectAssistantRunSnapshotToAPI(snapshot projectAssistantRunSnapshot) projectAssistantRunSnapshotResponse {
	return projectAssistantRunSnapshotResponse{
		Run:     projectAssistantRunToAPI(snapshot.Run),
		Message: projectMessageToAPI(snapshot.Message),
	}
}

type projectAssistantDurableStartResult struct {
	Run       store.AssistantRun
	User      store.Message
	Assistant store.Message
	Started   bool
}

// projectAssistantDurableFinalContent makes the engine's returned response
// authoritative when present. Chunk callbacks are progressive UI snapshots;
// they can be empty or partial and must never truncate or duplicate the final
// durable assistant message.
func projectAssistantDurableFinalContent(reply, streamed string) string {
	return projectAssistantStoredContent(reply, streamed)
}

func projectAssistantDurableTerminalContent(reply, streamed string, err error) string {
	return projectAssistantDurableFinalContent(reply, streamed)
}

func (s *Server) projectAssistantRunTerminalContent(
	ctx context.Context,
	scope store.Scope,
	run store.AssistantRun,
	reply, streamed string,
	err error,
	evidence projectAssistantCompletionEvidence,
	engineCompleted bool,
) string {
	return projectAssistantDurableFinalContent(reply, streamed)
}

func projectAssistantGroundPreviewTerminalContent(content string, toolCalls []projectToolCallStreamEvent) string {
	return content
}

func appendProjectAssistantAuthoritativeStatus(content, status string) string {
	content = strings.TrimSpace(content)
	status = strings.TrimSpace(status)
	if status == "" || strings.Contains(content, status) {
		return content
	}
	if content == "" {
		return status
	}
	return content + "\n\n" + status
}

func projectAssistantTerminalPlanGrounded(
	plan *projectAssistantPlanSnapshot,
	toolCalls []projectToolCallStreamEvent,
	evidence projectAssistantCompletionEvidence,
) bool {
	return plan != nil && evidence.PlanComplete
}

func projectAssistantLatestToolCallSucceeded(toolCalls []projectToolCallStreamEvent, name string) bool {
	for index := len(toolCalls) - 1; index >= 0; index-- {
		if projectToolBaseName(toolCalls[index].Name) != projectToolBaseName(name) {
			continue
		}
		return projectAssistantActionFeedItemStatus(toolCalls[index].Status) == projectAssistantActionFeedStatusSucceeded
	}
	return false
}

type projectAssistantDurableTerminalEffectCall struct {
	toolName   string
	argsDigest string
}

func (s *Server) projectAssistantSuccessfulTerminalEffectWithoutSourceEvidence(
	ctx context.Context,
	scope store.Scope,
	run store.AssistantRun,
	evidence projectAssistantCompletionEvidence,
	engineCompleted bool,
) bool {
	if !engineCompleted || evidence.SourceMutationRevision > 0 {
		return false
	}
	effect, err := s.projectAssistantSuccessfulNonSourceRunEffect(ctx, scope, run.ID)
	if err != nil {
		// If durable effect evidence cannot be read, fail closed on response
		// truthfulness: the disclosure makes no positive claim and is safer
		// than preserving a model-authored behavioral overclaim.
		return true
	}
	return effect
}

func (s *Server) projectAssistantSuccessfulNonSourceRunEffect(
	ctx context.Context,
	scope store.Scope,
	runID string,
) (bool, error) {
	if s == nil || s.store == nil || strings.TrimSpace(runID) == "" {
		return false, errors.New("assistant run event store is not configured")
	}
	registry := projectAssistantLocalToolRegistry(nil)
	calls := map[string]projectAssistantDurableTerminalEffectCall{}
	afterSequence := int64(0)
	for {
		events, err := s.store.ListAssistantRunEvents(
			ctx,
			scope,
			runID,
			afterSequence,
			projectAssistantRunEventPageSize,
		)
		if err != nil {
			return false, err
		}
		for _, event := range events {
			switch event.Type {
			case projectAssistantRunToolCallEventType:
				var payload projectAssistantRunToolCallPayload
				if json.Unmarshal(event.Payload, &payload) != nil || !payload.Effect ||
					!projectAssistantTerminalToolMutates(registry, event.ToolName) {
					continue
				}
				calls[event.CallID] = projectAssistantDurableTerminalEffectCall{
					toolName:   event.ToolName,
					argsDigest: event.ArgsDigest,
				}
			case projectAssistantRunToolResultEventType:
				call, ok := calls[event.CallID]
				if !ok || call.toolName != event.ToolName || call.argsDigest != event.ArgsDigest {
					continue
				}
				var payload projectAssistantRunToolResultPayload
				if json.Unmarshal(event.Payload, &payload) != nil {
					continue
				}
				if projectAssistantTerminalEffectResultSucceeded(call.toolName, payload) {
					return true, nil
				}
			}
		}
		if len(events) < projectAssistantRunEventPageSize {
			return false, nil
		}
		nextSequence := events[len(events)-1].Sequence
		if nextSequence <= afterSequence {
			return false, errors.New("assistant run event pagination did not advance")
		}
		afterSequence = nextSequence
	}
}

func projectAssistantTerminalToolMutates(
	registry projectAssistantToolRegistry,
	name string,
) bool {
	spec, ok := registry.Spec(name)
	if !ok {
		spec, ok = projectAssistantWorkflowToolSpec(name)
	}
	if !ok {
		spec, ok = projectAssistantMCPToolSpec(projectMCPTool{Name: name})
	}
	if !ok {
		return false
	}
	switch spec.Risk {
	case projectAssistantToolRiskWrite, projectAssistantToolRiskRuntime, projectAssistantToolRiskCommit:
		return true
	default:
		return false
	}
}

func projectAssistantTerminalEffectResultSucceeded(
	name string,
	payload projectAssistantRunToolResultPayload,
) bool {
	if payload.Disposition != "" {
		return payload.Disposition == projectAssistantToolDispositionSucceeded
	}
	var invokeErr error
	if payload.Failed {
		invokeErr = errors.New(payload.Error)
	}
	return projectAssistantToolResultDisposition(name, payload.Result, invokeErr) == projectAssistantToolDispositionSucceeded
}

// appendProjectAssistantStreamBlock keeps complete assistant updates readable
// while a tool-driven turn is running. These are accepted assistant prose
// blocks, not token deltas or reasoning content; the final returned response
// remains authoritative for the durable terminal message.
func appendProjectAssistantStreamBlock(content *strings.Builder, block string) string {
	block = strings.TrimSpace(block)
	if block == "" {
		return content.String()
	}
	current := strings.TrimSpace(content.String())
	if current == block || strings.HasSuffix(current, "\n\n"+block) {
		return content.String()
	}
	if content.Len() > 0 {
		content.WriteString("\n\n")
	}
	content.WriteString(block)
	return content.String()
}

// startProjectAssistantRunDurably is the one start boundary for every new
// conversation turn. It validates its durable inputs, reserves the project,
// atomically creates the user message, assistant placeholder and run, then
// hands the run to a server-owned worker. It deliberately accepts no response
// writer and never derives execution from the caller's request context.
func (s *Server) startProjectAssistantRunDurablyWithMode(ctx context.Context, scope store.Scope, actor, content, clientRequestID string, mode store.AssistantRunMode, start func(store.AssistantRun, store.Message, bool) error) (projectAssistantDurableStartResult, error) {
	content = strings.TrimSpace(content)
	clientRequestID = strings.TrimSpace(clientRequestID)
	actor = strings.TrimSpace(actor)
	if content == "" || clientRequestID == "" || actor == "" {
		return projectAssistantDurableStartResult{}, newValidationError("content, clientRequestID, and actor are required")
	}
	if mode != store.AssistantRunModeDefault && mode != store.AssistantRunModePlan && mode != store.AssistantRunModeReview {
		return projectAssistantDurableStartResult{}, newValidationError("collaborationMode must be default, plan, or review")
	}
	if err := s.reconcileOrphanedProjectAssistantRun(ctx, scope); err != nil {
		return projectAssistantDurableStartResult{}, err
	}
	if prior, err := s.store.FindAssistantRunByClientRequestID(ctx, scope, clientRequestID); err == nil {
		if err := validateProjectAssistantStartReplay(prior, actor, content, mode); err != nil {
			return projectAssistantDurableStartResult{}, err
		}
		return projectAssistantDurableStartResult{Run: prior}, nil
	} else if !errors.Is(err, store.ErrAssistantRunNotFound) {
		return projectAssistantDurableStartResult{}, err
	}
	supervisor := s.projectAssistantSupervisor()
	releaseReservation, err := supervisor.Reserve(scope)
	if err != nil {
		return projectAssistantDurableStartResult{}, err
	}
	defer releaseReservation()
	messages, err := s.store.ListMessages(ctx, scope, 1, "")
	if err != nil {
		return projectAssistantDurableStartResult{}, err
	}
	transcriptEmpty := len(messages.Items) == 0
	now := time.Now().UTC()
	assistantAt := now.Add(time.Microsecond)
	user := store.Message{ID: newMessageID(), Role: aiv1alpha1.ProjectMessageRoleUser, ActorID: actor, Content: content, CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: newMessageID(), Role: aiv1alpha1.ProjectMessageRoleAssistant, CreatedAt: assistantAt, UpdatedAt: assistantAt}
	run := store.AssistantRun{ID: "run-" + uuid.NewString(), Mode: mode, Status: store.AssistantRunStatusRunning, ClientRequestID: clientRequestID, UserMessageID: user.ID, ActiveMessageID: assistant.ID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.captureProjectAssistantApprovalMode(ctx, scope, actor, &run); err != nil {
		return projectAssistantDurableStartResult{}, err
	}
	if err := bindProjectAssistantStartRequest(&run, actor, content); err != nil {
		return projectAssistantDurableStartResult{}, err
	}
	assistant.Metadata = projectAssistantDurableMetadataForTransition(run, "Working", false, false, nil, nil)
	created, err := s.store.CreateAssistantRun(ctx, scope, user, assistant, run)
	if err != nil {
		if prior, ok := s.recoverProjectAssistantStartReplay(ctx, scope, err, clientRequestID, actor, content, mode); ok {
			return projectAssistantDurableStartResult{Run: prior}, nil
		}
		return projectAssistantDurableStartResult{}, err
	}
	if created.ID != run.ID {
		if err := validateProjectAssistantStartReplay(created, actor, content, mode); err != nil {
			return projectAssistantDurableStartResult{}, err
		}
		return projectAssistantDurableStartResult{Run: created}, nil
	}
	if err := appendProjectAssistantConversationMessage(ctx, s.store, scope, created.ID, "message-"+user.ID, projectAssistantConversationUser, chatMessage{Role: "user", Content: user.Content}); err != nil {
		persistErr := fmt.Errorf("persist assistant conversation user item: %w", err)
		failedRun := created
		failedMessage := assistant
		failedRun.Status = store.AssistantRunStatusFailed
		failedRun.Error = projectAssistantRunErrorJSON(persistErr, "internal_server_error")
		failedRun.Revision++
		failedRun.UpdatedAt = time.Now().UTC()
		failedMessage.Metadata = projectAssistantDurableMetadataForTransition(failedRun, "Failed", false, false, nil, nil)
		failedMessage.UpdatedAt = failedRun.UpdatedAt
		if saveErr := s.store.SaveAssistantRunSnapshot(ctx, scope, failedRun, []store.Message{failedMessage}, created.Revision); saveErr != nil {
			return projectAssistantDurableStartResult{Run: created, User: user, Assistant: assistant}, errors.Join(persistErr, fmt.Errorf("terminalize assistant run after conversation persistence failure: %w", saveErr))
		}
		return projectAssistantDurableStartResult{Run: failedRun, User: user, Assistant: failedMessage}, persistErr
	}
	if err := start(created, assistant, transcriptEmpty); err != nil {
		failedRun, failedMessage, compensateErr := s.compensateProjectAssistantStartFailure(ctx, scope, created, assistant, err)
		if compensateErr != nil {
			return projectAssistantDurableStartResult{Run: failedRun, User: user, Assistant: failedMessage}, errors.Join(err, compensateErr)
		}
		return projectAssistantDurableStartResult{Run: failedRun, User: user, Assistant: failedMessage}, err
	}
	return projectAssistantDurableStartResult{Run: created, User: user, Assistant: assistant, Started: true}, nil
}

// compensateProjectAssistantStartFailure closes a generic assistant run when
// its caller cannot complete the provider-specific startup boundary (for
// example, before a canonical thread turn or worker is attached). Without
// this transition the retry identity would point at a permanently running
// orphan until a later reconciliation pass happened to observe it.
func (s *Server) compensateProjectAssistantStartFailure(ctx context.Context, scope store.Scope, run store.AssistantRun, message store.Message, startErr error) (store.AssistantRun, store.Message, error) {
	if assistantRunTerminal(run.Status) {
		return run, message, nil
	}
	if startErr == nil {
		startErr = errors.New("assistant run startup failed")
	}
	run.Status = store.AssistantRunStatusFailed
	run.Error = projectAssistantRunErrorJSON(startErr, "internal_server_error")
	run.Revision++
	run.UpdatedAt = time.Now().UTC()
	message.UpdatedAt = run.UpdatedAt
	message.Metadata = projectAssistantDurableMetadataForTransition(run, "Failed", false, false, nil, nil)
	if err := s.store.SaveAssistantRunSnapshot(ctx, scope, run, []store.Message{message}, run.Revision-1); err != nil {
		return run, message, fmt.Errorf("compensate assistant start failure: %w", err)
	}
	return run, message, nil
}

type projectAssistantSupervisorRunContextKey struct{}

const (
	projectAssistantMetadataRunID                = "assistantRunID"
	projectAssistantMetadataRevision             = "assistantRevision"
	projectAssistantMetadataWorkingStatus        = "assistantStatus"
	projectAssistantMetadataProvisional          = "assistantProvisional"
	projectAssistantMetadataPreviewRefreshNeeded = "previewRefreshNeeded"
	projectAssistantMetadataPlan                 = "assistantPlan"
	projectAssistantMetadataInitialBuild         = "assistantInitialBuild"
	projectAssistantMetadataProgress             = "assistantProgress"
	projectAssistantProgressMaxMessages          = 32
	projectAssistantWorkedDurationMaxMS          = int64((7 * 24 * time.Hour) / time.Millisecond)
	projectAssistantTraceMaxSequence             = 10_000
)

type projectAssistantProgressSnapshot struct {
	Version          int      `json:"version"`
	Messages         []string `json:"messages"`
	MessageSequences []int    `json:"messageSequences"`
	WorkedDurationMS int64    `json:"workedDurationMs"`
}

func projectAssistantDurableMetadataForTransition(run store.AssistantRun, status string, provisional, preview bool, toolCalls []projectToolCallStreamEvent, plan *projectAssistantPlanSnapshot) map[string]any {
	metadata := projectAssistantMessageMetadata(status, sanitizeProjectToolCallStreamEventsForMetadata(toolCalls))
	metadata[projectAssistantMetadataRunID] = run.ID
	metadata[projectAssistantMetadataRevision] = run.Revision
	metadata[projectAssistantMetadataWorkingStatus] = status
	metadata[projectAssistantMetadataProvisional] = provisional
	metadata[projectAssistantMetadataPreviewRefreshNeeded] = preview
	if plan, ok := projectAssistantPlanSnapshotFromMetadata(plan); ok {
		metadata[projectAssistantMetadataPlan] = *plan
	}
	return metadata
}

func projectAssistantDurableMetadataFromExisting(run store.AssistantRun, status string, provisional bool, existing map[string]any) map[string]any {
	metadata := map[string]any{}
	actions := projectAssistantActionFeedFromMetadata(existing[projectMessageMetadataAssistantActionFeed])
	if assistantRunTerminal(run.Status) {
		actions = finalizeProjectAssistantActionFeed(actions, run.Status)
	}
	if len(actions) > 0 {
		metadata[projectMessageMetadataAssistantActionFeed] = actions
	}
	if !assistantRunTerminal(run.Status) {
		if interrupt := projectAssistantUIInterruptFromMetadata(existing[projectMessageMetadataAssistantInterrupt]); interrupt != nil {
			metadata[projectMessageMetadataAssistantInterrupt] = interrupt
		}
	}
	if plan, ok := projectAssistantPlanSnapshotFromMetadata(existing[projectAssistantMetadataPlan]); ok {
		metadata[projectAssistantMetadataPlan] = *plan
	}
	if initialBuild, _ := existing[projectAssistantMetadataInitialBuild].(bool); initialBuild {
		metadata[projectAssistantMetadataInitialBuild] = true
	}
	if progress, ok := projectAssistantProgressSnapshotFromMetadata(existing[projectAssistantMetadataProgress]); ok {
		metadata[projectAssistantMetadataProgress] = *progress
	}
	preview, _ := existing[projectAssistantMetadataPreviewRefreshNeeded].(bool)
	metadata[projectAssistantMetadataRunID] = run.ID
	metadata[projectAssistantMetadataRevision] = run.Revision
	metadata[projectAssistantMetadataWorkingStatus] = status
	metadata[projectAssistantMetadataProvisional] = provisional
	metadata[projectAssistantMetadataPreviewRefreshNeeded] = preview
	return metadata
}

func projectAssistantProgressSnapshotFromMetadata(value any) (*projectAssistantProgressSnapshot, bool) {
	if value == nil {
		return nil, false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var progress projectAssistantProgressSnapshot
	if err := decoder.Decode(&progress); err != nil ||
		progress.Version != 1 ||
		len(progress.Messages) > projectAssistantProgressMaxMessages ||
		progress.WorkedDurationMS < 0 ||
		progress.WorkedDurationMS > projectAssistantWorkedDurationMaxMS {
		return nil, false
	}
	for _, message := range progress.Messages {
		if message == "" ||
			message != strings.TrimSpace(message) ||
			len(message) > projectEinoAssistantProgressMaxBytes ||
			!utf8.ValidString(message) ||
			strings.IndexFunc(message, unicode.IsControl) >= 0 {
			return nil, false
		}
	}
	if len(progress.MessageSequences) > 0 {
		if len(progress.MessageSequences) != len(progress.Messages) {
			return nil, false
		}
		previous := 0
		for _, sequence := range progress.MessageSequences {
			if sequence <= 0 || sequence > projectAssistantTraceMaxSequence {
				return nil, false
			}
			if sequence <= previous {
				return nil, false
			}
			previous = sequence
		}
	}
	return &progress, true
}

// projectAssistantPlanSnapshotFromMetadata is the durable metadata boundary
// for plans. Postgres rehydrates JSON values as generic maps, so decode them
// back into the public snapshot shape and retain only values the write_todos
// producer could have emitted. Validation deliberately does not sanitize or
// redact labels again: a retained plan must preserve its already-sanitized
// user-facing wording exactly.
func projectAssistantPlanSnapshotFromMetadata(value any) (*projectAssistantPlanSnapshot, bool) {
	if value == nil {
		return nil, false
	}
	raw, err := json.Marshal(value)
	if err != nil || !projectAssistantPlanMetadataKeysValid(raw) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan projectAssistantPlanSnapshot
	if err := decoder.Decode(&plan); err != nil || !projectAssistantPlanSnapshotValid(plan) {
		return nil, false
	}
	return &plan, true
}

func projectAssistantPlanMetadataKeysValid(raw []byte) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || len(object) != 1 {
		return false
	}
	rawSteps, ok := object["steps"]
	if !ok {
		return false
	}
	var steps []map[string]json.RawMessage
	if err := json.Unmarshal(rawSteps, &steps); err != nil {
		return false
	}
	for _, step := range steps {
		if _, ok := step["content"]; !ok {
			return false
		}
		if _, ok := step["status"]; !ok {
			return false
		}
		for key := range step {
			switch key {
			case "content", "activeForm", "status":
			default:
				return false
			}
		}
	}
	return true
}

func projectAssistantPlanSnapshotValid(plan projectAssistantPlanSnapshot) bool {
	if len(plan.Steps) == 0 || len(plan.Steps) > projectEinoAssistantTodoProgressMaxItems {
		return false
	}
	inProgress := 0
	for _, step := range plan.Steps {
		if !projectAssistantPlanLabelValid(step.Content, true) || !projectAssistantPlanLabelValid(step.ActiveForm, false) {
			return false
		}
		switch step.Status {
		case "pending", "completed":
		case "in_progress":
			inProgress++
			if inProgress > 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func projectAssistantPlanLabelValid(label string, required bool) bool {
	if !utf8.ValidString(label) || len(label) > projectEinoAssistantTodoProgressMaxLabelBytes {
		return false
	}
	if label != strings.TrimSpace(label) || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return false
	}
	return !required || strings.TrimSpace(label) != ""
}

type projectAssistantDurableMetadataState struct {
	status             string
	provisional        bool
	toolCalls          []projectToolCallStreamEvent
	plan               *projectAssistantPlanSnapshot
	initialBuild       bool
	progressMessages   []string
	progressSequences  []int
	actionSequences    map[string]int
	nextTraceSequence  int
	workedDuration     time.Duration
	workSegmentStarted time.Time
	terminalError      json.RawMessage
	abortReason        store.AssistantRunAbortReason
}

func (s *projectAssistantDurableMetadataState) appendProgress(message string) bool {
	if s == nil {
		return false
	}
	message, reason := projectEinoAssistantProgressMessage(message)
	if reason != "" {
		return false
	}
	if len(s.progressMessages) > 0 && s.progressMessages[len(s.progressMessages)-1] == message {
		return false
	}
	if len(s.progressMessages) >= projectAssistantProgressMaxMessages {
		return false
	}
	s.progressMessages = append(s.progressMessages, message)
	s.progressSequences = append(s.progressSequences, s.nextSequence())
	return true
}

func (s *projectAssistantDurableMetadataState) restoreTrace(progress *projectAssistantProgressSnapshot, actions []projectAssistantActionFeedItem) {
	if s == nil {
		return
	}
	s.actionSequences = map[string]int{}
	if progress != nil {
		s.progressMessages = append([]string(nil), progress.Messages...)
		if len(progress.MessageSequences) == len(progress.Messages) {
			s.progressSequences = append([]int(nil), progress.MessageSequences...)
		} else {
			s.progressSequences = make([]int, len(progress.Messages))
		}
		for _, sequence := range s.progressSequences {
			if sequence > s.nextTraceSequence {
				s.nextTraceSequence = sequence
			}
		}
		s.workedDuration = time.Duration(progress.WorkedDurationMS) * time.Millisecond
	}
	for _, action := range actions {
		if action.ID == "" || action.Sequence <= 0 || action.Sequence > projectAssistantTraceMaxSequence {
			continue
		}
		s.actionSequences[action.ID] = action.Sequence
		if action.Sequence > s.nextTraceSequence {
			s.nextTraceSequence = action.Sequence
		}
	}
}

func (s *projectAssistantDurableMetadataState) upsertToolCall(event projectToolCallStreamEvent) {
	if s == nil || event.ID == "" {
		return
	}
	// Ordering is owned by the durable callback boundary. Ignore any sequence
	// carried by an upstream event and recover only from server state.
	event.Sequence = 0
	publicID := projectAssistantActionPublicID(event.ID)
	if s.actionSequences != nil {
		event.Sequence = s.actionSequences[publicID]
	}
	if event.Sequence == 0 {
		for _, existing := range s.toolCalls {
			if existing.ID == event.ID && existing.Sequence > 0 {
				event.Sequence = existing.Sequence
				break
			}
		}
	}
	if event.Sequence == 0 {
		event.Sequence = s.nextSequence()
	}
	if event.Sequence > 0 {
		if s.actionSequences == nil {
			s.actionSequences = map[string]int{}
		}
		s.actionSequences[publicID] = event.Sequence
	}
	s.toolCalls = upsertProjectToolCallStreamEvent(s.toolCalls, event)
}

func (s *projectAssistantDurableMetadataState) nextSequence() int {
	if s == nil || s.nextTraceSequence >= projectAssistantTraceMaxSequence {
		return 0
	}
	s.nextTraceSequence++
	return s.nextTraceSequence
}

func (s *projectAssistantDurableMetadataState) progressSnapshot(now time.Time, hasActions bool) *projectAssistantProgressSnapshot {
	if s == nil || (len(s.progressMessages) == 0 && !hasActions) {
		return nil
	}
	duration := s.workedDuration
	if !s.workSegmentStarted.IsZero() && now.After(s.workSegmentStarted) {
		duration += now.Sub(s.workSegmentStarted)
	}
	durationMS := duration.Milliseconds()
	if durationMS < 0 {
		durationMS = 0
	}
	if durationMS > projectAssistantWorkedDurationMaxMS {
		durationMS = projectAssistantWorkedDurationMaxMS
	}
	messageSequences := append([]int{}, s.progressSequences...)
	if len(messageSequences) != len(s.progressMessages) {
		messageSequences = nil
	} else {
		for _, sequence := range messageSequences {
			if sequence <= 0 {
				messageSequences = nil
				break
			}
		}
	}
	return &projectAssistantProgressSnapshot{
		Version:          1,
		Messages:         append([]string{}, s.progressMessages...),
		MessageSequences: messageSequences,
		WorkedDurationMS: durationMS,
	}
}

func projectAssistantRunDisplayStatus(status store.AssistantRunStatus, fallback string) string {
	switch status {
	case store.AssistantRunStatusCompleted:
		return "Completed"
	case store.AssistantRunStatusFailed:
		return "Failed"
	case store.AssistantRunStatusInterrupted:
		return "Interrupted"
	case store.AssistantRunStatusAborted:
		return "Aborted"
	case store.AssistantRunStatusPendingPermission:
		return projectMessageStatusPendingPermission
	case store.AssistantRunStatusPendingInput:
		return projectMessageStatusPendingInput
	}
	return fallback
}

// persistProjectAssistantDurableMetadata is the one metadata write path for
// both a fresh run and a resumed continuation. It derives the metadata revision
// from the same transition that persists the run and message.
func (s *Server) persistProjectAssistantDurableMetadata(ctx context.Context, accumulator *projectAssistantSnapshotAccumulator, workspaceScope workspace.Scope, state *projectAssistantDurableMetadataState, runStatus *store.AssistantRunStatus) error {
	return accumulator.UpdateSnapshot(ctx, func(run *store.AssistantRun, message *store.Message) {
		if runStatus != nil {
			run.Status = *runStatus
			run.Error = append(json.RawMessage(nil), state.terminalError...)
			run.AbortReason = state.abortReason
		}
		if assistantRunTerminal(run.Status) {
			state.provisional = false
		}
		next := *run
		next.Revision++
		metadata := projectAssistantDurableMetadataForTransition(
			next,
			projectAssistantRunDisplayStatus(run.Status, state.status),
			state.provisional,
			s.projectAssistantPreviewRefreshNeeded(ctx, workspaceScope, "", false, state.toolCalls),
			state.toolCalls,
			state.plan,
		)
		// Resumed segments begin with durable actions from the previous segment.
		// Keep that history and only upsert new action updates.
		actions := projectAssistantActionFeedFromMetadata(message.Metadata[projectMessageMetadataAssistantActionFeed])
		for _, action := range projectAssistantActionFeedUpdatesFromToolCalls(state.toolCalls) {
			actions = applyProjectAssistantActionFeedUpdate(actions, action)
		}
		if assistantRunTerminal(run.Status) {
			actions = finalizeProjectAssistantActionFeed(actions, run.Status)
		}
		if len(actions) > 0 {
			metadata[projectMessageMetadataAssistantActionFeed] = actions
		}
		if preview, _ := message.Metadata[projectAssistantMetadataPreviewRefreshNeeded].(bool); preview {
			metadata[projectAssistantMetadataPreviewRefreshNeeded] = true
		}
		if _, ok := metadata[projectAssistantMetadataPlan]; !ok {
			if plan, ok := projectAssistantPlanSnapshotFromMetadata(message.Metadata[projectAssistantMetadataPlan]); ok {
				metadata[projectAssistantMetadataPlan] = *plan
			}
		}
		if state.initialBuild {
			metadata[projectAssistantMetadataInitialBuild] = true
		} else if initialBuild, _ := message.Metadata[projectAssistantMetadataInitialBuild].(bool); initialBuild {
			metadata[projectAssistantMetadataInitialBuild] = true
		}
		if progress := state.progressSnapshot(time.Now().UTC(), len(actions) > 0); progress != nil {
			metadata[projectAssistantMetadataProgress] = *progress
		} else if progress, ok := projectAssistantProgressSnapshotFromMetadata(message.Metadata[projectAssistantMetadataProgress]); ok {
			metadata[projectAssistantMetadataProgress] = *progress
		}
		message.Metadata = metadata
	})
}

func (s *Server) startProjectAssistantRun(w http.ResponseWriter, r *http.Request) {
	c, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var request CreateProjectMessageRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	request.Content = strings.TrimSpace(request.Content)
	request.ClientRequestID = strings.TrimSpace(request.ClientRequestID)
	request.ExpectedRunID = strings.TrimSpace(request.ExpectedRunID)
	if request.Content == "" || request.ClientRequestID == "" {
		writeProjectError(w, newValidationError("content and clientRequestID are required"))
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	supervisor := s.projectAssistantSupervisor()
	mode, err := request.collaborationMode()
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if request.ExpectedRunID != "" {
		active, user, assistant, handled, steerErr := supervisor.EnqueueSteering(
			r.Context(),
			scope,
			request.ExpectedRunID,
			id.user,
			request.Content,
			request.ClientRequestID,
			store.AssistantRunMode(mode),
		)
		if !handled && steerErr == nil {
			active, user, assistant, handled, steerErr = s.recoverProjectAssistantSteeringReplay(
				r.Context(), scope, request.ExpectedRunID, id.user, request.Content, request.ClientRequestID, store.AssistantRunMode(mode),
			)
		}
		if !handled && steerErr == nil {
			steerErr = store.ErrAssistantRunConflict
		}
		if steerErr != nil {
			if errors.Is(steerErr, store.ErrAssistantRunConflict) {
				s.writeProjectAssistantRunConflict(w, r.Context(), scope, id.user)
				return
			}
			writeProjectError(w, steerErr)
			return
		}
		writeJSON(w, http.StatusAccepted, projectAssistantRunStartResponse{
			Run:       projectAssistantRunToAPI(active),
			User:      projectMessagePointerToAPI(user),
			Assistant: projectMessageToAPI(assistant),
		})
		return
	}
	initialBootstrap, err := s.consumeProjectInitialBootstrap(r.Context(), scope, id.user, request.Content, request.ClientRequestID)
	if err != nil {
		if errors.Is(err, store.ErrProjectBootstrapPermitConflict) {
			writeStatus(w, http.StatusConflict, "Conflict", "initial project bootstrap is already reserved")
			return
		}
		writeProjectError(w, err)
		return
	}
	if initialBootstrap {
		mode = projectAssistantCollaborationModeDefault
	}
	startWorker := func(created store.AssistantRun, assistant store.Message, start *projectAssistantStreamStart) error {
		return supervisor.Start(r.Context(), scope, created, assistant, func(ctx context.Context, accumulator *projectAssistantSnapshotAccumulator) {
			s.runProjectAssistantWorker(ctx, accumulator, r, id, c, project, created, start)
		})
	}
	started, err := s.startProjectAssistantRunDurablyWithMode(
		r.Context(),
		scope,
		id.user,
		request.Content,
		request.ClientRequestID,
		store.AssistantRunMode(mode),
		func(created store.AssistantRun, assistant store.Message, transcriptEmpty bool) error {
			var start *projectAssistantStreamStart
			if initialBootstrap && transcriptEmpty {
				plan := projectAssistantInitialCreationPlan(request.Content)
				start = &projectAssistantStreamStart{InitialApprovedPlan: cloneProjectAssistantApprovedPlan(&plan)}
			}
			return startWorker(created, assistant, start)
		},
	)
	if err != nil {
		if errors.Is(err, store.ErrAssistantRunConflict) {
			if _, latestErr := s.store.LatestAssistantRun(r.Context(), scope); latestErr == nil {
				s.writeProjectAssistantRunConflict(w, r.Context(), scope, id.user)
			} else {
				writeStatus(w, http.StatusConflict, "Conflict", "assistant run start is already in progress")
			}
			return
		}
		writeProjectError(w, err)
		return
	}
	s.writeProjectAssistantRunStart(w, http.StatusAccepted, scope, started.Run)
}

func (s *Server) recoverProjectAssistantSteeringReplay(
	ctx context.Context,
	scope store.Scope,
	expectedRunID string,
	actor string,
	content string,
	clientRequestID string,
	mode store.AssistantRunMode,
) (store.AssistantRun, store.Message, store.Message, bool, error) {
	run, err := s.store.GetAssistantRun(ctx, scope, expectedRunID)
	if err != nil {
		if errors.Is(err, store.ErrAssistantRunNotFound) {
			return store.AssistantRun{}, store.Message{}, store.Message{}, false, nil
		}
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, err
	}
	if run.Mode != mode || !projectAssistantRunActorMatches(run, actor) {
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, fmt.Errorf("%w: assistant steering identity does not match the expected run", store.ErrAssistantRunConflict)
	}
	user, found, err := findProjectAssistantSteeringReceipt(ctx, s.store, scope, run, actor, content, clientRequestID)
	if err != nil {
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, err
	}
	if !found {
		return store.AssistantRun{}, store.Message{}, store.Message{}, false, nil
	}
	assistant, err := s.findProjectMessage(ctx, scope, run.ActiveMessageID)
	if err != nil {
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, err
	}
	return run, user, assistant, true, nil
}

func (s *Server) runProjectAssistantWorker(ctx context.Context, accumulator *projectAssistantSnapshotAccumulator, request *http.Request, id identity, c *asclient.Client, project *aiv1alpha1.Project, run store.AssistantRun, start *projectAssistantStreamStart) {
	content := &strings.Builder{}
	workSegmentStarted := time.Now().UTC()
	state := &projectAssistantDurableMetadataState{
		status: "Working",
		initialBuild: start != nil &&
			start.InitialApprovedPlan != nil &&
			strings.TrimSpace(start.InitialApprovedPlan.Goal) != "",
		workSegmentStarted: workSegmentStarted,
	}
	activeMessageID := run.ActiveMessageID
	syncSteeringSegment := func() {
		messageID := accumulator.ActiveMessageID()
		if messageID == "" || messageID == activeMessageID {
			return
		}
		content.Reset()
		activeMessageID = messageID
		state = &projectAssistantDurableMetadataState{
			status:             "Working",
			initialBuild:       state.initialBuild,
			workSegmentStarted: time.Now().UTC(),
		}
	}
	workspaceScope := projectWorkspaceScope(id, project)
	persistMetadata := func(ctx context.Context, runStatus *store.AssistantRunStatus) error {
		return s.persistProjectAssistantDurableMetadata(ctx, accumulator, workspaceScope, state, runStatus)
	}
	var snapshotErr error
	var snapshotErrMu sync.Mutex
	recordSnapshotErr := func(err error) {
		if err == nil {
			return
		}
		snapshotErrMu.Lock()
		if snapshotErr == nil {
			snapshotErr = err
		}
		snapshotErrMu.Unlock()
	}
	getSnapshotErr := func() error {
		snapshotErrMu.Lock()
		defer snapshotErrMu.Unlock()
		return snapshotErr
	}
	var callbackMu sync.Mutex
	callbacksClosed := false
	req := request.Clone(context.WithValue(ctx, projectAssistantSupervisorRunContextKey{}, run))
	result, err := s.generateProjectAssistantResultWithStart(req, id, c, project, projectAssistantStreamCallbacks{
		OnChunk: func(chunk string) {
			callbackMu.Lock()
			defer callbackMu.Unlock()
			if callbacksClosed {
				return
			}
			syncSteeringSegment()
			recordSnapshotErr(accumulator.UpdateText(ctx, appendProjectAssistantStreamBlock(content, chunk), false))
		},
		OnProgress: func(message string) {
			callbackMu.Lock()
			defer callbackMu.Unlock()
			if callbacksClosed {
				return
			}
			syncSteeringSegment()
			if state.appendProgress(message) {
				recordSnapshotErr(persistMetadata(ctx, nil))
			}
		},
		OnProvisionalText: func(_ string) {
			callbackMu.Lock()
			defer callbackMu.Unlock()
			if callbacksClosed {
				return
			}
			syncSteeringSegment()
			state.provisional = true
			recordSnapshotErr(persistMetadata(ctx, nil))
		},
		OnProvisionalReset: func() {
			callbackMu.Lock()
			defer callbackMu.Unlock()
			if callbacksClosed {
				return
			}
			syncSteeringSegment()
			state.provisional = false
			recordSnapshotErr(persistMetadata(ctx, nil))
		},
		OnStatus: func(nextStatus string) {
			callbackMu.Lock()
			defer callbackMu.Unlock()
			if callbacksClosed {
				return
			}
			syncSteeringSegment()
			state.status = nextStatus
			recordSnapshotErr(persistMetadata(ctx, nil))
		},
		OnPlan: func(plan projectAssistantPlanSnapshot) {
			callbackMu.Lock()
			defer callbackMu.Unlock()
			if callbacksClosed {
				return
			}
			syncSteeringSegment()
			state.plan = &plan
			state.status = projectEinoAssistantPlanProgressStatus(plan)
			recordSnapshotErr(persistMetadata(ctx, nil))
		},
		OnToolCall: func(event projectToolCallStreamEvent) {
			callbackMu.Lock()
			defer callbackMu.Unlock()
			if callbacksClosed {
				return
			}
			syncSteeringSegment()
			state.upsertToolCall(event)
			recordSnapshotErr(persistMetadata(ctx, nil))
		},
		OnAssistantEvent: func(event projectAssistantEvent) {
			callbackMu.Lock()
			defer callbackMu.Unlock()
			if callbacksClosed {
				return
			}
			syncSteeringSegment()
			if event.Permission != nil && event.Permission.ToolCallID != "" {
				state.upsertToolCall(projectToolCallStreamEvent{ID: event.Permission.ToolCallID, Name: event.Permission.ToolName, Status: "permission_required", Summary: event.Permission.Reason, Permission: event.Permission})
			}
			if event.FollowUp != nil && event.FollowUp.ToolCallID != "" {
				state.upsertToolCall(projectToolCallStreamEvent{ID: event.FollowUp.ToolCallID, Name: projectToolAskFollowUp, Status: "input_required", Summary: event.FollowUp.Prompt, FollowUp: event.FollowUp})
			}
			if event.Checkpoint != nil {
				for i := range state.toolCalls {
					if state.toolCalls[i].Status == "permission_required" || state.toolCalls[i].Status == "input_required" {
						state.toolCalls[i].Checkpoint = event.Checkpoint
					}
				}
			}
			recordSnapshotErr(persistMetadata(ctx, nil))
		},
	}, start)
	callbackMu.Lock()
	syncSteeringSegment()
	callbacksClosed = true
	contentText := content.String()
	callbackMu.Unlock()
	state.initialBuild = state.initialBuild || result.InitialBuild
	reply := result.Content
	if result.CompletionEvidence.PlanComplete && state.plan != nil {
		completed := cloneProjectAssistantPlanSnapshot(*state.plan)
		for index := range completed.Steps {
			completed.Steps[index].Status = "completed"
			completed.Steps[index].ActiveForm = ""
		}
		state.plan = &completed
	}
	engineCompleted := err == nil
	finalContent := s.projectAssistantRunTerminalContent(
		ctx,
		projectMessageScope(id.orgUUID, id.workspaceUUID, project),
		run,
		reply,
		contentText,
		err,
		result.CompletionEvidence,
		engineCompleted,
	)
	recordSnapshotErr(accumulator.UpdateText(ctx, finalContent, true))
	if persistErr := getSnapshotErr(); persistErr != nil {
		accumulator.FailPersistence(persistErr)
		return
	}
	if strings.TrimSpace(finalContent) != "" {
		persistCtx, cancel := detachedProjectPersistenceContext(ctx)
		itemErr := appendProjectAssistantConversationMessage(
			persistCtx,
			s.store,
			projectMessageScope(id.orgUUID, id.workspaceUUID, project),
			run.ID,
			"assistant-"+accumulator.ActiveMessageID(),
			projectAssistantConversationAssistant,
			chatMessage{Role: "assistant", Content: finalContent},
		)
		cancel()
		recordSnapshotErr(itemErr)
		if persistErr := getSnapshotErr(); persistErr != nil {
			accumulator.FailPersistence(persistErr)
			return
		}
	}
	// A durable Stop wins even if the engine concurrently returns success or
	// an interrupt. Do this before interpreting the engine result so a stopped
	// run cannot become completed or pending again.
	if ctx.Err() != nil {
		state.status = "Interrupted"
		state.abortReason = store.AssistantRunAbortReasonInterrupted
		_, transitionErr := accumulator.supervisor.AbortWith(projectMessageScope(id.orgUUID, id.workspaceUUID, project), run.ID, func(run *store.AssistantRun, _ *store.Message) error {
			run.AbortReason = store.AssistantRunAbortReasonInterrupted
			return nil
		})
		recordSnapshotErr(transitionErr)
		return
	}
	if err == nil {
		state.status = "Completed"
		runStatus := store.AssistantRunStatusCompleted
		transitionErr := persistMetadata(ctx, &runStatus)
		recordSnapshotErr(transitionErr)
		if transitionErr == nil {
			if committed, ok := accumulator.CommittedRun(); ok {
				accumulator.supervisor.log("completed", projectMessageScope(id.orgUUID, id.workspaceUUID, project), committed)
			}
		}
		return
	}
	if errors.Is(err, context.Canceled) {
		state.status = "Interrupted"
		state.abortReason = store.AssistantRunAbortReasonInterrupted
		runStatus := store.AssistantRunStatusInterrupted
		transitionErr := persistMetadata(context.Background(), &runStatus)
		recordSnapshotErr(transitionErr)
		if transitionErr == nil {
			if committed, ok := accumulator.CommittedRun(); ok {
				accumulator.supervisor.log("interrupted", projectMessageScope(id.orgUUID, id.workspaceUUID, project), committed)
			}
		}
		return
	}
	var permissionErr *projectAssistantPermissionRequiredError
	if errors.As(err, &permissionErr) {
		state.status = projectMessageStatusPendingPermission
		runStatus := store.AssistantRunStatusPendingPermission
		recordSnapshotErr(persistMetadata(context.Background(), &runStatus))
		return
	}
	var inputErr *projectAssistantInputRequiredError
	if errors.As(err, &inputErr) {
		state.status = projectMessageStatusPendingInput
		runStatus := store.AssistantRunStatusPendingInput
		recordSnapshotErr(persistMetadata(context.Background(), &runStatus))
		return
	}
	if projectEinoAssistantIterationLimited(err) {
		state.status = "Failed"
		state.abortReason = store.AssistantRunAbortReasonIterationLimited
		state.terminalError = projectAssistantRunErrorJSON(err, "max_iterations_exceeded")
		runStatus := store.AssistantRunStatusFailed
		transitionErr := persistMetadata(context.Background(), &runStatus)
		recordSnapshotErr(transitionErr)
		if transitionErr == nil {
			if committed, ok := accumulator.CommittedRun(); ok {
				accumulator.supervisor.log("failed", projectMessageScope(id.orgUUID, id.workspaceUUID, project), committed)
			}
		}
		return
	}
	if projectEinoAssistantBudgetLimited(err) {
		state.status = "Failed"
		state.abortReason = store.AssistantRunAbortReasonBudgetLimited
		state.terminalError = projectAssistantRunErrorJSON(err, "session_budget_exceeded")
		runStatus := store.AssistantRunStatusFailed
		transitionErr := persistMetadata(context.Background(), &runStatus)
		recordSnapshotErr(transitionErr)
		if transitionErr == nil {
			if committed, ok := accumulator.CommittedRun(); ok {
				accumulator.supervisor.log("failed", projectMessageScope(id.orgUUID, id.workspaceUUID, project), committed)
			}
		}
		return
	}
	state.status = "Failed"
	state.terminalError = projectAssistantRunErrorJSON(err, projectAssistantRunErrorInfo(err))
	runStatus := store.AssistantRunStatusFailed
	transitionErr := persistMetadata(context.Background(), &runStatus)
	recordSnapshotErr(transitionErr)
	if transitionErr == nil {
		if committed, ok := accumulator.CommittedRun(); ok {
			accumulator.supervisor.log("failed", projectMessageScope(id.orgUUID, id.workspaceUUID, project), committed)
		}
	}
}

func projectAssistantRunErrorJSON(err error, errorInfo string) json.RawMessage {
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(projectEinoAssistantSafeErrorText(err))
	if message == "" {
		message = "The assistant provider could not complete the response."
	}
	raw, marshalErr := json.Marshal(projectAssistantRunErrorView{Message: message, ErrorInfo: strings.TrimSpace(errorInfo)})
	if marshalErr != nil {
		return json.RawMessage(`{"message":"The assistant provider could not complete the response."}`)
	}
	return raw
}

func projectAssistantRunErrorInfo(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errProjectAssistantNoOutput) {
		return "internal_server_error"
	}
	if errors.Is(err, adk.ErrExceedMaxRetries) {
		return "response_too_many_failed_attempts"
	}
	if projectEinoAssistantShouldRetryModelError(err) || projectEinoAssistantWillRetry(err) {
		return "response_too_many_failed_attempts"
	}
	return "other"
}

func (s *Server) writeProjectAssistantRunStart(w http.ResponseWriter, status int, scope store.Scope, run store.AssistantRun) {
	message, err := s.findProjectMessage(context.Background(), scope, run.ActiveMessageID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	response := projectAssistantRunStartResponse{Run: projectAssistantRunToAPI(run), Assistant: projectMessageToAPI(message)}
	if strings.TrimSpace(run.UserMessageID) != "" {
		user, err := s.findProjectMessage(context.Background(), scope, run.UserMessageID)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		apiUser := projectMessageToAPI(user)
		response.User = &apiUser
	}
	writeJSON(w, status, response)
}

func projectMessagePointerToAPI(message store.Message) *aiv1alpha1.ProjectMessage {
	apiMessage := projectMessageToAPI(message)
	return &apiMessage
}

func (s *Server) writeProjectAssistantRunConflict(w http.ResponseWriter, ctx context.Context, scope store.Scope, actor string) {
	run, err := s.store.LatestAssistantRun(ctx, scope)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if s.authorizeProjectAssistantRunActor(ctx, scope, run, actor, false) != nil {
		writeStatus(w, http.StatusConflict, "Conflict", "another assistant run is active")
		return
	}
	writeJSON(w, http.StatusConflict, projectAssistantRunSnapshotResponse{Run: projectAssistantRunToAPI(run)})
}

func (s *Server) latestProjectAssistantRun(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	if err := s.reconcileOrphanedProjectAssistantRun(r.Context(), scope); err != nil {
		writeProjectError(w, err)
		return
	}
	run, err := s.store.LatestAssistantRun(r.Context(), scope)
	if errors.Is(err, store.ErrAssistantRunNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if s.authorizeProjectAssistantRunActor(r.Context(), scope, run, id.user, false) != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	message, err := s.findProjectMessage(r.Context(), scope, run.ActiveMessageID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectAssistantRunSnapshotToAPI(projectAssistantRunSnapshot{Run: run, Message: message}))
}

func (s *Server) streamProjectAssistantSnapshots(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	runID := mux.Vars(r)["run"]
	if err := s.reconcileOrphanedProjectAssistantRun(r.Context(), scope); err != nil {
		writeProjectError(w, err)
		return
	}
	run, err := s.store.GetAssistantRun(r.Context(), scope, runID)
	if err != nil || s.authorizeProjectAssistantRunActor(r.Context(), scope, run, id.user, false) != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", "assistant run not found")
		return
	}
	after := projectAssistantAfterRevision(r)
	updates, unsubscribe, err := s.projectAssistantSupervisor().Subscribe(scope, runID, after)
	if errors.Is(err, store.ErrAssistantRunNotFound) {
		message, loadErr := s.findProjectMessage(r.Context(), scope, run.ActiveMessageID)
		if loadErr != nil {
			writeProjectError(w, loadErr)
			return
		}
		flusher, streamOK := startProjectAssistantSnapshotStream(w)
		if !streamOK {
			return
		}
		_ = writeProjectAssistantSnapshotSSE(w, flusher, projectAssistantRunSnapshot{Run: run, Message: message})
		return
	}
	if err != nil {
		writeProjectError(w, err)
		return
	}
	defer unsubscribe()
	flusher, streamOK := startProjectAssistantSnapshotStream(w)
	if !streamOK {
		return
	}
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case snapshot, open := <-updates:
			if !open {
				return
			}
			if err := writeProjectAssistantSnapshotSSE(w, flusher, snapshot); err != nil {
				return
			}
			if assistantRunTerminal(snapshot.Run.Status) {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func startProjectAssistantSnapshotStream(w http.ResponseWriter) (http.Flusher, bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "streaming unsupported")
		return nil, false
	}
	return flusher, true
}

func projectAssistantAfterRevision(r *http.Request) int64 {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("afterRevision")
	}
	value, _ := strconv.ParseInt(raw, 10, 64)
	return value
}

func writeProjectAssistantSnapshotSSE(w http.ResponseWriter, flusher http.Flusher, snapshot projectAssistantRunSnapshot) error {
	data, err := json.Marshal(projectAssistantRunSnapshotToAPI(snapshot))
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: snapshot\ndata: %s\n\n", snapshot.Run.Revision, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) reconcileOrphanedProjectAssistantRun(ctx context.Context, scope store.Scope) error {
	run, err := s.store.LatestAssistantRun(ctx, scope)
	if err != nil {
		if errors.Is(err, store.ErrAssistantRunNotFound) {
			return nil
		}
		return err
	}
	if assistantRunTerminal(run.Status) {
		return nil
	}
	if run.Status != store.AssistantRunStatusRunning && run.Status != store.AssistantRunStatusStopping {
		// Current v2 permission/input checkpoints remain intentionally resumable.
		return nil
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	supervisor := s.projectAssistantSupervisor()
	if supervisor.reserved(scope) {
		return nil
	}
	supervisor.mu.Lock()
	active := supervisor.runs[key]
	supervisor.mu.Unlock()
	if active != nil && active.run.ID == run.ID {
		return nil
	}
	run.Status = store.AssistantRunStatusInterrupted
	run.AbortReason = store.AssistantRunAbortReasonInterrupted
	run.UpdatedAt = time.Now().UTC()
	run.Revision++
	message, err := s.findProjectMessage(ctx, scope, run.ActiveMessageID)
	if err != nil {
		return err
	}
	message.UpdatedAt = run.UpdatedAt
	message.Metadata = projectAssistantDurableMetadataFromExisting(run, "Interrupted", false, message.Metadata)
	projectAssistantClearPendingInterruptMetadata(&message, run.ID)
	if err := s.store.SaveAssistantRunSnapshot(ctx, scope, run, []store.Message{message}, run.Revision-1); err != nil {
		return err
	}
	if err := appendProjectAssistantConversationMessage(ctx, s.store, scope, run.ID, "interruption-"+run.ID, projectAssistantConversationInterruption, chatMessage{Role: "system", Content: "The prior assistant turn was interrupted by provider process loss. Completed response items and tool effects remain authoritative; do not replay in-flight effects."}); err != nil {
		return err
	}
	s.projectAssistantSupervisor().log("orphan_interrupted", scope, run)
	return nil
}
