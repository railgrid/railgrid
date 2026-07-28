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
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

type projectEinoAssistantPhase string

const (
	projectEinoAssistantPhaseApproval projectEinoAssistantPhase = "approval"
	projectEinoAssistantPhaseMutate   projectEinoAssistantPhase = "mutate"
	projectEinoAssistantPhaseVerify   projectEinoAssistantPhase = "verify"
	projectEinoAssistantPhaseRepair   projectEinoAssistantPhase = "repair"
	projectEinoAssistantPhaseCommit   projectEinoAssistantPhase = "commit"
	projectEinoAssistantPhaseReport   projectEinoAssistantPhase = "report"
)

const (
	projectEinoAssistantWriteTodosTool = "write_todos"
	projectEinoAssistantToolSearchTool = "tool_search"
)

const (
	projectEinoAssistantTodoProgressMaxItems      = 50
	projectEinoAssistantTodoProgressMaxInputBytes = 64 * 1024
	projectEinoAssistantTodoProgressMaxLabelBytes = 120
)

type projectEinoAssistantTodoProgressInput struct {
	Todos []projectEinoAssistantTodoProgressItem `json:"todos"`
}

type projectEinoAssistantTodoProgressItem struct {
	Content    string `json:"content"`
	ActiveForm string `json:"activeForm"`
	Status     string `json:"status"`
}

type projectEinoAssistantPhaseFilterMiddleware struct {
	*adk.BaseChatModelAgentMiddleware

	req               projectAssistantRunRequest
	runState          *projectEinoAssistantRunState
	toolInfos         []*schema.ToolInfo
	deferredToolInfos []*schema.ToolInfo
	phase             projectEinoAssistantPhase
	approvedPlan      *projectAssistantApprovedPlan
}

func projectEinoAssistantPhaseMiddleware(
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) adk.ChatModelAgentMiddleware {
	phase := projectEinoAssistantPhaseApproval
	if req.Continuation != nil {
		if messages, err := projectChatMessagesToEino(req.Continuation.Messages); err == nil {
			state := &adk.ChatModelAgentState{Messages: messages}
			phase = projectEinoAssistantPhaseForState(req, runState, state)
			// Asking permission for a commit consumes the plan grant before
			// checkpointing. Restore only the execution phase for that exact
			// interrupted commit after re-validating write -> verification
			// ordering from the persisted message history.
			if phase == projectEinoAssistantPhaseApproval &&
				req.Continuation.Eino != nil &&
				projectEinoAssistantCommitTool(req.Continuation.Eino.ToolName) {
				phase = projectEinoAssistantPhaseForStateWithApproval(req, state, true)
			}
		}
	}
	return &projectEinoAssistantPhaseFilterMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		req:                          req,
		runState:                     runState,
		phase:                        phase,
		approvedPlan:                 cloneProjectAssistantApprovedPlan(projectEinoAssistantPhaseApprovedPlan(req, runState)),
	}
}

// BeforeAgent captures the full tool inventory after earlier middleware has
// augmented the agent. Eino persists rewritten ToolInfos across interrupts, so
// the next agent instance must not treat that prior phase-filtered slice as the
// canonical catalog.
func (m *projectEinoAssistantPhaseFilterMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}
	infos := make([]*schema.ToolInfo, 0, len(runCtx.Tools))
	for _, tool := range runCtx.Tools {
		if tool == nil {
			continue
		}
		info, err := tool.Info(ctx)
		if err != nil {
			return ctx, nil, fmt.Errorf("read phase tool info: %w", err)
		}
		infos = append(infos, info)
	}
	m.toolInfos = projectEinoAssistantPhaseMergeTools(m.toolInfos, infos)
	return ctx, runCtx, nil
}

