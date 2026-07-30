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
	"fmt"
	"io"
	"strings"
	"time"

	approvaltool "github.com/cloudwego/eino-examples/adk/common/tool"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/dynamictool/toolsearch"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectEinoAssistantSummaryContextMessages  = 128
	projectEinoAssistantSummaryContextTokens    = 24000
	projectEinoAssistantClosingEvidenceMaxItems = 64
	projectEinoAssistantSummaryInstruction      = "Summarize this App Studio project session for the next builder turn. Preserve user requirements, accepted plans, files touched or inspected, unresolved questions, repository/runtime state, and any constraints. Keep it concise and operational."
	projectEinoAssistantDeepInstruction         = "Use only the currently exposed App Studio tools; do not assume shell, browser, host filesystem, or subagent access. For a new project's initial build, first call define_initial_project_plan; this internal plan is auto-authorized and must include concrete acceptance criteria. Source mutations require an approved target-path grant and successful development verification before commit. For multi-step source work, use write_todos to keep the visible execution-plan progress current; never create todo.md or todos.md for execution tracking. Keep exactly one step in progress, mark every step complete only when its outcome is satisfied, never include secrets or raw tool data, and remember that todos track progress but grant no authority. Do not add repository commit or handoff as a todo step; App Studio enters a dedicated commit phase after runtime verification. Runtime, infrastructure, and repository effects use their exact tools and approval boundaries. Never tell the user to approve or authorize a phase unless you have actually called a permission-bearing tool and App Studio created a pending permission request. Repair defects found by verification inside the same objective; do not invent a next phase for unfinished work. Keep changes minimal and focused on the user's request. After initial project creation, write_file may create new paths but must not replace existing files; read each existing target in the current turn and use apply_patch for exact, localized changes. Treat successful whole-file writes as authoritative; do not reread them unless a later result shows a conflict or failure. Batch independent reads, inspect existing content before editing, and report blockers honestly instead of calling unrelated tools. Finish with a concise evidence-based result."
	projectEinoAssistantNoOutputFallback        = "I couldn't produce a response for that turn. Please try again or rephrase the request, and I can continue from the current project context."
)

var errProjectAssistantNoOutput = errors.New("assistant model produced no accepted output")

var errProjectAssistantNoProgress = errors.New("assistant stopped after making no implementation progress")

const projectAssistantGracefulStopTimeout = 5 * time.Second

type projectEinoAssistantEngine struct {
	server   *Server
	newModel projectEinoAssistantModelFactory
	newTools projectEinoAssistantToolsFactory
}

type projectEinoAssistantModelFactory func(
	context.Context,
	projectAssistantRunRequest,
	*projectEinoAssistantRunState,
) (einomodel.BaseChatModel, error)

type projectEinoAssistantToolsFactory func(
	context.Context,
	projectAssistantRunRequest,
	*projectEinoAssistantRunState,
) ([]einotool.BaseTool, error)

// NewEinoAssistantEngine returns the Eino-backed assistant engine. The App
// Studio assistant uses Eino's ChatModelAgent as the only chat/tool execution
// loop; App Studio adapters stay at model, tool, storage, and event boundaries.
func NewEinoAssistantEngine(server *Server) projectAssistantEngine {
	return projectEinoAssistantEngine{
		server:   server,
		newModel: newProjectEinoAssistantModelFactory(server),
		newTools: newProjectEinoAssistantToolsFactory(server),
	}
}

func (e projectEinoAssistantEngine) StreamProjectAssistant(
	ctx context.Context,
	req projectAssistantRunRequest,
) (projectAssistantRunResult, error) {
	if req.Project == nil {
		return projectAssistantRunResult{}, errors.New("project is required")
	}
	if e.newModel == nil {
		return projectAssistantRunResult{}, errors.New("eino model factory is not configured")
	}
	if e.newTools == nil {
		return projectAssistantRunResult{}, errors.New("eino tool factory is not configured")
	}

	req.TurnPolicy = normalizeProjectAssistantTurnPolicy(req.TurnPolicy, req.TurnProfile)
	req.TurnProfile = req.TurnPolicy.profile
	runState := newProjectEinoAssistantRunState()
	runState.SetTurnPolicy(req.TurnPolicy)
	runState.SetProjectRepositoryRef(projectEinoAssistantProjectRepositoryRef(req))
	if projectAssistantTurnProfileAllowsMutation(req.TurnProfile) {
		authority := e.executionAuthority(req)
		loaded, err := authority.Load(ctx)
		if err != nil {
			return projectAssistantRunResult{}, fmt.Errorf("load assistant execution authority: %w", err)
		}
		runState.SetApprovedPlanGrantRevision(loaded.GrantRevision)
		if loaded.ApprovedPlan != nil {
			runState.ApprovePlan(*loaded.ApprovedPlan)
		}
		if loaded.ExecutionPlan != nil {
			runState.SetExecutionPlan(*loaded.ExecutionPlan, loaded.ExecutionPlanRevision)
			runState.ApprovePlan(*loaded.ExecutionPlan)
			if progress, ok := projectEinoAssistantLatestPlanProgress(req.History); ok {
				runState.SetPlanProgress(progress)
			}
			if projectEinoAssistantHistoryHasSourceMutation(req.History) {
				runState.RecordSourceMutation()
			}
		}
	}
	// A fresh Project creation prompt is explicit authorization to build the
	// initial source tree. Keep this grant run-local: it may survive an
	// interrupt through the run checkpoint, but is never persisted as the
	// cross-turn plan grant used by subsequent conversations.
	if req.InitialApprovedPlan != nil {
		runState.ApprovePlan(*req.InitialApprovedPlan)
	}

	checkpointID := newProjectAssistantRunID()
	auditRecorder, err := e.startProjectAssistantRunAudit(ctx, &req, checkpointID)
	if err != nil {
		return projectAssistantRunResult{}, err
	}
	checkpointStore := newProjectEinoAssistantCheckpointStore()
	turn := newProjectAssistantTurnItem(projectAssistantTurnMessage, req.Identity, req.Project.Name)
	turn.ProjectUID = req.MessageScope.ProjectUID
	result, runErr := e.runProjectAssistantTurnLoop(ctx, req, runState, checkpointStore, checkpointID, []projectAssistantTurnItem{turn})
	return result, e.finishProjectAssistantRunAudit(ctx, req, auditRecorder, runErr)
}

