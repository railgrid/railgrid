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
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectAssistantAuditVersion        = 2
	projectAssistantAuditMaxTools       = 128
	projectAssistantAuditMaxModelCalls  = 32
	projectAssistantAuditMaxCompactions = 16
	projectAssistantAuditMaxToolNames   = 32
	projectAssistantAuditMaxDecisions   = 64
	projectAssistantAuditMaxPathBytes   = 512
	projectAssistantAuditMaxSummaryLen  = 256
	projectAssistantAuditMaxUsageDedupe = 64
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
	ID            string `json:"id,omitempty"`
	Name          string `json:"name"`
	Path          string `json:"path,omitempty"`
	Status        string `json:"status"`
	Summary       string `json:"summary,omitempty"`
	Additions     int    `json:"additions,omitempty"`
	Deletions     int    `json:"deletions,omitempty"`
	Replacements  int    `json:"replacements,omitempty"`
	Diff          string `json:"diff,omitempty"`
	DiffTruncated bool   `json:"diffTruncated,omitempty"`
	RecoveryOf    string `json:"recoveryOf,omitempty"`
	AtOffsetMS    int64  `json:"atOffsetMs"`
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
	InputBytes                int64    `json:"inputBytes,omitempty"`
	PromptTokens              int64    `json:"promptTokens,omitempty"`
	CachedPromptTokens        int64    `json:"cachedPromptTokens,omitempty"`
	CompletionTokens          int64    `json:"completionTokens,omitempty"`
	TotalTokens               int64    `json:"totalTokens,omitempty"`
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
	mu                   sync.Mutex
	run                  *store.AssistantRun
	started              time.Time
	audit                projectAssistantRunAudit
	persist              func(context.Context, []byte) error
	modelUsageByOrdinal  map[int]projectAssistantAuditTokenUsage
	missingUsageOrdinals map[int]struct{}
	usageDedupeFloor     int
}