func (m *projectEinoAssistantPhaseFilterMiddleware) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil {
		return ctx, state, nil
	}
	m.toolInfos = projectEinoAssistantPhaseMergeTools(m.toolInfos, state.ToolInfos)
	m.deferredToolInfos = projectEinoAssistantPhaseMergeTools(m.deferredToolInfos, state.DeferredToolInfos)
	// Read-only profiles already receive their exact tool set from turn-policy
	// filtering. They do not participate in the mutation lifecycle, but still
	// need the canonical static inventory after resuming a legacy checkpoint
	// whose persisted ToolInfos were phase-filtered.
	if !projectEinoAssistantPhaseLifecycleApplies(m.req) {
		templateBootstrapAllowed := projectEinoAssistantPhaseTemplateBootstrapAllowed(m.req.Project)
		state.ToolInfos = projectEinoAssistantPhaseFilterReadOnlyTools(
			templateBootstrapAllowed,
			projectEinoAssistantPhaseVisibleTools(m.toolInfos, state.ToolInfos),
		)
		state.DeferredToolInfos = projectEinoAssistantPhaseFilterReadOnlyTools(
			templateBootstrapAllowed,
			projectEinoAssistantPhaseMergeTools(m.deferredToolInfos, state.DeferredToolInfos),
		)
		return ctx, state, nil
	}
	phase := projectEinoAssistantPhaseForState(m.req, m.runState, state)
	approvedPlan := projectEinoAssistantPhaseApprovedPlan(m.req, m.runState)
	templateBootstrapAllowed := projectEinoAssistantPhaseTemplateBootstrapAllowed(m.req.Project)
	m.phase = phase
	m.approvedPlan = cloneProjectAssistantApprovedPlan(approvedPlan)
	if m.req.auditRecorder != nil {
		m.req.auditRecorder.recordPhase(phase)
	}
	state.ToolInfos = projectEinoAssistantPhaseFilterTools(
		phase,
		approvedPlan,
		templateBootstrapAllowed,
		projectEinoAssistantPhaseVisibleTools(m.toolInfos, state.ToolInfos),
	)
	state.DeferredToolInfos = projectEinoAssistantPhaseFilterTools(
		phase,
		approvedPlan,
		templateBootstrapAllowed,
		m.deferredToolInfos,
	)
	return ctx, state, nil
}

func (m *projectEinoAssistantPhaseFilterMiddleware) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	if toolCtx == nil {
		return endpoint, nil
	}
	rawName := toolCtx.Name
	name := projectToolBaseName(rawName)
	if !projectEinoAssistantPhaseLifecycleApplies(m.req) {
		templateBootstrapAllowed := projectEinoAssistantPhaseTemplateBootstrapAllowed(m.req.Project)
		tool := m.toolInfoForInvocation(rawName)
		risk, bundle, hasMetadata := projectEinoAssistantPhaseToolMetadata(tool)
		rawName = strings.TrimSpace(rawName)
		templateInspection := projectEinoAssistantPhaseTemplateInspectionTool(
			rawName,
			risk,
			bundle,
			templateBootstrapAllowed,
		)
		operationalRead := projectEinoAssistantPhaseOperationalReadTool(rawName, risk, bundle)
		templateToolDenied := name == projectToolSelectTemplate ||
			(name == projectToolInspectDevelopmentTemplates && !templateInspection)
		operationalReadDenied := projectEinoAssistantPhaseReservedOperationalReadName(rawName) &&
			!operationalRead
		if !templateToolDenied &&
			!operationalReadDenied &&
			!projectEinoAssistantPhaseReservedDirectActionName(rawName) &&
			(!hasMetadata || risk != projectAssistantToolRiskRuntime) {
			return endpoint, nil
		}
		return func(context.Context, string, ...einotool.Option) (string, error) {
			return fmt.Sprintf("Tool call denied: %s is unavailable in the current assistant phase", name), nil
		}, nil
	}
	return func(ctx context.Context, argumentsInJSON string, opts ...einotool.Option) (string, error) {
		tool := m.toolInfoForInvocation(rawName)
		approvedPlan := projectEinoAssistantPhaseApprovedPlan(m.req, m.runState)
		if approvedPlan == nil {
			approvedPlan = m.approvedPlan
		}
		if !projectEinoAssistantPhaseAllowsTool(
			m.phase,
			approvedPlan,
			projectEinoAssistantPhaseTemplateBootstrapAllowed(m.req.Project),
			tool,
		) {
			return fmt.Sprintf("Tool call denied: %s is unavailable in the current assistant phase", name), nil
		}
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return result, err
		}
		if name == projectEinoAssistantWriteTodosTool {
			plan, status := projectEinoAssistantTodoProgress(
				argumentsInJSON,
				!projectAssistantToolDisclosureMinimal,
			)
			if status != "" && m.req.StreamCallbacks.OnStatus != nil {
				m.req.StreamCallbacks.OnStatus(status)
			}
			if len(plan.Steps) > 0 && m.req.StreamCallbacks.OnPlan != nil {
				m.req.StreamCallbacks.OnPlan(plan)
			}
		}
		return result, nil
	}, nil
}

