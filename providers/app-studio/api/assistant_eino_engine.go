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
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectEinoAssistantClosingEvidenceMaxItems = 64
	projectEinoAssistantLiveContextPrefix       = "App Studio live request context (regenerated before every model sample):\n"
	projectEinoAssistantProjectPromptPrefix     = "You are the assistant for a persistent Kedge Project workspace. "
	projectEinoAssistantSessionSnapshotPrefix   = "Current project snapshot (authoritative for the start of this turn;"
	projectEinoAssistantV2DeepInstruction       = "You are the App Studio project assistant. Use only the tools exposed in this turn; do not assume shell, browser, host filesystem, or subagent access. " +
		projectAssistantBrowserConsoleTrustInstruction +
		projectAssistantRepairRecoveryInstruction +
		"Browser inspection is available only when inspect_development_preview is exposed; it is read-only and cannot click, type, press keys, or run arbitrary JavaScript. Static text and role assertions verify rendered state only; they never verify keyboard, click, form, or other interaction behavior. Describe such behavior as source-reviewed but not browser-exercised, and never say it is live, working, or independently verified from static assertions. The server-selected Default, Plan, or Review collaboration mode is fixed for this turn. Plan and Review are read-only. In Default, infer inspection versus action authority from the user's request, diagnose reported defects from current evidence before editing, and keep changes narrowly scoped. When the user asks you to change, build, or fix the project, persist until the request is handled end-to-end whenever feasible: do not stop at analysis or a partial fix, and carry the work through implementation, relevant verification, and a clear explanation unless the user pauses, redirects, or required authority or input is missing. Tool calls continue the turn; a final assistant message ends it. You may call independent tools together when their arguments do not depend on one another. When tool_search is exposed, use it to load a less-common provider or repository capability before calling that capability. " +
		"The only source-mutation tools are create_file, replace_file, edit_file, delete_file, and move_file. create_file is always create-only. replace_file, delete_file, and move_file require a complete bounded read and the opaque expectedVersion from that read. edit_file reads the current file under the workspace mutation lock and applies exact oldString replacement, so a separate read and expectedVersion are optional; stale or ambiguous text fails closed. " +
		"Delete and move are supported only within server-approved workspace paths. Dirty files are workspace information, not an obligation to verify or commit. Use verify_development_runtime only when operational synchronization, process, log, or preview reachability evidence is relevant. After changing a dependency manifest, start command, or build/runtime configuration, restart the development runtime before verification because file synchronization does not reload process configuration. Never call commit_project_files unless the user explicitly asked to persist changes to the repository. Do not claim rendered content, interactions, data flow, or acceptance criteria were independently verified unless inspect_development_preview actually observed them. Finish with the model response that directly answers the user; do not add status boilerplate."
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
	if req.Workspace == nil && e.server != nil {
		req.Workspace = e.server.workspaces
	}

	req.TurnPolicy = normalizeProjectAssistantTurnPolicy(req.TurnPolicy, req.TurnProfile)
	req.TurnProfile = req.TurnPolicy.profile
	runState := newProjectEinoAssistantRunState()
	runState.SetAgentOptimizationMode(projectEinoAssistantOptimizationModeFromEnvironment())
	runState.SetTurnPolicy(req.TurnPolicy)
	if req.SkillSnapshot == nil && e.server != nil {
		snapshot, err := e.server.projectAssistantSkillSnapshotForIdentity(ctx, req.WorkspaceScope, req.Identity)
		if err != nil {
			return projectAssistantRunResult{}, err
		}
		req.SkillSnapshot = &snapshot
	}
	if req.SkillSnapshot != nil {
		if err := runState.ConfigureSkillSnapshot(*req.SkillSnapshot, req.SelectedSkills, nil); err != nil {
			return projectAssistantRunResult{}, err
		}
	}
	runState.SetContextResources(req.SelectedContextResources)
	runState.SetContentParts(req.ContentParts)
	runState.SetProjectRepositoryRef(projectEinoAssistantProjectRepositoryRef(req))
	if projectAssistantTurnProfileAllowsMutation(runState.TurnPolicy().profile) {
		e.resumeCurrentDevelopmentSync(req, runState)
	}
	// A fresh Project creation prompt is explicit authorization to build the
	// initial source tree. Keep this grant run-local: it may survive an
	// interrupt through the run checkpoint, but is never persisted as the
	// cross-turn plan grant used by subsequent conversations.
	if req.InitialApprovedPlan != nil {
		runState.ApprovePlan(*req.InitialApprovedPlan)
	}
	if e.server != nil && e.server.store != nil {
		req.eventLedger = newProjectAssistantRunEventLedger(e.server.store, req.MessageScope, projectAssistantRunID(req))
	}
	if err := projectEinoAssistantHydratePlanProgress(ctx, req.eventLedger, runState, req.StreamCallbacks); err != nil {
		return projectAssistantRunResult{}, fmt.Errorf("restore durable assistant plan progress: %w", err)
	}
	if err := e.restoreProjectAssistantDirtyBundle(ctx, req, runState); err != nil {
		return projectAssistantRunResult{}, err
	}

	checkpointID := newProjectAssistantRunID()
	auditRecorder, err := e.startProjectAssistantRunAudit(ctx, &req, checkpointID)
	if err != nil {
		return projectAssistantRunResult{}, err
	}
	if err := projectEinoAssistantConfigureRolloutBudget(ctx, e.server, req, runState, auditRecorder); err != nil {
		return projectAssistantRunResult{}, err
	}
	checkpointStore := newProjectEinoAssistantCheckpointStore()
	turn := newProjectAssistantTurnItem(projectAssistantTurnMessage, req.Identity, req.Project.Name)
	turn.ProjectUID = req.MessageScope.ProjectUID
	result, runErr := e.runProjectAssistantTurnLoop(ctx, req, runState, checkpointStore, checkpointID, []projectAssistantTurnItem{turn})
	return result, e.finishProjectAssistantRunAudit(ctx, req, auditRecorder, runErr)
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
	if err := projectAssistantValidateSkillCheckpointProvenance(state); err != nil {
		projectAssistantSkillMetric("drift", "detected")
		return projectAssistantRunResult{}, err
	}
	if e.newModel == nil {
		return projectAssistantRunResult{}, errors.New("eino model factory is not configured")
	}
	if e.newTools == nil {
		return projectAssistantRunResult{}, errors.New("eino tool factory is not configured")
	}
	if req.Workspace == nil && e.server != nil {
		req.Workspace = e.server.workspaces
	}

	runState := newProjectEinoAssistantRunState()
	runState.RestoreCheckpointState(state)
	req.SelectedContextResources = runState.ContextResources()
	req.ContentParts = runState.ContentParts()
	hasSkillCheckpoint := state.CatalogDigest != "" || len(state.SelectedSkillReceipts) > 0 || len(state.LoadedSkillReceipts) > 0
	if e.server == nil && hasSkillCheckpoint {
		return projectAssistantRunResult{}, errors.New("assistant skill catalog is unavailable")
	}
	if e.server != nil {
		skillSnapshot, err := e.server.projectAssistantSkillSnapshotForIdentity(ctx, req.WorkspaceScope, req.Identity)
		if err != nil {
			return projectAssistantRunResult{}, err
		}
		if state.CatalogDigest != "" && skillSnapshot.CatalogDigest != state.CatalogDigest {
			projectAssistantSkillMetric("drift", "detected")
			return projectAssistantRunResult{}, errProjectAssistantSkillCatalogDrift
		}
		if err := runState.ConfigureSkillSnapshot(skillSnapshot, state.SelectedSkillReceipts, state.LoadedSkillReceipts); err != nil {
			return projectAssistantRunResult{}, err
		}
		req.SkillSnapshot = &skillSnapshot
		req.SelectedSkills = cloneProjectAssistantSkillReceipts(state.SelectedSkillReceipts)
	}
	if projectAssistantTurnProfileAllowsMutation(runState.TurnPolicy().profile) {
		e.resumeCurrentDevelopmentSync(req, runState)
	}
	runState.SetProjectRepositoryRef(projectEinoAssistantProjectRepositoryRef(projectAssistantRunRequest{
		Project:      req.Project,
		Continuation: &state,
	}))
	resumeRunReq := req
	resumeRunReq.Continuation = &state
	if e.server != nil && e.server.store != nil {
		resumeRunReq.eventLedger = newProjectAssistantRunEventLedger(e.server.store, req.MessageScope, projectAssistantRunID(req))
	}
	if err := projectEinoAssistantHydratePlanProgress(ctx, resumeRunReq.eventLedger, runState, resumeRunReq.StreamCallbacks); err != nil {
		return projectAssistantRunResult{}, fmt.Errorf("restore durable assistant plan progress: %w", err)
	}
	if err := e.restoreProjectAssistantDirtyBundle(ctx, resumeRunReq, runState); err != nil {
		return projectAssistantRunResult{}, err
	}
	// Resume the exact sticky collaboration mode stored on the run. Prompt
	// wording, checkpoint policy, and follow-up text cannot change it.
	if req.AssistantRun == nil {
		return projectAssistantRunResult{}, store.ErrAssistantRunConflict
	}
	mode, ok := projectAssistantCollaborationModeForRun(*req.AssistantRun)
	if !ok {
		return projectAssistantRunResult{}, store.ErrAssistantRunConflict
	}
	profile := projectAssistantTurnProfileImplementation
	if projectAssistantCollaborationModeReadOnly(mode) {
		profile = projectAssistantTurnProfileDebugging
		runState.ClearApprovedPlan()
		runState.ClearExecutionPlan()
	} else if approved := runState.ApprovedPlan(); approved != nil && !approved.RunLocal {
		runState.ClearApprovedPlan()
	}
	resumeRunReq.CollaborationMode = mode
	resumeRunReq.TurnPolicy = projectAssistantTurnPolicyForProfile(profile)
	resumeRunReq.TurnProfile = profile
	runState.SetTurnPolicy(resumeRunReq.TurnPolicy)
	checkpointStore := newProjectEinoAssistantCheckpointStoreWithCheckpoint(state.Eino.CheckpointID, state.Eino.Checkpoint)
	turn := newProjectAssistantTurnItem(projectAssistantTurnResume, req.Identity, req.Project.Name)
	turn.ProjectUID = req.MessageScope.ProjectUID
	turn.RequestID = strings.TrimSpace(resumeReq.RequestID)
	turn.Decision = strings.TrimSpace(resumeReq.Decision)
	turn.Answer = strings.TrimSpace(resumeReq.Answer)
	turn.Answers = cloneProjectAssistantFollowUpAnswers(resumeReq.Answers)
	turn.EditedArguments = cloneProjectAssistantToolArguments(resumeReq.EditedArguments)
	auditRecorder, err := e.resumeProjectAssistantRunAudit(&resumeRunReq)
	if err != nil {
		return projectAssistantRunResult{}, err
	}
	if err := projectEinoAssistantConfigureRolloutBudget(ctx, e.server, resumeRunReq, runState, auditRecorder); err != nil {
		return projectAssistantRunResult{}, err
	}
	result, runErr := e.runProjectAssistantTurnLoop(ctx, resumeRunReq, runState, checkpointStore, state.Eino.CheckpointID, []projectAssistantTurnItem{turn})
	return result, e.finishProjectAssistantRunAudit(ctx, resumeRunReq, auditRecorder, runErr)
}

