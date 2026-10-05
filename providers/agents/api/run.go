// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	einomodel "github.com/cloudwego/eino/components/model"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	backendmodel "github.com/railgrid/provider-agents/backend/model"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/internal/harnesspolicy"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tools"
)

// ErrBudgetExceeded is returned when an agent has spent its budget for the
// current window. Interactive callers surface it; background callers suspend.
var ErrBudgetExceeded = errors.New("budget exceeded")

// budgetWindow maps an AgentBudget window to a duration (default 30d/month).
func budgetWindow(b *agentsv1alpha1.AgentBudget) time.Duration {
	if b != nil && b.Window == "day" {
		return 24 * time.Hour
	}
	return 30 * 24 * time.Hour
}

// checkBudget reports an ErrBudgetExceeded (wrapped with detail) when the
// agent's rolling-window usage has reached its token or USD cap. A nil budget,
// or zero limits, never blocks.
func (s *Server) checkBudget(ctx context.Context, scope store.Scope, agent *agentsv1alpha1.Agent, now time.Time) error {
	b := agent.Spec.Budget
	if b == nil || (b.TokenLimit == 0 && strings.TrimSpace(b.USDLimit) == "") {
		return nil
	}
	u, err := s.store.GetUsage(ctx, scope, agent.Name, now, budgetWindow(b))
	if err != nil {
		return nil // fail open on usage-read errors; don't wedge the agent
	}
	if b.TokenLimit > 0 && u.InputTokens+u.OutputTokens >= b.TokenLimit {
		return fmt.Errorf("%w: %d/%d tokens used this %s", ErrBudgetExceeded, u.InputTokens+u.OutputTokens, b.TokenLimit, budgetName(b))
	}
	if usd := strings.TrimSpace(b.USDLimit); usd != "" {
		if lim, perr := strconv.ParseFloat(usd, 64); perr == nil && lim > 0 {
			spent := float64(u.USDMicros) / 1e6
			if spent >= lim {
				return fmt.Errorf("%w: $%.2f/$%.2f used this %s", ErrBudgetExceeded, spent, lim, budgetName(b))
			}
		}
	}
	return nil
}

func budgetName(b *agentsv1alpha1.AgentBudget) string {
	if b != nil && b.Window == "day" {
		return "day"
	}
	return "month"
}

// runResult is the outcome of a non-streaming agent execution. A non-nil
// Pending means the run paused on an approval gate rather than finishing.
type runResult struct {
	RunID   string `json:"runID"`
	Content string `json:"content"`
	// FinalContent is the last model response only, except that a turn stopped at
	// the tool-call limit carries the engine's standalone explanatory notice.
	// Content intentionally keeps the historical concatenated result used by
	// background callers and the run record; the chat projection uses
	// FinalContent so completed commentary is not shown a second time as the
	// answer.
	FinalContent string       `json:"finalContent,omitempty"`
	Pending      *pendingInfo `json:"pending,omitempty"`
	// Phase is the run's terminal phase. It is EMPTY when the turn never
	// started — the setup before it (the budget gate, resolving the model)
	// failed — which tells a caller that holds the run's durable record that
	// nothing was written and it has to close the run itself.
	Phase      store.RunPhase `json:"phase,omitempty"`
	StartedAt  *time.Time     `json:"startedAt,omitempty"`
	FinishedAt *time.Time     `json:"finishedAt,omitempty"`
	// DurationMS is active work accumulated from model response and tool
	// callbacks. Approval/recovery idle time is intentionally excluded.
	DurationMS int64 `json:"durationMS,omitempty"`
	Usage      struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
		USDMicros    int64 `json:"usdMicros"`
	} `json:"usage"`
}

// pendingInfo describes the approval gate a paused run is waiting on.
// pendingInfo is why a run is parked, and there are two reasons that need
// telling apart.
//
// A GATE is a tool call the provider wrapped: it has a tool and arguments, and
// what resolves it is a verdict. A QUESTION is the turn asking a human
// something — what a harness does when it cannot proceed — and it has no tool
// at all; what resolves it is an answer. Reporting the second as the first made
// the portal render "Approval required … tool unavailable" with an Approve
// button that could not mean anything.
type pendingInfo struct {
	InboxID string `json:"inboxID"`
	// Kind is "approval" or "question", matching the inbox item's own kind.
	Kind string `json:"kind"`
	Tool string `json:"tool,omitempty"`
	Args string `json:"args,omitempty"`
	// Question is the text to put to the person, for Kind "question".
	Question string `json:"question,omitempty"`
}

// pendingFor says which of the two a park is. A park with a tool is a gate; one
// without is a question, which is what a harness turn produces.
func pendingFor(inboxID string, parked *backend.Parked) *pendingInfo {
	if parked == nil {
		return nil
	}
	if strings.TrimSpace(parked.Tool) == "" {
		return &pendingInfo{InboxID: inboxID, Kind: string(store.InboxKindQuestion), Question: parked.Question}
	}
	return &pendingInfo{InboxID: inboxID, Kind: string(store.InboxKindApproval), Tool: parked.Tool, Args: parked.Args}
}

// harnessCancelTarget is the non-secret address needed to reach one runner.
// It is persisted beside a parked checkpoint so an Agent edit cannot redirect
// cancellation to a different edge.
type harnessCancelTarget struct {
	ClusterID string `json:"clusterID"`
	EdgeKind  string `json:"edgeKind"`
	EdgeName  string `json:"edgeName"`
	Service   string `json:"service"`
	RunnerID  string `json:"runnerID"`
}

// runCheckpoint is the payload persisted in store.Run.Checkpoint: the resume
// state the backend handed back, plus what the api layer needs to rebuild the
// run around it.
type runCheckpoint struct {
	// Engine is the IN-PROCESS backend's own resume state. It crosses the seam
	// as opaque bytes (backend.Parked.State) and is stored here in the shape the
	// rows have always had.
	Engine engine.Checkpoint `json:"engine"`
	// Harness is the EDGE-HARNESS backend's resume state, and it is a sibling
	// field rather than a re-use of Engine because the two are not the same kind
	// of thing: an engine checkpoint is a snapshot of a conversation this process
	// was holding, while a harness checkpoint is the coordinates of one somebody
	// else is still holding (attempt, session, cursor). Decoding either as the
	// other would silently produce an empty resume, so they do not share a
	// field; which one is set is decided by the run's Backend.
	Harness json.RawMessage `json:"harness,omitempty"`
	// HarnessRunner is the safe, non-secret address of the runner holding a
	// parked harness attempt. It survives edits to the Agent or removal of its
	// ModelCredential, so cancel can still stop the old attempt without storing
	// a URL or bearer token.
	HarnessRunner *harnessCancelTarget `json:"harnessRunner,omitempty"`
	Tool          string               `json:"tool"`
	Args          string               `json:"args"`
	InboxID       string               `json:"inboxID"`
	SourceName    string               `json:"sourceName,omitempty"`
	NotifyChannel string               `json:"notifyChannel,omitempty"`
	// Worker carries a spawned sub-task's constraints (its narrowed families,
	// approval class and tool-turn budget) so a resumed worker is rebuilt as the
	// worker it was rather than as a top-level run of its agent.
	Worker *workerRun `json:"worker,omitempty"`
	// Backend names which of the two resume states above is the live one, so a
	// resume never has to guess from the agent's CURRENT spec — an agent
	// re-pointed from a model to a harness while a run was parked would otherwise
	// resume it with the wrong half.
	Backend string `json:"backend,omitempty"`
	// WorkedDurationMS is the active model/tool time already represented by the
	// engine checkpoint. Keeping it beside the opaque engine payload lets a
	// resumed run continue its timing without counting approval or recovery idle
	// time, and does not require a store schema change.
	WorkedDurationMS int64 `json:"workedDurationMS,omitempty"`
}