func projectEinoAssistantTodoProgress(argumentsInJSON string, includeLabels bool) (projectAssistantPlanSnapshot, string) {
	if len(argumentsInJSON) == 0 || len(argumentsInJSON) > projectEinoAssistantTodoProgressMaxInputBytes {
		return projectAssistantPlanSnapshot{}, ""
	}
	var input projectEinoAssistantTodoProgressInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil ||
		len(input.Todos) == 0 ||
		len(input.Todos) > projectEinoAssistantTodoProgressMaxItems {
		return projectAssistantPlanSnapshot{}, ""
	}

	completed := 0
	active := ""
	activeCount := 0
	planValid := true
	plan := projectAssistantPlanSnapshot{Steps: make([]projectAssistantPlanStep, 0, len(input.Todos))}
	for _, todo := range input.Todos {
		content := projectEinoAssistantTodoProgressLabel(todo.Content)
		activeForm := projectEinoAssistantTodoProgressLabel(todo.ActiveForm)
		if content == "" {
			planValid = false
		}
		switch todo.Status {
		case "pending":
		case "in_progress":
			activeCount++
			if activeCount > 1 {
				return projectAssistantPlanSnapshot{}, ""
			}
			active = activeForm
			if active == "" {
				active = content
			}
		case "completed":
			completed++
		default:
			return projectAssistantPlanSnapshot{}, ""
		}
		if includeLabels {
			plan.Steps = append(plan.Steps, projectAssistantPlanStep{
				Content:    content,
				ActiveForm: activeForm,
				Status:     todo.Status,
			})
		}
	}
	if !planValid {
		return projectAssistantPlanSnapshot{}, ""
	}

	total := len(input.Todos)
	noun := "steps"
	if total == 1 {
		noun = "step"
	}
	count := fmt.Sprintf("%d of %d %s", completed, total, noun)
	if includeLabels && active != "" {
		return plan, active + " · " + count
	}
	if includeLabels {
		return plan, count + " complete"
	}
	return projectAssistantPlanSnapshot{}, count + " complete"
}

func projectEinoAssistantTodoProgressLabel(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = projectEinoAssistantSafeText(value)
	if len(value) <= projectEinoAssistantTodoProgressMaxLabelBytes {
		return value
	}
	end := projectEinoAssistantTodoProgressMaxLabelBytes - 3
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return strings.TrimSpace(value[:end]) + "..."
}

func (m *projectEinoAssistantPhaseFilterMiddleware) toolInfoForInvocation(rawName string) *schema.ToolInfo {
	for _, tools := range [][]*schema.ToolInfo{m.toolInfos, m.deferredToolInfos} {
		for _, info := range tools {
			if info != nil && strings.TrimSpace(info.Name) == strings.TrimSpace(rawName) {
				return info
			}
		}
	}

	tool := &schema.ToolInfo{Name: rawName}
	switch projectToolBaseName(rawName) {
	case projectToolRequestProjectPlanApproval:
		tool.Extra = map[string]any{
			"bundle": string(projectAssistantToolBundleCollaboration),
			"risk":   string(projectAssistantToolRiskPlan),
		}
	case projectToolSelectTemplate:
		tool.Extra = map[string]any{
			"bundle": string(projectAssistantToolBundleWorkflow),
			"risk":   string(projectAssistantToolRiskWrite),
		}
	case projectToolCommitProjectFiles, projectToolCommitFiles:
		tool.Extra = map[string]any{
			"bundle": string(projectAssistantToolBundleRepo),
			"risk":   string(projectAssistantToolRiskCommit),
		}
	}
	return tool
}

func projectEinoAssistantPhaseLifecycleApplies(req projectAssistantRunRequest) bool {
	profile := req.TurnPolicy.profile
	if strings.TrimSpace(string(profile)) == "" {
		profile = req.TurnProfile
	}
	if strings.TrimSpace(string(profile)) == "" {
		// Phase-helper tests and callers outside the engine historically omit
		// turn policy. Keep lifecycle control enabled for that legacy shape;
		// the engine always supplies a normalized profile.
		return true
	}
	return projectAssistantTurnProfileAllowsMutation(profile)
}

