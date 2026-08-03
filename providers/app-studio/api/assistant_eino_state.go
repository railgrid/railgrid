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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectAssistantApprovedPlanVersionWorkspaceMutation = 2
	projectAssistantCapabilityWorkspaceMutate            = "workspace.mutate"
)

const projectEinoAssistantMaxTrackedReads = 128
const projectEinoAssistantMaxFailedPatchFingerprints = 32

type projectAssistantApprovedPlan struct {
	Goal               string    `json:"goal,omitempty"`
	Summary            string    `json:"summary,omitempty"`
	Steps              []string  `json:"steps,omitempty"`
	TargetPaths        []string  `json:"targetPaths,omitempty"`
	Version            int       `json:"version,omitempty"`
	Capabilities       []string  `json:"capabilities,omitempty"`
	AcceptanceCriteria []string  `json:"acceptanceCriteria,omitempty"`
	ApprovedAt         time.Time `json:"approvedAt,omitempty"`
	ApprovalTool       string    `json:"approvalTool,omitempty"`
	// RunLocal marks authorization that is valid only for the current Eino
	// run/checkpoint and must never be promoted into the cross-turn grant.
	RunLocal bool `json:"runLocal,omitempty"`
	// AllowAllWrites is the run-local, unbounded source-edit authority derived
	// only from an explicit fresh-project creation request.
	AllowAllWrites bool `json:"allowAllWrites,omitempty"`
}

type projectEinoAssistantRunState struct {
	mu         sync.Mutex
	callbackMu sync.Mutex

	messages                  []chatMessage
	lastToolMessages          []chatMessage
	toolEvidence              []chatMessage
	toolCalls                 []chatToolCall
	seenToolCalls             map[string]int
	turn                      int
	turnPolicy                projectAssistantTurnPolicy
	projectRepositoryRef      string
	toolPrompt                string
	toolDiscovery             *projectEinoAssistantToolDiscovery
	sessionSnapshot           *projectEinoAssistantSessionSnapshot
	rolloutBudget             *projectEinoAssistantRolloutBudget
	restoredRolloutBudget     *projectAssistantRolloutBudgetState
	permissionBarrier         bool
	approvedPlan              *projectAssistantApprovedPlan
	executionPlan             *projectAssistantApprovedPlan
	planProgress              projectAssistantPlanSnapshot
	sourceMutationRevision    uint64
	verifiedMutationRevision  uint64
	commitRequired            bool
	committedMutationRevision uint64
	commitAttemptedRevision   uint64
	verifiedWorkspaceDigest   string
	committedWorkspaceDigest  string
	checkedMutationRevision   uint64
	verificationAttempted     bool
	verificationOutcome       string
	verificationSummary       string
	verificationBlockers      []string
	developmentSyncRevision   uint64
	developmentSyncStatus     string
	developmentSyncFailure    string
	developmentSyncRetry      uint64
	developmentSyncChanged    chan struct{}
	completedReadCalls        map[string]uint64
	observedReadFilePaths     map[string]struct{}
	successfulMutationPaths   map[string]struct{}
	readFileCoverage          map[string][]projectEinoAssistantLineRange
	repeatedActionSignature   string
	repeatedActionToolName    string
	repeatedActionCount       int
	patchFailureCount         int
	failedPatchFingerprints   map[string]uint64
	patchRecoveryPath         string
	patchRecoveryReadComplete bool
	runtimeWarmupAttempts     int
	noProgressModelCallCount  int
	actionBatchModelCall      int
	actionBatchObserved       bool
	actionBatchMadeProgress   bool
	modelCallOrdinal          int
	transientToolResults      map[string]string
	transientPreviewImages    map[string]projectEinoAssistantTransientPreviewImage
	transientToolResultCount  uint64
	lastProgressMessage       string
	deferSteeringOnce         bool
}

type projectEinoAssistantLineRange struct {
	start int
	end   int
}

const projectEinoAssistantReadThroughEOF = 1<<31 - 1

func (s *projectEinoAssistantRunState) EmitToolCall(
	callback func(projectToolCallStreamEvent),
	event projectToolCallStreamEvent,
) {
	if callback == nil {
		return
	}
	if s == nil {
		callback(event)
		return
	}
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	callback(event)
}

func newProjectEinoAssistantRunState() *projectEinoAssistantRunState {
	return &projectEinoAssistantRunState{
		seenToolCalls:           map[string]int{},
		completedReadCalls:      map[string]uint64{},
		readFileCoverage:        map[string][]projectEinoAssistantLineRange{},
		successfulMutationPaths: map[string]struct{}{},
		transientToolResults:    map[string]string{},
		transientPreviewImages:  map[string]projectEinoAssistantTransientPreviewImage{},
		developmentSyncChanged:  make(chan struct{}),
		turnPolicy:              projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging),
	}
}

type projectEinoAssistantTransientPreviewImage struct {
	Base64Data string
	MIMEType   string
}

