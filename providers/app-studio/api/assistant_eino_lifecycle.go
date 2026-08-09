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
	"errors"
	"sort"
	"strings"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/faroshq/provider-app-studio/workspace"
)

// projectEinoAssistantLifecycle records durable effects without adding hidden
// verification or commit obligations to Eino's conversational loop.
type projectEinoAssistantLifecycle struct {
	*adk.BaseChatModelAgentMiddleware

	runState              *projectEinoAssistantRunState
	server                *Server
	req                   projectAssistantRunRequest
	repositoryRef         string
	workspace             *workspace.FileStore
	workspaceScope        workspace.Scope
	repositoryView        func(context.Context) (*ProjectRepositoryView, error)
	auditRecorder         *projectAssistantRunAuditRecorder
	steering              <-chan projectAssistantSteeringInput
	activateSteering      func(context.Context, []projectAssistantSteeringInput) error
	managedToolNames      map[string]struct{}
	liveContext           string
	liveContextReady      bool
	liveContextGeneration uint64
	liveContextSections   map[string]string
}

func projectEinoAssistantLifecycleMiddleware(
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	servers ...*Server,
) adk.ChatModelAgentMiddleware {
	lifecycle := &projectEinoAssistantLifecycle{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     runState,
		req:                          req,
		repositoryRef:                projectEinoAssistantProjectRepositoryRef(req),
		workspace:                    req.Workspace,
		workspaceScope:               req.WorkspaceScope,
		auditRecorder:                req.auditRecorder,
		steering:                     req.Steering,
		activateSteering:             req.ActivateSteering,
	}
	if len(servers) > 0 {
		lifecycle.server = servers[0]
	}
	if req.Client != nil && req.Project != nil {
		lifecycle.repositoryView = func(ctx context.Context) (*ProjectRepositoryView, error) {
			runCtx, err := refreshProjectAssistantWorkflowRunContext(ctx, projectAssistantWorkflowRunContext{
				Client:     req.Client,
				Project:    req.Project,
				Repository: req.Repository,
			})
			if err != nil {
				return nil, err
			}
			return runCtx.Repository, nil
		}
	}
	return lifecycle
}

func (m *projectEinoAssistantLifecycle) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if m.runState == nil {
		return ctx, state, nil
	}
	if state == nil {
		return ctx, state, nil
	}
	// A failed mutation gets one deterministic reread/repair attempt. Once the
	// same canonical target fails again at the same source revision, terminate
	// at this model boundary instead of sampling the model into an unbounded
	// recovery loop. Read-only/Q&A turns and permission waits remain outside
	// this implementation-only guard.
	if projectEinoAssistantProgressApplies(m.req, m.runState) && !m.runState.PermissionBarrierActive() {
		if err := m.runState.MutationRecoveryBlockedError(); err != nil {
			return ctx, state, err
		}
		if err := m.runState.NoProgressModelCallError(); err != nil {
			return ctx, state, err
		}
	}
	if err := m.refreshLiveRequestContext(ctx); err != nil {
		return ctx, state, err
	}
	if err := m.refreshExecutableToolContext(ctx, state, modelCtx); err != nil {
		return ctx, state, err
	}
	if !m.runState.TakeSteeringDeferral() {
		if _, err := projectEinoAssistantDrainSteeringAtBoundary(ctx, m.steering, m.runState, state, m.activateSteering); err != nil {
			return ctx, state, err
		}
	}
	budget := m.runState.RolloutBudget()
	if err := budget.ExhaustionError(); err != nil {
		return ctx, state, err
	}
	var rolloutBudgetRemaining *int64
	if reminder := budget.PendingReminder(); reminder != nil {
		state.Messages = append(state.Messages, projectEinoAssistantRolloutBudgetMessage(reminder))
		remaining := reminder.RemainingTokens
		rolloutBudgetRemaining = &remaining
		if err := budget.DeliverReminder(ctx, reminder); err != nil {
			return ctx, state, err
		}
	}
	if err := m.rewriteLiveContext(ctx, state); err != nil {
		return ctx, state, err
	}
	ordinal := m.runState.NextModelCallOrdinal()
	if m.auditRecorder != nil {
		sourceRevision, verifiedRevision := m.runState.SourceMutationRevisions()
		if err := m.auditRecorder.recordModelCall(
			ctx,
			ordinal,
			sourceRevision,
			verifiedRevision,
			rolloutBudgetRemaining,
			state.ToolInfos,
			nil,
			projectAssistantAuditInputBytes(state.Messages, state.ToolInfos),
		); err != nil {
			return ctx, state, err
		}
	}
	return ctx, state, nil
}