// backendState is the resume state to hand back across the seam: whichever of
// the two the checkpoint carries.
//
// Backend is preferred over "whichever field is populated" because an empty
// engine checkpoint is a valid-looking JSON object, so a harness checkpoint
// decoded as one would resume a conversation with no messages instead of failing.
// A checkpoint written before the field existed has no Backend and is an engine
// one, which it always was.
func (c runCheckpoint) backendState() (json.RawMessage, error) {
	if c.Backend == agentsv1alpha1.AgentBackendHarness {
		if len(c.Harness) == 0 {
			return nil, errors.New("the checkpoint says this run is harness-backed but carries no harness resume state")
		}
		return c.Harness, nil
	}
	return json.Marshal(c.Engine)
}

// setBackendState stores the state a backend handed back, in the field that
// backend's resumes read.
func (c *runCheckpoint) setBackendState(backendType string, state json.RawMessage) error {
	c.Backend = backendType
	if backendType == agentsv1alpha1.AgentBackendHarness {
		c.Harness = state
		return nil
	}
	return json.Unmarshal(state, &c.Engine)
}

// harnessAttemptID is the runner attempt a harness-backed run IS, for the run
// record. Empty for a model-backed run, which has no attempt.
func harnessAttemptID(h *harnessTurn, runID string) string {
	if h == nil {
		return ""
	}
	return runID
}

// runAccess is the tenant access one run executes with: who reads its model
// credential and its CRs, and what reach it has beyond them.
//
// It is the ONE thing the ways of starting a run ever differed in — a
// caller-credentialed client on a data-plane verb, the agent's own identity
// through the APIExport virtual workspace for unattended work — so it is a
// parameter rather than a second copy of the starter. A resume takes the same
// thing: continuing a run needs exactly the access running it did.
type runAccess struct {
	// Creds reads the model credential (the ModelCredential and the Secret it
	// points at), CR the tenant's CRs. Both act as the provider.
	Creds llm.CredentialResolver
	CR    tools.CRAccess

	// ClusterID is the tenant workspace's kcp logical-cluster ID, which
	// addresses instance-backed tools on the infrastructure provider's
	// instances/proxy verb through this provider's export virtual workspace.
	ClusterID string

	// HubToken authenticates against the hub: the caller's bearer on the MCP
	// class (the one route that still carries one), the agent's own minted
	// identity on a background run, absent on a data-plane verb.
	HubToken string
	// EdgesEndpoint is the hub mcpserver MCP URL ("" → the edges family is
	// absent, which it is for anything with no caller credential).
	EdgesEndpoint string
	EdgesInsecure bool

	// Notify delivers a finished run's output to the agent channel its
	// schedule/trigger reports to. Nil where there is nothing to deliver to —
	// an API-invoked run answers its caller, not a channel.
	Notify func(ctx context.Context, agent *agentsv1alpha1.Agent, role, prefix, text string)
}

// applyTo stamps the access onto a run about to execute.
func (a runAccess) applyTo(tr *taskRun) {
	tr.Creds, tr.CR = a.Creds, a.CR
	tr.ClusterID, tr.HubToken = a.ClusterID, a.HubToken
	tr.EdgesEndpoint, tr.EdgesInsecure = a.EdgesEndpoint, a.EdgesInsecure
}

// inherited is the access a child run of this one gets: a spawned worker, which
// is the same agent on a sub-task, or a delegated sub-agent.
//
// Edges is deliberately absent. It dials the hub's aggregate MCP endpoint AS THE
// CALLING HUMAN, and a child run is not the human — the same reason
// grantableWorkerFamilies drops the family. Everything else is the parent's, so
// a child can reach what its parent could and nothing more.
func (r taskRun) inherited() runAccess {
	return runAccess{Creds: r.Creds, CR: r.CR, ClusterID: r.ClusterID, HubToken: r.HubToken}
}

// callerAccess is the access a request-driven run executes with: the client the
// request resolved (the gate's provider client on a verb; the
// caller-credentialed one on the MCP class) and, where there is a caller bearer,
// the hub's aggregate MCP endpoint for the edges family.
func (s *Server) callerAccess(ctx context.Context, c *agentsclient.Client, id identity) runAccess {
	return runAccess{
		Creds: c, CR: clientCR{c},
		ClusterID:     id.clusterID,
		HubToken:      id.token,
		EdgesEndpoint: s.aggregateMCPEndpoint(ctx, id),
		EdgesInsecure: s.cfg.HubInsecure,
		Notify: func(ctx context.Context, agent *agentsv1alpha1.Agent, role, prefix, text string) {
			s.deliverToNotifyChannel(ctx, c, agent, role, prefix, text)
		},
	}
}

// taskRun bundles everything one agent execution needs: its access (see
// runAccess), what to run, where the answer goes, and the callbacks a live
// caller wants. ParentRunID links sub-agent (delegation) runs.
type taskRun struct {
	Creds llm.CredentialResolver
	CR    tools.CRAccess
	Scope store.Scope
	Agent *agentsv1alpha1.Agent

	// RunID pre-assigns the run's ID (async run-now, chat's SSE start event,
	// resume). Empty → generated.
	RunID string

	SessionID   string
	Task        string
	Trigger     string
	SourceName  string // schedule/trigger/connection that fired this run
	ParentRunID string

	// IdempotencyKey is the caller's de-duplication token for an API-invoked run.
	// Carried here because the lifecycle rewrites the run record from scratch and
	// would otherwise drop what the pre-write stored.
	IdempotencyKey string

	// NotifyChannel is the agent-channel role a paused/resumed background run
	// delivers to (recorded in the checkpoint for resume delivery).
	NotifyChannel string
	// ReplyTarget pins the exact chat a channel conversation came from, so the
	// answer goes back where the question was asked.
	ReplyTarget string
	// DeliveryKind marks a channel conversation ("channel") so a run that dies
	// can be reported in the chat rather than to the agent's notify channel.
	DeliveryKind string

	// Callback, when set, is POSTed the run's outcome once it finishes. Delivery,
	// not execution — consumed by the detached-start helper. See api/callback.go.
	Callback *runCallback

	// Worker, when non-nil, marks this run as a spawned sub-agent worker and
	// carries the constraints its parent imposed (depth, narrowed families,
	// approval class, tool-turn budget). See api/spawn.go.
	Worker *workerRun

	// ApproveTool/ApproveArgs pre-authorize exactly one tool call — the resume
	// path sets them after the user approved the gated call.
	ApproveTool string
	ApproveArgs string
	approveUsed *bool

	EdgesEndpoint string // hub mcpserver MCP URL ("" → edges family absent)
	// HubToken authenticates the edges MCP dial against the hub. It is the
	// caller's bearer on the MCP class (the one route that still carries one)
	// and the agent's own minted identity on a background run; a data-plane
	// verb has neither, so it is empty there and edges is absent. It plays no
	// part in reaching instance-backed tools any more: those are called as
	// this provider through its export virtual workspace (dataPlaneFor).
	HubToken      string
	EdgesInsecure bool

	// ClusterID is the tenant workspace's kcp logical-cluster ID, which
	// addresses instance-backed tools on the infrastructure provider's
	// instances/proxy verb through this provider's export virtual workspace.
	ClusterID string

	OnDelta     func(string)
	OnToolStart func(id, name, args string)
	OnTool      func(backend.ToolEvent)
	// OnRunStarted runs after the durable Running record is written. It is used
	// by chat SSE to expose the server-owned start timestamp.
	OnRunStarted func(startedAt time.Time)
	// OnAssistantMessage receives complete model responses, after their tool-call
	// status is known. The timestamp is assigned at the persistence boundary.
	OnAssistantMessage func(backend.AssistantMessage, time.Time)
	transcriptWrites   *transcriptWriteState
}

// transcriptWriteState latches the FIRST transcript write failure of a turn. A
// turn whose progress is not being recorded must stop rather than carry on
// against a history nobody can read back, so the sink reports the latched error
// through Aborted and the backend unwinds the way it does for a cancel.
type transcriptWriteState struct {
	mu  sync.Mutex
	err error
}