func projectEinoAssistantLatestPlanProgress(history []store.Message) (projectAssistantPlanSnapshot, bool) {
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if message.Metadata == nil {
			continue
		}
		plan, ok := projectAssistantPlanSnapshotFromMetadata(message.Metadata[projectAssistantMetadataPlan])
		if ok && plan != nil && len(plan.Steps) > 0 {
			return cloneProjectAssistantPlanSnapshot(*plan), true
		}
	}
	return projectAssistantPlanSnapshot{}, false
}

func projectEinoAssistantHistoryHasSourceMutation(history []store.Message) bool {
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if message.Metadata == nil {
			continue
		}
		actions := projectAssistantActionFeedFromMetadata(message.Metadata[projectMessageMetadataAssistantActionFeed])
		for _, action := range actions {
			if action.Kind == projectAssistantActionFeedItemEdit &&
				action.Status == projectAssistantActionFeedStatusSucceeded {
				return true
			}
		}
	}
	return false
}

func (e projectEinoAssistantEngine) ResumeProjectAssistant(
	ctx context.Context,
	req projectAssistantRunRequest,
	resumeReq projectAssistantResumeRequest,
	state projectAssistantCheckpointState,
) (projectAssistantRunResult, error) {
	if req.Project == nil {
		return projectAssistantRunResult{}, errors.New("project is required")
	}
	if state.Eino == nil || len(state.Eino.Checkpoint) == 0 || strings.TrimSpace(state.Eino.CheckpointID) == "" || strings.TrimSpace(state.Eino.InterruptID) == "" {
		return projectAssistantRunResult{}, errors.New("eino checkpoint is required")
	}
	if e.newModel == nil {
		return projectAssistantRunResult{}, errors.New("eino model factory is not configured")
	}
	if e.newTools == nil {
		return projectAssistantRunResult{}, errors.New("eino tool factory is not configured")
	}

	hasWorkItem := req.AssistantRun != nil && strings.TrimSpace(req.AssistantRun.WorkItemID) != ""
	checkpointCarriesAuthority := state.ApprovedPlan != nil || strings.TrimSpace(state.ApprovedPlanGrantRevision) != "" ||
		state.ExecutionPlan != nil || strings.TrimSpace(state.ExecutionPlanRevision) != ""
	checkpointAllowsMutation := projectAssistantTurnProfileAllowsMutation(state.TurnPolicy.Profile)
	if !hasWorkItem && (checkpointAllowsMutation || checkpointCarriesAuthority) {
		return projectAssistantRunResult{}, store.ErrAssistantWorkItemConflict
	}
	var approvedPlan *projectAssistantApprovedPlan
	var revision string
	if hasWorkItem {
		if e.server == nil && req.executionAuthority == nil {
			return projectAssistantRunResult{}, errors.New("server is required to validate resumed assistant plan grant")
		}
		loaded, err := e.executionAuthority(req).Load(ctx)
		if err != nil {
			return projectAssistantRunResult{}, fmt.Errorf("validate resumed assistant plan grant: %w", err)
		}
		approvedPlan, revision = loaded.ApprovedPlan, loaded.GrantRevision
		if strings.TrimSpace(state.ApprovedPlanGrantRevision) != revision {
			return projectAssistantRunResult{}, fmt.Errorf("%w: checkpoint revision %q does not match WorkItem revision %q", errProjectAssistantCheckpointGrantStale, state.ApprovedPlanGrantRevision, revision)
		}
		if state.ApprovedPlan != nil && state.ApprovedPlan.RunLocal {
			approvedPlan = cloneProjectAssistantApprovedPlan(state.ApprovedPlan)
		}
	}
	runState := newProjectEinoAssistantRunState()
	runState.RestoreCheckpointState(state)
	runState.ClearApprovedPlan()
	runState.SetApprovedPlanGrantRevision(revision)
	if approvedPlan != nil {
		runState.ApprovePlan(*approvedPlan)
	}
	runState.SetProjectRepositoryRef(projectEinoAssistantProjectRepositoryRef(projectAssistantRunRequest{
		Project:      req.Project,
		Continuation: &state,
	}))
	resumeRunReq := req
	resumeRunReq.Continuation = &state
	if req.AssistantRun != nil &&
		strings.TrimSpace(req.AssistantRun.WorkItemID) != "" &&
		runState.TurnPolicy().profile == projectAssistantTurnProfileAdaptive {
		// WorkItem membership is the durable server-owned signal that this
		// continuation is an implementation attempt. In particular, an Auto
		// run is promoted before its plan checkpoint, while that checkpoint
		// still contains the pre-promotion adaptive policy.
		resumeRunReq.TurnPolicy = projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
	}
	// The checkpoint restores the policy the run STARTED with; req.TurnPolicy
	// carries the (optional) re-routed decision for this resume. Escalate-only
	// merge: a "go for it" follow-up gains implementation tools, but an
	// in-flight fix never loses tools to a chatty-looking reply.
	resumeRunReq.TurnPolicy = escalateProjectAssistantTurnPolicy(runState.TurnPolicy(), resumeRunReq.TurnPolicy)
	resumeRunReq.TurnProfile = resumeRunReq.TurnPolicy.profile
	runState.SetTurnPolicy(resumeRunReq.TurnPolicy)
	checkpointStore := newProjectEinoAssistantCheckpointStoreWithCheckpoint(state.Eino.CheckpointID, state.Eino.Checkpoint)
	turn := newProjectAssistantTurnItem(projectAssistantTurnResume, req.Identity, req.Project.Name)
	turn.ProjectUID = req.MessageScope.ProjectUID
	turn.RequestID = strings.TrimSpace(resumeReq.RequestID)
	turn.Decision = strings.TrimSpace(resumeReq.Decision)
	turn.Answer = strings.TrimSpace(resumeReq.Answer)
	turn.EditedArguments = cloneProjectAssistantToolArguments(resumeReq.EditedArguments)
	auditRecorder, err := e.resumeProjectAssistantRunAudit(&resumeRunReq)
	if err != nil {
		return projectAssistantRunResult{}, err
	}
	result, runErr := e.runProjectAssistantTurnLoop(ctx, resumeRunReq, runState, checkpointStore, state.Eino.CheckpointID, []projectAssistantTurnItem{turn})
	return result, e.finishProjectAssistantRunAudit(ctx, resumeRunReq, auditRecorder, runErr)
}