func (s *projectEinoAssistantRunState) RegisterTransientPreviewImage(result, base64Data, mimeType string) string {
	persistent := projectEinoAssistantPersistentToolResult(projectToolInspectDevelopmentPreview, result)
	if s == nil || strings.TrimSpace(base64Data) == "" || mimeType != "image/png" {
		return persistent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transientToolResultCount++
	digest := sha256.Sum256([]byte(fmt.Sprintf("preview\x00%d\x00%s", s.transientToolResultCount, base64Data)))
	reference := hex.EncodeToString(digest[:16])
	if s.transientPreviewImages == nil || len(s.transientPreviewImages) >= 4 {
		s.transientPreviewImages = map[string]projectEinoAssistantTransientPreviewImage{}
	}
	s.transientPreviewImages[reference] = projectEinoAssistantTransientPreviewImage{Base64Data: base64Data, MIMEType: mimeType}
	var placeholder map[string]any
	if err := json.Unmarshal([]byte(persistent), &placeholder); err != nil {
		placeholder = map[string]any{"status": "unavailable", "summary": "transient preview image omitted from persistence"}
	}
	placeholder["transientImageReference"] = reference
	encoded, err := json.Marshal(placeholder)
	if err != nil {
		return persistent
	}
	return string(encoded)
}

func (s *projectEinoAssistantRunState) AcceptProgressMessage(message string) bool {
	if s == nil {
		return true
	}
	message = strings.TrimSpace(message)
	s.mu.Lock()
	defer s.mu.Unlock()
	if message == "" || message == s.lastProgressMessage {
		return false
	}
	s.lastProgressMessage = message
	return true
}

func (s *projectEinoAssistantRunState) RegisterTransientToolResult(name, result string) string {
	persistent := projectEinoAssistantPersistentToolResult(name, result)
	if s == nil || projectToolBaseName(name) != projectToolGetPreviewConsoleLogs {
		return persistent
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.transientToolResultCount++
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", s.transientToolResultCount, result)))
	reference := hex.EncodeToString(digest[:16])
	if s.transientToolResults == nil {
		s.transientToolResults = map[string]string{}
	} else if len(s.transientToolResults) >= 8 {
		// Transient evidence exists only to bridge the immediately following
		// model call. Older snapshots remain as safe placeholders.
		s.transientToolResults = map[string]string{}
	}
	s.transientToolResults[reference] = result

	var placeholder map[string]any
	if err := json.Unmarshal([]byte(persistent), &placeholder); err != nil {
		placeholder = map[string]any{
			"status":         "unavailable",
			"summary":        "transient preview console result omitted from persistence",
			"transientEvent": true,
		}
	}
	placeholder["transientReference"] = reference
	encoded, err := json.Marshal(placeholder)
	if err != nil {
		return `{"status":"unavailable","summary":"transient preview console result omitted from persistence"}`
	}
	return string(encoded)
}

func (s *projectEinoAssistantRunState) ExpandTransientToolMessages(input []*schema.Message) []*schema.Message {
	if s == nil || len(input) == 0 {
		return input
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.transientToolResults) == 0 && len(s.transientPreviewImages) == 0 {
		return input
	}

	var expanded []*schema.Message
	for index, message := range input {
		if message == nil || message.Role != schema.Tool {
			continue
		}
		toolName := message.ToolName
		if strings.TrimSpace(toolName) == "" {
			toolName = message.Name
		}
		var placeholder struct {
			TransientReference      string `json:"transientReference"`
			TransientImageReference string `json:"transientImageReference"`
		}
		if err := json.Unmarshal([]byte(message.Content), &placeholder); err != nil {
			continue
		}
		if projectToolBaseName(toolName) == projectToolGetPreviewConsoleLogs {
			result, ok := s.transientToolResults[strings.TrimSpace(placeholder.TransientReference)]
			if !ok {
				continue
			}
			if expanded == nil {
				expanded = append([]*schema.Message(nil), input...)
			}
			cloned := *message
			cloned.Content = result
			expanded[index] = &cloned
			continue
		}
		if projectToolBaseName(toolName) != projectToolInspectDevelopmentPreview {
			continue
		}
		preview, ok := s.transientPreviewImages[strings.TrimSpace(placeholder.TransientImageReference)]
		if !ok {
			continue
		}
		if expanded == nil {
			expanded = append([]*schema.Message(nil), input...)
		}
		cloned := *message
		data := preview.Base64Data
		cloned.UserInputMultiContent = []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: message.Content},
			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: preview.MIMEType}}},
		}
		expanded[index] = &cloned
	}
	if expanded == nil {
		return input
	}
	return expanded
}

func (s *projectEinoAssistantRunState) SetTurnPolicy(policy projectAssistantTurnPolicy) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnPolicy = normalizeProjectAssistantTurnPolicy(policy, projectAssistantTurnProfileDebugging)
}

func (s *projectEinoAssistantRunState) TurnProfile() projectAssistantTurnProfile {
	return s.TurnPolicy().profile
}

func (s *projectEinoAssistantRunState) TurnPolicy() projectAssistantTurnPolicy {
	if s == nil {
		return projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return normalizeProjectAssistantTurnPolicy(s.turnPolicy, projectAssistantTurnProfileDebugging)
}

func (s *projectEinoAssistantRunState) SetToolPrompt(prompt string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolPrompt = strings.TrimSpace(prompt)
}

func (s *projectEinoAssistantRunState) SetToolDiscovery(discovery projectEinoAssistantToolDiscovery) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	discovery.Prompt = strings.TrimSpace(discovery.Prompt)
	s.toolDiscovery = &discovery
	s.toolPrompt = discovery.Prompt
}

func (s *projectEinoAssistantRunState) ToolDiscovery() (projectEinoAssistantToolDiscovery, bool) {
	if s == nil {
		return projectEinoAssistantToolDiscovery{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolDiscovery == nil {
		return projectEinoAssistantToolDiscovery{}, false
	}
	return *s.toolDiscovery, true
}

func (s *projectEinoAssistantRunState) ToolPrompt() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolPrompt
}

func (s *projectEinoAssistantRunState) SetSessionSnapshot(snapshot projectEinoAssistantSessionSnapshot) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionSnapshot = cloneProjectEinoAssistantSessionSnapshot(&snapshot)
}

func (s *projectEinoAssistantRunState) SessionSnapshot() *projectEinoAssistantSessionSnapshot {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectEinoAssistantSessionSnapshot(s.sessionSnapshot)
}

func (s *projectEinoAssistantRunState) SetProjectRepositoryRef(ref string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projectRepositoryRef = strings.TrimSpace(ref)
}