// restoreProjectAssistantDirtyBundle promotes project-scoped pending source
// into the current run exactly once. A later turn can therefore synchronize,
// verify, and commit an earlier turn's complete dirty bundle without inventing
// a new edit or silently treating it as revision zero.
func (e projectEinoAssistantEngine) restoreProjectAssistantDirtyBundle(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) error {
	if runState == nil || req.Workspace == nil || !projectAssistantTurnProfileAllowsMutation(runState.TurnPolicy().profile) {
		return nil
	}
	if revision, _ := runState.SourceMutationRevisions(); revision > 0 {
		return nil
	}
	if _, err := req.Workspace.ReconcileCommitSettlement(ctx, req.WorkspaceScope); err != nil {
		return fmt.Errorf("restore project commit settlement: %w", err)
	}
	paths, err := req.Workspace.UncommittedPaths(ctx, req.WorkspaceScope)
	if err != nil {
		return fmt.Errorf("restore project dirty workspace bundle: %w", err)
	}
	if len(paths) == 0 {
		return nil
	}
	for _, path := range paths {
		runState.RecordSuccessfulMutationPath(path)
	}
	revision := runState.BeginDevelopmentSyncForNextMutation()
	if e.server == nil || !e.server.scheduleDevelopmentSyncAfterMutationWithCompletion(
		req.Identity,
		req.Project,
		projectActionWorkspaceSync,
		func(syncErr error) { runState.CompleteDevelopmentSync(revision, syncErr) },
	) {
		runState.CompleteDevelopmentSync(revision, errors.New("workspace synchronization was not scheduled"))
	}
	runState.RecordSourceMutation()
	return nil
}