func projectEinoAssistantPhaseVisibleTools(canonical, current []*schema.ToolInfo) []*schema.ToolInfo {
	visibleNames := make(map[string]struct{}, len(current))
	for _, tool := range current {
		if tool != nil {
			visibleNames[projectAssistantToolKey(tool.Name)] = struct{}{}
		}
	}
	visible := make([]*schema.ToolInfo, 0, len(canonical))
	for _, tool := range canonical {
		if tool == nil {
			continue
		}
		if !projectEinoAssistantPhaseSearchableTool(tool) {
			visible = append(visible, tool)
			continue
		}
		if _, selected := visibleNames[projectAssistantToolKey(tool.Name)]; selected {
			visible = append(visible, tool)
		}
	}
	return visible
}

func projectEinoAssistantPhaseFilterReadOnlyTools(
	templateBootstrapAllowed bool,
	tools []*schema.ToolInfo,
) []*schema.ToolInfo {
	filtered := make([]*schema.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		risk, bundle, hasMetadata := projectEinoAssistantPhaseToolMetadata(tool)
		if projectEinoAssistantPhaseReservedDirectActionName(tool.Name) ||
			(hasMetadata && risk == projectAssistantToolRiskRuntime) {
			continue
		}
		if projectEinoAssistantPhaseReservedOperationalReadName(tool.Name) &&
			!projectEinoAssistantPhaseOperationalReadTool(tool.Name, risk, bundle) {
			continue
		}
		rawName := strings.TrimSpace(tool.Name)
		switch projectToolBaseName(rawName) {
		case projectToolSelectTemplate:
			continue
		case projectToolInspectDevelopmentTemplates:
			if !projectEinoAssistantPhaseTemplateInspectionTool(
				rawName,
				risk,
				bundle,
				templateBootstrapAllowed,
			) {
				continue
			}
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

func projectEinoAssistantPhaseSearchableTool(tool *schema.ToolInfo) bool {
	if tool == nil || tool.Extra == nil {
		return false
	}
	searchable, _ := tool.Extra[projectEinoToolSearchableExtraKey].(bool)
	return searchable
}

func projectEinoAssistantPhaseForState(
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	state *adk.ChatModelAgentState,
) projectEinoAssistantPhase {
	approvedPlan := projectEinoAssistantPhaseApprovedPlan(req, runState)
	return projectEinoAssistantPhaseForHistory(
		projectEinoAssistantPhaseHistoryForState(state),
		approvedPlan != nil,
		req.InitialApprovedPlan != nil || (approvedPlan != nil && approvedPlan.RunLocal),
	)
}

func projectEinoAssistantPhaseForStateWithApproval(
	req projectAssistantRunRequest,
	state *adk.ChatModelAgentState,
	approved bool,
) projectEinoAssistantPhase {
	return projectEinoAssistantPhaseForHistory(
		projectEinoAssistantPhaseHistoryForState(state),
		approved,
		req.InitialApprovedPlan != nil,
	)
}

type projectEinoAssistantPhaseHistory struct {
	latestWrite             int
	latestDeniedWrite       int
	latestTemplateBootstrap int
	latestDirectAction      int
	latestVerification      int
	latestCommit            int
	verificationReady       bool
}

func projectEinoAssistantPhaseHistoryForState(state *adk.ChatModelAgentState) projectEinoAssistantPhaseHistory {
	history := projectEinoAssistantPhaseHistory{
		latestWrite:             -1,
		latestDeniedWrite:       -1,
		latestTemplateBootstrap: -1,
		latestDirectAction:      -1,
		latestVerification:      -1,
		latestCommit:            -1,
	}
	if state != nil {
		for index, message := range state.Messages {
			if message == nil {
				continue
			}
			rawName := strings.TrimSpace(message.ToolName)
			name := projectToolBaseName(message.ToolName)
			content := strings.ToLower(strings.TrimSpace(message.Content))
			if rawName == projectToolSelectTemplate {
				if projectEinoAssistantPhasePermissionDenied(content) {
					history.latestDeniedWrite = index
				} else if projectEinoAssistantPhaseSuccessfulToolResult(message) {
					history.latestTemplateBootstrap = index
				}
				continue
			}
			if projectEinoAssistantPhaseDirectActionName(rawName) {
				if projectEinoAssistantPhaseSuccessfulToolResult(message) ||
					projectEinoAssistantPhasePermissionDenied(content) {
					history.latestDirectAction = index
				}
				continue
			}
			if projectEinoAssistantPhaseCanonicalEditTool(name) &&
				projectEinoAssistantPhasePermissionDenied(content) {
				history.latestDeniedWrite = index
				continue
			}
			if name == projectToolVerifyDevelopmentRuntime && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(message.Content)), "tool call skipped: waiting for approval") {
				history.latestVerification = index
				history.verificationReady = projectEinoAssistantPhaseSuccessfulToolResult(message) &&
					projectEinoAssistantPhaseVerificationReady(message.Content)
				continue
			}
			if !projectEinoAssistantPhaseSuccessfulToolResult(message) {
				continue
			}
			switch name {
			case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
				history.latestWrite = index
			case projectToolCommitProjectFiles, projectToolCommitFiles:
				history.latestCommit = index
			}
		}
	}
	return history
}

func projectEinoAssistantPhasePermissionDenied(content string) bool {
	content = strings.ToLower(strings.TrimSpace(content))
	return strings.HasPrefix(content, "tool call failed: permission denied:")
}

func projectEinoAssistantPhaseForHistory(
	history projectEinoAssistantPhaseHistory,
	approved bool,
	initialCreation bool,
) projectEinoAssistantPhase {
	// A completed commit is terminal even though the tool execution clears the
	// run-local approval grant before the next model call.
	if history.latestCommit > history.latestWrite {
		return projectEinoAssistantPhaseReport
	}
	if history.latestWrite < 0 && history.latestDeniedWrite >= 0 {
		return projectEinoAssistantPhaseReport
	}
	if history.latestWrite < 0 && history.latestTemplateBootstrap >= 0 {
		if approved && initialCreation {
			return projectEinoAssistantPhaseMutate
		}
		return projectEinoAssistantPhaseReport
	}
	if history.latestWrite < 0 && history.latestDirectAction >= 0 {
		return projectEinoAssistantPhaseReport
	}
	if !approved {
		return projectEinoAssistantPhaseApproval
	}
	if history.latestWrite < 0 {
		return projectEinoAssistantPhaseMutate
	}
	if history.latestVerification < history.latestWrite {
		return projectEinoAssistantPhaseVerify
	}
	if !history.verificationReady {
		return projectEinoAssistantPhaseRepair
	}
	if initialCreation {
		return projectEinoAssistantPhaseReport
	}
	return projectEinoAssistantPhaseCommit
}

func projectEinoAssistantCommitTool(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolCommitProjectFiles, projectToolCommitFiles:
		return true
	default:
		return false
	}
}

