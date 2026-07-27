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
	"strings"
	"time"

	"github.com/google/uuid"
	"k8s.io/klog/v2"

	"github.com/faroshq/provider-app-studio/store"
)

// projectAssistantApprovedPlanGrantRunID is a reserved AssistantRun id that
// holds the active plan-approval grant for a project. Real assistant runs use
// "run-<uuid>" ids, so this fixed id never collides. Reusing the AssistantRun
// blob keeps the grant encrypted at rest and persisted per project without a
// new store method or schema migration. The grant lives until the next commit,
// which matches the approval prompt's promise to the user.
const projectAssistantApprovedPlanGrantRunID = "approved-plan-grant"

var errProjectAssistantCheckpointGrantStale = errors.New("assistant checkpoint plan grant is stale")

type projectAssistantApprovedPlanGrantRecord struct {
	Revision string                        `json:"revision"`
	Plan     *projectAssistantApprovedPlan `json:"plan,omitempty"`
}

// projectAssistantInitialCreationPlan is the narrow authorization implied by
// an explicit prompt submitted to create a new Project. It stays in the
// Eino run/checkpoint only; unlike a user-approved plan it is never written to
// the cross-turn grant store. Permission policy limits it to source edits and
// always requires separate template-selection and commit approval.
func projectAssistantInitialCreationPlan() projectAssistantApprovedPlan {
	return normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Summary:        "Initial project creation prompt authorizes source edits for this run.",
		Operations:     []string{projectToolWriteFile, projectToolApplyPatch, projectToolMkdir},
		AllowAllWrites: true,
		ApprovedAt:     time.Now().UTC(),
		ApprovalTool:   "project_create_prompt",
		RunLocal:       true,
	})
}

func projectAssistantApprovedPlanScopeReady(scope store.Scope) bool {
	return scope.OrgUUID != "" && scope.WorkspaceUUID != "" && scope.ProjectName != ""
}

// loadProjectAssistantApprovedPlan returns the active cross-turn plan grant for
// a project, or nil when none is active. It is best effort: any load failure is
// logged and treated as "no grant" so a single bad read never blocks a turn.
func (s *Server) loadProjectAssistantApprovedPlan(ctx context.Context, scope store.Scope) *projectAssistantApprovedPlan {
	plan, _, err := s.loadProjectAssistantApprovedPlanGrant(ctx, scope)
	if err != nil {
		klog.FromContext(ctx).V(4).Info("no active App Studio plan grant", "project", scope.ProjectName, "reason", err.Error())
		return nil
	}
	return plan
}

func (s *Server) loadProjectAssistantApprovedPlanGrant(
	ctx context.Context,
	scope store.Scope,
) (*projectAssistantApprovedPlan, string, error) {
	if s == nil || s.store == nil || !projectAssistantApprovedPlanScopeReady(scope) {
		return nil, "", errors.New("assistant plan grant store is not configured")
	}
	run, err := s.store.GetAssistantRun(ctx, scope, projectAssistantApprovedPlanGrantRunID)
	if err != nil {
		if errors.Is(err, store.ErrAssistantRunNotFound) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("read App Studio plan grant: %w", err)
	}
	if len(run.Checkpoint) == 0 {
		return nil, "", nil
	}

	var record projectAssistantApprovedPlanGrantRecord
	if err := json.Unmarshal(run.Checkpoint, &record); err != nil {
		return nil, "", fmt.Errorf("decode App Studio plan grant record: %w", err)
	}
	if strings.TrimSpace(record.Revision) != "" {
		if run.RequestID != "" && run.RequestID != strings.TrimSpace(record.Revision) {
			return nil, "", errors.New("app studio plan grant revision metadata does not match its payload")
		}
		if record.Plan == nil || len(record.Plan.Operations) == 0 {
			return nil, strings.TrimSpace(record.Revision), nil
		}
		normalized := normalizeProjectAssistantApprovedPlan(*record.Plan)
		return &normalized, strings.TrimSpace(record.Revision), nil
	}

	// Compatibility for pre-revision active grants. Their persisted update
	// timestamp is a stable generation token; an empty legacy object is no
	// authority and has no generation.
	var legacy projectAssistantApprovedPlan
	if err := json.Unmarshal(run.Checkpoint, &legacy); err != nil {
		return nil, "", fmt.Errorf("decode legacy App Studio plan grant: %w", err)
	}
	if len(legacy.Operations) == 0 {
		revision, err := s.retireProjectAssistantApprovedPlan(ctx, scope, "")
		if errors.Is(err, store.ErrAssistantRunConflict) {
			return s.loadProjectAssistantApprovedPlanGrant(ctx, scope)
		}
		if err != nil {
			return nil, "", fmt.Errorf("migrate legacy App Studio plan tombstone: %w", err)
		}
		return nil, revision, nil
	}
	normalized := normalizeProjectAssistantApprovedPlan(legacy)
	revision, err := s.persistProjectAssistantApprovedPlan(ctx, scope, &normalized, "")
	if errors.Is(err, store.ErrAssistantRunConflict) {
		// Another pod migrated or replaced the legacy row first. Reload the
		// now-authoritative record instead of trusting the stale payload.
		return s.loadProjectAssistantApprovedPlanGrant(ctx, scope)
	}
	if err != nil {
		return nil, "", fmt.Errorf("migrate legacy App Studio plan grant: %w", err)
	}
	return &normalized, revision, nil
}