func (m *projectEinoAssistantLifecycle) refreshExecutableToolContext(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) error {
	if m == nil || m.server == nil || m.runState == nil || state == nil {
		return nil
	}
	discovery, ok := m.runState.ToolDiscovery()
	if !ok {
		return nil
	}
	tools, err := projectEinoAssistantToolsForDiscovery(ctx, m.server, m.req, m.runState, discovery)
	if err != nil {
		return err
	}
	infos := make([]*schema.ToolInfo, 0, len(tools))
	currentNames := make(map[string]struct{}, len(tools))
	exposedNames := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		info, infoErr := tool.Info(ctx)
		if infoErr != nil {
			return infoErr
		}
		if info == nil || strings.TrimSpace(info.Name) == "" {
			continue
		}
		name := projectAssistantToolKey(info.Name)
		currentNames[name] = struct{}{}
		if wrapped, isWrapped := tool.(projectEinoAssistantTool); isWrapped &&
			wrapped.searchSelectionRequired && !m.runState.DynamicToolSelected(name) {
			continue
		}
		if _, exists := exposedNames[name]; exists {
			continue
		}
		exposedNames[name] = struct{}{}
		infos = append(infos, info)
	}
	if m.managedToolNames == nil {
		m.managedToolNames = map[string]struct{}{}
	}
	frameworkInfos := make([]*schema.ToolInfo, 0, len(state.ToolInfos))
	for _, info := range state.ToolInfos {
		if info == nil {
			continue
		}
		name := projectAssistantToolKey(info.Name)
		if _, managed := m.managedToolNames[name]; managed {
			continue
		}
		if _, current := currentNames[name]; current {
			continue
		}
		frameworkInfos = append(frameworkInfos, info)
	}
	for name := range currentNames {
		m.managedToolNames[name] = struct{}{}
	}
	// Tool order is part of the model-visible request. Keep it deterministic
	// even when provider/MCP discovery returns the same capabilities in a
	// different order; a stable prefix lets provider-side prompt caches reuse
	// the request while preserving the existing tool contracts.
	state.ToolInfos = projectEinoAssistantStableToolInfos(append(frameworkInfos, infos...))
	state.DeferredToolInfos = nil
	if modelCtx != nil {
		modelCtx.Tools = state.ToolInfos
	}
	return nil
}

// projectEinoAssistantStableToolInfos returns a new slice sorted by the
// canonical tool name and contract presentation. The caller owns the returned
// slice; individual ToolInfo values are intentionally not mutated because the
// same values may still be referenced by Eino's tool node.
func projectEinoAssistantStableToolInfos(infos []*schema.ToolInfo) []*schema.ToolInfo {
	if len(infos) == 0 {
		return nil
	}
	type keyedToolInfo struct {
		info     *schema.ToolInfo
		name     string
		contract string
	}
	keyed := make([]keyedToolInfo, 0, len(infos))
	for _, info := range infos {
		entry := keyedToolInfo{info: info}
		if info != nil {
			entry.name = projectAssistantToolKey(info.Name)
		}
		// ToolInfo's custom JSON marshaler includes the provider-facing schema
		// and description while keeping map keys deterministic. It is used only
		// as a tie-breaker for duplicate/case-variant names.
		if info != nil {
			raw, err := json.Marshal(info)
			if err != nil {
				entry.contract = strings.TrimSpace(info.Desc)
			} else {
				entry.contract = string(raw)
			}
		}
		keyed = append(keyed, entry)
	}
	sort.SliceStable(keyed, func(i, j int) bool {
		if keyed[i].name != keyed[j].name {
			return keyed[i].name < keyed[j].name
		}
		return keyed[i].contract < keyed[j].contract
	})
	out := make([]*schema.ToolInfo, len(keyed))
	for i := range keyed {
		out[i] = keyed[i].info
	}
	return out
}