type projectAssistantAuditTokenUsage struct {
	promptTokens       int64
	cachedPromptTokens int64
	completionTokens   int64
	totalTokens        int64
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
	if audit.Version < projectAssistantAuditVersion {
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
	if audit.CatalogDigest == "" && req.SkillSnapshot != nil {
		audit.CatalogDigest = projectAssistantAuditString(req.SkillSnapshot.CatalogDigest, projectAssistantAuditMaxSummaryLen)
	}
	if len(audit.SelectedSkills) == 0 && len(req.SelectedSkills) > 0 {
		audit.SelectedSkills = cloneProjectAssistantSkillReceipts(req.SelectedSkills)
	}
	if len(audit.SelectedContextResources) == 0 && len(req.SelectedContextResources) > 0 {
		audit.SelectedContextResources = cloneProjectAssistantContextResourceReceipts(req.SelectedContextResources)
	}
	if len(audit.ContentParts) == 0 && len(req.ContentParts) > 0 {
		audit.ContentParts = projectAssistantCanonicalContentPartsForAudit(req.ContentParts)
	}
	projectAssistantAuditApplyEffectiveSettings(&audit, req)
	recorder := &projectAssistantRunAuditRecorder{
		run:                  run,
		started:              audit.StartedAt,
		audit:                audit,
		modelUsageByOrdinal:  make(map[int]projectAssistantAuditTokenUsage),
		missingUsageOrdinals: make(map[int]struct{}),
	}
	// Rehydrate retained per-call usage so duplicate callbacks after a process
	// restart remain idempotent instead of adding the same token counts twice.
	for _, call := range audit.ModelCalls {
		if call.PromptTokens == 0 && call.CachedPromptTokens == 0 && call.CompletionTokens == 0 && call.TotalTokens == 0 {
			continue
		}
		recorder.modelUsageByOrdinal[call.Ordinal] = projectAssistantAuditTokenUsage{
			promptTokens:       call.PromptTokens,
			cachedPromptTokens: call.CachedPromptTokens,
			completionTokens:   call.CompletionTokens,
			totalTokens:        call.TotalTokens,
		}
	}
	if len(audit.ModelCalls) > 0 {
		firstRetained := audit.ModelCalls[0].Ordinal
		for _, call := range audit.ModelCalls[1:] {
			firstRetained = min(firstRetained, call.Ordinal)
		}
		recorder.usageDedupeFloor = max(firstRetained-1, 0)
	} else if audit.ModelCallStats != nil {
		recorder.usageDedupeFloor = max(audit.ModelCallStats.TotalCalls, 0)
	}
	recorder.trimUsageDedupeLocked()
	recorder.updateRunLocked()
	return recorder
}

// projectAssistantAuditApplyEffectiveSettings captures the settings known at
// the start of a fresh or resumed segment. A continuation owns its persisted
// optimization mode; falling back to the process environment is only valid for
// a fresh segment where no checkpoint mode exists yet.
func projectAssistantAuditApplyEffectiveSettings(audit *projectAssistantRunAudit, req projectAssistantRunRequest) {
	if audit == nil {
		return
	}
	if audit.EffectiveSettings == nil {
		audit.EffectiveSettings = &projectAssistantAuditEffectiveSettings{}
	}
	settings := audit.EffectiveSettings
	if strings.TrimSpace(settings.Provider) == "" {
		settings.Provider = projectAssistantAuditString(req.LLM.Provider, projectAssistantAuditMaxSummaryLen)
	}
	if strings.TrimSpace(settings.Provider) == "" {
		settings.Provider = projectAssistantAuditString(audit.Provider, projectAssistantAuditMaxSummaryLen)
	}
	if strings.TrimSpace(settings.Model) == "" {
		settings.Model = projectAssistantAuditString(req.LLM.Model, projectAssistantAuditMaxSummaryLen)
	}
	if strings.TrimSpace(settings.Model) == "" {
		settings.Model = projectAssistantAuditString(audit.Model, projectAssistantAuditMaxSummaryLen)
	}
	if strings.TrimSpace(settings.OptimizationMode) == "" {
		if req.Continuation != nil {
			settings.OptimizationMode = projectEinoAssistantNormalizeOptimizationMode(req.Continuation.AgentOptimizationMode)
		} else {
			settings.OptimizationMode = projectEinoAssistantOptimizationModeFromEnvironment()
		}
	}
	if settings.DynamicToolCatalogDigest == "" && req.Continuation != nil {
		settings.DynamicToolCatalogDigest = projectAssistantAuditString(
			req.Continuation.DynamicToolCatalogDigest,
			projectAssistantAuditMaxSummaryLen,
		)
	}
	projectAssistantAuditRefreshEffectiveSettings(audit)
	if settings.Provider != "" {
		audit.Provider = settings.Provider
	}
	if settings.Model != "" {
		audit.Model = settings.Model
	}
}

// projectAssistantAuditRefreshEffectiveSettings keeps terminal snapshots
// truthful when dynamic tools or a resumed segment add later model calls. It
// intentionally does not consult process configuration, so finalizing a
// legacy run cannot invent a new optimization mode.
func projectAssistantAuditRefreshEffectiveSettings(audit *projectAssistantRunAudit) {
	if audit == nil {
		return
	}
	if audit.EffectiveSettings == nil {
		audit.EffectiveSettings = &projectAssistantAuditEffectiveSettings{}
	}
	settings := audit.EffectiveSettings
	settings.Provider = projectAssistantAuditString(settings.Provider, projectAssistantAuditMaxSummaryLen)
	settings.Model = projectAssistantAuditString(settings.Model, projectAssistantAuditMaxSummaryLen)
	if settings.Provider == "" {
		settings.Provider = projectAssistantAuditString(audit.Provider, projectAssistantAuditMaxSummaryLen)
	}
	if settings.Model == "" {
		settings.Model = projectAssistantAuditString(audit.Model, projectAssistantAuditMaxSummaryLen)
	}
	settings.OptimizationMode = projectEinoAssistantNormalizeOptimizationMode(settings.OptimizationMode)
	settings.ToolContractDigest = projectAssistantAuditString(settings.ToolContractDigest, projectAssistantAuditMaxSummaryLen)
	settings.DynamicToolCatalogDigest = projectAssistantAuditString(settings.DynamicToolCatalogDigest, projectAssistantAuditMaxSummaryLen)
	if settings.InstructionDigest == "" {
		settings.InstructionDigest = projectAssistantAuditInstructionDigest()
	}
	settings.InstructionDigest = projectAssistantAuditString(settings.InstructionDigest, projectAssistantAuditMaxSummaryLen)
	for index := len(audit.ModelCalls) - 1; index >= 0; index-- {
		if digest := strings.TrimSpace(audit.ModelCalls[index].ToolContractDigest); digest != "" {
			settings.ToolContractDigest = projectAssistantAuditString(digest, projectAssistantAuditMaxSummaryLen)
			break
		}
	}
}

func projectAssistantAuditInstructionDigest() string {
	sum := sha256.Sum256([]byte(projectEinoAssistantV2DeepInstruction))
	return "sha256:" + hex.EncodeToString(sum[:])
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
		Path:       projectAssistantAuditToolPathForEvent(event),
		Status:     projectAssistantAuditString(event.Status, projectAssistantAuditMaxSummaryLen),
		Summary:    projectAssistantAuditToolEventSummary(event),
		AtOffsetMS: projectAssistantAuditOffsetMS(r.started, at),
	}
	if event.Mutation != nil {
		entry.Additions = event.Mutation.Additions
		entry.Deletions = event.Mutation.Deletions
		entry.Replacements = event.Mutation.Replacements
		entry.Diff = event.Mutation.Diff
		entry.DiffTruncated = event.Mutation.DiffTruncated
		entry.RecoveryOf = projectAssistantAuditString(event.Mutation.RecoveryOf, projectAssistantAuditMaxSummaryLen)
	}
	if entry.RecoveryOf == "" {
		entry.RecoveryOf = projectAssistantAuditString(event.RecoveryOf, projectAssistantAuditMaxSummaryLen)
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
	inputBytes ...int64,
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
	if len(inputBytes) > 0 && inputBytes[0] > 0 {
		entry.InputBytes = inputBytes[0]
	}
	r.mu.Lock()
	stats := r.ensureModelCallStatsLocked()
	stats.TotalCalls++
	if len(r.audit.ModelCalls) >= projectAssistantAuditMaxModelCalls {
		stats.DroppedCalls++
		r.audit.ModelCalls = append(
			[]projectAssistantAuditModelCall(nil),
			r.audit.ModelCalls[len(r.audit.ModelCalls)-projectAssistantAuditMaxModelCalls+1:]...,
		)
	}
	r.audit.ModelCalls = append(r.audit.ModelCalls, entry)
	projectAssistantAuditRefreshEffectiveSettings(&r.audit)
	stats.RetainedCalls = len(r.audit.ModelCalls)
	if entry.InputBytes > 0 {
		stats.InputBytes += entry.InputBytes
	}
	r.updateRunLocked()
	raw := r.auditSnapshotLocked()
	r.mu.Unlock()
	return r.persistSnapshot(ctx, raw)
}

// ensureModelCallStatsLocked initializes the v2 rollup lazily so decoding an
// older v1 audit remains compatible and untouched runs do not gain an empty
// stats object merely by being read.
func (r *projectAssistantRunAuditRecorder) ensureModelCallStatsLocked() *projectAssistantAuditModelCallStats {
	if r == nil {
		return nil
	}
	if r.audit.ModelCallStats == nil {
		r.audit.ModelCallStats = &projectAssistantAuditModelCallStats{}
	}
	stats := r.audit.ModelCallStats
	if stats.TotalCalls < len(r.audit.ModelCalls) {
		stats.TotalCalls = len(r.audit.ModelCalls)
	}
	stats.RetainedCalls = len(r.audit.ModelCalls)
	minimumDropped := stats.TotalCalls - stats.RetainedCalls
	if minimumDropped > stats.DroppedCalls {
		stats.DroppedCalls = minimumDropped
	}
	return stats
}

func (r *projectAssistantRunAuditRecorder) recordModelRetryAttempt(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	stats := r.ensureModelCallStatsLocked()
	stats.RetryAttempts++
	r.updateRunLocked()
	raw := r.auditSnapshotLocked()
	r.mu.Unlock()
	_ = r.persistSnapshot(ctx, raw)
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

// projectAssistantAuditInputBytes measures the model-visible request without
// retaining its payload. It intentionally counts serialized messages and tool
// contracts only; callers persist the aggregate size, never the bytes.
func projectAssistantAuditInputBytes(messages []*schema.Message, toolSets ...[]*schema.ToolInfo) int64 {
	var total int64
	if raw, err := json.Marshal(projectEinoMessagesToChat(messages)); err == nil {
		total += int64(len(raw))
	}
	for _, tools := range toolSets {
		if raw, err := json.Marshal(tools); err == nil {
			total += int64(len(raw))
		}
	}
	return total
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
	stats := r.ensureModelCallStatsLocked()
	for i := len(r.audit.ModelCalls) - 1; i >= 0; i-- {
		entry := &r.audit.ModelCalls[i]
		if entry.Ordinal == ordinal && entry.Outcome == "" {
			entry.Outcome = outcome
			entry.RequestedTools = requestedTools
			completed := projectAssistantAuditOffsetMS(r.started, time.Now().UTC())
			entry.CompletedAtOffsetMS = &completed
			r.recordModelUsageLocked(stats, ordinal, entry, projectAssistantAuditTokenUsageFromMessage(response))
			r.updateRunLocked()
			raw := r.auditSnapshotLocked()
			r.mu.Unlock()
			return r.persistSnapshot(ctx, raw)
		}
	}
	// A model result may be reported after its bounded detail entry has rolled
	// out of the retention window. Keep the uncapped usage/missing counters even
	// when no detailed row remains to update.
	if stats != nil {
		r.recordModelUsageLocked(stats, ordinal, nil, projectAssistantAuditTokenUsageFromMessage(response))
		r.updateRunLocked()
		raw := r.auditSnapshotLocked()
		r.mu.Unlock()
		return r.persistSnapshot(ctx, raw)
	}
	r.mu.Unlock()
	return nil
}

func projectAssistantAuditTokenUsageFromMessage(response *schema.Message) *projectAssistantAuditTokenUsage {
	if response == nil || response.ResponseMeta == nil || response.ResponseMeta.Usage == nil {
		return nil
	}
	usage := response.ResponseMeta.Usage
	return &projectAssistantAuditTokenUsage{
		promptTokens:       int64(max(usage.PromptTokens, 0)),
		cachedPromptTokens: int64(max(usage.PromptTokenDetails.CachedTokens, 0)),
		completionTokens:   int64(max(usage.CompletionTokens, 0)),
		totalTokens:        int64(max(usage.TotalTokens, 0)),
	}
}

func (r *projectAssistantRunAuditRecorder) recordModelUsageLocked(
	stats *projectAssistantAuditModelCallStats,
	ordinal int,
	entry *projectAssistantAuditModelCall,
	usage *projectAssistantAuditTokenUsage,
) {
	if r == nil || stats == nil || ordinal <= 0 {
		return
	}
	if ordinal <= r.usageDedupeFloor {
		return
	}
	if r.missingUsageOrdinals == nil {
		r.missingUsageOrdinals = make(map[int]struct{})
	}
	if usage == nil {
		// Streaming adapters can report an assembled response with usage and
		// then invoke the ordinary output callback with the same response but
		// without metadata. Once usage has been observed, a later nil callback
		// must not turn that model call back into "missing" telemetry.
		if _, observed := r.modelUsageByOrdinal[ordinal]; observed {
			return
		}
		if _, counted := r.missingUsageOrdinals[ordinal]; counted {
			return
		}
		r.missingUsageOrdinals[ordinal] = struct{}{}
		stats.MissingUsageCalls++
		r.trimUsageDedupeLocked()
		return
	}
	if r.modelUsageByOrdinal == nil {
		r.modelUsageByOrdinal = make(map[int]projectAssistantAuditTokenUsage)
	}
	previous := r.modelUsageByOrdinal[ordinal]
	delta := projectAssistantAuditTokenUsage{
		promptTokens:       usage.promptTokens - previous.promptTokens,
		cachedPromptTokens: usage.cachedPromptTokens - previous.cachedPromptTokens,
		completionTokens:   usage.completionTokens - previous.completionTokens,
		totalTokens:        usage.totalTokens - previous.totalTokens,
	}
	stats.PromptTokens += delta.promptTokens
	stats.CachedPromptTokens += delta.cachedPromptTokens
	stats.CompletionTokens += delta.completionTokens
	stats.TotalTokens += delta.totalTokens
	r.modelUsageByOrdinal[ordinal] = *usage
	if _, counted := r.missingUsageOrdinals[ordinal]; counted {
		delete(r.missingUsageOrdinals, ordinal)
		if stats.MissingUsageCalls > 0 {
			stats.MissingUsageCalls--
		}
	}
	if entry != nil {
		entry.PromptTokens = usage.promptTokens
		entry.CachedPromptTokens = usage.cachedPromptTokens
		entry.CompletionTokens = usage.completionTokens
		entry.TotalTokens = usage.totalTokens
	}
	r.trimUsageDedupeLocked()
}

func (r *projectAssistantRunAuditRecorder) trimUsageDedupeLocked() {
	if r == nil {
		return
	}
	for len(r.modelUsageByOrdinal)+len(r.missingUsageOrdinals) > projectAssistantAuditMaxUsageDedupe {
		oldest := 0
		for ordinal := range r.modelUsageByOrdinal {
			if oldest == 0 || ordinal < oldest {
				oldest = ordinal
			}
		}
		for ordinal := range r.missingUsageOrdinals {
			if oldest == 0 || ordinal < oldest {
				oldest = ordinal
			}
		}
		if oldest == 0 {
			return
		}
		delete(r.modelUsageByOrdinal, oldest)
		delete(r.missingUsageOrdinals, oldest)
		r.usageDedupeFloor = max(r.usageDedupeFloor, oldest)
	}
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
	stats := r.ensureModelCallStatsLocked()
	for i := len(r.audit.ModelCalls) - 1; i >= 0; i-- {
		entry := &r.audit.ModelCalls[i]
		if entry.Outcome == "" {
			entry.Outcome = "error"
			completed := projectAssistantAuditOffsetMS(r.started, time.Now().UTC())
			entry.CompletedAtOffsetMS = &completed
			r.recordModelUsageLocked(stats, entry.Ordinal, entry, nil)
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
	projectAssistantAuditRefreshEffectiveSettings(&r.audit)
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
	case "org_spend_cap":
		var capExceeded *projectAssistantOrgSpendCapExceededError
		if errors.As(err, &capExceeded) {
			summary = capExceeded.Error()
		} else {
			summary = errProjectAssistantOrgSpendCapExceeded.Error()
		}
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
	case projectEinoAssistantOrgSpendCapExceeded(err):
		return "org_spend_cap"
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
	if audit.Version < projectAssistantAuditVersion {
		audit.Version = projectAssistantAuditVersion
	}
	audit.Failure = projectAssistantAuditFailureForError(cause)
	projectAssistantAuditRefreshEffectiveSettings(&audit)
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
	projectAssistantAuditRefreshEffectiveSettings(&r.audit)
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
	case projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep,
		projectToolCreateFile, projectToolReplaceFile, projectToolEditFile, projectToolDeleteFile, projectToolMoveFile:
	default:
		return ""
	}
	for _, part := range strings.Split(arguments, ";") {
		part = strings.TrimSpace(part)
		path := ""
		switch {
		case strings.HasPrefix(part, "path "):
			path = strings.TrimSpace(strings.TrimPrefix(part, "path "))
		case strings.HasPrefix(part, "source ") && base == projectToolMoveFile:
			path = strings.TrimSpace(strings.TrimPrefix(part, "source "))
		case strings.HasPrefix(part, "destination ") && base == projectToolMoveFile:
			path = strings.TrimSpace(strings.TrimPrefix(part, "destination "))
		default:
			continue
		}
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

func projectAssistantAuditToolPathForEvent(event projectToolCallStreamEvent) string {
	if event.Mutation != nil && projectAssistantWorkspaceMutationTool(event.Name) {
		base := projectToolBaseName(event.Name)
		path := event.Mutation.Path
		if base == projectToolMoveFile && event.Mutation.PreviousPath != "" {
			path = event.Mutation.PreviousPath
		}
		clean, err := workspace.CleanProjectPath(path)
		if err == nil {
			return projectAssistantAuditString(clean, projectAssistantAuditMaxPathBytes)
		}
	}
	return projectAssistantAuditToolPath(event.Name, event.Arguments)
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
	case strings.Contains(failure, errProjectAssistantOrgSpendCapExceeded.Error()):
		return "org_spend_cap"
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
	if audit.Version < projectAssistantAuditVersion {
		audit.Version = projectAssistantAuditVersion
	}
	if audit.StartedAt.IsZero() {
		audit.StartedAt = run.CreatedAt.UTC()
		if audit.StartedAt.IsZero() {
			audit.StartedAt = at.UTC()
		}
	}
	projectAssistantAuditRefreshEffectiveSettings(&audit)
	audit.Outcome = outcome
	audit.DurationMS = projectAssistantAuditOffsetMS(audit.StartedAt, at)
	raw, err := json.Marshal(audit)
	if err != nil {
		return store.AssistantRun{}, err
	}
	run.Audit = raw
	return run, nil
}