func projectEinoAssistantPhaseApprovedPlan(
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) *projectAssistantApprovedPlan {
	if approvedPlan := runState.ApprovedPlan(); approvedPlan != nil {
		return approvedPlan
	}
	return req.InitialApprovedPlan
}

func projectEinoAssistantPhaseSuccessfulToolResult(message *schema.Message) bool {
	if message == nil || message.Role != schema.Tool || strings.TrimSpace(message.ToolName) == "" {
		return false
	}
	content := strings.ToLower(strings.TrimSpace(message.Content))
	for _, prefix := range []string{
		"tool call failed:",
		"tool call denied:",
		"tool call skipped: waiting for approval",
		"permission denied:",
	} {
		if strings.HasPrefix(content, prefix) {
			return false
		}
	}
	return true
}

func projectEinoAssistantPhaseVerificationReady(content string) bool {
	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &result); err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(result.Status)) {
	case "reachable", "ready", "available":
		return true
	default:
		return false
	}
}

func projectEinoAssistantPhaseFilterTools(
	phase projectEinoAssistantPhase,
	approvedPlan *projectAssistantApprovedPlan,
	templateBootstrapAllowed bool,
	tools []*schema.ToolInfo,
) []*schema.ToolInfo {
	if tools == nil {
		return nil
	}
	filtered := make([]*schema.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if projectEinoAssistantPhaseAllowsTool(phase, approvedPlan, templateBootstrapAllowed, tool) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func projectEinoAssistantPhaseMergeTools(existing, current []*schema.ToolInfo) []*schema.ToolInfo {
	if len(current) == 0 {
		return existing
	}
	merged := append([]*schema.ToolInfo(nil), existing...)
	for _, tool := range current {
		if tool == nil {
			continue
		}
		found := false
		for _, known := range merged {
			if known != nil && strings.EqualFold(strings.TrimSpace(known.Name), strings.TrimSpace(tool.Name)) {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, tool)
		}
	}
	return merged
}

func projectEinoAssistantPhaseAllowsTool(
	phase projectEinoAssistantPhase,
	approvedPlan *projectAssistantApprovedPlan,
	templateBootstrapAllowed bool,
	tool *schema.ToolInfo,
) bool {
	if tool == nil {
		return false
	}
	name := projectToolBaseName(tool.Name)
	if strings.TrimSpace(tool.Name) == projectToolInspectDevelopmentTemplates &&
		!templateBootstrapAllowed {
		return false
	}
	if name == projectEinoAssistantToolSearchTool {
		return phase == projectEinoAssistantPhaseApproval
	}
	if name == projectEinoAssistantWriteTodosTool {
		return (phase == projectEinoAssistantPhaseMutate ||
			phase == projectEinoAssistantPhaseVerify ||
			phase == projectEinoAssistantPhaseRepair) &&
			approvedPlan != nil && len(approvedPlan.Steps) > 1
	}
	risk, bundle, ok := projectEinoAssistantPhaseToolMetadata(tool)
	if !ok {
		return false
	}
	templateBootstrap := projectEinoAssistantPhaseTemplateBootstrapTool(
		tool.Name,
		risk,
		bundle,
		templateBootstrapAllowed,
	)
	templateInspection := projectEinoAssistantPhaseTemplateInspectionTool(
		tool.Name,
		risk,
		bundle,
		templateBootstrapAllowed,
	)
	directAction := projectEinoAssistantPhaseDirectActionTool(tool.Name, risk, bundle)
	operationalRead := projectEinoAssistantPhaseOperationalReadTool(tool.Name, risk, bundle)
	if name == projectToolSelectTemplate && !templateBootstrap {
		return false
	}
	if name == projectToolInspectDevelopmentTemplates && !templateInspection {
		return false
	}
	if projectEinoAssistantPhaseReservedDirectActionName(tool.Name) && !directAction {
		return false
	}
	if projectEinoAssistantPhaseReservedOperationalReadName(tool.Name) && !operationalRead {
		return false
	}

	switch phase {
	case projectEinoAssistantPhaseApproval:
		if templateBootstrap || directAction || operationalRead {
			return true
		}
		if bundle == projectAssistantToolBundleRuntime {
			return false
		}
		return risk == projectAssistantToolRiskRead ||
			risk == projectAssistantToolRiskInput ||
			risk == projectAssistantToolRiskPlan
	case projectEinoAssistantPhaseMutate:
		return (bundle == projectAssistantToolBundleWorkspaceRead && risk == projectAssistantToolRiskRead) ||
			(projectEinoAssistantPhaseCanonicalEditTool(tool.Name) &&
				bundle == projectAssistantToolBundleEdit && risk == projectAssistantToolRiskWrite) ||
			templateBootstrap ||
			templateInspection ||
			directAction ||
			operationalRead ||
			(name == projectToolAskFollowUp && risk == projectAssistantToolRiskInput)
	case projectEinoAssistantPhaseVerify:
		return (name != projectToolVerifyDevelopmentRuntime &&
			bundle == projectAssistantToolBundleWorkspaceRead && risk == projectAssistantToolRiskRead) ||
			(projectEinoAssistantPhaseCanonicalEditTool(tool.Name) &&
				bundle == projectAssistantToolBundleEdit && risk == projectAssistantToolRiskWrite) ||
			(tool.Name == projectToolVerifyDevelopmentRuntime &&
				bundle == projectAssistantToolBundleRuntime && risk == projectAssistantToolRiskRead) ||
			(name == projectToolAskFollowUp && risk == projectAssistantToolRiskInput)
	case projectEinoAssistantPhaseRepair:
		return (bundle == projectAssistantToolBundleWorkspaceRead && risk == projectAssistantToolRiskRead) ||
			(projectEinoAssistantPhaseCanonicalEditTool(tool.Name) &&
				bundle == projectAssistantToolBundleEdit && risk == projectAssistantToolRiskWrite) ||
			templateBootstrap ||
			templateInspection ||
			directAction ||
			operationalRead ||
			(name == projectToolAskFollowUp && risk == projectAssistantToolRiskInput)
	case projectEinoAssistantPhaseCommit:
		return tool.Name == projectToolCommitProjectFiles &&
			bundle == projectAssistantToolBundleRepo &&
			risk == projectAssistantToolRiskCommit
	case projectEinoAssistantPhaseReport:
		return false
	default:
		return false
	}
}

func projectEinoAssistantPhaseReservedDirectActionName(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolRestartRuntime,
		projectToolSetRuntimeEnv,
		projectToolPromoteProject,
		projectToolRebuildProject,
		"provision":
		return true
	default:
		return false
	}
}

func projectEinoAssistantPhaseDirectActionName(name string) bool {
	switch strings.TrimSpace(name) {
	case projectToolRestartRuntime,
		projectToolSetRuntimeEnv,
		projectToolPromoteProject,
		projectToolRebuildProject,
		projectToolInfrastructureProvision:
		return true
	default:
		return false
	}
}

func projectEinoAssistantPhaseDirectActionTool(
	name string,
	risk projectAssistantToolRisk,
	bundle projectAssistantToolBundle,
) bool {
	if risk != projectAssistantToolRiskRuntime || !projectEinoAssistantPhaseDirectActionName(name) {
		return false
	}
	switch strings.TrimSpace(name) {
	case projectToolInfrastructureProvision:
		return bundle == projectAssistantToolBundleInfrastructure
	default:
		return bundle == projectAssistantToolBundleRuntime
	}
}

func projectEinoAssistantPhaseOperationalReadTool(
	name string,
	risk projectAssistantToolRisk,
	bundle projectAssistantToolBundle,
) bool {
	if risk != projectAssistantToolRiskRead {
		return false
	}
	switch strings.TrimSpace(name) {
	case projectToolGetRuntimeStatus,
		projectToolGetPreviewURL,
		projectToolGetRuntimeLogs,
		projectToolVerifyDevelopmentRuntime:
		return bundle == projectAssistantToolBundleRuntime
	case projectToolCheckProjectBuild, projectToolGetBuildLogs:
		return bundle == projectAssistantToolBundleWorkflow
	case projectToolInfrastructureListTemplates,
		projectToolInfrastructureDescribeTemplate,
		projectToolInfrastructureListInstances,
		projectToolInfrastructureGetInstance:
		return bundle == projectAssistantToolBundleInfrastructure
	default:
		return false
	}
}

func projectEinoAssistantPhaseReservedOperationalReadName(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolGetRuntimeStatus,
		projectToolGetPreviewURL,
		projectToolGetRuntimeLogs,
		projectToolVerifyDevelopmentRuntime,
		projectToolCheckProjectBuild,
		projectToolGetBuildLogs,
		"list_templates",
		"describe_template",
		"list_instances",
		"get_instance":
		return true
	default:
		return false
	}
}