func (e projectEinoAssistantEngine) resumeCurrentDevelopmentSync(
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) {
	if runState == nil {
		return
	}
	revision, _ := runState.SourceMutationRevisions()
	if revision == 0 {
		return
	}
	status, _ := runState.DevelopmentSyncEvidence(revision)
	switch status {
	case "failed":
		var claimed bool
		revision, claimed = runState.ClaimDevelopmentSyncRetry(revision)
		if !claimed {
			return
		}
	case "unknown", "pending":
		revision = runState.BeginDevelopmentSyncForCurrentMutation()
	default:
		return
	}
	if revision == 0 {
		return
	}
	if e.server == nil || !e.server.scheduleDevelopmentSyncAfterMutationWithCompletion(
		req.Identity,
		req.Project,
		projectActionWorkspaceSync,
		func(syncErr error) { runState.CompleteDevelopmentSync(revision, syncErr) },
	) {
		runState.CompleteDevelopmentSync(revision, errors.New("workspace synchronization was not scheduled after assistant resume"))
	}
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
		strings.TrimSpace(scope.ProjectName) != "" &&
		strings.TrimSpace(scope.ProjectUID) != ""
}

func projectEinoAssistantConfigureRolloutBudget(
	ctx context.Context,
	server *Server,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	recorder *projectAssistantRunAuditRecorder,
) error {
	if runState == nil {
		return nil
	}
	restored := runState.RestoredRolloutBudget()
	if restored == nil && recorder != nil {
		restored = recorder.rolloutBudgetSnapshot()
	}
	if restored == nil && server != nil && server.store != nil {
		var err error
		restored, err = loadProjectAssistantConversationRolloutBudgetState(ctx, server.store, req.MessageScope)
		if err != nil {
			return fmt.Errorf("restore assistant conversation rollout budget: %w", err)
		}
	}
	var persistReminder func(context.Context, *projectAssistantRolloutBudgetReminder) error
	var persistState func(context.Context, projectAssistantRolloutBudgetState) error
	if server != nil && server.store != nil {
		runID := projectAssistantRunID(req)
		persistReminder = func(ctx context.Context, reminder *projectAssistantRolloutBudgetReminder) error {
			message := projectEinoAssistantRolloutBudgetMessage(reminder)
			if message == nil {
				return nil
			}
			itemID := fmt.Sprintf("rollout-budget-%s-%d-%d", runID, reminder.WindowID, reminder.ReminderIndex)
			return appendProjectAssistantConversationMessage(
				ctx,
				server.store,
				req.MessageScope,
				runID,
				itemID,
				projectAssistantConversationRolloutBudget,
				chatMessage{Role: "system", Content: message.Content},
			)
		}
		persistState = func(ctx context.Context, state projectAssistantRolloutBudgetState) error {
			return appendProjectAssistantConversationRolloutBudgetState(ctx, server.store, req.MessageScope, runID, state)
		}
	}
	budget := newProjectEinoAssistantRolloutBudget(
		projectAssistantRolloutBudgetTokens(),
		restored,
		recorder,
		persistReminder,
	)
	if budget != nil {
		budget.persistState = persistState
	}
	runState.SetRolloutBudget(budget)
	return nil
}

