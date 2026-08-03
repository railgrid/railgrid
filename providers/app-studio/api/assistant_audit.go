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
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/faroshq/provider-app-studio/store"
)

const (
	projectAssistantAuditVersion        = 1
	projectAssistantAuditMaxTools       = 128
	projectAssistantAuditMaxModelCalls  = 32
	projectAssistantAuditMaxCompactions = 16
	projectAssistantAuditMaxToolNames   = 32
	projectAssistantAuditMaxDecisions   = 64
	projectAssistantAuditMaxPathBytes   = 512
	projectAssistantAuditMaxSummaryLen  = 256
)

func (r *projectAssistantRunAuditRecorder) recordCompaction(
	ctx context.Context,
	entry projectAssistantAuditCompaction,
) error {
	if r == nil || strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Status) == "" {
		return nil
	}
	now := time.Now().UTC()
	entry.ID = projectAssistantAuditString(entry.ID, projectAssistantAuditMaxSummaryLen)
	entry.Trigger = projectAssistantAuditString(entry.Trigger, projectAssistantAuditMaxSummaryLen)
	entry.Status = projectAssistantAuditString(entry.Status, projectAssistantAuditMaxSummaryLen)
	entry.PreviousWindowID = projectAssistantAuditString(entry.PreviousWindowID, projectAssistantAuditMaxSummaryLen)
	entry.WindowID = projectAssistantAuditString(entry.WindowID, projectAssistantAuditMaxSummaryLen)
	entry.Error = projectAssistantAuditString(entry.Error, projectAssistantAuditMaxSummaryLen)
	entry.AtOffsetMS = projectAssistantAuditOffsetMS(r.started, now)
	if entry.Status != "started" {
		completed := entry.AtOffsetMS
		entry.CompletedAtOffsetMS = &completed
	}

	r.mu.Lock()
	for index := range r.audit.Compactions {
		if r.audit.Compactions[index].ID != entry.ID {
			continue
		}
		entry.AtOffsetMS = r.audit.Compactions[index].AtOffsetMS
		r.audit.Compactions[index] = entry
		r.updateRunLocked()
		raw := r.auditSnapshotLocked()
		r.mu.Unlock()
		return r.persistSnapshot(ctx, raw)
	}
	if len(r.audit.Compactions) >= projectAssistantAuditMaxCompactions {
		r.audit.Compactions = append(
			[]projectAssistantAuditCompaction(nil),
			r.audit.Compactions[len(r.audit.Compactions)-projectAssistantAuditMaxCompactions+1:]...,
		)
	}
	r.audit.Compactions = append(r.audit.Compactions, entry)
	r.updateRunLocked()
	raw := r.auditSnapshotLocked()
	r.mu.Unlock()
	return r.persistSnapshot(ctx, raw)
}

type projectAssistantAuditOutcome string

const (
	projectAssistantAuditOutcomeSucceeded projectAssistantAuditOutcome = "succeeded"
	projectAssistantAuditOutcomeFailed    projectAssistantAuditOutcome = "failed"
	projectAssistantAuditOutcomePreempted projectAssistantAuditOutcome = "preempted"
	projectAssistantAuditOutcomeAborted   projectAssistantAuditOutcome = "aborted"
)

type projectAssistantAuditTool struct {
	ID             string `json:"id,omitempty"`
	Name           string `json:"name"`
	Path           string `json:"path,omitempty"`
	Status         string `json:"status"`
	Summary        string `json:"summary,omitempty"`
	Additions      int    `json:"additions,omitempty"`
	Deletions      int    `json:"deletions,omitempty"`
	Replacements   int    `json:"replacements,omitempty"`
	Patch          string `json:"patch,omitempty"`
	PatchTruncated bool   `json:"patchTruncated,omitempty"`
	AtOffsetMS     int64  `json:"atOffsetMs"`
}