func (e projectEinoAssistantEngine) startProjectAssistantRunAudit(
	ctx context.Context,
	req *projectAssistantRunRequest,
	_ string,
) (*projectAssistantRunAuditRecorder, error) {
	if req == nil || !projectAssistantAuditScopeComplete(req.MessageScope) {
		return nil, nil
	}
	if req.AssistantRun != nil && strings.TrimSpace(req.AssistantRun.ID) != "" {
		recorder := newProjectAssistantRunAuditRecorder(*req, req.AssistantRun, req.AssistantRun.CreatedAt)
		req.auditRecorder = recorder
		recorder.setPersister(e.executionAuthority(*req).PersistAudit)
		recorder.wrapCallbacks(&req.StreamCallbacks)
		return recorder, nil
	}
	// Durable runs are created by the App Studio supervisor before Eino is
	// entered. Do not manufacture an unmanaged AssistantRun for audit data.
	return nil, nil
}

func (e projectEinoAssistantEngine) resumeProjectAssistantRunAudit(
	req *projectAssistantRunRequest,
) (*projectAssistantRunAuditRecorder, error) {
	if req == nil || req.AssistantRun == nil || !projectAssistantAuditScopeComplete(req.MessageScope) {
		return nil, nil
	}
	started := req.AssistantRun.CreatedAt
	recorder := newProjectAssistantRunAuditRecorder(*req, req.AssistantRun, started)
	req.auditRecorder = recorder
	recorder.setPersister(e.executionAuthority(*req).PersistAudit)
	recorder.wrapCallbacks(&req.StreamCallbacks)
	return recorder, nil
}

func (e projectEinoAssistantEngine) finishProjectAssistantRunAudit(
	ctx context.Context,
	req projectAssistantRunRequest,
	recorder *projectAssistantRunAuditRecorder,
	runErr error,
) error {
	if recorder == nil || req.AssistantRun == nil {
		return runErr
	}
	var permissionErr *projectAssistantPermissionRequiredError
	var inputErr *projectAssistantInputRequiredError
	if errors.As(runErr, &permissionErr) || errors.As(runErr, &inputErr) {
		return runErr
	}
	outcome := projectAssistantAuditOutcomeSucceeded
	if runErr != nil {
		outcome = projectAssistantAuditOutcomeFailed
		recorder.recordModelError()
		recorder.recordFailure(runErr)
		if errors.Is(runErr, errProjectAssistantTurnPreempted) || errors.Is(context.Cause(ctx), errProjectAssistantTurnPreempted) {
			outcome = projectAssistantAuditOutcomePreempted
		}
	}
	recorder.finalize(outcome)
	persistCtx, cancel := detachedProjectPersistenceContext(ctx)
	defer cancel()
	if err := e.executionAuthority(req).PersistAudit(persistCtx, req.AssistantRun.Audit); err != nil {
		persistErr := fmt.Errorf("persist assistant run audit: %w", err)
		if runErr != nil {
			return errors.Join(runErr, persistErr)
		}
		return persistErr
	}
	return runErr
}

func projectAssistantAuditScopeComplete(scope store.Scope) bool {
	return strings.TrimSpace(scope.OrgUUID) != "" &&
		strings.TrimSpace(scope.WorkspaceUUID) != "" &&
		strings.TrimSpace(scope.ProjectName) != ""
}