func (e projectEinoAssistantEngine) newAgent(ctx context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) (adk.Agent, error) {
	req = projectAssistantRunRequestWithExecutionContext(req)
	tools, err := e.newTools(ctx, req, runState)
	if err != nil {
		return nil, err
	}
	chatModel, err := e.newModel(ctx, req, runState)
	if err != nil {
		return nil, err
	}
	chatModel, compactionModel := projectEinoAssistantModels(chatModel, req, runState)
	var handlers []adk.ChatModelAgentMiddleware
	// Keep the durable ledger and telemetry on the full tool result while the
	// next model request receives Codex-style bounded output. Eino applies the
	// first registered handler outermost.
	handlers = append(handlers, projectEinoAssistantModelToolOutputMiddlewareForModel())
	toolCallsMiddleware, err := projectEinoAssistantToolCallsMiddleware(ctx, req.eventLedger)
	if err != nil {
		return nil, fmt.Errorf("create eino tool calls middleware: %w", err)
	}
	handlers = append(handlers, toolCallsMiddleware)
	reductionMiddleware, err := projectEinoAssistantReductionMiddleware(ctx)
	if err != nil {
		return nil, fmt.Errorf("create eino reduction middleware: %w", err)
	}
	handlers = append(handlers, reductionMiddleware)
	summaryMiddleware, err := projectEinoAssistantCompactionMiddleware(ctx, compactionModel, e.server, req, runState)
	if err != nil {
		return nil, fmt.Errorf("create App Studio compaction middleware: %w", err)
	}
	handlers = append(handlers, summaryMiddleware)
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
	// Validate and normalize each completed model response before dispatch.
	handlers = append(handlers, projectEinoAssistantToolBatchAdmissionMiddleware(runState, req.executionContext))
	// Eino makes the first registered tool wrapper outermost. Safe-error must
	// therefore precede telemetry so telemetry observes backend errors before
	// they are shaped for the model. Phase must also precede telemetry so a
	// hidden or terminal-phase call is denied without emitting read progress.
	handlers = append(handlers, &projectEinoAssistantSafeToolErrorMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
	})
	handlers = append(handlers, projectEinoAssistantLifecycleMiddleware(req, runState, e.server))
	if filesystemMiddleware != nil {
		handlers = append(handlers, projectEinoAssistantFilesystemTelemetryMiddleware(req, runState))
	}
	toolsConfig := adk.ToolsConfig{
		ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               tools,
			UnknownToolsHandler: projectEinoUnknownToolHandler(e.server, req, runState),
			// Preserve model batches. Eino executes calls concurrently and rejoins
			// their results in model order; durable per-call admission remains in
			// the tool ledger.
			ExecuteSequentially: false,
		},
	}
	agent, err := deep.New(ctx, &deep.Config{
		Name:         "app-studio-project-assistant",
		Description:  "Runs App Studio project assistant turns.",
		ChatModel:    chatModel,
		Instruction:  projectEinoAssistantV2DeepInstruction,
		ToolsConfig:  toolsConfig,
		MaxIteration: projectAssistantDeepIterations(),
		// App Studio owns write_todos so its request, admission, result, and
		// replay all pass through the durable run ledger. Do not also install
		// Deep's framework middleware: that path only mutates Eino session state.
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
		Handlers:               handlers,
		ModelRetryConfig:       projectEinoAssistantModelRetryConfig(req, runState),
	})
	if err != nil {
		return nil, fmt.Errorf("create eino assistant agent: %w", err)
	}
	return agent, nil
}