func (s *transcriptWriteState) record(err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

func (s *transcriptWriteState) failure() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// delivery describes where this run's answer should go, for the run record. A
// worker or a delegated child reports to its parent in memory and has no channel
// of its own, so it records none.
func (r taskRun) delivery() *store.RunDelivery {
	if r.Worker != nil || r.Trigger == agentsv1alpha1.RunTriggerDelegation {
		return nil
	}
	if r.SourceName == "" && r.ReplyTarget == "" && r.NotifyChannel == "" {
		return nil
	}
	return &store.RunDelivery{
		SourceName: r.SourceName, ReplyTarget: r.ReplyTarget,
		NotifyChannel: r.NotifyChannel, Kind: r.DeliveryKind,
	}
}

// continuation is what a resumed turn picks up from: the run's persisted
// checkpoint, when the run started, the work it had already done, and — for an
// approval resume — the user's verdict on the call it parked on.
//
// A recovery resume carries no verdict: its checkpoint holds no pending call
// (see engine.Callbacks.OnCheckpoint), so there is nothing to approve or deny
// and the turn simply carries on.
type continuation struct {
	Checkpoint runCheckpoint
	// Billed is the usage durably recorded at the last approval park. A running
	// checkpoint can include additional consumption that has not been charged.
	Billed    backend.Cost
	StartedAt time.Time
	Tracker   *turnProgressTracker
	Decided   bool
	Approved  bool
	Note      string
}

// turnPurpose picks the model role a turn runs on. A spawned worker resolves the
// cheap "background" purpose (falling back to chat), so a fan-out of ten
// sub-tasks can run on a cheap model while the parent — which does the synthesis
// — keeps the strong one. The trigger is checked as well as the worker marker so
// a resumed worker, whose constraints come back off its checkpoint, lands on the
// same model it started on.
func turnPurpose(run taskRun) string {
	if run.Worker != nil || run.Trigger == agentsv1alpha1.RunTriggerSpawn {
		return llm.PurposeBackground
	}
	return llm.PurposeChat
}

// maxToolTurns bounds a turn's tool-call loop: the agent's own allowance,
// except for a worker, which works one sub-task on the shorter budget its parent
// chose.
func maxToolTurns(agent *agentsv1alpha1.Agent, run taskRun) int {
	if run.Worker != nil {
		return run.Worker.MaxToolTurns
	}
	if v := int(agent.Spec.Limits.MaxToolTurns); v > 0 {
		return min(v, 32)
	}
	return 16
}

// backendCancelTimeout bounds telling a backend to stop a turn. The in-process
// one has nothing to do; a remote one gets a round trip, not forever.
const backendCancelTimeout = 30 * time.Second

// runTurn is THE run lifecycle. Every entry point goes through it — chat,
// run-now, an API invoke, a background fire, a channel message, a delegated
// child, a spawned worker, and a resume, whether from an approval or from a
// recovery — and they differ only in whether they hand it a continuation.
//
// It owns everything the RUN owns: the budget gate, the model and toolset for
// this turn, the durable Running record and the events that announce it, the
// transcript, the checkpoint a parked run resumes from, usage accounting, and
// the terminal phase. What happens INSIDE the turn belongs to the Backend (see
// provider-agents/backend), which is why there is one of these and not one per
// way of starting work: a bug fixed here is fixed for all of them.
func (s *Server) runTurn(ctx context.Context, run taskRun, cont *continuation) (runResult, error) {
	scope, agent := run.Scope, run.Agent
	// A status condition alone cannot enforce unsupported tool restrictions:
	// direct API callers and background triggers may run before reconciliation.
	if agent.Spec.HarnessBacked() {
		if _, message := harnesspolicy.UnsupportedFields(agent); message != "" {
			return runResult{}, errors.New(message)
		}
	}

	// Before every turn, resumed ones included: a run that parked on an approval
	// spends from the same rolling window a fresh one does.
	if err := s.checkBudget(ctx, scope, agent, time.Now().UTC()); err != nil {
		return runResult{}, err
	}

	purpose := turnPurpose(run)

	sessionID := run.SessionID
	if sessionID == "" {
		sessionID = run.Trigger // e.g. schedules share a per-trigger session
	}
	runID := run.RunID
	if runID == "" {
		runID = uuid.NewString()
	}
	run.RunID = runID

	// spec.limits.timeoutSeconds bounds the run's wall clock (default 1h).
	timeout := time.Hour
	if v := agent.Spec.Limits.TimeoutSeconds; v > 0 {
		timeout = time.Duration(v) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// WHICH BACKEND. The one branch in the lifecycle, and it is a branch in the
	// setup rather than in the flow: past this point the turn is a Backend and
	// everything below — the durable record, the transcript, the checkpoint, the
	// usage, the terminal phase — is identical for both.
	var (
		b         backend.Backend
		harnessT  *harnessTurn
		modelName string
		err       error
	)
	if agent.Spec.HarnessBacked() {
		var resolved harnessTurn
		if resolved, err = s.harnessBackendFor(ctx, run, sessionID, runID, cont); err != nil {
			return runResult{}, err
		}
		harnessT, b = &resolved, resolved.backend
		log.Printf("agents: run %s (agent %s) dispatching turn %d to %s on service %s",
			runID, agent.Name, resolved.Session.Turns, resolved.Harness, resolved.Service)
	} else {
		var chatModel einomodel.BaseChatModel
		if chatModel, err = s.buildModelForPurpose(ctx, run.Creds, agent, purpose); err != nil {
			return runResult{}, err
		}
		// Names the model that will actually be called, which is what prices the
		// turn and sizes its context window.
		modelName = s.modelNameForPurpose(ctx, run.Creds, agent, purpose)
		b = backendmodel.New(s.engine, chatModel, modelName, s.contextCompactor(run, sessionID, modelName))
	}
	brun := &backend.Run{ID: runID, SessionID: sessionID, Agent: agent.Name, Trigger: run.Trigger}
	// Register for POST /api/runs/{id}/cancel and for the Run reconciler's
	// deadline. Both reach the run here: cancelling the context ends the turn,
	// and the backend is told as well because one that left work running
	// elsewhere has to go and stop it there.
	//
	// The context cancel is synchronous because it IS the mechanism; telling the
	// backend is not, because whoever asked for the cancel may be the turn's own
	// loop (see cancelCheck) and must not wait on a round trip to the thing it is
	// stopping.
	var stopOnce sync.Once
	stopRemote := func() {
		stopOnce.Do(func() {
			go func() {
				// Bounded, and detached from the context just cancelled: a backend
				// that cannot be reached must not leave a goroutine waiting on it.
				stopCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), backendCancelTimeout)
				defer stop()
				if err := b.Cancel(stopCtx, brun); err != nil {
					log.Printf("run %s: stopping the turn: %v", runID, err)
				}
			}()
		})
	}
	// A run context can end because its caller cancelled it or because the
	// agent's timeout expired. Those paths must stop work on the runner too.
	// Stop the callback before the lifecycle's own deferred cancel so a
	// successful park does not cancel its deliberately waiting permission.
	stopOnContextDone := context.AfterFunc(ctx, stopRemote)
	s.liveRuns.register(runID, func() {
		cancel()
		stopRemote()
	})
	defer s.liveRuns.unregister(runID)
	defer func() {
		// Unregister the callback first, then check the context: if cancellation
		// raced the unregister, this catches it; if the callback already began,
		// stopRemote's once guard deduplicates the call.
		stopOnContextDone()
		if ctx.Err() != nil {
			stopRemote()
		}
		cancel()
	}()
	// Approval resume and cancel can cross between ClaimRun and this local
	// registration. A cancel that saw no live entry has already closed the
	// durable run; observe its flag before dispatching another model or harness
	// request so that the late resume cannot restart the work.
	if stored, err := s.store.GetRun(ctx, scope, runID); err == nil && stored.CancelRequested {
		cancel()
	}
	if err := ctx.Err(); err != nil {
		// A cancellation landed after a resume claimed the row but before it
		// registered locally. The caller already recorded the terminal phase;
		// return before Continue can dispatch another request to the runner.
		return runResult{RunID: runID, Phase: store.RunPhaseAborted}, err
	}

	// Assemble the agent's tools for this trigger class (policy + approvals +
	// audit + delegation); MCP sessions are released when the run ends.
	//
	// Not for a harness-backed agent. The harness owns its tools (see
	// backend/harness), so the toolset would be assembled, MCP servers dialled
	// and approval gates wrapped for a turn that ignores every one of them — and
	// the Agent reconciler already reports spec.tools on such an agent as
	// meaningless rather than letting it read as granted.
	var (
		toolset         []backend.Tool
		mcpInstructions string
		closeTools      = func() {}
	)
	if !agent.Spec.HarnessBacked() {
		toolset, mcpInstructions, closeTools = s.buildToolset(ctx, tools.Deps{
			Store: s.store, Scope: scope, Agent: agent, CR: run.CR,
			Secrets: run.Creds, ConnSecretName: connectionSecretName,
			RunID:     runID,
			DataPlane: s.dataPlaneFor(run),
		}, run)
	}
	defer closeTools()

	limits := backend.Limits{
		MaxToolTurns:        maxToolTurns(agent, run),
		ContextBudgetTokens: turnContextBudget(modelName),
		CheckpointEvery:     checkpointEveryIterations,
	}

	// The two heads: a fresh turn writes the durable Running record and assembles
	// a conversation; a resumed one continues from state that already exists.
	var (
		startedAt time.Time
		tracker   *turnProgressTracker
		in        backend.Input
		answer    backend.Answer
	)
	if cont == nil {
		// Assemble the turn before persisting the task message — LoadRecentMessages
		// has no notion of "current run", so appending first would replay the task
		// into history and the model would see it twice.
		// Derived from the toolset that was actually built, not from the grant: a
		// depth-limited worker has no spawn tool and must not be told to fan out.
		msgs, aerr := s.assembleTurnCtx(ctx, run, sessionID, mcpInstructions, hasToolNamed(toolset, "spawn"))
		if aerr != nil {
			return s.failBeforeStart(ctx, scope, run, sessionID, time.Time{}, fmt.Errorf("assemble agent history: %w", aerr))
		}

		// A detached starter may have created a Pending record while this run was
		// queued. Start the active turn clock only when the durable Running record
		// is written; queue/model/toolset setup is not worked time.
		startedAt = time.Now().UTC()
		taskMessageID := uuid.NewString()
		if aerr := s.store.AppendMessage(ctx, scope, store.Message{
			ID: taskMessageID, AgentName: agent.Name, SessionID: sessionID, RunID: runID,
			Role: "user", Content: run.Task, CreatedAt: startedAt,
		}); aerr != nil {
			return s.failBeforeStart(ctx, scope, run, sessionID, startedAt, fmt.Errorf("persist agent task message: %w", aerr))
		}
		// The active history already contains this request as its final user row.
		// Attach the durable ID so a compaction checkpoint can prove which stored
		// message that anchor represents, even though the append API assigns
		// sequence numbers internally and does not return the updated row.
		msgs[len(msgs)-1].ID = taskMessageID
		running := store.Run{
			ID: runID, AgentName: agent.Name, SessionID: sessionID, Trigger: run.Trigger,
			ParentRunID: run.ParentRunID, IdempotencyKey: run.IdempotencyKey,
			Delivery: run.delivery(),
			// Where this run executed, recorded up front so a resume or a
			// recovery reads it off the row rather than off an agent spec that
			// may have been re-pointed since.
			Backend: agent.Spec.BackendType(), AttemptID: harnessAttemptID(harnessT, runID),
			Phase: store.RunPhaseRunning, Input: run.Task, CreatedAt: startedAt, UpdatedAt: startedAt, StartedAt: &startedAt,
		}
		if agent.Spec.HarnessBacked() {
			checkpoint, err := initialHarnessCheckpoint(run, harnessT)
			if err != nil {
				return s.failBeforeStart(ctx, scope, run, sessionID, startedAt, fmt.Errorf("preparing the harness cancellation checkpoint: %w", err))
			}
			running.Checkpoint = checkpoint
		}
		if err := s.saveRun(ctx, scope, running); err != nil {
			// The runner is an external process. Do not dispatch it unless its
			// attempt coordinates and safe cancel target are already durable.
			return s.failBeforeStart(ctx, scope, run, sessionID, startedAt, fmt.Errorf("persisting the running record before dispatch: %w", err))
		}
		if run.OnRunStarted != nil {
			run.OnRunStarted(startedAt)
		}
		s.publishRunEvent(scope, runEvent{ID: runID, Agent: agent.Name, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: store.RunPhaseRunning})
		tracker = newTurnProgressTracker(0)
		in = backend.Input{Messages: msgs, Tools: toolset, Limits: limits}
	} else {
		startedAt, tracker = cont.StartedAt, cont.Tracker
		state, merr := cont.Checkpoint.backendState()
		if merr != nil {
			return runResult{}, fmt.Errorf("reading the run's checkpoint: %w", merr)
		}
		answer = backend.Answer{
			State: state, Decided: cont.Decided, Approved: cont.Approved, Note: cont.Note,
			Tools: toolset, Limits: limits,
		}
	}

	sink := s.turnSink(ctx, run, sessionID, startedAt, agent.Spec.BackendType(), tracker)
	var out backend.Outcome
	if cont == nil {
		out, err = b.Turn(ctx, brun, in, sink)
	} else {
		out, err = b.Continue(ctx, brun, answer, sink)
	}
	end := time.Now().UTC()
	if harnessT != nil {
		// The next chat reads the session row, not this run's display metadata.
		// Save the receipt's session even on a park or cancellation so a later
		// turn can resume the actual Codex thread instead of replaying history
		// into a new one. A failed write must not be reported as success.
		if sessionErr := s.persistHarnessTurn(ctx, scope, harnessT, brun, out, end); sessionErr != nil {
			err = errors.Join(err, sessionErr)
		}
	}
	// A backend can return useful usage together with an error (for example, a
	// runner event stream can fail after reporting token usage). Bill that work
	// and retain the run total on every terminal path, including cancellation.
	if harnessT != nil && cont != nil {
		// A continuation can fail before the backend restores its snapshot
		// (for example, an Inspect failure), and then report no usage at all.
		// The parked run's stored totals are still authoritative in that case.
		out.Usage.Total.InputTokens = max(out.Usage.Total.InputTokens, cont.Billed.InputTokens)
		out.Usage.Total.OutputTokens = max(out.Usage.Total.OutputTokens, cont.Billed.OutputTokens)
		out.Usage.Total.CostMicros = max(out.Usage.Total.CostMicros, cont.Billed.CostMicros)
		out.Usage.Billed = unbilledHarnessUsage(out.Usage.Total, cont.Billed)
	}
	usageCtx, cancelUsage := boundedPersistContext(ctx)
	window, usageErr := s.store.AddUsage(usageCtx, scope, agent.Name,
		out.Usage.Billed.InputTokens, out.Usage.Billed.OutputTokens, out.Usage.Billed.CostMicros, end, budgetWindow(agent.Spec.Budget))
	cancelUsage()
	if usageErr != nil {
		log.Printf("run %s: recording usage: %v", runID, usageErr)
	}

	finishFailedTurn := func(cause error) (runResult, error) {
		phase := store.RunPhaseFailed
		if out.Status == backend.StatusCancelled {
			phase = store.RunPhaseAborted
		}
		persistCtx, cancelPersist := boundedPersistContext(ctx)
		defer cancelPersist()
		s.appendTurnTerminal(persistCtx, scope, run, sessionID, startedAt, end, tracker, turnStatusForRunPhase(phase), tracker.partialText(), cause.Error())
		s.finishRun(persistCtx, scope, runID, runOutcome{
			Phase: phase, Message: cause.Error(), Usage: out.Usage.Total.Tokens,
			CostMicros: out.Usage.Total.CostMicros, WorkedDurationMS: tracker.workedDurationMS(), Harness: harnessT,
		}, end)
		s.recordAgentRun(persistCtx, run.CR, agent, end, &window)
		s.publishRunEvent(scope, runEvent{ID: runID, Agent: agent.Name, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: phase})
		res := runResult{RunID: runID, Content: tracker.partialText(), Phase: phase,
			StartedAt: &startedAt, FinishedAt: &end, DurationMS: tracker.durationMS()}
		res.Usage.InputTokens = out.Usage.Total.InputTokens
		res.Usage.OutputTokens = out.Usage.Total.OutputTokens
		res.Usage.USDMicros = out.Usage.Total.CostMicros
		return res, cause
	}

	// A model the upstream refuses on Chat Completions (a responses-only
	// family, a retired snapshot) is a credential to edit, not a crash. The
	// provider's own sentence is kept; what is added is which credential owns
	// the model id and where to change it.
	if err = llm.ExplainChatCompletionsRefusal(err, credentialNameForPurpose(agent, purpose)); err != nil {
		return finishFailedTurn(err)
	}

	// Parked on an approval gate, or on a question the turn itself asked: persist
	// the checkpoint and stop here — resolving the inbox item continues the run in
	// place.
	if out.Parked != nil {
		res, parkErr := s.parkRun(ctx, run, sessionID, startedAt, end, tracker, harnessT, out)
		if parkErr != nil {
			return finishFailedTurn(parkErr)
		}
		return res, nil
	}

	finalContent := tracker.finalText(out.Final)
	// A run whose answer could not be written down did not succeed. Reporting it
	// as succeeded would leave a terminal phase pointing at a transcript that
	// never received the reply.
	if werr := s.appendTurnFinal(ctx, scope, run, sessionID, startedAt, end, tracker, finalContent); werr != nil {
		writeErr := fmt.Errorf("persist final assistant message: %w", werr)
		persistCtx, cancelPersist := boundedPersistContext(ctx)
		defer cancelPersist()
		s.appendTurnTerminal(persistCtx, scope, run, sessionID, startedAt, end, tracker, turnStatusForRunPhase(store.RunPhaseFailed), "", writeErr.Error())
		s.finishRun(persistCtx, scope, runID, runOutcome{
			Phase: store.RunPhaseFailed, Message: writeErr.Error(),
			Usage: out.Usage.Total.Tokens, CostMicros: out.Usage.Total.CostMicros,
			WorkedDurationMS: tracker.workedDurationMS(), Harness: harnessT,
		}, end)
		s.publishRunEvent(scope, runEvent{ID: runID, Agent: agent.Name, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: store.RunPhaseFailed})
		return runResult{RunID: runID, Content: out.Text, FinalContent: finalContent, Phase: store.RunPhaseFailed,
			StartedAt: &startedAt, FinishedAt: &end, DurationMS: tracker.durationMS()}, writeErr
	}
	// The answer goes on the run record too, so a programmatic reader (the parent
	// of a spawned worker, GET /api/runs/{id}) finds the result where it found the
	// phase instead of having to locate the session and dig out its last message.
	persistCtx, cancelPersist := boundedPersistContext(ctx)
	s.finishRun(persistCtx, scope, runID, runOutcome{
		Phase: store.RunPhaseSucceeded, Usage: out.Usage.Total.Tokens, CostMicros: out.Usage.Total.CostMicros,
		Output: out.Output, Sources: out.Sources, WorkedDurationMS: tracker.workedDurationMS(),
		Harness: harnessT,
	}, end)
	s.recordAgentRun(persistCtx, run.CR, agent, end, &window)
	cancelPersist()
	s.publishRunEvent(scope, runEvent{ID: runID, Agent: agent.Name, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: store.RunPhaseSucceeded})

	res := runResult{RunID: runID, Content: out.Text, FinalContent: finalContent,
		Phase: store.RunPhaseSucceeded, StartedAt: &startedAt, FinishedAt: &end, DurationMS: tracker.durationMS()}
	res.Usage.InputTokens = out.Usage.Total.InputTokens
	res.Usage.OutputTokens = out.Usage.Total.OutputTokens
	res.Usage.USDMicros = out.Usage.Total.CostMicros
	return res, nil
}

