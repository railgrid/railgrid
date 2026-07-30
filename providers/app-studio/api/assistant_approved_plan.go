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

	"github.com/faroshq/provider-app-studio/store"
)

var errProjectAssistantCheckpointGrantStale = errors.New("assistant checkpoint plan grant is stale")

// projectAssistantInitialCreationPlan is the narrow authorization implied by
// an explicit prompt submitted to create a new Project. It stays in the
// Eino run/checkpoint only; unlike a user-approved plan it is never written to
// the cross-turn grant store. Permission policy limits it to source edits and
// always requires separate template-selection and commit approval.
func projectAssistantInitialCreationPlan(goal ...string) projectAssistantApprovedPlan {
	initialGoal := ""
	if len(goal) > 0 {
		initialGoal = strings.TrimSpace(goal[0])
	}
	return normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Goal:         initialGoal,
		Summary:      "Initial project creation prompt authorizes source edits for this run.",
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		// Initial creation does not know the generated project paths yet.
		AllowAllWrites: true,
		ApprovedAt:     time.Now().UTC(),
		ApprovalTool:   "project_create_prompt",
		RunLocal:       true,
	})
}

// loadProjectAssistantWorkItemApprovedPlan is the mutation-run authority
// boundary. A run may only recover the grant owned by its exact WorkItem,
// creator, and expected grant revision.
func (s *Server) loadProjectAssistantWorkItemApprovedPlan(
	ctx context.Context,
	scope store.Scope,
	actor string,
	run store.AssistantRun,
) (*projectAssistantApprovedPlan, string, error) {
	if s == nil || s.store == nil || strings.TrimSpace(run.WorkItemID) == "" {
		return nil, "", nil
	}
	item, err := s.store.GetAssistantWorkItem(ctx, scope, run.WorkItemID)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(actor) == "" || item.CreatedBy != actor || item.ActiveRunID != run.ID ||
		item.Status != store.AssistantWorkItemStatusActive {
		return nil, "", store.ErrAssistantWorkItemConflict
	}
	if run.ExpectedGrantRevision != item.GrantRevision {
		return nil, item.GrantRevision, errProjectAssistantCheckpointGrantStale
	}
	if len(item.PlanGrant) == 0 {
		return nil, item.GrantRevision, nil
	}
	var plan projectAssistantApprovedPlan
	if err := json.Unmarshal(item.PlanGrant, &plan); err != nil {
		return nil, item.GrantRevision, fmt.Errorf("decode WorkItem plan grant: %w", err)
	}
	normalized := normalizeProjectAssistantApprovedPlan(plan)
	if !projectAssistantApprovedPlanActive(&normalized) {
		return nil, item.GrantRevision, nil
	}
	return &normalized, item.GrantRevision, nil
}

func (s *Server) persistProjectAssistantWorkItemApprovedPlan(
	ctx context.Context,
	scope store.Scope,
	actor string,
	run store.AssistantRun,
	plan *projectAssistantApprovedPlan,
	expectedRevision string,
) (string, error) {
	if s == nil || s.store == nil || plan == nil || plan.RunLocal || run.WorkItemID == "" {
		return "", nil
	}
	raw, err := json.Marshal(normalizeProjectAssistantApprovedPlan(*plan))
	if err != nil {
		return "", err
	}
	revision := uuid.NewString()
	if accumulator := s.projectAssistantSupervisor().accumulatorFor(scope, run.ID); accumulator != nil {
		if err := accumulator.ApproveWorkItemPlan(ctx, actor, expectedRevision, revision, raw); err != nil {
			return "", err
		}
		return revision, nil
	}
	item, err := s.store.GetAssistantWorkItem(ctx, scope, run.WorkItemID)
	if err != nil {
		return "", err
	}
	if actor == "" || item.CreatedBy != actor || item.ActiveRunID != run.ID ||
		item.Status != store.AssistantWorkItemStatusActive || item.GrantRevision != strings.TrimSpace(expectedRevision) {
		return "", store.ErrAssistantWorkItemConflict
	}
	if _, err := s.store.ApproveWorkItemPlan(ctx, scope, item.ID, run.ID, item.Revision, revision, raw, time.Now().UTC()); err != nil {
		return "", err
	}
	return revision, nil
}

// retireProjectAssistantWorkItemApprovedPlan consumes the WorkItem-owned plan
// grant before a commit permission checkpoint. The store updates the WorkItem
// and active run atomically, retaining their lifecycle/checkpoint while writing
// the shared tombstone revision. A resumed checkpoint can therefore not revive
// the grant after the commit request.
func (s *Server) retireProjectAssistantWorkItemApprovedPlan(
	ctx context.Context,
	scope store.Scope,
	actor string,
	run store.AssistantRun,
	expectedGrantRevision string,
) (string, error) {
	if s == nil || s.store == nil || strings.TrimSpace(run.WorkItemID) == "" {
		return "", store.ErrAssistantWorkItemConflict
	}
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(expectedGrantRevision) == "" {
		return "", store.ErrAssistantWorkItemConflict
	}
	accumulator := s.projectAssistantSupervisor().accumulatorFor(scope, run.ID)
	if accumulator == nil {
		return "", store.ErrAssistantRunConflict
	}
	tombstoneRevision := uuid.NewString()
	if err := accumulator.RetireWorkItemPlan(
		ctx,
		actor,
		strings.TrimSpace(expectedGrantRevision),
		tombstoneRevision,
	); err != nil {
		return "", err
	}
	return tombstoneRevision, nil
}

// mergeProjectAssistantApprovedPlans is used only after a separate direct
// user approval. Model-authored plan restatements never call this helper.
func mergeProjectAssistantApprovedPlans(existing, next projectAssistantApprovedPlan) projectAssistantApprovedPlan {
	merged := next
	merged.TargetPaths = normalizeProjectAssistantStringList(append(append([]string(nil), existing.TargetPaths...), next.TargetPaths...))
	merged.Capabilities = normalizeProjectAssistantStringList(append(append([]string(nil), existing.Capabilities...), next.Capabilities...))
	if existing.Version > merged.Version {
		merged.Version = existing.Version
	}
	merged.AllowAllWrites = existing.AllowAllWrites || next.AllowAllWrites
	merged.RunLocal = existing.RunLocal || next.RunLocal
	return merged
}