func projectEinoAssistantModels(
	base einomodel.BaseChatModel,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) (mainModel, compactionModel einomodel.BaseChatModel) {
	bounded := projectEinoAssistantBoundModel(base, req.LLM)
	// Compaction uses the same provider and timeout boundary, but it must not
	// consume or mutate main-turn budget, transient-evidence, or progress-
	// reminder state. Capture the bounded base before applying those stateful
	// wrappers to the ordinary agent model.
	compactionModel = bounded
	mainModel = projectEinoAssistantModelWithContextRecovery(bounded)
	mainModel = projectEinoAssistantBudgetModel(mainModel, runState.RolloutBudget())
	mainModel = &projectEinoAssistantTransientEvidenceModel{
		BaseChatModel: mainModel,
		runState:      runState,
	}
	mainModel = &projectEinoAssistantProgressReminderModel{
		BaseChatModel: mainModel,
		req:           req,
		runState:      runState,
	}
	return mainModel, compactionModel
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
			steered := items[0].Kind == projectAssistantTurnSteer
			if steered && e.server != nil {
				runState.SetToolDiscovery(projectEinoAssistantDiscoverTools(loopCtx, e.server, req))
			}
			input, err := projectEinoAssistantInputMessages(loopCtx, req, runState, steered)
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
		return projectEinoAssistantResultWithCompletion(outcome.result, runState), exit.ExitReason
	}
	if !outcome.receivedOutput {
		return projectEinoAssistantResultWithCompletion(outcome.result, runState), errProjectAssistantNoOutput
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
	result.InitialPlan = runState.ExecutionPlan()
	if result.InitialPlan != nil && strings.TrimSpace(result.InitialPlan.Goal) != "" {
		result.InitialBuild = true
	} else if authority := runState.ApprovedPlan(); authority != nil &&
		authority.RunLocal &&
		authority.ApprovalTool == "project_create_prompt" &&
		strings.TrimSpace(authority.Goal) != "" {
		result.InitialBuild = true
	}
	result.CompletionEvidence = runState.CompletionEvidence()
	result.Content = projectEinoAssistantFinalContentWithEvidenceScope(result.Content, runState)
	return result
}