func (m *projectEinoAssistantLifecycle) refreshLiveRequestContext(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if m.req.Client != nil && m.req.Project != nil && strings.TrimSpace(m.req.Project.Name) != "" {
		current, err := refreshProjectAssistantWorkflowRunContext(ctx, projectAssistantWorkflowRunContext{
			Client:     m.req.Client,
			Project:    m.req.Project,
			Repository: m.req.Repository,
		})
		// This is a fresh-view attempt, not a new availability dependency: a
		// transient project read must not discard the request view that was
		// already authorized when the run began.
		if err == nil {
			m.req.Project = current.Project
			m.req.Repository = current.Repository
		}
	}
	m.refreshRepositoryState(ctx)
	// newAgent resolves the first request's executable tool set immediately
	// before this hook runs. Reuse that just-captured view for its first sample;
	// every later sample refreshes discovery before rebuilding prompt context.
	if m.server != nil && m.runState.CurrentModelCallOrdinal() > 0 {
		m.runState.SetToolDiscovery(projectEinoAssistantDiscoverTools(ctx, m.server, m.req))
	}
	// Publish only after every live field needed by prompt construction and
	// dispatch has been refreshed. Tool wrappers read this same immutable copy
	// when Eino starts executing the accepted model response.
	m.req.publishExecutionRequest()
	return nil
}

func (m *projectEinoAssistantLifecycle) rewriteLiveContext(ctx context.Context, state *adk.ChatModelAgentState) error {
	if m == nil || state == nil || m.req.Project == nil {
		return nil
	}
	sections := m.liveRequestContextSections(ctx)
	contextMessages := make([]chatMessage, 0, len(sections))
	for _, section := range sections {
		contextMessages = append(contextMessages, section.message)
	}
	digest := projectEinoAssistantLiveContextDigest(sections)
	contextGeneration := m.runState.ModelContextGeneration()
	if m.liveContextReady && m.liveContextGeneration != contextGeneration {
		// Codex clears its reference context baseline when compaction replaces
		// history. The next model boundary must rebuild the full canonical
		// context, even when its content digest is unchanged.
		m.liveContextReady = false
		m.liveContextSections = nil
	}
	if m.liveContextReady && digest == m.liveContext {
		return nil
	}
	if m.liveContextReady {
		updates := projectEinoAssistantLiveContextUpdates(sections, m.liveContextSections)
		if len(updates) == 0 {
			m.liveContext = digest
			m.liveContextSections = projectEinoAssistantCloneLiveContextSections(sections)
			return nil
		}
		live, err := projectChatMessagesToEino(updates)
		if err != nil {
			return err
		}
		boundary := len(state.Messages)
		for index, message := range state.Messages {
			if message == nil || !strings.HasPrefix(message.Content, projectEinoAssistantLiveContextPrefix) {
				boundary = index
				break
			}
		}
		withUpdate := make([]*schema.Message, 0, len(state.Messages)+len(live))
		withUpdate = append(withUpdate, state.Messages[:boundary]...)
		withUpdate = append(withUpdate, live...)
		withUpdate = append(withUpdate, state.Messages[boundary:]...)
		state.Messages = withUpdate
		m.liveContext = digest
		m.liveContextGeneration = contextGeneration
		m.liveContextSections = projectEinoAssistantCloneLiveContextSections(sections)
		return nil
	}
	live, err := projectChatMessagesToEino(contextMessages)
	if err != nil {
		return err
	}
	conversation := projectEinoMessagesToChat(state.Messages)
	conversation = projectEinoAssistantConversationPayload(conversation)
	conversationMessages, err := projectChatMessagesToEino(conversation)
	if err != nil {
		return err
	}
	state.Messages = append(live, conversationMessages...)
	m.liveContext = digest
	m.liveContextReady = true
	m.liveContextGeneration = contextGeneration
	m.liveContextSections = projectEinoAssistantCloneLiveContextSections(sections)
	return nil
}

type projectEinoAssistantLiveContextSection struct {
	name    string
	content string
	message chatMessage
}