func projectEinoAssistantPhaseTemplateInspectionTool(
	name string,
	risk projectAssistantToolRisk,
	bundle projectAssistantToolBundle,
	allowed bool,
) bool {
	return allowed &&
		strings.TrimSpace(name) == projectToolInspectDevelopmentTemplates &&
		risk == projectAssistantToolRiskRead &&
		bundle == projectAssistantToolBundleWorkflow
}

// Template selection is the one workflow/write exception because binding the
// initial development template is the first mutation for a template-less app.
func projectEinoAssistantPhaseTemplateBootstrapTool(
	name string,
	risk projectAssistantToolRisk,
	bundle projectAssistantToolBundle,
	allowed bool,
) bool {
	return allowed &&
		name == projectToolSelectTemplate &&
		risk == projectAssistantToolRiskWrite &&
		bundle == projectAssistantToolBundleWorkflow
}

func projectEinoAssistantPhaseTemplateBootstrapAllowed(project *aiv1alpha1.Project) bool {
	return project != nil &&
		(project.Spec.Template == nil || strings.TrimSpace(project.Spec.Template.Name) == "")
}

func projectEinoAssistantPhaseCanonicalEditTool(name string) bool {
	switch name {
	case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
		return true
	default:
		return false
	}
}

func projectEinoAssistantPhaseToolMetadata(tool *schema.ToolInfo) (projectAssistantToolRisk, projectAssistantToolBundle, bool) {
	if tool == nil {
		return "", "", false
	}
	if tool.Extra != nil {
		risk, riskOK := tool.Extra["risk"].(string)
		bundle, bundleOK := tool.Extra["bundle"].(string)
		if riskOK && bundleOK {
			return projectAssistantToolRisk(strings.TrimSpace(risk)), projectAssistantToolBundle(strings.TrimSpace(bundle)), true
		}
	}
	if projectEinoAssistantFilesystemReadTool(tool.Name) {
		return projectAssistantToolRiskRead, projectAssistantToolBundleWorkspaceRead, true
	}
	return "", "", false
}