// parkRun records a turn that stopped for a human: the checkpoint it resumes
// from, the waiting phase on the run and in the transcript, and the pending
// details a caller shows the user.
func (s *Server) parkRun(ctx context.Context, run taskRun, sessionID string, startedAt, end time.Time, tracker *turnProgressTracker, harnessT *harnessTurn, out backend.Outcome) (runResult, error) {
	scope, runID := run.Scope, run.RunID
	if out.Parked == nil {
		return runResult{}, errors.New("cannot park a run without backend resume state")
	}
	requestID := out.Parked.RequestID
	ck := runCheckpoint{
		Tool: out.Parked.Tool, Args: out.Parked.Args,
		SourceName: run.SourceName, NotifyChannel: run.NotifyChannel, Worker: run.Worker,
		WorkedDurationMS: tracker.durationMS(),
	}
	if harnessT != nil {
		if cfg := run.Agent.Spec.Harness(); cfg != nil {
			ck.HarnessRunner = &harnessCancelTarget{
				ClusterID: run.ClusterID, EdgeKind: cfg.EdgeRef.Kind,
				EdgeName: cfg.EdgeRef.Name, Service: harnessT.Service, RunnerID: harnessT.Service,
			}
		}
	}
	// The backend's resume state is opaque across the seam; the api layer only
	// stores it, in the field this run's backend resumes from. Do this before
	// making a new inbox item visible so malformed state cannot strand a gate.
	if err := ck.setBackendState(run.Agent.Spec.BackendType(), out.Parked.State); err != nil {
		return runResult{}, s.failPark(ctx, run, sessionID, harnessT, requestID, err)
	}
	if requestID == "" {
		// A park with no request behind it came from the turn itself rather
		// than from a gate the provider wrapped, so there is no inbox item yet
		// and filing one is this layer's job — reaching the store is the
		// provider's side of the seam. Without it the run would be parked on
		// something nobody can see, let alone answer.
		//
		// Which KIND to file is decided the same way pendingFor decides what to
		// show: a park that names a tool is a call waiting on a verdict and
		// files an approval; one that does not is a question and files a
		// question. A harness produces both — its own request-user-input, and
		// now its permission prompts.
		var err error
		if strings.TrimSpace(out.Parked.Tool) != "" {
			requestID, err = s.postHarnessApproval(ctx, run, out.Parked.Tool, out.Parked.Args)
		} else {
			requestID, err = s.postHarnessQuestion(ctx, run, out.Parked.Question)
		}
		if err != nil {
			return runResult{}, s.failPark(ctx, run, sessionID, harnessT, "", err)
		}
	}
	ck.InboxID = requestID
	ckJSON, err := json.Marshal(ck)
	if err != nil {
		cause := fmt.Errorf("encoding the run's resume checkpoint: %w", err)
		return runResult{}, s.failPark(ctx, run, sessionID, harnessT, requestID, cause)
	}
	persistCtx, cancelPersist := boundedPersistContext(ctx)
	defer cancelPersist()
	stored, err := s.store.GetRun(persistCtx, scope, runID)
	if err != nil {
		cause := fmt.Errorf("reading run %s before saving its approval checkpoint: %w", runID, err)
		return runResult{}, s.failPark(ctx, run, sessionID, harnessT, requestID, cause)
	}
	stored.Phase = store.RunPhasePendingApproval
	stored.Checkpoint = ckJSON
	applyHarnessObservation(&stored, harnessT)
	stored.InputTokens = out.Usage.Total.InputTokens
	stored.OutputTokens = out.Usage.Total.OutputTokens
	stored.USDMicros = out.Usage.Total.CostMicros
	stored.WorkedDurationMS = tracker.workedDurationMS()
	stored.UpdatedAt = end
	if err := s.saveRun(persistCtx, scope, stored); err != nil {
		cause := fmt.Errorf("saving run %s's approval checkpoint: %w", runID, err)
		return runResult{}, s.failPark(ctx, run, sessionID, harnessT, requestID, cause)
	}
	s.appendTurnTerminal(persistCtx, scope, run, sessionID, startedAt, end, tracker, turnStatusForRunPhase(store.RunPhasePendingApproval), "", "")
	s.publishRunEvent(scope, runEvent{ID: runID, Agent: run.Agent.Name, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: store.RunPhasePendingApproval})

	res := runResult{RunID: runID, Content: out.Text, Phase: store.RunPhasePendingApproval,
		StartedAt: &startedAt, FinishedAt: &end, DurationMS: tracker.durationMS(),
		Pending: pendingFor(requestID, out.Parked)}
	res.Usage.InputTokens = out.Usage.Total.InputTokens
	res.Usage.OutputTokens = out.Usage.Total.OutputTokens
	res.Usage.USDMicros = out.Usage.Total.CostMicros
	return res, nil
}