func (m *projectEinoAssistantLifecycle) liveRequestContextSections(ctx context.Context) []projectEinoAssistantLiveContextSection {
	if m == nil || m.req.Project == nil {
		return nil
	}
	sections := []projectEinoAssistantLiveContextSection{{
		name: "project",
		content: projectSystemPromptForMode(
			m.req.Project,
			m.req.Repository,
			m.req.CollaborationMode,
			projectAssistantInitialBuildActive(m.req, m.runState),
		),
	}}
	sections[0].message = chatMessage{Role: "system", Content: projectEinoAssistantLiveContextPrefix + sections[0].content}
	if snapshot, ok := projectEinoAssistantSessionContextMessage(ctx, m.req, m.runState); ok {
		content := strings.TrimPrefix(snapshot.Content, projectEinoAssistantLiveContextPrefix)
		sections = append(sections, projectEinoAssistantLiveContextSection{
			name:    "session",
			content: content,
			message: chatMessage{Role: "system", Content: projectEinoAssistantLiveContextPrefix + content},
		})
	}
	if prompt := m.runState.ToolPrompt(); prompt != "" {
		sections = append(sections, projectEinoAssistantLiveContextSection{
			name: "tools", content: prompt,
			message: chatMessage{Role: "system", Content: projectEinoAssistantLiveContextPrefix + prompt},
		})
	}
	if prompt := m.runState.SkillPrompt(); prompt != "" {
		sections = append(sections, projectEinoAssistantLiveContextSection{
			name: "skills", content: prompt,
			message: chatMessage{Role: "system", Content: projectEinoAssistantLiveContextPrefix + prompt},
		})
	}
	return sections
}

func projectEinoAssistantLiveContextDigest(sections []projectEinoAssistantLiveContextSection) string {
	parts := make([]string, 0, len(sections))
	for _, section := range sections {
		parts = append(parts, section.name+"\x00"+section.content)
	}
	return strings.Join(parts, "\x00")
}

func projectEinoAssistantCloneLiveContextSections(sections []projectEinoAssistantLiveContextSection) map[string]string {
	if len(sections) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(sections))
	for _, section := range sections {
		cloned[section.name] = section.content
	}
	return cloned
}

func projectEinoAssistantLiveContextUpdates(
	sections []projectEinoAssistantLiveContextSection,
	previous map[string]string,
) []chatMessage {
	updates := make([]chatMessage, 0, len(sections))
	seen := make(map[string]struct{}, len(sections))
	for _, section := range sections {
		seen[section.name] = struct{}{}
		if prior, ok := previous[section.name]; ok && prior == section.content {
			continue
		}
		content := section.content
		if section.name == "project" {
			content = projectEinoAssistantProjectContextUpdateContent(content, previous[section.name])
		}
		updates = append(updates, chatMessage{Role: "system", Content: projectEinoAssistantLiveContextPrefix +
			"Context update since the previous model sample:\nSection: " + section.name + "\n" + content})
	}
	removed := make([]string, 0)
	for name := range previous {
		if _, ok := seen[name]; ok {
			continue
		}
		removed = append(removed, name)
	}
	sort.Strings(removed)
	for _, name := range removed {
		updates = append(updates, chatMessage{Role: "system", Content: projectEinoAssistantLiveContextPrefix +
			"Context update since the previous model sample:\nSection: " + name + "\nSection cleared."})
	}
	return updates
}

// The project section starts with a large, mostly static instruction block.
// Once the initial authoritative prompt is present, a refresh should carry
// only the project metadata that may have changed. Keep a collaboration-mode
// line when that specific live field changed; omitting unchanged instructions
// avoids replaying the full prompt on every model sample.
func projectEinoAssistantProjectContextUpdateContent(current, previous string) string {
	const metadataMarker = "Project metadata:\n"
	start := strings.Index(current, metadataMarker)
	if start < 0 {
		return current
	}
	metadata := current[start:]
	if end := strings.Index(metadata, "\nProject memory:"); end >= 0 {
		metadata = metadata[:end]
	}
	currentMemory := projectEinoAssistantProjectMemorySection(current)
	if currentMemory != "" && currentMemory != projectEinoAssistantProjectMemorySection(previous) {
		metadata += "\n" + currentMemory
	}
	currentMode := projectEinoAssistantCollaborationModeLine(current)
	if currentMode != "" && currentMode != projectEinoAssistantCollaborationModeLine(previous) {
		metadata = currentMode + "\n" + metadata
	}
	return metadata
}

