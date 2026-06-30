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
	"strings"
	"sync"
	"time"
)

type projectAssistantApprovedPlan struct {
	Summary            string    `json:"summary,omitempty"`
	Steps              []string  `json:"steps,omitempty"`
	TargetPaths        []string  `json:"targetPaths,omitempty"`
	Operations         []string  `json:"operations,omitempty"`
	AcceptanceCriteria []string  `json:"acceptanceCriteria,omitempty"`
	ApprovedAt         time.Time `json:"approvedAt,omitempty"`
	ApprovalTool       string    `json:"approvalTool,omitempty"`
	// AllowAllWrites grants every workspace write tool, on any path, until the
	// next commit. It is set when the user approves a write prompt directly (as
	// opposed to a model-supplied plan envelope), so a single "Allow" does not
	// re-prompt for each subsequent edit.
	AllowAllWrites bool `json:"allowAllWrites,omitempty"`
}

type projectEinoAssistantRunState struct {
	mu sync.Mutex

	messages             []chatMessage
	lastToolMessages     []chatMessage
	toolCalls            []chatToolCall
	seenToolCalls        map[string]int
	turn                 int
	turnPolicy           projectAssistantTurnPolicy
	projectRepositoryRef string
	toolPrompt           string
	toolDiscovery        *projectEinoAssistantToolDiscovery
	sessionSnapshot      *projectEinoAssistantSessionSnapshot
	permissionBarrier    bool
	approvedPlan         *projectAssistantApprovedPlan
}

func newProjectEinoAssistantRunState() *projectEinoAssistantRunState {
	return &projectEinoAssistantRunState{
		seenToolCalls: map[string]int{},
		turnPolicy:    projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDiscussion),
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
	s.toolCalls = cloneProjectAssistantToolCalls(state.ToolCalls)
	s.seenToolCalls = cloneProjectAssistantSeenToolCalls(state.SeenToolCalls)
	s.turn = state.Turn
	s.turnPolicy = projectAssistantTurnPolicyForCheckpoint(state)
	s.projectRepositoryRef = strings.TrimSpace(state.ProjectRepositoryRef)
	s.approvedPlan = cloneProjectAssistantApprovedPlan(state.ApprovedPlan)
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

func (s *projectEinoAssistantRunState) ClearApprovedPlan() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvedPlan = nil
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
			sig := tc.Function.Name + "\x00" + tc.Function.Arguments
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
		Messages:             cloneChatMessages(s.messages),
		LastToolMessages:     cloneChatMessages(s.lastToolMessages),
		ToolCalls:            cloneProjectAssistantToolCalls(s.toolCalls),
		SeenToolCalls:        cloneProjectAssistantSeenToolCalls(s.seenToolCalls),
		Turn:                 s.turn,
		ProjectRepositoryRef: strings.TrimSpace(s.projectRepositoryRef),
		TurnPolicy:           projectAssistantCheckpointTurnPolicyForPolicy(s.turnPolicy),
		ApprovedPlan:         cloneProjectAssistantApprovedPlan(s.approvedPlan),
		SessionSnapshot:      cloneProjectEinoAssistantSessionSnapshot(s.sessionSnapshot),
	}
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
	messages := make([]chatMessage, 0, len(s.messages)+len(s.lastToolMessages)+1)
	for _, msg := range s.messages {
		if msg.Role == "tool" || len(msg.ToolCalls) > 0 {
			break
		}
		messages = append(messages, cloneChatMessages([]chatMessage{msg})[0])
	}
	for _, msg := range s.lastToolMessages {
		if context := projectEinoAssistantToolLoopFinalToolContext(msg); context != "" {
			messages = append(messages, chatMessage{Role: "user", Content: context})
		}
	}
	messages = append(messages, chatMessage{Role: "system", Content: projectEinoAssistantToolLoopFinalInstruction(s.toolLoopReasonLocked())})
	return messages
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
	out.Operations = append([]string(nil), src.Operations...)
	out.AcceptanceCriteria = append([]string(nil), src.AcceptanceCriteria...)
	return &out
}

func normalizeProjectAssistantApprovedPlan(plan projectAssistantApprovedPlan) projectAssistantApprovedPlan {
	plan.Summary = strings.TrimSpace(plan.Summary)
	plan.Steps = normalizeProjectAssistantStringList(plan.Steps)
	plan.TargetPaths = normalizeProjectAssistantStringList(plan.TargetPaths)
	plan.Operations = normalizeProjectAssistantStringList(plan.Operations)
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