type projectAssistantAuditModelCall struct {
	Ordinal                   int      `json:"ordinal"`
	SourceRevision            uint64   `json:"sourceRevision,omitempty"`
	VerifiedRevision          uint64   `json:"verifiedRevision,omitempty"`
	RolloutBudgetRemaining    *int64   `json:"rolloutBudgetRemainingTokens,omitempty"`
	VisibleTools              []string `json:"visibleTools,omitempty"`
	ToolContractDigest        string   `json:"toolContractDigest,omitempty"`
	Outcome                   string   `json:"outcome,omitempty"`
	RequestedTools            []string `json:"requestedTools,omitempty"`
	TransportErrorObserved    bool     `json:"transportErrorObserved,omitempty"`
	AtOffsetMS                int64    `json:"atOffsetMs"`
	FirstResponseAtOffsetMS   *int64   `json:"firstResponseAtOffsetMs,omitempty"`
	ToolCallStartedAtOffsetMS *int64   `json:"toolCallStartedAtOffsetMs,omitempty"`
	CompletedAtOffsetMS       *int64   `json:"completedAtOffsetMs,omitempty"`
}

type projectAssistantAuditFailure struct {
	Kind             string `json:"kind"`
	ToolName         string `json:"toolName,omitempty"`
	Summary          string `json:"summary,omitempty"`
	Calls            int    `json:"calls,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	SourceRevision   uint64 `json:"sourceRevision,omitempty"`
	VerifiedRevision uint64 `json:"verifiedRevision,omitempty"`
}

type projectAssistantRunAuditRecorder struct {
	mu      sync.Mutex
	run     *store.AssistantRun
	started time.Time
	audit   projectAssistantRunAudit
	persist func(context.Context, []byte) error
}

func newProjectAssistantRunAuditRecorder(
	req projectAssistantRunRequest,
	run *store.AssistantRun,
	started time.Time,
) *projectAssistantRunAuditRecorder {
	if started.IsZero() {
		started = time.Now().UTC()
	}
	audit := projectAssistantRunAudit{}
	if run != nil && len(run.Audit) > 0 {
		_ = json.Unmarshal(run.Audit, &audit)
	}
	if audit.Version == 0 {
		audit.Version = projectAssistantAuditVersion
	}
	if audit.StartedAt.IsZero() {
		audit.StartedAt = started.UTC()
	}
	if strings.TrimSpace(audit.Provider) == "" {
		audit.Provider = projectAssistantAuditString(req.LLM.Provider, projectAssistantAuditMaxSummaryLen)
	}
	if strings.TrimSpace(audit.Model) == "" {
		audit.Model = projectAssistantAuditString(req.LLM.Model, projectAssistantAuditMaxSummaryLen)
	}
	if audit.ApprovalMode == "" {
		audit.ApprovalMode = req.ApprovalMode
	}
	if audit.Profile == "" {
		audit.Profile = req.TurnProfile
	}
	recorder := &projectAssistantRunAuditRecorder{
		run:     run,
		started: audit.StartedAt,
		audit:   audit,
	}
	recorder.updateRunLocked()
	return recorder
}

func (r *projectAssistantRunAuditRecorder) recordTool(event projectToolCallStreamEvent) {
	r.recordToolAt(event, time.Now().UTC())
}

func (r *projectAssistantRunAuditRecorder) wrapCallbacks(callbacks *projectAssistantStreamCallbacks) {
	if r == nil || callbacks == nil {
		return
	}
	onToolCall := callbacks.OnToolCall
	callbacks.OnToolCall = func(event projectToolCallStreamEvent) {
		r.recordTool(event)
		if onToolCall != nil {
			onToolCall(event)
		}
	}
}

func (r *projectAssistantRunAuditRecorder) recordToolAt(event projectToolCallStreamEvent, at time.Time) {
	if r == nil || strings.TrimSpace(event.Name) == "" || strings.TrimSpace(event.Status) == "" {
		return
	}
	entry := projectAssistantAuditTool{
		ID:         projectAssistantAuditString(event.ID, projectAssistantAuditMaxSummaryLen),
		Name:       projectAssistantAuditString(projectToolBaseName(event.Name), projectAssistantAuditMaxSummaryLen),
		Path:       projectAssistantAuditToolPath(event.Name, event.Arguments),
		Status:     projectAssistantAuditString(event.Status, projectAssistantAuditMaxSummaryLen),
		Summary:    projectAssistantAuditToolEventSummary(event),
		AtOffsetMS: projectAssistantAuditOffsetMS(r.started, at),
	}
	if event.Mutation != nil {
		entry.Additions = event.Mutation.Additions
		entry.Deletions = event.Mutation.Deletions
		entry.Replacements = event.Mutation.Replacements
		entry.Patch = event.Mutation.Patch
		entry.PatchTruncated = event.Mutation.PatchTruncated
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if entry.ID != "" {
		for i := range r.audit.Tools {
			if r.audit.Tools[i].ID == entry.ID {
				if entry.Path == "" {
					entry.Path = r.audit.Tools[i].Path
				}
				r.audit.Tools[i] = entry
				r.updateRunLocked()
				return
			}
		}
	}
	if len(r.audit.Tools) >= projectAssistantAuditMaxTools {
		return
	}
	r.audit.Tools = append(r.audit.Tools, entry)
	r.updateRunLocked()
}

func (r *projectAssistantRunAuditRecorder) recordModelCall(
	ctx context.Context,
	ordinal int,
	sourceRevision uint64,
	verifiedRevision uint64,
	rolloutBudgetRemaining *int64,
	toolInfos []*schema.ToolInfo,
	deferredToolInfos []*schema.ToolInfo,
) error {
	if r == nil || ordinal <= 0 {
		return nil
	}
	entry := projectAssistantAuditModelCall{
		Ordinal:                ordinal,
		SourceRevision:         sourceRevision,
		VerifiedRevision:       verifiedRevision,
		RolloutBudgetRemaining: rolloutBudgetRemaining,
		VisibleTools:           projectAssistantAuditToolNames(toolInfos, deferredToolInfos),
		ToolContractDigest:     projectAssistantAuditToolContractDigest(toolInfos, deferredToolInfos),
		AtOffsetMS:             projectAssistantAuditOffsetMS(r.started, time.Now().UTC()),
	}
	r.mu.Lock()
	if len(r.audit.ModelCalls) >= projectAssistantAuditMaxModelCalls {
		r.audit.ModelCalls = append(
			[]projectAssistantAuditModelCall(nil),
			r.audit.ModelCalls[len(r.audit.ModelCalls)-projectAssistantAuditMaxModelCalls+1:]...,
		)
	}
	r.audit.ModelCalls = append(r.audit.ModelCalls, entry)
	r.updateRunLocked()
	raw := r.auditSnapshotLocked()
	r.mu.Unlock()
	return r.persistSnapshot(ctx, raw)
}

func projectAssistantAuditToolContractDigest(groups ...[]*schema.ToolInfo) string {
	type contract struct {
		Name         string `json:"name"`
		Parameters   string `json:"parameters,omitempty"`
		Risk         string `json:"risk,omitempty"`
		ParallelSafe bool   `json:"parallelSafe,omitempty"`
	}
	contracts := make([]contract, 0)
	seen := map[string]struct{}{}
	for _, infos := range groups {
		for _, info := range infos {
			if info == nil {
				continue
			}
			name := projectAssistantToolKey(info.Name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			item := contract{Name: name}
			if info.Extra != nil {
				item.Parameters, _ = info.Extra[projectEinoToolParametersExtraKey].(string)
				item.Risk, _ = info.Extra["risk"].(string)
				item.ParallelSafe, _ = info.Extra["parallelSafe"].(bool)
			}
			if item.Parameters == "" && info.ParamsOneOf != nil {
				if raw, err := json.Marshal(info.ParamsOneOf); err == nil {
					item.Parameters = string(raw)
				}
			}
			contracts = append(contracts, item)
		}
	}
	if len(contracts) == 0 {
		return ""
	}
	sort.Slice(contracts, func(i, j int) bool { return contracts[i].Name < contracts[j].Name })
	raw, err := json.Marshal(contracts)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r *projectAssistantRunAuditRecorder) rolloutBudgetSnapshot() *projectAssistantRolloutBudgetState {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneProjectAssistantRolloutBudgetStatePtr(r.audit.RolloutBudget)
}

func (r *projectAssistantRunAuditRecorder) recordRolloutBudget(
	ctx context.Context,
	state projectAssistantRolloutBudgetState,
) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	copy := cloneProjectAssistantRolloutBudgetState(state)
	r.audit.RolloutBudget = &copy
	r.updateRunLocked()
	raw := r.auditSnapshotLocked()
	r.mu.Unlock()
	return r.persistSnapshot(ctx, raw)
}

func (r *projectAssistantRunAuditRecorder) recordModelResult(
	ctx context.Context,
	ordinal int,
	response *schema.Message,
) error {
	if r == nil || ordinal <= 0 {
		return nil
	}
	outcome := "empty"
	var requestedTools []string
	if response != nil {
		switch {
		case len(response.ToolCalls) > 0:
			outcome = "tool_calls"
			for _, call := range response.ToolCalls {
				requestedTools = append(requestedTools, call.Function.Name)
			}
			requestedTools = projectAssistantAuditNames(requestedTools)
		case strings.TrimSpace(response.Content) != "" ||
			strings.TrimSpace(response.ReasoningContent) != "" ||
			len(response.AssistantGenMultiContent) > 0:
			outcome = "text"
		}
	}
	r.mu.Lock()
	for i := len(r.audit.ModelCalls) - 1; i >= 0; i-- {
		entry := &r.audit.ModelCalls[i]
		if entry.Ordinal == ordinal && entry.Outcome == "" {
			entry.Outcome = outcome
			entry.RequestedTools = requestedTools
			completed := projectAssistantAuditOffsetMS(r.started, time.Now().UTC())
			entry.CompletedAtOffsetMS = &completed
			r.updateRunLocked()
			raw := r.auditSnapshotLocked()
			r.mu.Unlock()
			return r.persistSnapshot(ctx, raw)
		}
	}
	r.mu.Unlock()
	return nil
}

func (r *projectAssistantRunAuditRecorder) recordModelResponseChunk(ctx context.Context, toolCallStarted bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	var updated bool
	for i := len(r.audit.ModelCalls) - 1; i >= 0; i-- {
		entry := &r.audit.ModelCalls[i]
		if entry.Outcome != "" {
			continue
		}
		offset := projectAssistantAuditOffsetMS(r.started, time.Now().UTC())
		if entry.FirstResponseAtOffsetMS == nil {
			entry.FirstResponseAtOffsetMS = &offset
			updated = true
		}
		if toolCallStarted && entry.ToolCallStartedAtOffsetMS == nil {
			entry.ToolCallStartedAtOffsetMS = &offset
			updated = true
		}
		break
	}
	if !updated {
		r.mu.Unlock()
		return
	}
	r.updateRunLocked()
	raw := r.auditSnapshotLocked()
	r.mu.Unlock()
	_ = r.persistSnapshot(ctx, raw)
}

func (r *projectAssistantRunAuditRecorder) recordModelTransportError(ctx context.Context, modelErr error) {
	if r == nil || modelErr == nil {
		return
	}
	r.mu.Lock()
	for i := len(r.audit.ModelCalls) - 1; i >= 0; i-- {
		entry := &r.audit.ModelCalls[i]
		if entry.Outcome != "" {
			continue
		}
		entry.TransportErrorObserved = true
		r.updateRunLocked()
		raw := r.auditSnapshotLocked()
		r.mu.Unlock()
		_ = r.persistSnapshot(ctx, raw)
		return
	}
	r.mu.Unlock()
}

func (r *projectAssistantRunAuditRecorder) setPersister(persist func(context.Context, []byte) error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.persist = persist
}

func (r *projectAssistantRunAuditRecorder) auditSnapshotLocked() []byte {
	if r == nil || r.run == nil {
		return nil
	}
	return append([]byte(nil), r.run.Audit...)
}

func (r *projectAssistantRunAuditRecorder) persistSnapshot(ctx context.Context, audit []byte) error {
	if r == nil || len(audit) == 0 {
		return nil
	}
	r.mu.Lock()
	persist := r.persist
	r.mu.Unlock()
	if persist == nil {
		return nil
	}
	persistCtx, cancel := detachedProjectPersistenceContext(ctx)
	defer cancel()
	return persist(persistCtx, audit)
}

func (r *projectAssistantRunAuditRecorder) recordModelError() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.audit.ModelCalls) - 1; i >= 0; i-- {
		entry := &r.audit.ModelCalls[i]
		if entry.Outcome == "" {
			entry.Outcome = "error"
			completed := projectAssistantAuditOffsetMS(r.started, time.Now().UTC())
			entry.CompletedAtOffsetMS = &completed
			r.updateRunLocked()
			return
		}
	}
}

func (r *projectAssistantRunAuditRecorder) recordFailure(err error) {
	if r == nil || err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.audit.Failure = projectAssistantAuditFailureForError(err)
	r.updateRunLocked()
}

func projectAssistantAuditToolNames(toolSets ...[]*schema.ToolInfo) []string {
	names := make([]string, 0)
	for _, tools := range toolSets {
		for _, tool := range tools {
			if tool == nil {
				continue
			}
			names = append(names, tool.Name)
		}
	}
	return projectAssistantAuditNames(names)
}

func projectAssistantAuditNames(names []string) []string {
	out := make([]string, 0, min(len(names), projectAssistantAuditMaxToolNames))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = projectAssistantAuditString(projectToolBaseName(name), projectAssistantAuditMaxSummaryLen)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
		if len(out) >= projectAssistantAuditMaxToolNames {
			break
		}
	}
	return out
}

func projectAssistantAuditFailureForError(err error) *projectAssistantAuditFailure {
	if err == nil {
		return nil
	}
	failure := &projectAssistantAuditFailure{Kind: projectAssistantFailureKind(err)}
	failure.Summary = projectAssistantFailureSummary(err, failure.Kind)
	var noProgress *projectEinoAssistantNoProgressError
	if errors.As(err, &noProgress) {
		failure.ToolName = projectAssistantAuditString(projectToolBaseName(noProgress.ToolName), projectAssistantAuditMaxSummaryLen)
		failure.Calls = noProgress.Calls
		failure.Limit = noProgress.Limit
		failure.SourceRevision = noProgress.SourceRevision
		failure.VerifiedRevision = noProgress.VerifiedRevision
	}
	return failure
}

func projectAssistantFailureSummary(err error, kind string) string {
	var summary string
	switch kind {
	case "no_progress":
		var noProgress *projectEinoAssistantNoProgressError
		if errors.As(err, &noProgress) {
			summary = noProgress.Error()
		} else {
			summary = errProjectAssistantNoProgress.Error()
		}
	case "max_iterations":
		summary = "assistant reached the bounded model-call limit"
	case "session_budget":
		summary = errProjectAssistantSessionBudgetExceeded.Error()
	case "no_output":
		summary = "assistant model produced no accepted output"
	case "cancelled":
		summary = "assistant execution was cancelled"
	case "deadline_exceeded":
		summary = "assistant execution deadline was exceeded"
	default:
		summary = "assistant operation failed"
	}
	return projectAssistantAuditString(summary, projectAssistantAuditMaxSummaryLen)
}

func projectAssistantFailureKind(err error) string {
	switch {
	case err == nil:
		return ""
	case projectEinoAssistantNoProgressExceeded(err):
		return "no_progress"
	case projectEinoAssistantMaxIterationsExceeded(err):
		return "max_iterations"
	case projectEinoAssistantRolloutBudgetExceeded(err):
		return "session_budget"
	case errors.Is(err, errProjectAssistantNoOutput):
		return "no_output"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "operation_failed"
	}
}

func recordProjectAssistantRunAuditFailure(run store.AssistantRun, cause error) (store.AssistantRun, error) {
	if cause == nil {
		return run, nil
	}
	var audit projectAssistantRunAudit
	if len(run.Audit) > 0 {
		if err := json.Unmarshal(run.Audit, &audit); err != nil {
			return store.AssistantRun{}, err
		}
	}
	if audit.Version == 0 {
		audit.Version = projectAssistantAuditVersion
	}
	audit.Failure = projectAssistantAuditFailureForError(cause)
	raw, err := json.Marshal(audit)
	if err != nil {
		return store.AssistantRun{}, err
	}
	run.Audit = raw
	return run, nil
}

func (r *projectAssistantRunAuditRecorder) recordAutomaticApproval(callID, toolName, actor string, mode store.AssistantApprovalMode) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.audit.Decisions) >= projectAssistantAuditMaxDecisions {
		r.audit.Decisions = append(
			[]projectAssistantPermissionAudit(nil),
			r.audit.Decisions[len(r.audit.Decisions)-projectAssistantAuditMaxDecisions+1:]...,
		)
	}
	r.audit.Decisions = append(r.audit.Decisions, projectAssistantPermissionAudit{
		Decision:     projectAssistantPermissionAllow,
		Actor:        projectAssistantAuditString(actor, projectAssistantAuditMaxSummaryLen),
		ToolCallID:   projectAssistantAuditString(callID, projectAssistantAuditMaxSummaryLen),
		ToolName:     projectAssistantAuditString(projectToolBaseName(toolName), projectAssistantAuditMaxSummaryLen),
		Reason:       "approved by user-selected auto-approve mode",
		Source:       "approval_mode",
		ApprovalMode: mode,
		ResolvedAt:   time.Now().UTC(),
	})
	r.updateRunLocked()
}

func (r *projectAssistantRunAuditRecorder) finalize(outcome projectAssistantAuditOutcome) {
	r.finalizeAt(outcome, time.Now().UTC())
}

func (r *projectAssistantRunAuditRecorder) finalizeAt(outcome projectAssistantAuditOutcome, at time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.audit.Outcome = outcome
	r.audit.DurationMS = projectAssistantAuditOffsetMS(r.started, at)
	r.updateRunLocked()
}

func (r *projectAssistantRunAuditRecorder) updateRunLocked() {
	if r == nil || r.run == nil {
		return
	}
	raw, err := json.Marshal(r.audit)
	if err == nil {
		r.run.Audit = raw
	}
}

func projectAssistantAuditOffsetMS(started, at time.Time) int64 {
	if at.Before(started) {
		return 0
	}
	return at.Sub(started).Milliseconds()
}

func projectAssistantAuditToolPath(name, arguments string) string {
	rawName := strings.TrimSpace(name)
	base := projectToolBaseName(rawName)
	switch base {
	case projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep, projectToolApplyPatch:
	default:
		return ""
	}
	for _, part := range strings.Split(arguments, ";") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "path ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(part, "path "))
		if path == "" || strings.ContainsAny(path, "\r\n\x00") {
			return ""
		}
		if projectAssistantCanonicalFilesystemReadTool(rawName) {
			var ok bool
			path, ok = unescapeProjectCanonicalToolSummaryValue(path)
			if !ok {
				return ""
			}
		}
		return projectAssistantAuditString(path, projectAssistantAuditMaxPathBytes)
	}
	return ""
}

func projectAssistantAuditToolSummary(name, status string) string {
	name = strings.ReplaceAll(projectToolBaseName(name), "_", " ")
	status = strings.TrimSpace(strings.ToLower(status))
	if name == "" || status == "" {
		return ""
	}
	return projectAssistantAuditString(name+" "+status, projectAssistantAuditMaxSummaryLen)
}

func projectAssistantAuditToolEventSummary(event projectToolCallStreamEvent) string {
	if strings.EqualFold(strings.TrimSpace(event.Status), "skipped") &&
		projectEinoAssistantFilesystemReadTool(event.Name) {
		return "Skipped an unchanged duplicate read."
	}
	return projectAssistantAuditToolSummary(event.Name, event.Status)
}

func projectAssistantAuditString(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return strings.TrimSpace(value[:maxBytes])
}

func projectAssistantAuditReason(failure string) string {
	failure = strings.ToLower(strings.TrimSpace(failure))
	switch {
	case failure == "":
		return ""
	case strings.Contains(failure, "aborted by user"):
		return "user_aborted"
	case strings.Contains(failure, "repository binding changed"):
		return "stale_repository_binding"
	case strings.Contains(failure, "preempt"):
		return "preempted"
	case strings.Contains(failure, errProjectAssistantNoProgress.Error()):
		return "no_progress"
	case strings.Contains(failure, "exceed max iterations"):
		return "max_iterations"
	case strings.Contains(failure, errProjectAssistantSessionBudgetExceeded.Error()):
		return "session_budget"
	case strings.Contains(failure, errProjectAssistantNoOutput.Error()):
		return "no_output"
	default:
		return "operation_failed"
	}
}

func finalizeProjectAssistantRunAudit(
	run store.AssistantRun,
	outcome projectAssistantAuditOutcome,
	at time.Time,
) (store.AssistantRun, error) {
	var audit projectAssistantRunAudit
	if len(run.Audit) > 0 {
		if err := json.Unmarshal(run.Audit, &audit); err != nil {
			return store.AssistantRun{}, err
		}
	}
	if audit.Version == 0 {
		audit.Version = projectAssistantAuditVersion
	}
	if audit.StartedAt.IsZero() {
		audit.StartedAt = run.CreatedAt.UTC()
		if audit.StartedAt.IsZero() {
			audit.StartedAt = at.UTC()
		}
	}
	audit.Outcome = outcome
	audit.DurationMS = projectAssistantAuditOffsetMS(audit.StartedAt, at)
	raw, err := json.Marshal(audit)
	if err != nil {
		return store.AssistantRun{}, err
	}
	run.Audit = raw
	return run, nil
}