func projectEinoAssistantProjectMemorySection(content string) string {
	const marker = "Project memory:\n"
	start := strings.Index(content, marker)
	if start < 0 {
		return ""
	}
	return strings.TrimSpace(content[start:])
}

func projectEinoAssistantCollaborationModeLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "Collaboration mode: ") {
			return line
		}
	}
	return ""
}

func projectEinoAssistantDrainSteeringAtBoundary(
	ctx context.Context,
	steering <-chan projectAssistantSteeringInput,
	runState *projectEinoAssistantRunState,
	state *adk.ChatModelAgentState,
	activate func(context.Context, []projectAssistantSteeringInput) error,
) (int, error) {
	if steering == nil || runState == nil {
		return 0, nil
	}
	inputs := make([]projectAssistantSteeringInput, 0, 1)
	for {
		select {
		case input, ok := <-steering:
			if !ok {
				return projectEinoAssistantActivateSteeringInputs(ctx, inputs, runState, state, activate)
			}
			content := strings.TrimSpace(input.Content)
			if content == "" {
				continue
			}
			input.Content = content
			inputs = append(inputs, input)
		default:
			return projectEinoAssistantActivateSteeringInputs(ctx, inputs, runState, state, activate)
		}
	}
}

func projectEinoAssistantActivateSteeringInputs(
	ctx context.Context,
	inputs []projectAssistantSteeringInput,
	runState *projectEinoAssistantRunState,
	state *adk.ChatModelAgentState,
	activate func(context.Context, []projectAssistantSteeringInput) error,
) (int, error) {
	if len(inputs) == 0 {
		return 0, nil
	}
	if activate != nil {
		if err := activate(ctx, inputs); err != nil {
			return 0, err
		}
	}
	for _, input := range inputs {
		runState.RecordSteeringInput(input.Content)
		if state != nil {
			state.Messages = append(state.Messages, schema.UserMessage(input.Content))
		}
	}
	return len(inputs), nil
}

func (m *projectEinoAssistantLifecycle) refreshRepositoryState(ctx context.Context) {
	if m == nil || m.runState == nil || m.repositoryView == nil {
		return
	}
	repository, err := m.repositoryView(ctx)
	if err != nil || repository == nil {
		return
	}
	if ref := strings.TrimSpace(repository.Ref); ref != "" {
		m.repositoryRef = ref
		m.runState.SetProjectRepositoryRef(ref)
	}
}

func (m *projectEinoAssistantLifecycle) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	if toolCtx == nil {
		return endpoint, nil
	}
	name := projectToolBaseName(toolCtx.Name)
	return func(ctx context.Context, argumentsInJSON string, opts ...einotool.Option) (string, error) {
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			if name == projectToolVerifyDevelopmentRuntime && m.runState != nil {
				m.runState.RecordDevelopmentVerification(false)
			}
			if m.runState != nil && !projectEinoAssistantFilesystemReadTool(name) {
				if _, interrupted := compose.IsInterruptRerunError(err); !interrupted {
					if !projectEinoAssistantCommitTool(name) {
						m.runState.RecordCompletedAction(name, projectEinoAssistantCanonicalActionArguments(argumentsInJSON), false)
					}
				}
			}
			return result, err
		}
		succeeded := m.toolCallSucceeded(ctx, name, result)
		if name == projectEinoAssistantWriteTodosTool && succeeded {
			if planProgress, ok := m.settledPlanSnapshot(ctx); ok {
				previousPlan := m.runState.PlanProgress()
				projectEinoAssistantPublishAcceptedPlanProgress(m.runState, m.req.StreamCallbacks, planProgress)
				m.runState.QueuePlanProgressReminder(previousPlan, planProgress)
			}
		}
		switch {
		case name == projectToolVerifyDevelopmentRuntime:
			if m.runState != nil {
				m.runState.RecordDevelopmentVerificationResult(result)
				if m.runState.SourceMutationVerified() {
					var dirtyPaths []string
					var dirtyErr error
					if m.workspace == nil {
						dirtyErr = errors.New("project workspace store is not configured")
					} else {
						dirtyPaths, dirtyErr = m.workspace.UncommittedPaths(ctx, m.workspaceScope)
					}
					digest, digestErr := projectEinoAssistantWorkspaceDigest(ctx, m.workspace, m.workspaceScope, dirtyPaths)
					if dirtyErr != nil {
						digestErr = dirtyErr
					}
					if digestErr != nil {
						m.runState.RecordVerificationBindingFailure("operational verification could not be bound to current workspace content: " + digestErr.Error())
					} else {
						m.runState.RecordVerifiedWorkspaceDigest(digest)
					}
				}
			}
		}
		if m.runState != nil && !projectEinoAssistantFilesystemReadTool(name) &&
			!projectEinoAssistantCommitTool(name) && !projectEinoAssistantPendingPermissionResult(result) {
			successful := succeeded
			m.runState.RecordCompletedAction(name, projectEinoAssistantCanonicalActionArguments(argumentsInJSON), successful)
		}
		return result, nil
	}, nil
}