func (e projectEinoAssistantEngine) newAgent(ctx context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) (adk.Agent, error) {
	tools, err := e.newTools(ctx, req, runState)
	if err != nil {
		return nil, err
	}
	chatModel, err := e.newModel(ctx, req, runState)
	if err != nil {
		return nil, err
	}
	staticTools, dynamicTools, err := projectEinoAssistantToolSearchSets(ctx, tools)
	if err != nil {
		return nil, err
	}
	var handlers []adk.ChatModelAgentMiddleware
	patchToolCallsMiddleware, err := projectEinoAssistantPatchToolCallsMiddleware(ctx)
	if err != nil {
		return nil, fmt.Errorf("create eino patch tool calls middleware: %w", err)
	}
	handlers = append(handlers, patchToolCallsMiddleware)
	reductionMiddleware, err := projectEinoAssistantReductionMiddleware(ctx)
	if err != nil {
		return nil, fmt.Errorf("create eino reduction middleware: %w", err)
	}
	handlers = append(handlers, reductionMiddleware)
	summaryMiddleware, err := summarization.New(ctx, &summarization.Config{
		Model: chatModel,
		Trigger: &summarization.TriggerCondition{
			ContextMessages: projectEinoAssistantSummaryContextMessages,
			ContextTokens:   projectEinoAssistantSummaryContextTokens,
		},
		UserInstruction: projectEinoAssistantSummaryInstruction,
		Finalize:        projectEinoAssistantFinalizeSummary,
	})
	if err != nil {
		return nil, fmt.Errorf("create eino summarization middleware: %w", err)
	}
	handlers = append(handlers, summaryMiddleware)
	if len(dynamicTools) > 0 {
		searchMiddleware, err := toolsearch.New(ctx, &toolsearch.Config{
			DynamicTools: dynamicTools,
		})
		if err != nil {
			return nil, fmt.Errorf("create eino tool search middleware: %w", err)
		}
		handlers = append(handlers, searchMiddleware)
	}
	var workspaceStore *workspace.FileStore
	if e.server != nil {
		workspaceStore = e.server.workspaces
	}
	filesystemMiddleware, err := projectEinoAssistantFilesystemMiddleware(ctx, workspaceStore, req)
	if err != nil {
		return nil, fmt.Errorf("create App Studio Eino filesystem middleware: %w", err)
	}
	if filesystemMiddleware != nil {
		handlers = append(handlers, filesystemMiddleware)
	}
	// Eino makes the first registered tool wrapper outermost. Safe-error must
	// therefore precede telemetry so telemetry observes backend errors before
	// they are shaped for the model. Phase must also precede telemetry so a
	// hidden or terminal-phase call is denied without emitting read progress.
	handlers = append(handlers, &projectEinoAssistantSafeToolErrorMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
	})
	// Phase filtering keeps the model's choices aligned with the durable
	// approval -> mutate -> verify -> commit lifecycle. Invocation-time
	// lifecycle checks remain a second, independent authorization boundary.
	handlers = append(handlers, projectEinoAssistantPhaseMiddleware(req, runState))
	handlers = append(handlers, projectEinoAssistantLifecycleMiddleware(req, runState))
	if filesystemMiddleware != nil {
		handlers = append(handlers, projectEinoAssistantFilesystemTelemetryMiddleware(req, runState))
	}
	toolsConfig := adk.ToolsConfig{
		ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               staticTools,
			UnknownToolsHandler: projectEinoUnknownToolHandler(req, runState),
			// Build/Continue turns may batch writes and permission-bearing
			// effects, so preserve ordering there. Read-only profiles expose no
			// mutating tools and can safely execute independent reads in
			// parallel.
			ExecuteSequentially: projectAssistantTurnProfileAllowsMutation(req.TurnPolicy.profile) ||
				projectAssistantInlinePromotionEnabled(req),
		},
	}
	agent, err := deep.New(ctx, &deep.Config{
		Name:        "app-studio-project-assistant",
		Description: "Runs App Studio project assistant turns.",
		ChatModel:   chatModel,
		// Keep Eino's native coding-agent instruction and add the narrow App
		// Studio contract as an ordinary system message. Replacing the native
		// instruction caused this provider to grow a second, history-derived
		// agent protocol that fought the underlying loop.
		Instruction:            "",
		ToolsConfig:            toolsConfig,
		MaxIteration:           projectAssistantDeepIterations(),
		WithoutWriteTodos:      !projectEinoAssistantTurnUsesDeepTodos(req),
		WithoutGeneralSubAgent: true,
		Handlers:               handlers,
		ModelRetryConfig:       projectEinoAssistantModelRetryConfig(req, runState),
	})
	if err != nil {
		return nil, fmt.Errorf("create eino assistant agent: %w", err)
	}
	return agent, nil
}

func projectEinoAssistantTurnUsesDeepTodos(req projectAssistantRunRequest) bool {
	return projectAssistantTurnProfileAllowsMutation(req.TurnPolicy.profile) ||
		projectAssistantInlinePromotionEnabled(req)
}

func projectEinoAssistantFinalizeSummary(ctx context.Context, originalMessages []*schema.Message, summary *schema.Message) ([]*schema.Message, error) {
	if strings.TrimSpace(projectEinoAssistantSummaryText(summary)) == "" {
		summary = schema.AssistantMessage(projectEinoAssistantFallbackSummary(originalMessages), nil)
	}
	return summarization.DefaultFinalize(ctx, originalMessages, summary)
}

func projectEinoAssistantSummaryText(msg *schema.Message) string {
	if msg == nil || msg.Role != schema.Assistant {
		return ""
	}
	var parts []string
	for _, part := range msg.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return strings.TrimSpace(msg.Content)
}

func projectEinoAssistantFallbackSummary(messages []*schema.Message) string {
	const maxMessages = 12
	var b strings.Builder
	b.WriteString("Summary unavailable; preserving recent App Studio context.")
	start := len(messages) - maxMessages
	if start < 0 {
		start = 0
	}
	for _, msg := range messages[start:] {
		content := truncateProjectToolInfo(projectEinoAssistantMessageText(msg))
		if content == "" {
			continue
		}
		b.WriteString("\n- ")
		b.WriteString(projectEinoAssistantMessageRole(msg))
		b.WriteString(": ")
		b.WriteString(content)
	}
	return b.String()
}

func projectEinoAssistantMessageText(msg *schema.Message) string {
	if msg == nil {
		return ""
	}
	switch msg.Role {
	case schema.Assistant:
		return projectEinoAssistantSummaryText(msg)
	default:
		return strings.TrimSpace(msg.Content)
	}
}

func projectEinoAssistantMessageRole(msg *schema.Message) string {
	if msg == nil {
		return "message"
	}
	return strings.ToLower(string(msg.Role))
}

func projectEinoAssistantToolSearchSets(ctx context.Context, tools []einotool.BaseTool) ([]einotool.BaseTool, []einotool.BaseTool, error) {
	staticTools := make([]einotool.BaseTool, 0, len(tools))
	dynamicTools := make([]einotool.BaseTool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		info, err := tool.Info(ctx)
		if err != nil {
			return nil, nil, err
		}
		if projectEinoAssistantToolUsesSearch(info) {
			dynamicTools = append(dynamicTools, tool)
			continue
		}
		staticTools = append(staticTools, tool)
	}
	return staticTools, dynamicTools, nil
}

func projectEinoAssistantToolUsesSearch(info *schema.ToolInfo) bool {
	if info == nil || info.Extra == nil {
		return false
	}
	searchable, _ := info.Extra[projectEinoToolSearchableExtraKey].(bool)
	return searchable
}

type projectEinoAssistantTurnOutcome struct {
	result         projectAssistantRunResult
	receivedOutput bool
	interrupt      *adk.InterruptInfo
}