// failPark closes the remote attempt and any inbox item created for a
// checkpoint that could not be made durable. Returning this error lets the
// lifecycle mark the run terminal instead of claiming it is waiting for an
// approval nobody can safely resume.
func (s *Server) failPark(ctx context.Context, run taskRun, sessionID string, harnessT *harnessTurn, requestID string, cause error) error {
	cleanupCtx, cancel := boundedPersistContext(ctx)
	defer cancel()
	if requestID != "" {
		wsScope := store.Scope{OrgUUID: run.Scope.OrgUUID, WorkspaceUUID: run.Scope.WorkspaceUUID}
		item, err := s.store.GetInboxItem(cleanupCtx, wsScope, requestID)
		if err != nil {
			cause = errors.Join(cause, fmt.Errorf("checking inbox item %s after park failure: %w", requestID, err))
		} else if item.RunID == run.RunID && item.AgentName == run.Agent.Name && item.State == store.InboxStatePending {
			state, response := store.InboxStateDenied, "The run could not save its checkpoint and was stopped."
			if item.Kind == store.InboxKindQuestion {
				state = store.InboxStateAnswered
			}
			if _, err := s.store.ResolveInboxItem(cleanupCtx, wsScope, requestID, state, response, time.Now().UTC()); err != nil {
				cause = errors.Join(cause, fmt.Errorf("closing inbox item %s after park failure: %w", requestID, err))
			} else {
				s.events.publish(wsScope, "inbox", map[string]any{
					"id": item.ID, "state": string(state), "agent": item.AgentName, "runID": item.RunID,
				})
			}
		}
	}
	if err := stopHarnessTurn(cleanupCtx, harnessT, &backend.Run{ID: run.RunID, SessionID: sessionID, Agent: run.Agent.Name, Trigger: run.Trigger}); err != nil {
		cause = errors.Join(cause, fmt.Errorf("stopping the harness after the run could not be parked: %w", err))
	}
	return cause
}