func projectEinoAssistantFinalContentWithEvidenceScope(content string, runState *projectEinoAssistantRunState) string {
	content = strings.TrimSpace(content)
	if runState == nil || !runState.ReadOnlyPreviewInspectionObserved() {
		return content
	}
	const note = "Verification scope: rendered state and accessibility assertions passed. Keyboard, click, and form behavior was source-reviewed but not exercised by the read-only preview inspector."
	if strings.Contains(content, note) {
		return content
	}
	if content == "" {
		return note
	}
	return content + "\n\n" + note
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
		answers := cloneProjectAssistantFollowUpAnswers(item.Answers)
		if answer == "" && len(answers) == 0 {
			return nil, newValidationError("answer is required")
		}
		return &projectEinoFollowUpResumeData{Answer: answer, Answers: answers}, nil
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
		msg, err := projectEinoAssistantMessageOutput(eventCtx, messageOutput, req.StreamCallbacks, runState)
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
			if len(msg.ToolCalls) > 0 {
				continue
			}
			content := projectEinoAssistantSummaryText(msg)
			if strings.TrimSpace(content) == "" {
				continue
			}
			outcome.result.Content = content
			outcome.receivedOutput = true
		}
	}
	if outcome.interrupt == nil {
		if err := runState.RolloutBudget().ExhaustionError(); err != nil {
			return err
		}
		drained, err := projectEinoAssistantDrainSteeringAtBoundary(eventCtx, req.Steering, runState, nil, req.ActivateSteering)
		if err != nil {
			return err
		}
		if drained == 0 && req.SealSteering != nil && !req.SealSteering() {
			drained, err = projectEinoAssistantDrainSteeringAtBoundary(eventCtx, req.Steering, runState, nil, req.ActivateSteering)
			if err != nil {
				return err
			}
		}
		if drained > 0 {
			steer := newProjectAssistantTurnItem(projectAssistantTurnSteer, req.Identity, req.Project.Name)
			steer.ProjectUID = req.MessageScope.ProjectUID
			tc.Loop.Push(steer)
			outcome.result.Content = ""
			outcome.receivedOutput = false
		} else {
			tc.Loop.Stop()
		}
	}
	return nil
}