func (e projectEinoAssistantEngine) runProjectAssistantTurnLoop(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	checkpointStore *projectEinoAssistantCheckpointStore,
	checkpointID string,
	items []projectAssistantTurnItem,
) (projectAssistantRunResult, error) {
	outcome := &projectEinoAssistantTurnOutcome{}
	if e.server != nil {
		projectEinoAssistantEnsureToolDiscovery(ctx, e.server, req, runState)
	}
	loop := adk.NewTurnLoop[projectAssistantTurnItem, *schema.Message](adk.TurnLoopConfig[projectAssistantTurnItem, *schema.Message]{
		GenInput: func(loopCtx context.Context, _ *adk.TurnLoop[projectAssistantTurnItem, *schema.Message], items []projectAssistantTurnItem) (*adk.GenInputResult[projectAssistantTurnItem, *schema.Message], error) {
			if len(items) == 0 {
				return nil, errors.New("eino turn loop received no work")
			}
			input, err := projectEinoAssistantInputMessages(loopCtx, req, runState)
			if err != nil {
				return nil, err
			}
			return &adk.GenInputResult[projectAssistantTurnItem, *schema.Message]{
				RunCtx: loopCtx,
				Input: &adk.TypedAgentInput[*schema.Message]{
					Messages:        input,
					EnableStreaming: true,
				},
				Consumed:  append([]projectAssistantTurnItem(nil), items[:1]...),
				Remaining: append([]projectAssistantTurnItem(nil), items[1:]...),
				RunOpts:   projectEinoAssistantRunOptions(req, runState),
			}, nil
		},
		GenResume: func(loopCtx context.Context, _ *adk.TurnLoop[projectAssistantTurnItem, *schema.Message], interruptedItems, unhandledItems, newItems []projectAssistantTurnItem) (*adk.GenResumeResult[projectAssistantTurnItem, *schema.Message], error) {
			resumeItem, remainingNewItems, ok := projectEinoAssistantResumeTurnItem(newItems)
			if !ok {
				return nil, errors.New("eino turn loop resume requires an approval decision")
			}
			if strings.TrimSpace(req.Continuation.Eino.InterruptID) == "" {
				return nil, errors.New("eino interrupt id is required for resume")
			}
			resumeData, err := projectEinoAssistantResumeData(req.Continuation.Eino.InterruptType, resumeItem)
			if err != nil {
				return nil, err
			}
			remaining := make([]projectAssistantTurnItem, 0, len(unhandledItems)+len(remainingNewItems))
			remaining = append(remaining, unhandledItems...)
			remaining = append(remaining, remainingNewItems...)
			return &adk.GenResumeResult[projectAssistantTurnItem, *schema.Message]{
				RunCtx: loopCtx,
				ResumeParams: &adk.ResumeParams{
					Targets: map[string]any{
						req.Continuation.Eino.InterruptID: resumeData,
					},
				},
				Consumed:  append([]projectAssistantTurnItem(nil), interruptedItems...),
				Remaining: remaining,
				RunOpts:   projectEinoAssistantRunOptions(req, runState),
			}, nil
		},
		PrepareAgent: func(agentCtx context.Context, _ *adk.TurnLoop[projectAssistantTurnItem, *schema.Message], _ []projectAssistantTurnItem) (adk.TypedAgent[*schema.Message], error) {
			return e.newAgent(agentCtx, req, runState)
		},
		OnAgentEvents: func(eventCtx context.Context, tc *adk.TurnContext[projectAssistantTurnItem, *schema.Message], iter *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.Message]]) error {
			return e.collectProjectAssistantTurnEvents(eventCtx, tc, iter, req, runState, outcome)
		},
		Store:        checkpointStore,
		CheckpointID: checkpointID,
	})
	for _, item := range items {
		loop.Push(item)
	}
	// Let Eino own cancellation and safe-point unwinding. The outer context is
	// observed separately so a user Stop can request graceful cancellation,
	// suppress a stale checkpoint, and attach a durable cause.
	loop.Run(context.WithoutCancel(ctx))
	stopWatcherDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			options := []adk.StopOption{adk.WithGracefulTimeout(projectAssistantGracefulStopTimeout)}
			if errors.Is(context.Cause(ctx), errProjectAssistantUserStop) {
				options = append(options, adk.WithSkipCheckpoint(), adk.WithStopCause("user_stop"))
			}
			loop.Stop(options...)
		case <-stopWatcherDone:
		}
	}()
	exit := loop.Wait()
	close(stopWatcherDone)
	if exit.CheckpointErr != nil {
		return projectAssistantRunResult{}, exit.CheckpointErr
	}
	if outcome.interrupt != nil {
		return projectAssistantRunResult{}, e.saveProjectAssistantInterrupt(ctx, req, runState, checkpointStore, checkpointID, outcome.interrupt)
	}
	if exit.ExitReason != nil {
		if ctx.Err() == nil && projectEinoAssistantBoundedExit(exit.ExitReason) {
			outcome.result.Content = e.projectAssistantToolLoopFinalAnswer(ctx, req, runState)
		}
		return projectEinoAssistantResultWithCompletion(outcome.result, runState), exit.ExitReason
	}
	if !outcome.receivedOutput {
		return projectEinoAssistantResultWithCompletion(projectAssistantRunResult{
			Content: projectEinoAssistantBoundedClosingFallback("No usable assistant response was produced for this turn."),
		}, runState), errProjectAssistantNoOutput
	}
	return projectEinoAssistantResultWithCompletion(outcome.result, runState), nil
}

func projectEinoAssistantResultWithCompletion(
	result projectAssistantRunResult,
	runState *projectEinoAssistantRunState,
) projectAssistantRunResult {
	if runState == nil {
		return result
	}
	result.InitialPlan, _ = runState.ExecutionPlan()
	if result.InitialPlan != nil && strings.TrimSpace(result.InitialPlan.Goal) != "" {
		result.InitialBuild = true
	} else if authority := runState.ApprovedPlan(); authority != nil &&
		authority.RunLocal &&
		authority.ApprovalTool == "project_create_prompt" &&
		strings.TrimSpace(authority.Goal) != "" {
		result.InitialBuild = true
	}
	result.CompletionEvidence = runState.CompletionEvidence()
	return result
}

func projectEinoAssistantResumeTurnItem(items []projectAssistantTurnItem) (projectAssistantTurnItem, []projectAssistantTurnItem, bool) {
	remaining := make([]projectAssistantTurnItem, 0, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == projectAssistantTurnResume {
			remaining = append(remaining, items[:i]...)
			remaining = append(remaining, items[i+1:]...)
			return items[i], remaining, true
		}
	}
	return projectAssistantTurnItem{}, items, false
}

