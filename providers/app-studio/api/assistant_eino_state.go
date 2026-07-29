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
	"strings"
	"sync"
	"time"
)

const (
	projectAssistantApprovedPlanVersionWorkspaceMutation = 2
	projectAssistantCapabilityWorkspaceMutate            = "workspace.mutate"
)

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
	verificationAttempted     bool
	verificationOutcome       string
	completedReadCalls        map[string]uint64
}

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
		seenToolCalls:      map[string]int{},
		completedReadCalls: map[string]uint64{},
		turnPolicy:         projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDiscussion),
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
	s.verifiedMutationRevision = state.VerifiedMutationRevision
	s.verificationAttempted = state.VerificationAttempted
	s.verificationOutcome = strings.TrimSpace(state.VerificationOutcome)
	s.completedReadCalls = map[string]uint64{}
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
	} else {
		s.verificationOutcome = "not_ready"
	}
	if ready && s.sourceMutationRevision > 0 {
		s.verifiedMutationRevision = s.sourceMutationRevision
		return
	}
	s.verifiedMutationRevision = 0
}

func (s *projectEinoAssistantRunState) RecordDevelopmentVerificationResult(content string) {
	status := ""
	var payload struct {
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &payload) == nil {
		status = strings.ToLower(strings.TrimSpace(payload.Status))
	}
	ready := projectEinoAssistantPhaseVerificationReady(content)
	s.RecordDevelopmentVerification(ready)
	if ready {
		status = "ready"
	}
	if status == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
		PlanDefined:            planDefined,
		PlanComplete:           planComplete,
		LatestMutationVerified: latestVerified,
		VerificationOutcome:    outcome,
	}
	if planDefined && (!planComplete || !latestVerified) {
		evidence.Blockers = append(evidence.Blockers, "initial project objective is incomplete")
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
	s.completedReadCalls[signature] = s.sourceMutationRevision + 1
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
		VerificationAttempted:     s.verificationAttempted,
		VerificationOutcome:       strings.TrimSpace(s.verificationOutcome),
		SessionSnapshot:           cloneProjectEinoAssistantSessionSnapshot(s.sessionSnapshot),
	}
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