func projectEinoAssistantMessageOutput(
	ctx context.Context,
	output *adk.TypedMessageVariant[*schema.Message],
	streamCallbacks projectAssistantStreamCallbacks,
	runState *projectEinoAssistantRunState,
) (*schema.Message, error) {
	if output == nil {
		return nil, nil
	}
	if !output.IsStreaming {
		projectEinoAssistantPublishInlineCommentary(output.Role, output.Message, streamCallbacks, runState)
		projectEinoAssistantPublishAcceptedContent(output.Role, output.Message, streamCallbacks)
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
	if len(msg.ToolCalls) > 0 {
		resetProvisional()
	}
	// A model may explain the action it is about to take in the same completed
	// assistant message as its tool calls. Publish that prose exactly once at
	// the completion boundary as inline commentary. It is deliberately not a
	// content chunk: tool-adjacent prose is not the turn's terminal answer.
	projectEinoAssistantPublishInlineCommentary(output.Role, msg, streamCallbacks, runState)
	projectEinoAssistantPublishAcceptedContent(output.Role, msg, streamCallbacks)
	return msg, nil
}

func projectEinoAssistantPublishInlineCommentary(
	role schema.RoleType,
	message *schema.Message,
	streamCallbacks projectAssistantStreamCallbacks,
	runState *projectEinoAssistantRunState,
) {
	if message == nil || len(message.ToolCalls) == 0 || streamCallbacks.OnCommentary == nil {
		return
	}
	if role == "" {
		role = message.Role
	}
	if role != schema.Assistant {
		return
	}
	content := projectEinoAssistantStreamText(message)
	content, reason := projectEinoAssistantProgressMessage(content)
	if reason != "" {
		return
	}
	// Inline commentary and report_progress share the same run-owned acceptance
	// ledger. This makes a retried/replayed tool-adjacent message exactly-once,
	// prevents the model from immediately duplicating it through report_progress,
	// and satisfies the same silence reminder without changing durable UI
	// sequencing or the terminal-answer channel.
	if runState != nil && !runState.AcceptProgressMessage(content) {
		return
	}
	streamCallbacks.OnCommentary(content)
}

func projectEinoAssistantPublishAcceptedContent(
	role schema.RoleType,
	message *schema.Message,
	streamCallbacks projectAssistantStreamCallbacks,
) {
	if message == nil || strings.TrimSpace(message.Content) == "" {
		return
	}
	if role == "" {
		role = message.Role
	}
	if role != schema.Assistant {
		return
	}
	if len(message.ToolCalls) > 0 {
		return
	}
	if streamCallbacks.OnChunk != nil {
		streamCallbacks.OnChunk(message.Content)
	}
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
		Exec:            projectAssistantExecMetadataForToolArguments(spec.Name, args, "", "permission_required"),
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

func projectEinoAssistantInputMessages(ctx context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState, continueRun bool) ([]adk.Message, error) {
	var chatMessages []chatMessage
	if continueRun && len(runState.ModelMessages()) > 0 {
		chatMessages = runState.ModelMessages()
	} else if req.Continuation != nil && len(req.Continuation.Messages) > 0 {
		chatMessages = cloneChatMessages(req.Continuation.Messages)
	} else if len(req.Conversation) > 0 {
		chatMessages = cloneChatMessages(req.Conversation)
	} else {
		chatMessages = appendProjectAssistantConversationHistory(nil, req.History)
	}
	// Durable history and Eino resume state are conversational payload only.
	// The lifecycle middleware reconstructs the authoritative project, session,
	// and tool context immediately before every model sample.
	chatMessages = projectEinoAssistantConversationPayload(chatMessages)
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

func projectEinoAssistantConversationPayload(messages []chatMessage) []chatMessage {
	payload := make([]chatMessage, 0, len(messages))
	for _, message := range messages {
		if projectEinoAssistantLiveContextMessage(message) || projectEinoAssistantLegacyLiveContextMessage(message) {
			continue
		}
		payload = append(payload, message)
	}
	return payload
}

func projectEinoAssistantLiveContextMessage(message chatMessage) bool {
	return message.Role == "system" && strings.HasPrefix(message.Content, projectEinoAssistantLiveContextPrefix)
}

// Compaction checkpoints written before live context was explicitly tagged
// included these system messages in ReplacementHistory. They are derived from
// mutable project state, so they must never survive as conversational payload.
func projectEinoAssistantLegacyLiveContextMessage(message chatMessage) bool {
	if message.Role != "system" {
		return false
	}
	content := strings.TrimSpace(message.Content)
	return content == projectEinoAssistantV2DeepInstruction ||
		strings.HasPrefix(content, projectEinoAssistantProjectPromptPrefix) ||
		strings.HasPrefix(content, projectEinoAssistantSessionSnapshotPrefix) ||
		strings.HasPrefix(content, "Databricks guidance:") ||
		strings.HasPrefix(content, "External MCP tool discovery failed for this workspace:")
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
	return projectEinoAssistantIterationLimited(err) ||
		projectEinoAssistantBudgetLimited(err) ||
		projectEinoAssistantNoProgressExceeded(err)
}