func projectEinoAssistantResumeData(interruptType string, item projectAssistantTurnItem) (any, error) {
	switch strings.TrimSpace(interruptType) {
	case projectAssistantInterruptTypePermission:
		decision, err := parseProjectAssistantPermissionDecision(item.Decision)
		if err != nil {
			return nil, err
		}
		return &projectEinoPermissionResumeData{
			Decision:        decision,
			EditedArguments: cloneProjectAssistantToolArguments(item.EditedArguments),
		}, nil
	case projectAssistantInterruptTypeApproval:
		decision, err := parseProjectAssistantPermissionDecision(item.Decision)
		if err != nil {
			return nil, err
		}
		if decision == projectAssistantPermissionAllow {
			return &approvaltool.ApprovalResult{Approved: true}, nil
		}
		reason := "denied by user"
		return &approvaltool.ApprovalResult{Approved: false, DisapproveReason: &reason}, nil
	case projectAssistantInterruptTypeFollowUp:
		answer := strings.TrimSpace(item.Answer)
		if answer == "" {
			return nil, newValidationError("answer is required")
		}
		return &projectEinoFollowUpResumeData{Answer: answer}, nil
	default:
		return nil, fmt.Errorf("unsupported eino interrupt type %q", interruptType)
	}
}

func (e projectEinoAssistantEngine) collectProjectAssistantTurnEvents(
	eventCtx context.Context,
	tc *adk.TurnContext[projectAssistantTurnItem, *schema.Message],
	iter *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.Message]],
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	outcome *projectEinoAssistantTurnOutcome,
) error {
	if iter == nil {
		return errors.New("eino turn loop returned no event stream")
	}
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			if projectEinoAssistantWillRetry(event.Err) {
				continue
			}
			return event.Err
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			outcome.interrupt = event.Action.Interrupted
			return nil
		}
		if event.Output == nil {
			continue
		}
		if runResult, ok := event.Output.CustomizedOutput.(projectAssistantRunResult); ok {
			outcome.result = runResult
			outcome.receivedOutput = true
			continue
		}
		messageOutput := event.Output.MessageOutput
		if messageOutput == nil {
			continue
		}
		msg, err := projectEinoAssistantMessageOutput(eventCtx, messageOutput, req.StreamCallbacks)
		if err != nil {
			if projectEinoAssistantWillRetry(err) {
				continue
			}
			return err
		}
		role := messageOutput.Role
		if role == "" && msg != nil {
			role = msg.Role
		}
		if msg != nil && role == schema.Assistant {
			content := projectEinoAssistantSummaryText(msg)
			if strings.TrimSpace(content) == "" {
				continue
			}
			outcome.result.Content = content
			outcome.receivedOutput = true
		}
	}
	if outcome.interrupt == nil {
		tc.Loop.Stop()
	}
	return nil
}

func projectEinoAssistantMessageOutput(
	ctx context.Context,
	output *adk.TypedMessageVariant[*schema.Message],
	streamCallbacks projectAssistantStreamCallbacks,
) (*schema.Message, error) {
	if output == nil {
		return nil, nil
	}
	if !output.IsStreaming {
		return output.Message, nil
	}
	if output.MessageStream == nil {
		return nil, errors.New("eino assistant stream event missing message stream")
	}
	defer output.MessageStream.Close()

	var chunks []*schema.Message
	var provisional strings.Builder
	provisionalSent := false
	resetProvisional := func() {
		if provisionalSent && streamCallbacks.OnProvisionalReset != nil {
			streamCallbacks.OnProvisionalReset()
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			resetProvisional()
			return nil, err
		}
		msg, err := output.MessageStream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			resetProvisional()
			return nil, err
		}
		if msg == nil {
			continue
		}
		chunks = append(chunks, msg)
		if output.Role == schema.Assistant && streamCallbacks.OnProvisionalText != nil {
			if text := projectEinoAssistantStreamText(msg); text != "" {
				provisional.WriteString(text)
				streamCallbacks.OnProvisionalText(provisional.String())
				provisionalSent = true
			}
		}
	}
	msg, err := schema.ConcatMessages(chunks)
	if err != nil {
		resetProvisional()
		return nil, err
	}
	if output.Role == schema.Assistant && streamCallbacks.OnChunk != nil && msg.Content != "" {
		streamCallbacks.OnChunk(msg.Content)
	}
	return msg, nil
}

