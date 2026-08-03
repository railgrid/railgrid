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
	"strings"

	"github.com/faroshq/provider-app-studio/store"
)

// projectAssistantExecutionAuthority is the only mutation-authority surface
// available to the Eino adapter. Its production implementation is owned by
// App Studio and binds every mutation-capable turn to one active durable run.
// Keeping store records and supervisor admission behind this boundary prevents
// conversational orchestration from inventing or widening authority.
type projectAssistantExecutionAuthority interface {
	AdmitMutation(context.Context) error
	PersistRun(context.Context, store.AssistantRun) error
	PersistAudit(context.Context, []byte) error
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
	return a.supervisedRun()
}

// supervisedRun binds persistence and mutation admission to the active durable
// V2 run owned by this provider process.
func (a projectAssistantServerExecutionAuthority) supervisedRun() (store.AssistantRun, error) {
	if a.server == nil || a.server.store == nil || a.req.AssistantRun == nil ||
		strings.TrimSpace(a.req.AssistantRun.ID) == "" || strings.TrimSpace(a.req.Identity.user) == "" {
		return store.AssistantRun{}, store.ErrAssistantRunConflict
	}
	runID := a.req.AssistantRun.ID
	if a.server.projectAssistantSupervisor().accumulatorFor(a.req.MessageScope, runID) == nil {
		return store.AssistantRun{}, store.ErrAssistantRunConflict
	}
	// Only the immutable identity is needed by this authority boundary. Copying
	// the full request-local run would race the audit recorder updating its
	// byte-slice fields while a detached durable persistence is in flight.
	return store.AssistantRun{ID: runID}, nil
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
