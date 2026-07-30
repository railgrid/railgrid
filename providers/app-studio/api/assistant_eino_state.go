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
	permissionBarrier         bool
	approvedPlan              *projectAssistantApprovedPlan
	approvedPlanGrantRevision string
	executionPlan             *projectAssistantApprovedPlan
	executionPlanRevision     string
	planProgress              projectAssistantPlanSnapshot
	sourceMutationRevision    uint64
	verifiedMutationRevision  uint64
	checkedMutationRevision   uint64
	verificationAttempted     bool
	verificationOutcome       string
	verificationSummary       string
	verificationBlockers      []string
	completedReadCalls        map[string]uint64
	observedReadFilePaths     map[string]struct{}
	successfulMutationPaths   map[string]struct{}
	readFileCoverage          map[string][]projectEinoAssistantLineRange
	repeatedActionSignature   string
	repeatedActionToolName    string
	repeatedActionCount       int
	patchFailureCount         int
	patchRecoveryPath         string
	patchRecoveryReadComplete bool
	runtimeWarmupAttempts     int
	noProgressModelCallCount  int
	actionBatchModelCall      int
	actionBatchObserved       bool
	actionBatchMadeProgress   bool
	modelCallOrdinal          int
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
		turnPolicy:              projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDiscussion),
	}
}

func (s *projectEinoAssistantRunState) SetTurnProfile(profile projectAssistantTurnProfile) {
	s.SetTurnPolicy(projectAssistantTurnPolicyForProfile(profile))
}

func (s *projectEinoAssistantRunState) SetTurnPolicy(policy projectAssistantTurnPolicy) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnPolicy = normalizeProjectAssistantTurnPolicy(policy, projectAssistantTurnProfileDiscussion)
}

func (s *projectEinoAssistantRunState) TurnProfile() projectAssistantTurnProfile {
	return s.TurnPolicy().profile
}