func projectEinoAssistantStreamText(msg *schema.Message) string {
	if msg == nil {
		return ""
	}
	if len(msg.AssistantGenMultiContent) == 0 {
		return msg.Content
	}
	var b strings.Builder
	for _, part := range msg.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeText {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func (e projectEinoAssistantEngine) projectAssistantToolLoopFinalAnswer(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) string {
	fallback := projectEinoAssistantBoundedClosingFallback(runState.ToolLoopFallback())
	if e.newModel == nil {
		return fallback
	}
	input, err := projectChatMessagesToEino(runState.ToolLoopFinalAnswerMessages())
	if err != nil {
		return fallback
	}
	chatModel, err := e.newModel(ctx, req, runState)
	if err != nil {
		return fallback
	}
	msg, err := chatModel.Generate(ctx, input, einomodel.WithToolChoice(schema.ToolChoiceForbidden))
	if err != nil || msg == nil {
		return fallback
	}
	if !projectEinoAssistantBoundedClosingBodyValid(msg.Content) || len(msg.ToolCalls) > 0 {
		return fallback
	}
	return projectEinoAssistantBoundedClosingAnswer(msg.Content)
}

func (e projectEinoAssistantEngine) saveProjectAssistantInterrupt(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	checkpointStore *projectEinoAssistantCheckpointStore,
	checkpointID string,
	interrupted *adk.InterruptInfo,
) error {
	if e.server == nil {
		return errors.New("server is not configured for permission checkpoints")
	}
	checkpoint, ok, err := checkpointStore.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if !ok || len(checkpoint) == 0 {
		return errors.New("eino checkpoint was not saved for permission interrupt")
	}
	if info, interruptID, ok := projectEinoPermissionInterruptInfoFromEvent(interrupted); ok {
		return e.saveProjectAssistantPermissionInterrupt(ctx, req, runState, checkpoint, checkpointID, interruptID, projectAssistantInterruptTypePermission, info)
	}
	if info, interruptID, ok := projectEinoApprovalInterruptInfoFromEvent(interrupted); ok {
		return e.saveProjectAssistantPermissionInterrupt(ctx, req, runState, checkpoint, checkpointID, interruptID, projectAssistantInterruptTypeApproval, info)
	}
	if info, interruptID, ok := projectEinoFollowUpInterruptInfoFromEvent(interrupted); ok {
		return e.saveProjectAssistantFollowUpInterrupt(ctx, req, runState, checkpoint, checkpointID, interruptID, info)
	}
	return errors.New("eino interrupt did not include App Studio metadata")
}

func (e projectEinoAssistantEngine) saveProjectAssistantPermissionInterrupt(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	checkpoint []byte,
	checkpointID string,
	interruptID string,
	interruptType string,
	info *projectEinoPermissionInterruptInfo,
) error {
	_, index, toolCalls := runState.ToolCallByID(info.ToolCallID, info.ToolName, info.ArgumentsInJSON)
	state := runState.CheckpointState()
	if len(state.ToolCalls) == 0 {
		state.ToolCalls = cloneProjectAssistantToolCalls(toolCalls)
	}
	state.CurrentIndex = index
	state.Eino = &projectAssistantEinoCheckpointState{
		CheckpointID:  strings.TrimSpace(checkpointID),
		Checkpoint:    checkpoint,
		InterruptID:   interruptID,
		InterruptType: interruptType,
		ToolCallID:    info.ToolCallID,
		ToolName:      info.ToolName,
	}
	permissionErr, permission, checkpointEvent, err := e.server.saveProjectAssistantEinoPermissionCheckpoint(ctx, req, state, info)
	if err != nil {
		return err
	}
	if req.StreamCallbacks.OnAssistantEvent != nil {
		req.StreamCallbacks.OnAssistantEvent(projectAssistantEvent{
			Type:       projectAssistantEventPermissionNeeded,
			Permission: &permission,
		})
		req.StreamCallbacks.OnAssistantEvent(projectAssistantEvent{
			Type:       projectAssistantEventCheckpointSaved,
			Checkpoint: &checkpointEvent,
		})
	}
	if info.Risk == projectAssistantToolRiskPlan {
		emitProjectAssistantBuilderEvent(req.StreamCallbacks, projectAssistantBuilderEventView(projectBuilderEventPlanReady))
	}
	return permissionErr
}

func (e projectEinoAssistantEngine) saveProjectAssistantFollowUpInterrupt(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	checkpoint []byte,
	checkpointID string,
	interruptID string,
	info *projectEinoFollowUpInterruptInfo,
) error {
	_, index, toolCalls := runState.ToolCallByID(info.ToolCallID, projectToolAskFollowUp, projectEinoToolArgumentsString(map[string]any{"questions": info.Questions}))
	state := runState.CheckpointState()
	if len(state.ToolCalls) == 0 {
		state.ToolCalls = cloneProjectAssistantToolCalls(toolCalls)
	}
	state.CurrentIndex = index
	state.Eino = &projectAssistantEinoCheckpointState{
		CheckpointID:  strings.TrimSpace(checkpointID),
		Checkpoint:    checkpoint,
		InterruptID:   interruptID,
		InterruptType: projectAssistantInterruptTypeFollowUp,
		ToolCallID:    info.ToolCallID,
		ToolName:      projectToolAskFollowUp,
	}
	inputErr, followUp, checkpointEvent, err := e.server.saveProjectAssistantEinoFollowUpCheckpoint(ctx, req, state, info)
	if err != nil {
		return err
	}
	if req.StreamCallbacks.OnAssistantEvent != nil {
		req.StreamCallbacks.OnAssistantEvent(projectAssistantEvent{
			Type:     projectAssistantEventInputNeeded,
			FollowUp: &followUp,
		})
		req.StreamCallbacks.OnAssistantEvent(projectAssistantEvent{
			Type:       projectAssistantEventCheckpointSaved,
			Checkpoint: &checkpointEvent,
		})
	}
	return inputErr
}

func projectEinoPermissionInterruptInfoFromEvent(interrupted *adk.InterruptInfo) (*projectEinoPermissionInterruptInfo, string, bool) {
	if interrupted == nil {
		return nil, "", false
	}
	for _, interruptCtx := range interrupted.InterruptContexts {
		if interruptCtx == nil {
			continue
		}
		switch info := interruptCtx.Info.(type) {
		case *projectEinoPermissionInterruptInfo:
			if info != nil {
				return info, strings.TrimSpace(interruptCtx.ID), true
			}
		case projectEinoPermissionInterruptInfo:
			return &info, strings.TrimSpace(interruptCtx.ID), true
		}
	}
	return nil, "", false
}

func projectEinoApprovalInterruptInfoFromEvent(interrupted *adk.InterruptInfo) (*projectEinoPermissionInterruptInfo, string, bool) {
	if interrupted == nil {
		return nil, "", false
	}
	for _, interruptCtx := range interrupted.InterruptContexts {
		if interruptCtx == nil {
			continue
		}
		switch info := interruptCtx.Info.(type) {
		case *approvaltool.ApprovalInfo:
			if info != nil {
				return projectEinoPermissionInterruptInfoForApproval(info), strings.TrimSpace(interruptCtx.ID), true
			}
		case approvaltool.ApprovalInfo:
			return projectEinoPermissionInterruptInfoForApproval(&info), strings.TrimSpace(interruptCtx.ID), true
		}
	}
	return nil, "", false
}

func projectEinoPermissionInterruptInfoForApproval(info *approvaltool.ApprovalInfo) *projectEinoPermissionInterruptInfo {
	if info == nil {
		return nil
	}
	spec, ok := projectAssistantWorkflowToolSpec(info.ToolName)
	if !ok {
		spec = projectAssistantToolSpec{Name: strings.TrimSpace(info.ToolName)}
	}
	args := map[string]any{}
	_ = json.Unmarshal([]byte(info.ArgumentsInJSON), &args)
	return &projectEinoPermissionInterruptInfo{
		ToolName:        spec.Name,
		ArgumentsInJSON: strings.TrimSpace(info.ArgumentsInJSON),
		Reason:          projectAssistantPermissionReasonForArguments(spec, args),
		Risk:            spec.Risk,
	}
}

func projectEinoFollowUpInterruptInfoFromEvent(interrupted *adk.InterruptInfo) (*projectEinoFollowUpInterruptInfo, string, bool) {
	if interrupted == nil {
		return nil, "", false
	}
	for _, interruptCtx := range interrupted.InterruptContexts {
		if interruptCtx == nil {
			continue
		}
		switch info := interruptCtx.Info.(type) {
		case *projectEinoFollowUpInterruptInfo:
			if info != nil {
				return info, strings.TrimSpace(interruptCtx.ID), true
			}
		case projectEinoFollowUpInterruptInfo:
			return &info, strings.TrimSpace(interruptCtx.ID), true
		}
	}
	return nil, "", false
}

func projectEinoAssistantInputMessages(ctx context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) ([]adk.Message, error) {
	var chatMessages []chatMessage
	if req.Continuation != nil && len(req.Continuation.Messages) > 0 {
		chatMessages = cloneChatMessages(req.Continuation.Messages)
	} else {
		chatMessages = projectPromptMessagesForInitialPlan(req.Project, req.Repository, req.History, req.TurnProfile, req.InitialApprovedPlan != nil)
		if snapshot, ok := projectEinoAssistantSessionContextMessage(ctx, req, runState); ok {
			chatMessages = append(chatMessages, snapshot)
		}
		if prompt := runState.ToolPrompt(); prompt != "" {
			chatMessages = append(chatMessages, chatMessage{Role: "system", Content: prompt})
		}
	}
	chatMessages = append([]chatMessage{{
		Role:    "system",
		Content: projectEinoAssistantDeepInstruction,
	}}, chatMessages...)
	messages, err := projectChatMessagesToEino(chatMessages)
	if err != nil {
		return nil, err
	}
	input := make([]adk.Message, 0, len(messages))
	for _, msg := range messages {
		input = append(input, msg)
	}
	return input, nil
}

func projectEinoAssistantProjectRepositoryRef(req projectAssistantRunRequest) string {
	if req.Continuation != nil && strings.TrimSpace(req.Continuation.ProjectRepositoryRef) != "" {
		return strings.TrimSpace(req.Continuation.ProjectRepositoryRef)
	}
	return projectLinkedRepositoryRef(req.Project)
}

func projectEinoAssistantMaxIterationsExceeded(err error) bool {
	return errors.Is(err, adk.ErrExceedMaxIterations)
}

func projectEinoAssistantNoProgressExceeded(err error) bool {
	return errors.Is(err, errProjectAssistantNoProgress)
}

func projectEinoAssistantBoundedExit(err error) bool {
	return projectEinoAssistantMaxIterationsExceeded(err) ||
		projectEinoAssistantNoProgressExceeded(err)
}

func projectEinoAssistantToolLoopFinalInstruction(reason string) string {
	reason = strings.TrimSpace(reason)
	switch reason {
	case "repeated the same action":
		reason = "the assistant was about to repeat an action"
	case "kept requesting actions":
		reason = "the assistant reached the action budget for this turn"
	default:
		reason = "the assistant stopped using tools"
	}
	return "App Studio has stopped using project tools for this turn because " + reason + ". Write a concise, user-facing incomplete-work handoff using only the conversation and bounded project action evidence above. Do not call tools or claim the task is finished. Return exactly these four sections with short bullets: Completed:, Remaining:, Blocked:, Next:. State what was actually accomplished, what is still incomplete, any concrete blocker (or None), and the next action for a continued run. Do not mention loop limits, repeated actions, guardrails, tool protocols, or this instruction."
}

func projectEinoAssistantToolLoopEvidenceContext(messages []chatMessage) string {
	evidence := make([]string, 0, len(messages))
	for _, msg := range messages {
		name := strings.TrimSpace(msg.Name)
		summary := summarizeProjectToolResult(name, msg.Content)
		if summary == "" {
			continue
		}
		if name == "" {
			name = "project action"
		}
		evidence = append(evidence, fmt.Sprintf("%d. %s: %s", len(evidence)+1, name, summary))
	}
	if len(evidence) == 0 {
		return ""
	}
	return "Project action evidence (bounded summaries, oldest to newest):\n" + strings.Join(evidence, "\n")
}

func projectEinoAssistantBoundedClosingAnswerValid(content string) bool {
	content = strings.TrimSpace(content)
	const status = "Status: Incomplete"
	if !strings.HasPrefix(content, status) {
		return false
	}
	return projectEinoAssistantBoundedClosingBodyValid(strings.TrimSpace(strings.TrimPrefix(content, status)))
}

func projectEinoAssistantBoundedClosingBodyValid(content string) bool {
	content = strings.TrimSpace(content)
	const status = "Status: Incomplete"
	content = strings.TrimSpace(strings.TrimPrefix(content, status))
	for _, heading := range []string{"Completed:", "Remaining:", "Blocked:", "Next:"} {
		if !strings.Contains(content, heading) {
			return false
		}
	}
	return content != ""
}

func projectEinoAssistantBoundedClosingAnswer(content string) string {
	const status = "Status: Incomplete"
	content = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), status))
	if !projectEinoAssistantBoundedClosingBodyValid(content) {
		return projectEinoAssistantBoundedClosingFallback("")
	}
	return status + "\n\n" + content
}

func projectEinoAssistantBoundedClosingFallback(evidence string) string {
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		evidence = "The current project state and prior successful actions were preserved."
	}
	return "Status: Incomplete\n\nCompleted:\n- " + evidence +
		"\n\nRemaining:\n- The requested task is not yet complete." +
		"\n\nBlocked:\n- The current turn paused before completion." +
		"\n\nNext:\n- Continue from the preserved project state and verify the remaining work."
}