// projectAssistantApprovedPlanForCheckpointResume reconciles authorization
// captured in a pending checkpoint with the current durable grant record. The
// durable record is authoritative so a commit tombstone cannot be bypassed by
// resuming an older checkpoint.
func (s *Server) projectAssistantApprovedPlanForCheckpointResume(
	ctx context.Context,
	scope store.Scope,
	checkpointPlan *projectAssistantApprovedPlan,
	checkpointRevision string,
) (*projectAssistantApprovedPlan, string, error) {
	activePlan, activeRevision, err := s.loadProjectAssistantApprovedPlanGrant(ctx, scope)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(checkpointRevision) != activeRevision {
		return nil, activeRevision, fmt.Errorf(
			"%w: checkpoint revision %q does not match active revision %q",
			errProjectAssistantCheckpointGrantStale,
			strings.TrimSpace(checkpointRevision),
			activeRevision,
		)
	}
	if checkpointPlan == nil {
		return nil, activeRevision, nil
	}
	if checkpointPlan.RunLocal {
		return cloneProjectAssistantApprovedPlan(checkpointPlan), activeRevision, nil
	}
	return activePlan, activeRevision, nil
}

func (s *Server) saveProjectAssistantApprovedPlan(ctx context.Context, scope store.Scope, plan *projectAssistantApprovedPlan) error {
	_, revision, err := s.loadProjectAssistantApprovedPlanGrant(ctx, scope)
	if err != nil {
		return err
	}
	_, err = s.persistProjectAssistantApprovedPlan(ctx, scope, plan, revision)
	return err
}

func (s *Server) persistProjectAssistantApprovedPlan(
	ctx context.Context,
	scope store.Scope,
	plan *projectAssistantApprovedPlan,
	expectedRevision string,
) (string, error) {
	if s == nil || s.store == nil || plan == nil || plan.RunLocal || !projectAssistantApprovedPlanScopeReady(scope) {
		return "", nil
	}
	revision := uuid.NewString()
	raw, err := json.Marshal(projectAssistantApprovedPlanGrantRecord{
		Revision: revision,
		Plan:     cloneProjectAssistantApprovedPlan(plan),
	})
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if err := s.store.CompareAndSwapAssistantRun(ctx, scope, store.AssistantRun{
		ID:         projectAssistantApprovedPlanGrantRunID,
		Status:     store.AssistantRunStatusCompleted,
		RequestID:  revision,
		Checkpoint: raw,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, strings.TrimSpace(expectedRevision)); err != nil {
		return "", err
	}
	return revision, nil
}

// clearProjectAssistantApprovedPlan retires the active grant by persisting an
// empty payload, so the next edit turn prompts for plan approval again.
func (s *Server) clearProjectAssistantApprovedPlan(ctx context.Context, scope store.Scope) error {
	_, revision, err := s.loadProjectAssistantApprovedPlanGrant(ctx, scope)
	if err != nil {
		return err
	}
	_, err = s.retireProjectAssistantApprovedPlan(ctx, scope, revision)
	return err
}

func (s *Server) retireProjectAssistantApprovedPlan(
	ctx context.Context,
	scope store.Scope,
	expectedRevision string,
) (string, error) {
	if s == nil || s.store == nil || !projectAssistantApprovedPlanScopeReady(scope) {
		return "", nil
	}
	revision := uuid.NewString()
	raw, err := json.Marshal(projectAssistantApprovedPlanGrantRecord{Revision: revision})
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if err := s.store.CompareAndSwapAssistantRun(ctx, scope, store.AssistantRun{
		ID:         projectAssistantApprovedPlanGrantRunID,
		Status:     store.AssistantRunStatusCompleted,
		RequestID:  revision,
		Checkpoint: raw,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, strings.TrimSpace(expectedRevision)); err != nil {
		return "", err
	}
	return revision, nil
}

// mergeProjectAssistantApprovedPlans keeps the latest plan's narrative while
// unioning the approved path/operation envelope, so a re-stated plan can only
// widen what is already allowed, never silently shrink it mid-session.
func mergeProjectAssistantApprovedPlans(existing, next projectAssistantApprovedPlan) projectAssistantApprovedPlan {
	merged := next
	merged.TargetPaths = normalizeProjectAssistantStringList(append(append([]string(nil), existing.TargetPaths...), next.TargetPaths...))
	merged.Operations = normalizeProjectAssistantStringList(append(append([]string(nil), existing.Operations...), next.Operations...))
	merged.AllowAllWrites = existing.AllowAllWrites || next.AllowAllWrites
	merged.RunLocal = existing.RunLocal || next.RunLocal
	return merged
}
