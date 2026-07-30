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

	"github.com/google/uuid"

	"github.com/faroshq/provider-app-studio/store"
)

// projectAssistantExecutionAuthority is the only mutation-authority surface
// available to the Eino adapter. Its production implementation is owned by
// App Studio and binds every mutation-capable turn to one active WorkItem.
// Keeping store records and supervisor admission behind this boundary prevents
// conversational orchestration from inventing or widening authority.
type projectAssistantExecutionAuthority interface {
	Load(context.Context) (projectAssistantExecutionAuthorityState, error)
	PersistApprovedPlan(context.Context, *projectAssistantApprovedPlan, string) (string, error)
	RetireApprovedPlan(context.Context, string) (string, error)
	PersistInitialExecutionPlan(context.Context, *projectAssistantApprovedPlan) (string, error)
	PromoteAdaptiveRun(context.Context) (store.AssistantRun, error)
	AdmitMutation(context.Context) error
	PersistRun(context.Context, store.AssistantRun) error
	PersistAudit(context.Context, []byte) error
}

type projectAssistantExecutionAuthorityState struct {
	ApprovedPlan          *projectAssistantApprovedPlan
	GrantRevision         string
	ExecutionPlan         *projectAssistantApprovedPlan
	ExecutionPlanRevision string
}

type projectAssistantServerExecutionAuthority struct {
	server *Server
	req    projectAssistantRunRequest
}

func (e projectEinoAssistantEngine) executionAuthority(req projectAssistantRunRequest) projectAssistantExecutionAuthority {
	return projectAssistantExecutionAuthorityFor(e.server, req)
}

func (t projectEinoAssistantTool) executionAuthority() projectAssistantExecutionAuthority {
	return projectAssistantExecutionAuthorityFor(t.server, t.req)
}

func projectAssistantExecutionAuthorityFor(server *Server, req projectAssistantRunRequest) projectAssistantExecutionAuthority {
	if req.executionAuthority != nil {
		return req.executionAuthority
	}
	return projectAssistantServerExecutionAuthority{server: server, req: req}
}

func (a projectAssistantServerExecutionAuthority) durableRun() (store.AssistantRun, error) {
	run, err := a.supervisedRun()
	if err != nil || strings.TrimSpace(run.WorkItemID) == "" {
		return store.AssistantRun{}, store.ErrAssistantWorkItemConflict
	}
	return run, nil
}

// supervisedRun is sufficient for discussion checkpoint/audit persistence.
// Mutation authority additionally requires durableRun, which binds that run to
// an active WorkItem.
func (a projectAssistantServerExecutionAuthority) supervisedRun() (store.AssistantRun, error) {
	if a.server == nil || a.server.store == nil || a.req.AssistantRun == nil ||
		strings.TrimSpace(a.req.AssistantRun.ID) == "" || strings.TrimSpace(a.req.Identity.user) == "" {
		return store.AssistantRun{}, store.ErrAssistantRunConflict
	}
	if a.server.projectAssistantSupervisor().accumulatorFor(a.req.MessageScope, a.req.AssistantRun.ID) == nil {
		return store.AssistantRun{}, store.ErrAssistantRunConflict
	}
	return *a.req.AssistantRun, nil
}

func (a projectAssistantServerExecutionAuthority) Load(ctx context.Context) (projectAssistantExecutionAuthorityState, error) {
	run, err := a.durableRun()
	if err != nil {
		return projectAssistantExecutionAuthorityState{}, err
	}
	plan, revision, err := a.server.loadProjectAssistantWorkItemApprovedPlan(ctx, a.req.MessageScope, a.req.Identity.user, run)
	if err != nil {
		return projectAssistantExecutionAuthorityState{}, err
	}
	item, err := a.server.store.GetAssistantWorkItem(ctx, a.req.MessageScope, run.WorkItemID)
	if err != nil {
		return projectAssistantExecutionAuthorityState{}, err
	}
	state := projectAssistantExecutionAuthorityState{ApprovedPlan: plan, GrantRevision: revision}
	if len(item.ExecutionPlan) == 0 {
		return state, nil
	}
	var executionPlan projectAssistantApprovedPlan
	if err := json.Unmarshal(item.ExecutionPlan, &executionPlan); err != nil {
		return projectAssistantExecutionAuthorityState{}, fmt.Errorf("decode assistant execution plan: %w", err)
	}
	executionPlan = normalizeProjectAssistantApprovedPlan(executionPlan)
	if executionPlan.RunLocal && strings.TrimSpace(executionPlan.Goal) != "" {
		state.ExecutionPlan = &executionPlan
		state.ExecutionPlanRevision = item.ExecutionPlanRevision
	}
	return state, nil
}