func (s *projectEinoAssistantRunState) RestoreCheckpointState(state projectAssistantCheckpointState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = cloneChatMessages(state.Messages)
	s.lastToolMessages = cloneChatMessages(state.LastToolMessages)
	s.toolEvidence = projectEinoAssistantCollectToolEvidence(s.messages)
	s.toolCalls = cloneProjectAssistantToolCalls(state.ToolCalls)
	s.seenToolCalls = projectEinoAssistantSanitizeSeenToolCalls(state.SeenToolCalls)
	s.turn = state.Turn
	s.turnPolicy = projectAssistantTurnPolicyForCheckpoint(state)
	s.projectRepositoryRef = strings.TrimSpace(state.ProjectRepositoryRef)
	s.approvedPlan = cloneProjectAssistantApprovedPlan(state.ApprovedPlan)
	s.executionPlan = cloneProjectAssistantApprovedPlan(state.ExecutionPlan)
	s.planProgress = cloneProjectAssistantPlanSnapshot(state.PlanProgress)
	s.sourceMutationRevision = state.SourceMutationRevision
	s.checkedMutationRevision = state.CheckedMutationRevision
	s.verifiedMutationRevision = state.VerifiedMutationRevision
	s.developmentSyncRevision = state.DevelopmentSyncRevision
	s.developmentSyncStatus = strings.TrimSpace(state.DevelopmentSyncStatus)
	s.developmentSyncFailure = strings.TrimSpace(state.DevelopmentSyncFailure)
	s.developmentSyncRetry = state.DevelopmentSyncRetry
	s.commitRequired = state.CommitRequired
	s.committedMutationRevision = state.CommittedMutationRevision
	s.commitAttemptedRevision = state.CommitAttemptedRevision
	s.verifiedWorkspaceDigest = strings.TrimSpace(state.VerifiedWorkspaceDigest)
	s.committedWorkspaceDigest = strings.TrimSpace(state.CommittedWorkspaceDigest)
	s.verificationAttempted = state.VerificationAttempted
	s.verificationOutcome = strings.TrimSpace(state.VerificationOutcome)
	s.verificationSummary = strings.TrimSpace(state.VerificationSummary)
	s.verificationBlockers = append([]string(nil), state.VerificationBlockers...)
	s.repeatedActionSignature = projectEinoAssistantSanitizeActionSignature(state.RepeatedActionSignature)
	s.repeatedActionToolName = projectToolBaseName(state.RepeatedActionToolName)
	s.repeatedActionCount = min(max(state.RepeatedActionCount, 0), projectEinoAssistantRepeatedActionLimit)
	s.patchFailureCount = min(max(state.PatchFailureCount, 0), projectEinoAssistantRepeatedActionLimit)
	s.failedPatchFingerprints = projectEinoAssistantRestoreFailedPatchFingerprints(
		state.FailedPatchFingerprints,
		s.sourceMutationRevision,
	)
	if recoveryPath, err := workspace.CleanProjectPath(state.PatchRecoveryPath); err == nil {
		s.patchRecoveryPath = recoveryPath
		s.patchRecoveryReadComplete = state.PatchRecoveryReadComplete
	}
	s.runtimeWarmupAttempts = min(max(state.RuntimeWarmupAttempts, 0), projectEinoAssistantRepeatedActionLimit)
	// The durable counter tracks consecutive model calls without progress.
	// Bound restored state so a malformed checkpoint cannot disable the guard.
	s.noProgressModelCallCount = min(max(state.NoProgressModelCallCount, 0), projectEinoAssistantRepeatedActionLimit)
	s.actionBatchModelCall = max(state.ActionBatchModelCall, 0)
	s.actionBatchObserved = state.ActionBatchObserved
	s.actionBatchMadeProgress = state.ActionBatchMadeProgress
	s.modelCallOrdinal = max(state.ModelCallOrdinal, 0)
	if s.actionBatchModelCall > s.modelCallOrdinal {
		s.actionBatchModelCall = 0
		s.actionBatchObserved = false
		s.actionBatchMadeProgress = false
	}
	if s.repeatedActionSignature == "" || s.repeatedActionToolName == "" || s.repeatedActionCount == 0 {
		s.repeatedActionSignature = ""
		s.repeatedActionToolName = ""
		s.repeatedActionCount = 0
	}
	if s.checkedMutationRevision == 0 ||
		s.checkedMutationRevision != s.sourceMutationRevision ||
		s.verifiedMutationRevision != s.sourceMutationRevision ||
		strings.TrimSpace(s.verificationOutcome) != "ready" {
		s.verifiedMutationRevision = 0
	}
	s.completedReadCalls = projectEinoAssistantSanitizeCompletedReads(state.CompletedReadCalls)
	s.readFileCoverage = projectEinoAssistantRestoreReadCoverage(state.ReadFileCoverage)
	s.observedReadFilePaths = projectEinoAssistantReadPathSet(state.ObservedReadFilePaths)
	s.successfulMutationPaths = projectEinoAssistantReadPathSet(state.SuccessfulMutationPaths)
	s.sessionSnapshot = cloneProjectEinoAssistantSessionSnapshot(state.SessionSnapshot)
	s.restoredRolloutBudget = cloneProjectAssistantRolloutBudgetStatePtr(state.RolloutBudget)
}

func (s *projectEinoAssistantRunState) SetRolloutBudget(budget *projectEinoAssistantRolloutBudget) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rolloutBudget = budget
	s.restoredRolloutBudget = nil
}

func (s *projectEinoAssistantRunState) RolloutBudget() *projectEinoAssistantRolloutBudget {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rolloutBudget
}

func (s *projectEinoAssistantRunState) RestoredRolloutBudget() *projectAssistantRolloutBudgetState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectAssistantRolloutBudgetStatePtr(s.restoredRolloutBudget)
}

func (s *projectEinoAssistantRunState) ProjectRepositoryRef() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projectRepositoryRef
}

func (s *projectEinoAssistantRunState) ApprovePlan(plan projectAssistantApprovedPlan) {
	if s == nil {
		return
	}
	normalized := normalizeProjectAssistantApprovedPlan(plan)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvedPlan = &normalized
	// Approval closes the inspection phase. Let the mutation phase perform one
	// fresh, bounded read of approved existing targets so exact patch anchors
	// survive model-context reduction across the phase transition.
	s.completedReadCalls = map[string]uint64{}
	s.readFileCoverage = map[string][]projectEinoAssistantLineRange{}
	s.successfulMutationPaths = map[string]struct{}{}
	s.patchFailureCount = 0
	s.patchRecoveryPath = ""
	s.patchRecoveryReadComplete = false
	s.runtimeWarmupAttempts = 0
}

func (s *projectEinoAssistantRunState) ApprovedPlan() *projectAssistantApprovedPlan {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectAssistantApprovedPlan(s.approvedPlan)
}

func (s *projectEinoAssistantRunState) SetExecutionPlan(plan projectAssistantApprovedPlan) {
	if s == nil {
		return
	}
	normalized := normalizeProjectAssistantApprovedPlan(plan)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executionPlan = &normalized
}

func (s *projectEinoAssistantRunState) ClearExecutionPlan() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executionPlan = nil
	s.planProgress = projectAssistantPlanSnapshot{}
}

func (s *projectEinoAssistantRunState) ExecutionPlan() *projectAssistantApprovedPlan {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectAssistantApprovedPlan(s.executionPlan)
}

func (s *projectEinoAssistantRunState) SetPlanProgress(plan projectAssistantPlanSnapshot) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.planProgress = cloneProjectAssistantPlanSnapshot(plan)
}

func (s *projectEinoAssistantRunState) PlanProgress() projectAssistantPlanSnapshot {
	if s == nil {
		return projectAssistantPlanSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectAssistantPlanSnapshot(s.planProgress)
}

func (s *projectEinoAssistantRunState) ExecutionPlanComplete() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.executionPlan == nil ||
		len(s.executionPlan.Steps) == 0 ||
		len(s.planProgress.Steps) != len(s.executionPlan.Steps) {
		return false
	}
	for index, step := range s.planProgress.Steps {
		if step.Status != "completed" ||
			strings.TrimSpace(step.Content) != projectEinoAssistantTodoProgressLabel(s.executionPlan.Steps[index]) {
			return false
		}
	}
	return true
}

func (s *projectEinoAssistantRunState) CompleteExecutionPlan() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.executionPlan == nil || len(s.executionPlan.Steps) == 0 {
		return
	}
	completed := projectAssistantPlanSnapshot{
		Steps: make([]projectAssistantPlanStep, 0, len(s.executionPlan.Steps)),
	}
	for _, content := range s.executionPlan.Steps {
		completed.Steps = append(completed.Steps, projectAssistantPlanStep{
			Content: projectEinoAssistantTodoProgressLabel(content),
			Status:  "completed",
		})
	}
	s.planProgress = completed
}

