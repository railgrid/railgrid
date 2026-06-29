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
	"time"

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

func projectAssistantApprovedPlanScopeReady(scope store.Scope) bool {
	return scope.OrgUUID != "" && scope.WorkspaceUUID != "" && scope.ProjectName != ""
}

// loadProjectAssistantApprovedPlan returns the active cross-turn plan grant for
// a project, or nil when none is active. It is best effort: any load failure is
// logged and treated as "no grant" so a single bad read never blocks a turn.
func (s *Server) loadProjectAssistantApprovedPlan(ctx context.Context, scope store.Scope) *projectAssistantApprovedPlan {
	if s == nil || s.store == nil || !projectAssistantApprovedPlanScopeReady(scope) {
		return nil
	}
	run, err := s.store.GetAssistantRun(ctx, scope, projectAssistantApprovedPlanGrantRunID)
	if err != nil {
		// Missing grant is the common case (returned as an error); only note
		// it at high verbosity so genuine store errors remain discoverable.
		klog.FromContext(ctx).V(4).Info("no active App Studio plan grant", "project", scope.ProjectName, "reason", err.Error())
		return nil
	}
	if len(run.Checkpoint) == 0 {
		return nil
	}
	var plan projectAssistantApprovedPlan
	if err := json.Unmarshal(run.Checkpoint, &plan); err != nil {
		klog.FromContext(ctx).Error(err, "decode App Studio plan grant", "project", scope.ProjectName)
		return nil
	}
	if len(plan.Operations) == 0 {
		// A cleared grant is persisted as an empty object; treat it as none.
		return nil
	}
	normalized := normalizeProjectAssistantApprovedPlan(plan)
	return &normalized
}

func (s *Server) saveProjectAssistantApprovedPlan(ctx context.Context, scope store.Scope, plan *projectAssistantApprovedPlan) error {
	if s == nil || s.store == nil || plan == nil || !projectAssistantApprovedPlanScopeReady(scope) {
		return nil
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.store.SaveAssistantRun(ctx, scope, store.AssistantRun{
		ID:         projectAssistantApprovedPlanGrantRunID,
		Status:     store.AssistantRunStatusCompleted,
		Checkpoint: raw,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

// clearProjectAssistantApprovedPlan retires the active grant by persisting an
// empty payload, so the next edit turn prompts for plan approval again.
func (s *Server) clearProjectAssistantApprovedPlan(ctx context.Context, scope store.Scope) error {
	if s == nil || s.store == nil || !projectAssistantApprovedPlanScopeReady(scope) {
		return nil
	}
	now := time.Now().UTC()
	return s.store.SaveAssistantRun(ctx, scope, store.AssistantRun{
		ID:         projectAssistantApprovedPlanGrantRunID,
		Status:     store.AssistantRunStatusCompleted,
		Checkpoint: json.RawMessage(`{}`),
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

// mergeProjectAssistantApprovedPlans keeps the latest plan's narrative while
// unioning the approved path/operation envelope, so a re-stated plan can only
// widen what is already allowed, never silently shrink it mid-session.
func mergeProjectAssistantApprovedPlans(existing, next projectAssistantApprovedPlan) projectAssistantApprovedPlan {
	merged := next
	merged.TargetPaths = normalizeProjectAssistantStringList(append(append([]string(nil), existing.TargetPaths...), next.TargetPaths...))
	merged.Operations = normalizeProjectAssistantStringList(append(append([]string(nil), existing.Operations...), next.Operations...))
	return merged
}