func (m *projectEinoAssistantLifecycle) WrapEnhancedInvokableToolCall(
	_ context.Context,
	endpoint adk.EnhancedInvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (adk.EnhancedInvokableToolCallEndpoint, error) {
	if toolCtx == nil {
		return endpoint, nil
	}
	name := projectToolBaseName(toolCtx.Name)
	return func(ctx context.Context, argument *schema.ToolArgument, opts ...einotool.Option) (*schema.ToolResult, error) {
		result, err := endpoint(ctx, argument, opts...)
		if m.runState == nil || projectEinoAssistantFilesystemReadTool(name) || projectEinoAssistantCommitTool(name) {
			return result, err
		}
		rawArguments := "{}"
		if argument != nil && strings.TrimSpace(argument.Text) != "" {
			rawArguments = argument.Text
		}
		succeeded := false
		if err == nil {
			succeeded = m.toolCallSucceeded(ctx, name, projectEinoAssistantEnhancedToolText(result))
		} else if _, interrupted := compose.IsInterruptRerunError(err); interrupted {
			return result, err
		}
		m.runState.RecordCompletedAction(name, projectEinoAssistantCanonicalActionArguments(rawArguments), succeeded)
		return result, err
	}, nil
}

func projectEinoAssistantEnhancedToolText(result *schema.ToolResult) string {
	if result == nil {
		return ""
	}
	for _, part := range result.Parts {
		if part.Type == schema.ToolPartTypeText {
			return part.Text
		}
	}
	return ""
}

func (m *projectEinoAssistantLifecycle) toolCallSucceeded(ctx context.Context, name, result string) bool {
	if m != nil && m.req.eventLedger != nil {
		outcome, ok, err := m.req.eventLedger.ToolCallOutcome(ctx, compose.GetToolCallID(ctx))
		return err == nil && ok && outcome.Succeeded()
	}
	return projectAssistantToolResultDisposition(name, result, nil) == projectAssistantToolDispositionSucceeded
}

func (m *projectEinoAssistantLifecycle) settledPlanSnapshot(ctx context.Context) (projectAssistantPlanSnapshot, bool) {
	if m == nil || m.req.eventLedger == nil {
		return projectAssistantPlanSnapshot{}, false
	}
	outcome, ok, err := m.req.eventLedger.ToolCallOutcome(ctx, compose.GetToolCallID(ctx))
	if err != nil || !ok || !outcome.Succeeded() || outcome.PlanSnapshot == nil {
		return projectAssistantPlanSnapshot{}, false
	}
	return cloneProjectAssistantPlanSnapshot(*outcome.PlanSnapshot), true
}

func projectEinoAssistantCanonicalActionArguments(raw string) string {
	args, err := projectEinoToolArguments(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return projectEinoToolArgumentsString(args)
}

func projectEinoAssistantCommitTool(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolCommitProjectFiles, projectToolCommitFiles:
		return true
	default:
		return false
	}
}

func projectEinoAssistantPendingPermissionResult(content string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(content)), "tool call skipped: waiting for approval")
}