func (s *projectEinoAssistantRunState) ClearApprovedPlan() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvedPlan = nil
}

func (s *projectEinoAssistantRunState) RecordSourceMutation() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourceMutationRevision++
	s.failedPatchFingerprints = nil
	if s.developmentSyncRevision != s.sourceMutationRevision {
		s.developmentSyncRevision = s.sourceMutationRevision
		s.developmentSyncStatus = "unknown"
		s.developmentSyncFailure = "positive workspace synchronization evidence is unavailable for this mutation"
		s.signalDevelopmentSyncChangedLocked()
	}
	s.verifiedMutationRevision = 0
	s.checkedMutationRevision = 0
	s.verificationAttempted = false
	s.verificationOutcome = ""
	s.verificationSummary = ""
	s.verificationBlockers = nil
	s.runtimeWarmupAttempts = 0
	s.verifiedWorkspaceDigest = ""
	s.completedReadCalls = map[string]uint64{}
	s.readFileCoverage = map[string][]projectEinoAssistantLineRange{}
}

func (s *projectEinoAssistantRunState) BeginDevelopmentSyncForNextMutation() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	revision := s.sourceMutationRevision + 1
	s.developmentSyncRevision = revision
	s.developmentSyncStatus = "pending"
	s.developmentSyncFailure = ""
	s.signalDevelopmentSyncChangedLocked()
	return revision
}

func (s *projectEinoAssistantRunState) BeginDevelopmentSyncForCurrentMutation() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sourceMutationRevision == 0 {
		return 0
	}
	s.developmentSyncRevision = s.sourceMutationRevision
	s.developmentSyncStatus = "pending"
	s.developmentSyncFailure = ""
	s.signalDevelopmentSyncChangedLocked()
	return s.sourceMutationRevision
}

func (s *projectEinoAssistantRunState) ClaimDevelopmentSyncRetry(revision uint64) (uint64, bool) {
	if s == nil || revision == 0 {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sourceMutationRevision != revision ||
		s.developmentSyncRevision != revision ||
		s.developmentSyncStatus != "failed" ||
		s.developmentSyncRetry == revision {
		return 0, false
	}
	s.developmentSyncRetry = revision
	s.developmentSyncStatus = "pending"
	s.developmentSyncFailure = ""
	s.signalDevelopmentSyncChangedLocked()
	return revision, true
}

func (s *projectEinoAssistantRunState) CompleteDevelopmentSync(revision uint64, syncErr error) {
	if s == nil || revision == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.developmentSyncRevision != revision {
		return
	}
	if syncErr != nil {
		s.developmentSyncStatus = "failed"
		s.developmentSyncFailure = strings.TrimSpace(syncErr.Error())
		if s.developmentSyncFailure == "" {
			s.developmentSyncFailure = "workspace synchronization failed"
		}
		s.signalDevelopmentSyncChangedLocked()
		return
	}
	s.developmentSyncStatus = "succeeded"
	s.developmentSyncFailure = ""
	s.signalDevelopmentSyncChangedLocked()
}

func (s *projectEinoAssistantRunState) signalDevelopmentSyncChangedLocked() {
	if s.developmentSyncChanged != nil {
		close(s.developmentSyncChanged)
	}
	s.developmentSyncChanged = make(chan struct{})
}

// WaitForDevelopmentSync boundedly observes the current revision rather than
// making verification race the background synchronization goroutine.
func (s *projectEinoAssistantRunState) WaitForDevelopmentSync(
	ctx context.Context,
	revision uint64,
	timeout time.Duration,
) (string, string) {
	if s == nil || revision == 0 || timeout <= 0 {
		return s.DevelopmentSyncEvidence(revision)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		status := strings.TrimSpace(s.developmentSyncStatus)
		failure := strings.TrimSpace(s.developmentSyncFailure)
		if s.developmentSyncRevision != revision {
			status = "unknown"
			failure = "positive workspace synchronization evidence is unavailable for this mutation"
		}
		if status == "" {
			status = "unknown"
		}
		if status != "pending" {
			s.mu.Unlock()
			return status, failure
		}
		if s.developmentSyncChanged == nil {
			s.developmentSyncChanged = make(chan struct{})
		}
		changed := s.developmentSyncChanged
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return s.DevelopmentSyncEvidence(revision)
		case <-timer.C:
			return s.DevelopmentSyncEvidence(revision)
		case <-changed:
		}
	}
}