func (s *projectEinoAssistantRunState) TurnPolicy() projectAssistantTurnPolicy {
	if s == nil {
		return projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDiscussion)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return normalizeProjectAssistantTurnPolicy(s.turnPolicy, projectAssistantTurnProfileDiscussion)
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
	s.approvedPlanGrantRevision = strings.TrimSpace(state.ApprovedPlanGrantRevision)
	s.executionPlan = cloneProjectAssistantApprovedPlan(state.ExecutionPlan)
	s.executionPlanRevision = strings.TrimSpace(state.ExecutionPlanRevision)
	s.planProgress = cloneProjectAssistantPlanSnapshot(state.PlanProgress)
	s.sourceMutationRevision = state.SourceMutationRevision
	s.checkedMutationRevision = state.CheckedMutationRevision
	s.verifiedMutationRevision = state.VerifiedMutationRevision
	s.verificationAttempted = state.VerificationAttempted
	s.verificationOutcome = strings.TrimSpace(state.VerificationOutcome)
	s.verificationSummary = strings.TrimSpace(state.VerificationSummary)
	s.verificationBlockers = append([]string(nil), state.VerificationBlockers...)
	s.repeatedActionSignature = projectEinoAssistantSanitizeActionSignature(state.RepeatedActionSignature)
	s.repeatedActionToolName = projectToolBaseName(state.RepeatedActionToolName)
	s.repeatedActionCount = min(max(state.RepeatedActionCount, 0), projectEinoAssistantRepeatedActionLimit)
	s.patchFailureCount = min(max(state.PatchFailureCount, 0), projectEinoAssistantRepeatedActionLimit)
	if recoveryPath, err := workspace.CleanProjectPath(state.PatchRecoveryPath); err == nil {
		s.patchRecoveryPath = recoveryPath
		s.patchRecoveryReadComplete = state.PatchRecoveryReadComplete
	}
	s.runtimeWarmupAttempts = min(max(state.RuntimeWarmupAttempts, 0), projectEinoAssistantRepeatedActionLimit)
	// Legacy checkpoints counted every tool invocation and could therefore
	// exhaust the guard within one model response. Start those checkpoints with
	// a clean model-call counter rather than carrying forward false strikes.
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

func (s *projectEinoAssistantRunState) SetExecutionPlan(plan projectAssistantApprovedPlan, revision string) {
	if s == nil {
		return
	}
	normalized := normalizeProjectAssistantApprovedPlan(plan)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executionPlan = &normalized
	s.executionPlanRevision = strings.TrimSpace(revision)
}

func (s *projectEinoAssistantRunState) ExecutionPlan() (*projectAssistantApprovedPlan, string) {
	if s == nil {
		return nil, ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectAssistantApprovedPlan(s.executionPlan), s.executionPlanRevision
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
			strings.TrimSpace(step.Content) != strings.TrimSpace(s.executionPlan.Steps[index]) {
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
			Content: strings.TrimSpace(content),
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

func (s *projectEinoAssistantRunState) SetApprovedPlanGrantRevision(revision string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvedPlanGrantRevision = strings.TrimSpace(revision)
}

func (s *projectEinoAssistantRunState) ApprovedPlanGrantRevision() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approvedPlanGrantRevision
}

func (s *projectEinoAssistantRunState) RecordSourceMutation() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourceMutationRevision++
	s.verifiedMutationRevision = 0
	s.checkedMutationRevision = 0
	s.verificationAttempted = false
	s.verificationOutcome = ""
	s.verificationSummary = ""
	s.verificationBlockers = nil
	s.runtimeWarmupAttempts = 0
	s.completedReadCalls = map[string]uint64{}
	s.readFileCoverage = map[string][]projectEinoAssistantLineRange{}
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
	if payload.CheckedMutationRevision == 0 {
		// Legacy live verifier implementations did not echo the revision. The
		// result is still bound to the current revision at this synchronous
		// lifecycle boundary. Restored legacy checkpoints remain unverified
		// because they do not carry CheckedMutationRevision.
		payload.CheckedMutationRevision = s.sourceMutationRevision
	}
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

func (s *projectEinoAssistantRunState) RewriteCompletionAsVerification(message *schema.Message) {
	if s == nil || message == nil || len(message.ToolCalls) == 0 {
		return
	}
	reply := projectEinoAssistantReplyFromMessage(message)
	ensureProjectToolCallIDs(reply.ToolCalls)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) > 0 {
		last := s.messages[len(s.messages)-1]
		if last.Role == "assistant" && len(last.ToolCalls) == 0 {
			s.messages = s.messages[:len(s.messages)-1]
		}
	}
	s.toolCalls = cloneProjectAssistantToolCalls(reply.ToolCalls)
	for _, toolCall := range reply.ToolCalls {
		signature := projectEinoAssistantToolCallSignature(toolCall.Function.Name, toolCall.Function.Arguments)
		s.seenToolCalls[signature]++
	}
	s.messages = append(s.messages, chatMessage{
		Role:      "assistant",
		ToolCalls: cloneProjectAssistantToolCalls(reply.ToolCalls),
	})
	s.turn++
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
			strings.TrimSpace(step.Content) != strings.TrimSpace(s.executionPlan.Steps[index]) {
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
		PlanDefined:              planDefined,
		PlanComplete:             planComplete,
		SourceMutationRevision:   s.sourceMutationRevision,
		VerifiedMutationRevision: s.verifiedMutationRevision,
		LatestMutationVerified:   latestVerified,
		VerificationOutcome:      outcome,
		VerificationSummary:      strings.TrimSpace(s.verificationSummary),
		Blockers:                 append([]string(nil), s.verificationBlockers...),
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

func (s *projectEinoAssistantRunState) SourceMutationRevisions() (uint64, uint64) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sourceMutationRevision, s.verifiedMutationRevision
}

func (s *projectEinoAssistantRunState) NeedsCompletionVerification() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sourceMutationRevision > 0 &&
		s.checkedMutationRevision != s.sourceMutationRevision
}

func (s *projectEinoAssistantRunState) RepeatedCompletedRead(name, arguments string) bool {
	if s == nil {
		return false
	}
	signature := projectEinoAssistantToolCallSignature(name, arguments)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completedReadCalls[signature] == s.sourceMutationRevision+1
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

func (s *projectEinoAssistantRunState) ReadFileRangeCovered(path string, start, end int) bool {
	if s == nil || path == "" || start <= 0 || end < start {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, covered := range s.readFileCoverage[path] {
		if start >= covered.start && end <= covered.end {
			return true
		}
	}
	return false
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

func (s *projectEinoAssistantRunState) ReopenReadFile(path string) {
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
}

func (s *projectEinoAssistantRunState) CompletedReadFilePaths() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := make([]string, 0, len(s.readFileCoverage))
	for path, ranges := range s.readFileCoverage {
		if strings.TrimSpace(path) == "" || len(ranges) == 0 {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
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

func (s *projectEinoAssistantRunState) RecordPatchResult(successful bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if successful {
		s.patchFailureCount = 0
		return
	}
	s.patchFailureCount = min(s.patchFailureCount+1, projectEinoAssistantRepeatedActionLimit)
}

func (s *projectEinoAssistantRunState) StartPatchRecovery(path string) {
	if s == nil {
		return
	}
	path, err := workspace.CleanProjectPath(path)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patchRecoveryPath = path
	s.patchRecoveryReadComplete = false
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

func (s *projectEinoAssistantRunState) ClearPatchRecovery(path string) {
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
		s.patchRecoveryPath = ""
		s.patchRecoveryReadComplete = false
	}
}

func (s *projectEinoAssistantRunState) PatchRecovery() (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.patchRecoveryPath, s.patchRecoveryReadComplete
}

func (s *projectEinoAssistantRunState) RuntimeWarmupAttempts() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeWarmupAttempts
}

func (s *projectEinoAssistantRunState) BeginRuntimeWarmupModelCall() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimeWarmupAttempts = min(s.runtimeWarmupAttempts+1, projectEinoAssistantRepeatedActionLimit)
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

func (s *projectEinoAssistantRunState) ConsecutiveNoProgressModelCalls() (string, int) {
	if s == nil {
		return "", 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return "", s.noProgressModelCallCount
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

func (s *projectEinoAssistantRunState) RecordModelInput(messages []chatMessage) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = cloneChatMessages(messages)
}

func (s *projectEinoAssistantRunState) RecordAssistantReply(reply projectAssistantReply) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(reply.ToolCalls) > 0 {
		ensureProjectToolCallIDs(reply.ToolCalls)
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
	return projectAssistantCheckpointState{
		Messages:                  cloneChatMessages(s.messages),
		LastToolMessages:          cloneChatMessages(s.lastToolMessages),
		ToolCalls:                 cloneProjectAssistantToolCalls(s.toolCalls),
		SeenToolCalls:             projectEinoAssistantSanitizeSeenToolCalls(s.seenToolCalls),
		Turn:                      s.turn,
		ProjectRepositoryRef:      strings.TrimSpace(s.projectRepositoryRef),
		TurnPolicy:                projectAssistantCheckpointTurnPolicyForPolicy(s.turnPolicy),
		ApprovedPlan:              cloneProjectAssistantApprovedPlan(s.approvedPlan),
		ApprovedPlanGrantRevision: strings.TrimSpace(s.approvedPlanGrantRevision),
		ExecutionPlan:             cloneProjectAssistantApprovedPlan(s.executionPlan),
		ExecutionPlanRevision:     strings.TrimSpace(s.executionPlanRevision),
		PlanProgress:              cloneProjectAssistantPlanSnapshot(s.planProgress),
		SourceMutationRevision:    s.sourceMutationRevision,
		VerifiedMutationRevision:  s.verifiedMutationRevision,
		CheckedMutationRevision:   s.checkedMutationRevision,
		VerificationAttempted:     s.verificationAttempted,
		VerificationOutcome:       strings.TrimSpace(s.verificationOutcome),
		VerificationSummary:       strings.TrimSpace(s.verificationSummary),
		VerificationBlockers:      append([]string(nil), s.verificationBlockers...),
		RepeatedActionSignature:   s.repeatedActionSignature,
		RepeatedActionToolName:    s.repeatedActionToolName,
		RepeatedActionCount:       s.repeatedActionCount,
		PatchFailureCount:         s.patchFailureCount,
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
	}
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

func (s *projectEinoAssistantRunState) ToolLoopFallback() string {
	if s == nil {
		return projectToolLoopFallback(nil, "kept requesting actions")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return projectToolLoopFallback(cloneChatMessages(s.lastToolMessages), s.toolLoopReasonLocked())
}

func (s *projectEinoAssistantRunState) ToolLoopFinalAnswerMessages() []chatMessage {
	if s == nil {
		return []chatMessage{{Role: "system", Content: projectEinoAssistantToolLoopFinalInstruction("kept requesting actions")}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	messages := make([]chatMessage, 0, len(s.messages)+2)
	for _, msg := range s.messages {
		if msg.Role == "tool" || len(msg.ToolCalls) > 0 {
			break
		}
		messages = append(messages, cloneChatMessages([]chatMessage{msg})[0])
	}
	if evidence := projectEinoAssistantToolLoopEvidenceContext(s.toolEvidence); evidence != "" {
		messages = append(messages, chatMessage{Role: "user", Content: evidence})
	}
	messages = append(messages, chatMessage{Role: "system", Content: projectEinoAssistantToolLoopFinalInstruction(s.toolLoopReasonLocked())})
	return messages
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

func (s *projectEinoAssistantRunState) toolLoopReasonLocked() string {
	reason := "kept requesting actions"
	for _, count := range s.seenToolCalls {
		if count > 1 {
			return "repeated the same action"
		}
	}
	return reason
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
