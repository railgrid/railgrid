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
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

const (
	projectAssistantAuditVersion       = 1
	projectAssistantAuditMaxPhases     = 32
	projectAssistantAuditMaxTools      = 128
	projectAssistantAuditMaxDecisions  = 64
	projectAssistantAuditMaxPathBytes  = 512
	projectAssistantAuditMaxSummaryLen = 256
)

type projectAssistantAuditOutcome string

const (
	projectAssistantAuditOutcomeSucceeded projectAssistantAuditOutcome = "succeeded"
	projectAssistantAuditOutcomeFailed    projectAssistantAuditOutcome = "failed"
	projectAssistantAuditOutcomePreempted projectAssistantAuditOutcome = "preempted"
	projectAssistantAuditOutcomeAborted   projectAssistantAuditOutcome = "aborted"
)

type projectAssistantAuditPhase struct {
	Phase      projectEinoAssistantPhase `json:"phase"`
	AtOffsetMS int64                     `json:"atOffsetMs"`
}

type projectAssistantAuditTool struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name"`
	Path       string `json:"path,omitempty"`
	Status     string `json:"status"`
	Summary    string `json:"summary,omitempty"`
	AtOffsetMS int64  `json:"atOffsetMs"`
}

type projectAssistantRunAuditRecorder struct {
	mu      sync.Mutex
	run     *store.AssistantRun
	started time.Time
	audit   projectAssistantRunAudit
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

func (r *projectAssistantRunAuditRecorder) recordPhase(phase projectEinoAssistantPhase) {
	r.recordPhaseAt(phase, time.Now().UTC())
}

func (r *projectAssistantRunAuditRecorder) recordPhaseAt(phase projectEinoAssistantPhase, at time.Time) {
	if r == nil || phase == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.audit.PhaseTransitions); n > 0 && r.audit.PhaseTransitions[n-1].Phase == phase {
		return
	}
	if len(r.audit.PhaseTransitions) >= projectAssistantAuditMaxPhases {
		return
	}
	r.audit.PhaseTransitions = append(r.audit.PhaseTransitions, projectAssistantAuditPhase{
		Phase:      phase,
		AtOffsetMS: projectAssistantAuditOffsetMS(r.started, at),
	})
	r.updateRunLocked()
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
		Summary:    projectAssistantAuditToolSummary(event.Name, event.Status),
		AtOffsetMS: projectAssistantAuditOffsetMS(r.started, at),
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
	switch projectToolBaseName(name) {
	case projectToolReadProjectFile, projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
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