func (s *projectEinoAssistantRunState) DevelopmentSyncEvidence(revision uint64) (string, string) {
	if s == nil || revision == 0 {
		return "unknown", "positive workspace synchronization evidence is unavailable"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.developmentSyncRevision != revision {
		return "unknown", "positive workspace synchronization evidence is unavailable for this mutation"
	}
	status := strings.TrimSpace(s.developmentSyncStatus)
	if status == "" {
		status = "unknown"
	}
	return status, strings.TrimSpace(s.developmentSyncFailure)
}

func (s *projectEinoAssistantRunState) RecordDevelopmentVerification(ready bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verificationAttempted = true
	if ready {
		s.verificationOutcome = "ready"
		s.checkedMutationRevision = s.sourceMutationRevision
	} else {
		s.verificationOutcome = "not_ready"
		s.checkedMutationRevision = 0
	}
	s.verificationSummary = ""
	s.verificationBlockers = nil
	if ready && s.sourceMutationRevision > 0 {
		s.verifiedMutationRevision = s.sourceMutationRevision
		return
	}
	s.verifiedMutationRevision = 0
}

func (s *projectEinoAssistantRunState) RecordDevelopmentVerificationResult(content string) {
	if s == nil {
		return
	}
	var payload projectAssistantRuntimeVerificationResult
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &payload) != nil {
		s.RecordDevelopmentVerification(false)
		return
	}
	rawStatus := strings.TrimSpace(payload.Status)
	status := strings.ToLower(rawStatus)
	if rawStatus == "" {
		status = "not_ready"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verificationAttempted = true
	s.checkedMutationRevision = payload.CheckedMutationRevision
	s.verificationSummary = strings.TrimSpace(payload.Summary)
	s.verificationBlockers = append([]string(nil), payload.Blockers...)
	if projectEinoAssistantRuntimeVerificationDisposition(payload) != projectEinoAssistantVerificationOperational {
		s.runtimeWarmupAttempts = 0
	}
	if rawStatus == "ready" &&
		payload.CheckedMutationRevision > 0 &&
		payload.CheckedMutationRevision == s.sourceMutationRevision {
		s.verifiedMutationRevision = payload.CheckedMutationRevision
		s.verificationOutcome = "ready"
		return
	}
	s.verifiedMutationRevision = 0
	if rawStatus == "ready" {
		s.verificationOutcome = "stale"
		s.verificationBlockers = append(
			s.verificationBlockers,
			fmt.Sprintf(
				"verification checked workspace revision %d, but the current revision is %d",
				payload.CheckedMutationRevision,
				s.sourceMutationRevision,
			),
		)
		return
	}
	if status == "ready" {
		s.verificationOutcome = "unavailable"
		s.verificationBlockers = append(
			s.verificationBlockers,
			"verification returned a non-canonical ready status",
		)
		return
	}
	s.verificationOutcome = status
}

func (s *projectEinoAssistantRunState) CompletionEvidence() projectAssistantCompletionEvidence {
	if s == nil {
		return projectAssistantCompletionEvidence{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	planDefined := s.executionPlan != nil
	planComplete := planDefined &&
		len(s.executionPlan.Steps) > 0 &&
		len(s.planProgress.Steps) == len(s.executionPlan.Steps)
	for index, step := range s.planProgress.Steps {
		if !planComplete ||
			step.Status != "completed" ||
			strings.TrimSpace(step.Content) != projectEinoAssistantTodoProgressLabel(s.executionPlan.Steps[index]) {
			planComplete = false
			break
		}
	}
	latestVerified := s.sourceMutationRevision > 0 &&
		s.verifiedMutationRevision == s.sourceMutationRevision
	outcome := strings.TrimSpace(s.verificationOutcome)
	if outcome == "" && s.verificationAttempted {
		outcome = "not_ready"
	}
	if outcome == "" {
		outcome = "not_run"
	}
	evidence := projectAssistantCompletionEvidence{
		PlanDefined:               planDefined,
		PlanComplete:              planComplete,
		SourceMutationRevision:    s.sourceMutationRevision,
		VerifiedMutationRevision:  s.verifiedMutationRevision,
		LatestMutationVerified:    latestVerified,
		CommitRequired:            s.commitRequired,
		CommittedMutationRevision: s.committedMutationRevision,
		LatestMutationCommitted:   !s.commitRequired || (s.sourceMutationRevision > 0 && s.committedMutationRevision == s.sourceMutationRevision),
		VerificationOutcome:       outcome,
		VerificationSummary:       strings.TrimSpace(s.verificationSummary),
		Blockers:                  append([]string(nil), s.verificationBlockers...),
	}
	if outcome == "provisioning" {
		evidence.Blockers = append(evidence.Blockers, "runtime provisioning")
	}
	return evidence
}

func (s *projectEinoAssistantRunState) SourceMutationVerified() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sourceMutationRevision > 0 &&
		s.verifiedMutationRevision == s.sourceMutationRevision
}

func (s *projectEinoAssistantRunState) RecordSourceCommit(workspaceDigest string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workspaceDigest = strings.TrimSpace(workspaceDigest)
	if s.sourceMutationRevision > 0 && workspaceDigest != "" {
		s.commitAttemptedRevision = s.sourceMutationRevision
		s.committedMutationRevision = s.sourceMutationRevision
		s.committedWorkspaceDigest = workspaceDigest
		// Preserve a verification claim only when commit persisted the exact
		// same workspace bundle. Otherwise fail closed instead of allowing two
		// unrelated digests to appear as one completed state.
		if s.verifiedWorkspaceDigest != workspaceDigest {
			s.verifiedMutationRevision = 0
			s.checkedMutationRevision = 0
			s.verificationAttempted = false
			s.verificationOutcome = ""
			s.verificationSummary = ""
			s.verificationBlockers = nil
			s.verifiedWorkspaceDigest = ""
		}
	}
}

func (s *projectEinoAssistantRunState) RecordSourceCommitAttempt(revision uint64) {
	if s == nil || revision == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision > s.commitAttemptedRevision {
		s.commitAttemptedRevision = revision
	}
}

func (s *projectEinoAssistantRunState) RecordVerifiedWorkspaceDigest(digest string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sourceMutationRevision > 0 && s.verifiedMutationRevision == s.sourceMutationRevision {
		s.verifiedWorkspaceDigest = strings.TrimSpace(digest)
	}
}

func (s *projectEinoAssistantRunState) VerifiedWorkspaceDigestMatches(digest string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sourceMutationRevision > 0 &&
		s.verifiedMutationRevision == s.sourceMutationRevision &&
		s.verifiedWorkspaceDigest != "" &&
		s.verifiedWorkspaceDigest == strings.TrimSpace(digest)
}

func (s *projectEinoAssistantRunState) VerifiedWorkspaceDigest() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.verifiedWorkspaceDigest
}

func (s *projectEinoAssistantRunState) RecordVerificationBindingFailure(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verifiedMutationRevision = 0
	s.verificationOutcome = "not_ready"
	s.verificationBlockers = append(s.verificationBlockers, strings.TrimSpace(reason))
	s.verifiedWorkspaceDigest = ""
}

func (s *projectEinoAssistantRunState) SourceMutationRevisions() (uint64, uint64) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sourceMutationRevision, s.verifiedMutationRevision
}

func (s *projectEinoAssistantRunState) RecordCompletedRead(name, arguments string) {
	if s == nil {
		return
	}
	signature := projectEinoAssistantToolCallSignature(name, arguments)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completedReadCalls == nil {
		s.completedReadCalls = map[string]uint64{}
	}
	if len(s.completedReadCalls) >= projectEinoAssistantMaxTrackedReads {
		return
	}
	s.completedReadCalls[signature] = s.sourceMutationRevision + 1
}

func (s *projectEinoAssistantRunState) RecordReadFileRange(path string, start, end int) {
	if s == nil || path == "" || start <= 0 || end < start {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readFileCoverage == nil {
		s.readFileCoverage = map[string][]projectEinoAssistantLineRange{}
	}
	ranges := append(s.readFileCoverage[path], projectEinoAssistantLineRange{start: start, end: end})
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].start < ranges[j].start
	})
	merged := make([]projectEinoAssistantLineRange, 0, len(ranges))
	for _, lineRange := range ranges {
		if len(merged) == 0 ||
			(merged[len(merged)-1].end < projectEinoAssistantReadThroughEOF &&
				lineRange.start > merged[len(merged)-1].end+1) {
			merged = append(merged, lineRange)
			continue
		}
		merged[len(merged)-1].end = max(merged[len(merged)-1].end, lineRange.end)
	}
	otherRanges := 0
	for trackedPath, trackedRanges := range s.readFileCoverage {
		if trackedPath != path {
			otherRanges += len(trackedRanges)
		}
	}
	available := max(projectEinoAssistantMaxTrackedReads-otherRanges, 0)
	if len(merged) > available {
		merged = merged[:available]
	}
	s.readFileCoverage[path] = merged
}