func (a projectAssistantServerExecutionAuthority) PersistApprovedPlan(ctx context.Context, plan *projectAssistantApprovedPlan, expectedRevision string) (string, error) {
	run, err := a.durableRun()
	if err != nil {
		return "", err
	}
	return a.server.persistProjectAssistantWorkItemApprovedPlan(ctx, a.req.MessageScope, a.req.Identity.user, run, plan, expectedRevision)
}

func (a projectAssistantServerExecutionAuthority) RetireApprovedPlan(ctx context.Context, expectedRevision string) (string, error) {
	run, err := a.durableRun()
	if err != nil {
		return "", err
	}
	return a.server.retireProjectAssistantWorkItemApprovedPlan(ctx, a.req.MessageScope, a.req.Identity.user, run, expectedRevision)
}

func (a projectAssistantServerExecutionAuthority) PersistInitialExecutionPlan(ctx context.Context, plan *projectAssistantApprovedPlan) (string, error) {
	run, err := a.durableRun()
	if err != nil {
		return "", err
	}
	if plan == nil {
		return "", errors.New("initial execution plan is required")
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	revision := uuid.NewString()
	accumulator := a.server.projectAssistantSupervisor().accumulatorFor(a.req.MessageScope, run.ID)
	if accumulator == nil {
		return "", store.ErrAssistantRunConflict
	}
	if err := accumulator.SaveWorkItemExecutionPlan(ctx, a.req.Identity.user, revision, raw); err != nil {
		return "", err
	}
	return revision, nil
}

func (a projectAssistantServerExecutionAuthority) PromoteAdaptiveRun(ctx context.Context) (store.AssistantRun, error) {
	run, err := a.supervisedRun()
	if err != nil {
		return store.AssistantRun{}, err
	}
	if run.Mode != store.AssistantRunModeAdaptive || strings.TrimSpace(run.WorkItemID) != "" {
		return store.AssistantRun{}, store.ErrAssistantWorkItemConflict
	}
	_, promoted, err := a.server.projectAssistantSupervisor().PromoteAdaptiveRun(
		ctx,
		a.req.MessageScope,
		run.ID,
		a.req.Identity.user,
	)
	return promoted, err
}

func (a projectAssistantServerExecutionAuthority) AdmitMutation(ctx context.Context) error {
	run, err := a.durableRun()
	if err != nil {
		return err
	}
	if a.server.projectAssistantSupervisor().accumulatorFor(a.req.MessageScope, run.ID) == nil {
		return store.ErrAssistantRunConflict
	}
	if err := a.server.projectAssistantSupervisor().AdmitMutation(ctx, a.req.MessageScope, run.ID, a.req.Identity.user); err != nil {
		return err
	}
	return nil
}

func (a projectAssistantServerExecutionAuthority) PersistRun(ctx context.Context, run store.AssistantRun) error {
	bound, err := a.supervisedRun()
	if err != nil || strings.TrimSpace(run.ID) == "" || run.ID != bound.ID {
		return store.ErrAssistantRunConflict
	}
	accumulator := a.server.projectAssistantSupervisor().accumulatorFor(a.req.MessageScope, bound.ID)
	if accumulator == nil {
		return store.ErrAssistantRunConflict
	}
	return accumulator.UpdateRun(ctx, func(current *store.AssistantRun) {
		current.Status = run.Status
		current.RequestID = run.RequestID
		current.Checkpoint = append([]byte(nil), run.Checkpoint...)
		current.Audit = append([]byte(nil), run.Audit...)
	})
}

func (a projectAssistantServerExecutionAuthority) PersistAudit(ctx context.Context, audit []byte) error {
	run, err := a.supervisedRun()
	if err != nil {
		return err
	}
	accumulator := a.server.projectAssistantSupervisor().accumulatorFor(a.req.MessageScope, run.ID)
	if accumulator == nil {
		return store.ErrAssistantRunConflict
	}
	return accumulator.UpdateRun(ctx, func(current *store.AssistantRun) {
		current.Audit = append([]byte(nil), audit...)
	})
}