// executeTask runs a fresh turn: the lifecycle with nothing to continue from.
// It is the shared execution path for chat, run-now, background fires, channel
// messages, spawned workers and delegation. A gated tool call pauses the run
// (phase PendingApproval, checkpoint saved) instead of finishing; resolving the
// approval resumes it.
func (s *Server) executeTask(ctx context.Context, run taskRun) (runResult, error) {
	if run.RunID == "" {
		run.RunID = uuid.NewString()
	}
	res, err := s.runTurn(ctx, run, nil)
	if err != nil && res.Phase == "" {
		// Every entry point needs a durable refusal, including controller-owned
		// schedules and chats that announced their run ID before setup began.
		// Keeping this at the execution boundary prevents a claimed Pending run
		// from being stranded when no model or runner was ever dispatched.
		sessionID := run.SessionID
		if sessionID == "" {
			sessionID = run.Trigger
		}
		return s.failBeforeStart(ctx, run.Scope, run, sessionID, time.Time{}, err)
	}
	return res, err
}

// startDetachedRun starts a run-now for the request that asked for it: detached
// from the request context, executing with the caller's own access (the gate's
// provider client on a verb; the caller-credentialed one on the MCP class).
func (s *Server) startDetachedRun(r *http.Request, c *agentsclient.Client, id identity, agent *agentsv1alpha1.Agent, tr taskRun) (runAdmission, error) {
	// Detach from the request context: the response returns immediately while
	// the run continues (the lifecycle applies the agent's own timeout).
	return s.startRun(context.WithoutCancel(r.Context()), id.scope(agent.Name), agent, tr, s.callerAccess(r.Context(), c, id))
}

// startRun pre-creates a Pending run record and executes the task in a detached
// goroutine, so run-now endpoints return 202 with a runID immediately instead of
// blocking the HTTP request on the whole agent loop. Output is delivered to the
// run's notify channel when it finishes, like a real background fire.
//
// This is the one starter. What used to be four — a caller-credentialed one, a
// virtual-workspace one, and a hand-built taskRun in each of the background and
// spawn paths — differed only in the access the run executes with, so that is a
// parameter now. ctx must already be detached from whatever asked.
type runAdmission struct {
	ID     string
	Phase  store.RunPhase
	Reused bool
}