func (s *projectEinoAssistantRunState) InvalidateObservedReadFile(path string) {
	if s == nil {
		return
	}
	path, err := workspace.CleanProjectPath(path)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completedReadCalls = map[string]uint64{}
	delete(s.readFileCoverage, path)
	delete(s.observedReadFilePaths, path)
}

func (s *projectEinoAssistantRunState) RecordObservedReadFile(path string) {
	if s == nil {
		return
	}
	path, err := workspace.CleanProjectPath(path)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.observedReadFilePaths == nil {
		s.observedReadFilePaths = map[string]struct{}{}
	}
	s.observedReadFilePaths[path] = struct{}{}
}

func (s *projectEinoAssistantRunState) ObservedReadFilePaths() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := make([]string, 0, len(s.observedReadFilePaths))
	for path := range s.observedReadFilePaths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (s *projectEinoAssistantRunState) RecordSuccessfulMutationPath(path string) {
	if s == nil {
		return
	}
	path, err := workspace.CleanProjectPath(path)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.successfulMutationPaths == nil {
		s.successfulMutationPaths = map[string]struct{}{}
	}
	s.successfulMutationPaths[path] = struct{}{}
}

func (s *projectEinoAssistantRunState) SuccessfulMutationPaths() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return projectEinoAssistantObservedReadPaths(s.successfulMutationPaths)
}

func (s *projectEinoAssistantRunState) PatchFingerprint(patch string) (string, uint64, bool) {
	if s == nil {
		return "", 0, false
	}
	normalized := strings.TrimSpace(strings.ReplaceAll(patch, "\r\n", "\n"))
	digest := sha256.Sum256([]byte(normalized))
	fingerprint := hex.EncodeToString(digest[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	revision := s.sourceMutationRevision
	failedRevision, found := s.failedPatchFingerprints[fingerprint]
	return fingerprint, revision, found && failedRevision == revision
}

func (s *projectEinoAssistantRunState) RecordFailedPatchFingerprint(fingerprint string, revision uint64) {
	if s == nil || strings.TrimSpace(fingerprint) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision != s.sourceMutationRevision {
		return
	}
	if s.failedPatchFingerprints == nil {
		s.failedPatchFingerprints = map[string]uint64{}
	}
	if _, exists := s.failedPatchFingerprints[fingerprint]; !exists && len(s.failedPatchFingerprints) >= projectEinoAssistantMaxFailedPatchFingerprints {
		keys := make([]string, 0, len(s.failedPatchFingerprints))
		for key := range s.failedPatchFingerprints {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		delete(s.failedPatchFingerprints, keys[0])
	}
	s.failedPatchFingerprints[fingerprint] = revision
}

func projectEinoAssistantRestoreFailedPatchFingerprints(values map[string]uint64, revision uint64) map[string]uint64 {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for fingerprint, failedRevision := range values {
		fingerprint = strings.TrimSpace(fingerprint)
		if len(fingerprint) != sha256.Size*2 || failedRevision != revision {
			continue
		}
		if _, err := hex.DecodeString(fingerprint); err != nil {
			continue
		}
		keys = append(keys, fingerprint)
	}
	sort.Strings(keys)
	if len(keys) > projectEinoAssistantMaxFailedPatchFingerprints {
		keys = keys[:projectEinoAssistantMaxFailedPatchFingerprints]
	}
	restored := make(map[string]uint64, len(keys))
	for _, fingerprint := range keys {
		restored[fingerprint] = revision
	}
	if len(restored) == 0 {
		return nil
	}
	return restored
}

func (s *projectEinoAssistantRunState) RecordPatchRecoveryRead(path string) {
	if s == nil {
		return
	}
	path, err := workspace.CleanProjectPath(path)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if path == s.patchRecoveryPath {
		s.patchRecoveryReadComplete = true
	}
}

func (s *projectEinoAssistantRunState) RuntimeWarmupAttempts() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeWarmupAttempts
}

func (s *projectEinoAssistantRunState) PatchFailureCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.patchFailureCount
}

func (s *projectEinoAssistantRunState) RecordCompletedAction(name, arguments string, madeProgress bool) {
	if s == nil {
		return
	}
	name = projectToolBaseName(name)
	signature := projectEinoAssistantToolCallSignature(name, arguments)
	s.mu.Lock()
	defer s.mu.Unlock()
	if signature == s.repeatedActionSignature {
		s.repeatedActionCount++
	} else {
		s.repeatedActionSignature = signature
		s.repeatedActionToolName = name
		s.repeatedActionCount = 1
	}
	if s.actionBatchModelCall != s.modelCallOrdinal {
		s.actionBatchModelCall = s.modelCallOrdinal
		s.actionBatchObserved = false
		s.actionBatchMadeProgress = false
	}
	wasObserved := s.actionBatchObserved
	s.actionBatchObserved = true
	if madeProgress {
		s.actionBatchMadeProgress = true
		s.noProgressModelCallCount = 0
		return
	}
	if !wasObserved && !s.actionBatchMadeProgress {
		s.noProgressModelCallCount++
	}
	s.repeatedActionToolName = name
}

func (s *projectEinoAssistantRunState) RepeatedCompletedAction() (string, int) {
	if s == nil {
		return "", 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noProgressModelCallCount > s.repeatedActionCount {
		return "", s.noProgressModelCallCount
	}
	return s.repeatedActionToolName, s.repeatedActionCount
}

func (s *projectEinoAssistantRunState) NextModelCallOrdinal() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelCallOrdinal++
	return s.modelCallOrdinal
}

func (s *projectEinoAssistantRunState) CurrentModelCallOrdinal() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelCallOrdinal
}

func (s *projectEinoAssistantRunState) RecordModelInput(messages []chatMessage) {
	if s == nil {
		return
	}
	messages = cloneChatMessages(messages)
	for index := range messages {
		if messages[index].Role == "tool" &&
			projectToolBaseName(messages[index].Name) == projectToolGetPreviewConsoleLogs {
			messages[index].Content = projectEinoAssistantPersistentToolResult(
				messages[index].Name,
				messages[index].Content,
			)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = messages
}

func (s *projectEinoAssistantRunState) RecordAssistantReply(reply projectAssistantReply) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(reply.ToolCalls) > 0 {
		ensureProjectToolCallIDs(reply.ToolCalls, s.modelCallOrdinal)
		s.toolCalls = cloneProjectAssistantToolCalls(reply.ToolCalls)
		for _, tc := range reply.ToolCalls {
			sig := projectEinoAssistantToolCallSignature(tc.Function.Name, tc.Function.Arguments)
			s.seenToolCalls[sig]++
		}
		s.messages = append(s.messages, chatMessage{
			Role:      "assistant",
			Content:   reply.Content,
			ToolCalls: cloneProjectAssistantToolCalls(reply.ToolCalls),
		})
		s.turn++
		return
	}
	if strings.TrimSpace(reply.Content) != "" {
		s.messages = append(s.messages, chatMessage{
			Role:    "assistant",
			Content: reply.Content,
		})
	}
}

func (s *projectEinoAssistantRunState) RecordSteeringInput(content string) {
	if s == nil || strings.TrimSpace(content) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, chatMessage{Role: "user", Content: strings.TrimSpace(content)})
}

// DeferSteeringOnceAfterCompaction preserves Codex's continuation ordering:
// when a completed tool result forced the next model step, that continuation
// gets the first request in the new context window. Persisted steering remains
// queued for the following model-safe boundary.
func (s *projectEinoAssistantRunState) DeferSteeringOnceAfterCompaction() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deferSteeringOnce = true
}

func (s *projectEinoAssistantRunState) TakeSteeringDeferral() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deferred := s.deferSteeringOnce
	s.deferSteeringOnce = false
	return deferred
}

func (s *projectEinoAssistantRunState) ModelMessages() []chatMessage {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneChatMessages(s.messages)
}

func (s *projectEinoAssistantRunState) RecordToolMessage(msg chatMessage) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cloned := cloneChatMessages([]chatMessage{msg})[0]
	s.messages = append(s.messages, cloned)
	s.lastToolMessages = []chatMessage{cloned}
	s.toolEvidence = append(s.toolEvidence, cloned)
	if len(s.toolEvidence) > projectEinoAssistantClosingEvidenceMaxItems {
		s.toolEvidence = cloneChatMessages(s.toolEvidence[len(s.toolEvidence)-projectEinoAssistantClosingEvidenceMaxItems:])
	}
}

func (s *projectEinoAssistantRunState) ReadOnlyPreviewInspectionObserved() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := len(s.toolEvidence) - 1; index >= 0; index-- {
		message := s.toolEvidence[index]
		if projectToolBaseName(message.Name) != projectToolInspectDevelopmentPreview {
			continue
		}
		var evidence struct {
			EvidenceScope       string `json:"evidenceScope"`
			InteractionEvidence bool   `json:"interactionEvidence"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(message.Content)), &evidence) == nil &&
			evidence.EvidenceScope == "rendered_state_only" && !evidence.InteractionEvidence {
			return true
		}
	}
	return false
}

func (s *projectEinoAssistantRunState) PermissionBarrierActive() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.permissionBarrier
}

func (s *projectEinoAssistantRunState) TryStartPermissionBarrier() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.permissionBarrier {
		return false
	}
	s.permissionBarrier = true
	return true
}

func (s *projectEinoAssistantRunState) ToolCallByID(callID, name, arguments string) (chatToolCall, int, []chatToolCall) {
	if s == nil {
		return projectEinoAssistantFallbackToolCall(callID, name, arguments), 0, []chatToolCall{projectEinoAssistantFallbackToolCall(callID, name, arguments)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	toolCalls := cloneProjectAssistantToolCalls(s.toolCalls)
	for i, tc := range toolCalls {
		if strings.TrimSpace(callID) != "" && tc.ID == callID {
			return tc, i, toolCalls
		}
	}
	tc := projectEinoAssistantFallbackToolCall(callID, name, arguments)
	if len(toolCalls) == 0 {
		toolCalls = []chatToolCall{tc}
	}
	return tc, 0, toolCalls
}

func (s *projectEinoAssistantRunState) CheckpointState() projectAssistantCheckpointState {
	if s == nil {
		return projectAssistantCheckpointState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rolloutBudget := cloneProjectAssistantRolloutBudgetStatePtr(s.restoredRolloutBudget)
	if s.rolloutBudget != nil {
		rolloutBudget = s.rolloutBudget.Snapshot()
	}
	return projectAssistantCheckpointState{
		Messages:                  cloneChatMessages(s.messages),
		LastToolMessages:          cloneChatMessages(s.lastToolMessages),
		ToolCalls:                 cloneProjectAssistantToolCalls(s.toolCalls),
		SeenToolCalls:             projectEinoAssistantSanitizeSeenToolCalls(s.seenToolCalls),
		Turn:                      s.turn,
		ProjectRepositoryRef:      strings.TrimSpace(s.projectRepositoryRef),
		TurnPolicy:                projectAssistantCheckpointTurnPolicyForPolicy(s.turnPolicy),
		ApprovedPlan:              cloneProjectAssistantApprovedPlan(s.approvedPlan),
		ExecutionPlan:             cloneProjectAssistantApprovedPlan(s.executionPlan),
		PlanProgress:              cloneProjectAssistantPlanSnapshot(s.planProgress),
		SourceMutationRevision:    s.sourceMutationRevision,
		VerifiedMutationRevision:  s.verifiedMutationRevision,
		DevelopmentSyncRevision:   s.developmentSyncRevision,
		DevelopmentSyncStatus:     strings.TrimSpace(s.developmentSyncStatus),
		DevelopmentSyncFailure:    strings.TrimSpace(s.developmentSyncFailure),
		DevelopmentSyncRetry:      s.developmentSyncRetry,
		CommitRequired:            s.commitRequired,
		CommittedMutationRevision: s.committedMutationRevision,
		CommitAttemptedRevision:   s.commitAttemptedRevision,
		VerifiedWorkspaceDigest:   s.verifiedWorkspaceDigest,
		CommittedWorkspaceDigest:  s.committedWorkspaceDigest,
		CheckedMutationRevision:   s.checkedMutationRevision,
		VerificationAttempted:     s.verificationAttempted,
		VerificationOutcome:       strings.TrimSpace(s.verificationOutcome),
		VerificationSummary:       strings.TrimSpace(s.verificationSummary),
		VerificationBlockers:      append([]string(nil), s.verificationBlockers...),
		RepeatedActionSignature:   s.repeatedActionSignature,
		RepeatedActionToolName:    s.repeatedActionToolName,
		RepeatedActionCount:       s.repeatedActionCount,
		PatchFailureCount:         s.patchFailureCount,
		FailedPatchFingerprints:   projectEinoAssistantRestoreFailedPatchFingerprints(s.failedPatchFingerprints, s.sourceMutationRevision),
		PatchRecoveryPath:         s.patchRecoveryPath,
		PatchRecoveryReadComplete: s.patchRecoveryReadComplete,
		RuntimeWarmupAttempts:     s.runtimeWarmupAttempts,
		NoProgressModelCallCount:  s.noProgressModelCallCount,
		ActionBatchModelCall:      s.actionBatchModelCall,
		ActionBatchObserved:       s.actionBatchObserved,
		ActionBatchMadeProgress:   s.actionBatchMadeProgress,
		ModelCallOrdinal:          s.modelCallOrdinal,
		CompletedReadCalls:        projectEinoAssistantCloneCompletedReads(s.completedReadCalls),
		ReadFileCoverage:          projectEinoAssistantCheckpointReadCoverage(s.readFileCoverage),
		ObservedReadFilePaths:     projectEinoAssistantObservedReadPaths(s.observedReadFilePaths),
		SuccessfulMutationPaths:   projectEinoAssistantObservedReadPaths(s.successfulMutationPaths),
		SessionSnapshot:           cloneProjectEinoAssistantSessionSnapshot(s.sessionSnapshot),
		RolloutBudget:             rolloutBudget,
	}
}

func cloneProjectAssistantRolloutBudgetStatePtr(state *projectAssistantRolloutBudgetState) *projectAssistantRolloutBudgetState {
	if state == nil {
		return nil
	}
	copy := cloneProjectAssistantRolloutBudgetState(*state)
	return &copy
}

func projectEinoAssistantReadPathSet(paths []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, raw := range paths {
		path, err := workspace.CleanProjectPath(raw)
		if err == nil {
			out[path] = struct{}{}
		}
	}
	return out
}

func projectEinoAssistantObservedReadPaths(paths map[string]struct{}) []string {
	out := make([]string, 0, len(paths))
	for path := range paths {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func cloneProjectAssistantPlanSnapshot(plan projectAssistantPlanSnapshot) projectAssistantPlanSnapshot {
	return projectAssistantPlanSnapshot{
		Steps: append([]projectAssistantPlanStep(nil), plan.Steps...),
	}
}

func projectEinoAssistantToolCallSignature(name, arguments string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(name) + "\x00" + arguments))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func projectEinoAssistantSanitizeActionSignature(signature string) string {
	signature = strings.TrimSpace(signature)
	if len(signature) == len("sha256:")+sha256.Size*2 && strings.HasPrefix(signature, "sha256:") {
		return signature
	}
	return ""
}

func projectEinoAssistantSanitizeCompletedReads(in map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, min(len(in), projectEinoAssistantMaxTrackedReads))
	for signature, revision := range in {
		signature = projectEinoAssistantSanitizeActionSignature(signature)
		if signature == "" || revision == 0 || len(out) >= projectEinoAssistantMaxTrackedReads {
			continue
		}
		out[signature] = revision
	}
	return out
}

func projectEinoAssistantCloneCompletedReads(in map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(in))
	for signature, revision := range in {
		out[signature] = revision
	}
	return out
}

func projectEinoAssistantRestoreReadCoverage(
	in map[string][]projectAssistantCheckpointLineRange,
) map[string][]projectEinoAssistantLineRange {
	out := make(map[string][]projectEinoAssistantLineRange, min(len(in), projectEinoAssistantMaxTrackedReads))
	total := 0
	for path, ranges := range in {
		path = strings.TrimSpace(path)
		if path == "" || total >= projectEinoAssistantMaxTrackedReads {
			continue
		}
		for _, lineRange := range ranges {
			end := min(lineRange.End, uint64(projectEinoAssistantReadThroughEOF))
			if lineRange.Start > 0 && end >= uint64(lineRange.Start) {
				out[path] = append(out[path], projectEinoAssistantLineRange{start: lineRange.Start, end: int(end)})
				total++
				if total >= projectEinoAssistantMaxTrackedReads {
					break
				}
			}
		}
	}
	return out
}

func projectEinoAssistantCheckpointReadCoverage(
	in map[string][]projectEinoAssistantLineRange,
) map[string][]projectAssistantCheckpointLineRange {
	out := make(map[string][]projectAssistantCheckpointLineRange, len(in))
	total := 0
	for path, ranges := range in {
		for _, lineRange := range ranges {
			if total >= projectEinoAssistantMaxTrackedReads {
				return out
			}
			out[path] = append(out[path], projectAssistantCheckpointLineRange{
				Start: lineRange.start,
				End:   uint64(max(lineRange.end, 0)),
			})
			total++
		}
	}
	return out
}

func projectEinoAssistantCollectToolEvidence(messages []chatMessage) []chatMessage {
	evidence := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "tool" {
			evidence = append(evidence, cloneChatMessages([]chatMessage{msg})[0])
		}
	}
	if len(evidence) > projectEinoAssistantClosingEvidenceMaxItems {
		evidence = evidence[len(evidence)-projectEinoAssistantClosingEvidenceMaxItems:]
	}
	return evidence
}

func projectEinoAssistantSanitizeSeenToolCalls(src map[string]int) map[string]int {
	out := make(map[string]int, len(src))
	for signature, count := range src {
		if !strings.HasPrefix(signature, "sha256:") {
			sum := sha256.Sum256([]byte(signature))
			signature = "sha256:" + hex.EncodeToString(sum[:])
		}
		out[signature] += count
	}
	return out
}

func cloneProjectAssistantApprovedPlan(src *projectAssistantApprovedPlan) *projectAssistantApprovedPlan {
	if src == nil {
		return nil
	}
	out := *src
	out.Steps = append([]string(nil), src.Steps...)
	out.TargetPaths = append([]string(nil), src.TargetPaths...)
	if src.Capabilities != nil {
		out.Capabilities = append([]string{}, src.Capabilities...)
	}
	out.AcceptanceCriteria = append([]string(nil), src.AcceptanceCriteria...)
	return &out
}

func normalizeProjectAssistantApprovedPlan(plan projectAssistantApprovedPlan) projectAssistantApprovedPlan {
	plan.Goal = strings.TrimSpace(plan.Goal)
	plan.Summary = strings.TrimSpace(plan.Summary)
	plan.Steps = normalizeProjectAssistantStringList(plan.Steps)
	plan.TargetPaths = normalizeProjectAssistantStringList(plan.TargetPaths)
	plan.Capabilities = normalizeProjectAssistantStringList(plan.Capabilities)
	plan.AcceptanceCriteria = normalizeProjectAssistantStringList(plan.AcceptanceCriteria)
	plan.ApprovalTool = strings.TrimSpace(plan.ApprovalTool)
	if plan.ApprovedAt.IsZero() {
		plan.ApprovedAt = time.Now().UTC()
	} else {
		plan.ApprovedAt = plan.ApprovedAt.UTC()
	}
	return plan
}

func normalizeProjectAssistantStringList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func projectEinoAssistantFallbackToolCall(callID, name, arguments string) chatToolCall {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		callID = "tool-1"
	}
	return chatToolCall{
		ID:   callID,
		Type: "function",
		Function: chatToolCallFunction{
			Name:      strings.TrimSpace(name),
			Arguments: strings.TrimSpace(arguments),
		},
	}
}