func (s *Server) startRun(ctx context.Context, scope store.Scope, agent *agentsv1alpha1.Agent, tr taskRun, access runAccess) (runAdmission, error) {
	runID := uuid.NewString()
	now := time.Now().UTC()
	tr.RunID = runID
	tr.Scope = scope
	tr.Agent = agent
	access.applyTo(&tr)

	if err := s.saveRun(ctx, scope, store.Run{
		ID: runID, AgentName: agent.Name, SessionID: tr.SessionID, Trigger: tr.Trigger,
		IdempotencyKey: tr.IdempotencyKey,
		Phase:          store.RunPhasePending, Input: tr.Task, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		// The unique store key is the admission boundary. Concurrent requests
		// may both miss the earlier lookup; only the one that records a run may
		// execute. A loser returns the already recorded run, never a phantom ID.
		if tr.IdempotencyKey != "" {
			if existing, found, lookupErr := s.store.FindRunByIdempotencyKey(ctx, scope, tr.IdempotencyKey); lookupErr == nil && found {
				return runAdmission{ID: existing.ID, Phase: existing.Phase, Reused: true}, nil
			}
		}
		return runAdmission{}, fmt.Errorf("persisting run before execution: %w", err)
	}
	go func() {
		res, err := s.executeTask(ctx, tr)
		if err != nil {
			log.Printf("agents: run %s on agent %s failed: %v", runID, agent.Name, err)
		}
		if err != nil || res.Pending != nil {
			// The outcome is on the run record (approvals resume separately), but a
			// caller that asked to be told must hear about a failure or a pause too —
			// otherwise it waits forever for a callback that never comes.
			s.deliverRunCallback(ctx, scope, runID, tr.Callback)
			return
		}
		// An API-invoked run answers its caller, which polls, waits, or named a
		// callback. Pushing to the agent's channel as well would message the user
		// about work they did not ask to hear about; run-now for a schedule/trigger
		// is the opposite — its whole point is the channel delivery.
		if tr.Trigger == agentsv1alpha1.RunTriggerAPI || access.Notify == nil {
			s.deliverRunCallback(ctx, scope, runID, tr.Callback)
			return
		}
		access.Notify(ctx, agent, tr.NotifyChannel, tr.SourceName, res.Content)
	}()
	return runAdmission{ID: runID, Phase: store.RunPhasePending}, nil
}

// dataPlaneFor describes how instance-backed tools reach tenant workloads for
// this run: the infrastructure provider's instances/proxy verb, claimed on this
// provider's own export and called through its virtual workspace AS THIS
// PROVIDER. The identity of whoever started the run does not enter into it —
// an interactive run and an unattended one reach an instance the same way. A
// provider with no provider-scoped config is unusable by design; the tool
// reports that precisely rather than failing two hops away.
func (s *Server) dataPlaneFor(run taskRun) tools.DataPlane {
	return tools.DataPlane{ClusterID: run.ClusterID, Callers: s.verbCallers}
}

func (s *Server) failBeforeStart(ctx context.Context, scope store.Scope, run taskRun, sessionID string, at time.Time, cause error) (runResult, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	persistCtx, cancel := boundedPersistContext(ctx)
	defer cancel()
	if existing, err := s.store.GetRun(persistCtx, scope, run.RunID); err == nil {
		// A detached/background starter may have recorded Pending before this
		// worker tried to load history. Settle that same record instead of
		// leaving a run that can only look stuck after a read failure.
		existing.Phase = store.RunPhaseFailed
		existing.Message = cause.Error()
		existing.Checkpoint = nil
		existing.UpdatedAt = at
		existing.FinishedAt = &at
		if err := s.saveRun(persistCtx, scope, existing); err != nil {
			cause = errors.Join(cause, fmt.Errorf("persisting failed run: %w", err))
		}
	} else {
		record := store.Run{
			ID: run.RunID, AgentName: scope.AgentName, SessionID: sessionID,
			Trigger: run.Trigger, ParentRunID: run.ParentRunID,
			IdempotencyKey: run.IdempotencyKey, Delivery: run.delivery(),
			Phase: store.RunPhaseFailed, Input: run.Task, Message: cause.Error(),
			CreatedAt: at, UpdatedAt: at, FinishedAt: &at,
		}
		if err := s.saveRun(persistCtx, scope, record); err != nil {
			cause = errors.Join(cause, fmt.Errorf("persisting failed run: %w", err))
		}
	}
	s.publishRunEvent(scope, runEvent{ID: run.RunID, Agent: scope.AgentName, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: store.RunPhaseFailed})
	return runResult{RunID: run.RunID, Phase: store.RunPhaseFailed, FinishedAt: &at}, cause
}

// turnSink is the provider's side of a turn in flight. It records progress in
// the transcript, accumulates active work, persists recovery checkpoints,
// answers the durable cancel flag, and forwards everything a live caller (chat's
// SSE stream) asked to see.
//
// Complete model responses that led to a tool call are persisted immediately as
// commentary; the final response is persisted by the lifecycle after the run
// reaches a terminal phase, so it can carry authoritative timing.
type turnSink struct {
	s           *Server
	ctx         context.Context
	run         taskRun
	sessionID   string
	startedAt   time.Time
	tracker     *turnProgressTracker
	backendType string
	// record persists a mid-turn recovery checkpoint; abort reads the durable
	// cancel flag. Both are built once so their throttling and closures live for
	// the whole turn.
	//
	// record takes the backend's state VERBATIM. It used to take an
	// engine.Checkpoint, which only one backend has; the envelope it is stored in
	// knows which field the bytes belong in (runCheckpoint.setBackendState).
	record func(json.RawMessage)
	abort  func(context.Context) error
}

var _ backend.EventSink = (*turnSink)(nil)

func (s *Server) turnSink(ctx context.Context, run taskRun, sessionID string, startedAt time.Time, backendType string, tracker *turnProgressTracker) *turnSink {
	if tracker == nil {
		tracker = newTurnProgressTracker(0)
	}
	if run.transcriptWrites == nil {
		run.transcriptWrites = &transcriptWriteState{}
	}
	return &turnSink{
		s: s, ctx: ctx, run: run, sessionID: sessionID, startedAt: startedAt, tracker: tracker,
		backendType: backendType,
		// Periodic checkpoints make a long run recoverable: if this replica dies,
		// the Run reconciler resumes from the last one instead of losing the work.
		// A resumed run keeps checkpointing too, so a replica that dies again picks
		// up from where the resume got to rather than from the original snapshot.
		record: s.checkpointRecorder(ctx, run, sessionID, backendType, func() int64 { return tracker.durationMS() }),
		abort:  s.cancelCheck(run.Scope, run.RunID),
	}
}

func (k *turnSink) Delta(text string) {
	k.tracker.delta(text)
	if k.run.OnDelta != nil {
		k.run.OnDelta(text)
	}
}

func (k *turnSink) Assistant(message backend.AssistantMessage) {
	k.tracker.assistant(message)
	at := time.Now().UTC()
	persisted := true
	if message.Complete && (message.HasToolCalls || len(message.ToolCalls) > 0) {
		metadata := turnMetadata("commentary", "running", k.startedAt, 0, message.Duration.Milliseconds(), "")
		metadata[historyToolCallsKey] = storedToolCalls(message.ToolCalls)
		metadata["modelOnly"] = strings.TrimSpace(message.Content) == ""
		if err := k.s.appendProgressMessage(k.ctx, k.run.Scope, store.Message{
			ID: uuid.NewString(), AgentName: k.run.Agent.Name, SessionID: k.sessionID, RunID: k.run.RunID,
			Role: "assistant", Content: message.Content,
			Metadata:  metadata,
			CreatedAt: at,
		}); err != nil {
			k.run.transcriptWrites.record(fmt.Errorf("persist assistant tool-call group: %w", err))
			persisted = false
		}
	}
	if persisted && k.run.OnAssistantMessage != nil {
		k.run.OnAssistantMessage(message, at)
	}
}

func (k *turnSink) ToolStart(id, name, args string) {
	if k.run.OnToolStart != nil {
		k.run.OnToolStart(id, name, args)
	}
}

func (k *turnSink) ToolEnd(ev backend.ToolEvent) {
	k.tracker.tool(ev)
	// Model tools are audited by wrapTool. Harness tools execute on the Edge,
	// so their completion events are the provider's audit boundary.
	if k.backendType == agentsv1alpha1.AgentBackendHarness {
		outcome, errorText := "ok", ""
		if ev.Err {
			outcome, errorText = "error", safeTruncate(ev.Result, 4000)
		}
		persistCtx, cancel := boundedPersistContext(k.ctx)
		err := k.s.store.AppendToolCall(persistCtx, k.run.Scope, store.ToolCall{
			ID: uuid.NewString(), AgentName: k.run.Agent.Name, RunID: k.run.RunID, Trigger: k.run.Trigger,
			Tool: ev.Name, Args: redactArgs(ev.Args), Result: safeTruncate(ev.Result, maxStoredResult),
			Outcome: outcome, Error: errorText, DurationMS: ev.Duration.Milliseconds(), CreatedAt: time.Now().UTC(),
		})
		cancel()
		if err != nil {
			k.run.transcriptWrites.record(fmt.Errorf("persist harness tool audit %q: %w", ev.ID, err))
		}
	}
	if err := k.s.appendProgressMessage(k.ctx, k.run.Scope, store.Message{
		ID: uuid.NewString(), AgentName: k.run.Agent.Name, SessionID: k.sessionID, RunID: k.run.RunID,
		Role: "tool", Content: ev.Result,
		Metadata: map[string]any{
			"tool": ev.Name, historyToolNameKey: ev.Name,
			historyToolCallIDKey: ev.ID, "args": redactHistoryArgs(ev.Args),
			"error": ev.Err, "durationMS": ev.Duration.Milliseconds(),
		},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		k.run.transcriptWrites.record(fmt.Errorf("persist tool result %q: %w", ev.ID, err))
		return
	}
	if k.run.OnTool != nil {
		k.run.OnTool(ev)
	}
}

func (k *turnSink) Checkpoint(state json.RawMessage) { k.record(state) }

func (k *turnSink) Aborted(ctx context.Context) error {
	if err := k.run.transcriptWrites.failure(); err != nil {
		return err
	}
	return k.abort(ctx)
}

// runOutcome is what a run ended with. Message carries the failure reason (or
// "" on success); Output/Sources carry the answer, so a caller reading the run
// record gets the result and not just the phase.
type runOutcome struct {
	Phase            store.RunPhase
	Message          string
	Usage            backend.Tokens
	CostMicros       int64
	Output           string
	Sources          []string
	WorkedDurationMS *int64
	// Harness, when set, is the harness turn that just ran, so the run record
	// keeps the attempt it was and the harness session the NEXT turn has to chain
	// onto. Nil for a model-backed run.
	Harness *harnessTurn
}

// finishRun stamps a run's terminal phase, result, usage, and timestamps, and
// clears any checkpoint.
func (s *Server) finishRun(ctx context.Context, scope store.Scope, runID string, out runOutcome, end time.Time) {
	stored, err := s.store.GetRun(ctx, scope, runID)
	if err != nil {
		return
	}
	stored.Phase = out.Phase
	stored.Message = out.Message
	stored.Checkpoint = nil
	if out.Output != "" {
		stored.Output = safeTruncate(out.Output, maxStoredOutput)
	}
	if len(out.Sources) > 0 {
		stored.Sources = out.Sources
	}
	if out.Usage.InputTokens > 0 || out.Usage.OutputTokens > 0 {
		stored.InputTokens = out.Usage.InputTokens
		stored.OutputTokens = out.Usage.OutputTokens
	}
	if out.CostMicros > 0 {
		stored.USDMicros = out.CostMicros
	}
	if out.WorkedDurationMS != nil {
		value := *out.WorkedDurationMS
		if value < 0 {
			value = 0
		}
		stored.WorkedDurationMS = &value
	}
	applyHarnessObservation(&stored, out.Harness)
	stored.UpdatedAt = end
	stored.FinishedAt = &end
	_ = s.saveRun(ctx, scope, stored)
}

// applyHarnessObservation records what a harness turn learned: which attempt ran
// and, authoritatively off the receipt, which harness session it ran in.
//
// This is the run's presentation metadata. persistHarnessSession separately
// writes the session row used to dispatch the next turn.
func applyHarnessObservation(run *store.Run, h *harnessTurn) {
	if h == nil || run == nil {
		return
	}
	observed := h.backend.Observed()
	if observed.AttemptID != "" {
		run.AttemptID = observed.AttemptID
	}
	if observed.SessionID != "" {
		run.HarnessSessionID = observed.SessionID
	}
}

// runEvent is one run lifecycle change pushed to /api/events subscribers. A
// struct rather than positional arguments: id, agent, trigger and parentRunID
// are all strings, and transposing two of them would be invisible at the call
// site and wrong on the wire.
type runEvent struct {
	ID      string
	Agent   string
	Trigger string
	// ParentRunID lets a client watching a run recognize a child it has not seen
	// before. Without it the run-detail view could only refresh for children it
	// already knew about, so a worker spawned after the page opened stayed
	// invisible until a manual reload.
	ParentRunID string
	Phase       store.RunPhase
}

// publishRunEvent pushes a run lifecycle change to /api/events subscribers.
func (s *Server) publishRunEvent(scope store.Scope, ev runEvent) {
	payload := map[string]any{
		"id": ev.ID, "agent": ev.Agent, "trigger": ev.Trigger, "phase": string(ev.Phase),
	}
	if ev.ParentRunID != "" {
		payload["parentRunID"] = ev.ParentRunID
	}
	s.events.publish(store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID}, "run", payload)
}

// memoryNoteClip bounds one injected note's body, and memoryNoteLimit how many
// notes are injected (spec.memory.maxNotes, capped). Shared with the compaction
// estimator so it measures the same injection this assembles.
const memoryNoteClip = 500

func memoryNoteLimit(agent *agentsv1alpha1.Agent) int {
	if v := int(agent.Spec.Memory.MaxNotes); v > 0 {
		return min(v, 100)
	}
	return 20
}

// assembleTurnCtx builds the message list (system prompt + recent history +
// task) using a context rather than an *http.Request, so background callers
// (scheduler) can reuse it.
// hasToolNamed reports whether the assembled toolset contains a tool.
func hasToolNamed(ts []backend.Tool, name string) bool {
	for _, t := range ts {
		if t.Name == name {
			return true
		}
	}
	return false
}

func (s *Server) assembleTurnCtx(ctx context.Context, run taskRun, sessionID, mcpInstructions string, fanOut bool) ([]backend.Message, error) {
	scope, agent, task, trigger := run.Scope, run.Agent, run.Task, run.Trigger
	var msgs []backend.Message
	// A worker gets a fixed sub-agent preamble above the agent's persona: it is
	// the same agent, but answering one scoped question as data rather than
	// holding a conversation.
	if run.Worker != nil {
		msgs = append(msgs, backend.Message{Role: backend.RoleSystem,
			Content: workerPreamble(agent.Name, run.Worker.ParentTask)})
	}
	if sp := agent.Spec.SystemPrompt; sp != "" {
		msgs = append(msgs, backend.Message{Role: backend.RoleSystem, Content: sp})
	}
	if fanOut && run.Worker == nil {
		msgs = append(msgs, backend.Message{Role: backend.RoleSystem, Content: fanOutGuidance})
	}
	if run.Worker != nil {
		if instr := strings.TrimSpace(run.Worker.Instructions); instr != "" {
			msgs = append(msgs, backend.Message{Role: backend.RoleSystem, Content: "Guidance for this sub-task:\n\n" + instr})
		}
		// A worker runs on a fresh session with no history, and gets no memory
		// injection: the parent owns recall and synthesis, and ten workers each
		// carrying the agent's whole note pile is cost without benefit.
		if mi := strings.TrimSpace(mcpInstructions); mi != "" {
			msgs = append(msgs, backend.Message{Role: backend.RoleSystem, Content: "Guidance from connected tools/services:\n\n" + mi})
		}
		return append(msgs, backend.Message{Role: backend.RoleUser, Content: task}), nil
	}
	// Long-term memory: auto-inject the agent's saved notes so recall does not
	// depend on the model remembering to call memory_list. spec.memory.enabled
	// defaults to true; maxNotes bounds the injection.
	if agent.Spec.Memory.Enabled == nil || *agent.Spec.Memory.Enabled {
		if notes, err := s.store.ListMemories(ctx, scope, memoryNoteLimit(agent)); err == nil && len(notes) > 0 {
			var b strings.Builder
			b.WriteString("Long-term notes you saved earlier (use memory_save to add or update):\n")
			for _, n := range notes {
				fmt.Fprintf(&b, "- %s: %s\n", n.Title, safeTruncate(n.Body, memoryNoteClip))
			}
			msgs = append(msgs, backend.Message{Role: backend.RoleSystem, Content: b.String()})
		}
	}
	// Ambient guidance from connected MCP servers (e.g. an edges Service's
	// spec.instructions describing its entity layout / quirks). Injected as a
	// system message so it reaches the model even though MCP clients don't
	// surface server `initialize` instructions on their own.
	if mi := strings.TrimSpace(mcpInstructions); mi != "" {
		msgs = append(msgs, backend.Message{Role: backend.RoleSystem, Content: "Guidance from connected tools/services:\n\n" + mi})
	}
	// History is loaded in full before the engine decides whether this request
	// needs compaction. Loading failures are returned so a new run never silently
	// proceeds as though a failed read meant the session was empty.
	sc, err := s.loadSessionContext(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	if sc.Checkpoint != nil {
		if sc.Summary == nil || strings.TrimSpace(sc.Summary.Summary) == "" || len(sc.Checkpoint.ReplacementHistory) == 0 {
			return nil, fmt.Errorf("session checkpoint has no summary or replacement history")
		}
		if sc.Checkpoint.Version != 1 {
			return nil, fmt.Errorf("unsupported session checkpoint version %d", sc.Checkpoint.Version)
		}
		replacement, err := engineMessagesFromCheckpoint(sc.Checkpoint.ReplacementHistory)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, replacement...)
	} else if sc.Summary != nil {
		if strings.TrimSpace(sc.Summary.Summary) == "" {
			return nil, fmt.Errorf("session summary is empty")
		}
		msgs = append(msgs, summaryMessage(*sc.Summary))
	}
	history := sc.Messages
	msgs = append(msgs, engineMessagesFromHistory(history)...)
	// Background sessions (schedule/heartbeat/wakeup) accumulate the agent's
	// own prior replies turn after turn — kept deliberately, so e.g. a news
	// schedule can see what it already posted and not repeat itself. That pile
	// can outweigh the one persona line at the top, so re-assert it after
	// history to keep the agent in character.
	if sp := agent.Spec.SystemPrompt; sp != "" && !isInteractive(trigger) && (len(history) > 0 || sc.Summary != nil || sc.Checkpoint != nil) {
		msgs = append(msgs, backend.Message{Role: backend.RoleSystem, Content: "Reminder — your persona and standing instructions still apply to this reply:\n\n" + sp})
	}
	msgs = append(msgs, backend.Message{Role: backend.RoleUser, Content: task})
	return msgs, nil
}
