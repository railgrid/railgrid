/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"

	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

// AssistantBusy reports whether an assistant turn or a reserved external
// operation currently owns the project's workspace — on ANY replica, not just
// this one. The Project reconciler's commit convergence gates on this:
// committing mid-turn would capture a half-written app. The local maps answer
// for this replica's activity; the durable activity claim answers for the
// fleet, and an unreadable store fails busy (never commit on uncertainty).
func (s *Server) AssistantBusy(scope workspace.Scope) bool {
	if s == nil {
		return false
	}
	key := projectAssistantRunKey{
		OrgUUID:       scope.OrgUUID,
		WorkspaceUUID: scope.WorkspaceUUID,
		ProjectName:   scope.ProjectName,
		ProjectUID:    scope.ProjectUID,
	}
	storeScope := store.Scope{
		OrgUUID:       scope.OrgUUID,
		WorkspaceUUID: scope.WorkspaceUUID,
		ProjectName:   scope.ProjectName,
		ProjectUID:    scope.ProjectUID,
	}
	if s.assistantRunManager != nil && s.assistantRunManager.busy(key) {
		return true
	}
	if s.assistantSupervisor != nil && s.assistantSupervisor.reserved(storeScope) {
		return true
	}
	if s.store == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	claim, ok, err := s.store.GetReplicaClaim(ctx, store.ActivityClaimKey(storeScope))
	if err != nil {
		klog.Background().Error(err, "reading assistant activity claim; treating project as busy",
			"org", scope.OrgUUID, "workspace", scope.WorkspaceUUID, "project", scope.ProjectName)
		return true
	}
	return ok && claim.Live(time.Now().UTC(), assistantActivityClaimTTL)
}

// StopAssistantForDeletedProject interrupts every active thread run for a
// project whose CR is being deleted, and reconciles runs this replica no
// longer owns.
//
// It exists because Cut D.4 turned deletion into a plain CR delete. The old
// `projects/{p}/delete` verb answered 409 while a turn was running and asked
// the user to stop it first; a delete of the object cannot answer 409 — by the
// time the finalizer runs, the deletionTimestamp is already set and the only
// correct answer is "yes". So the run is stopped here, and the finalizer waits
// for AssistantBusy to go false before purging anything the turn might still
// be writing to (controller/project/teardown.go).
//
// Each run is stopped independently so one thread cannot keep a sibling
// thread's controller state or cancellation boundary hidden from deletion.
func (s *Server) StopAssistantForDeletedProject(ctx context.Context, scope workspace.Scope) error {
	if s == nil || s.store == nil {
		return nil
	}
	storeScope := store.Scope{
		OrgUUID:       scope.OrgUUID,
		WorkspaceUUID: scope.WorkspaceUUID,
		ProjectName:   scope.ProjectName,
		ProjectUID:    scope.ProjectUID,
	}
	runs, err := s.store.ListActiveAssistantRuns(ctx, storeScope)
	if err != nil {
		return fmt.Errorf("read the project's active assistant runs: %w", err)
	}
	for _, run := range runs {
		_, stoppedLocally, err := s.projectAssistantSupervisor().Stop(storeScope, run.ID, run.ThreadID)
		if err != nil {
			return fmt.Errorf("stop assistant run %s on thread %s: %w", run.ID, run.ThreadID, err)
		}
		if !stoppedLocally {
			// A run owned by another replica (or by a previous incarnation of this
			// one) is not in the local map, so Stop does nothing. The per-run claim
			// lets recovery settle only this run; a live sibling thread is ignored.
			if err := s.reconcileOrphanedProjectAssistantRun(ctx, storeScope, run.ID); err != nil {
				return err
			}
		}
	}
	remaining, err := s.store.ListActiveAssistantRuns(ctx, storeScope)
	if err != nil {
		return fmt.Errorf("verify project assistant runs stopped: %w", err)
	}
	if len(remaining) > 0 {
		// Stop publishes cancellation before the worker reaches its terminal
		// checkpoint. The project finalizer's Busy gate waits for that last
		// write to settle before it purges the workspace and durable transcript.
		return nil
	}
	s.forgetProjectDeletedCaches(storeScope)
	return nil
}

// forgetProjectDeletedCaches drops this replica's in-process caches for a
// project that is going away. They are keyed by project and nothing else would
// ever evict them, so a long-lived replica would otherwise accumulate one
// entry per deleted project.
func (s *Server) forgetProjectDeletedCaches(scope store.Scope) {
	s.forgetProjectThumbnailCaptureScope(scope)
}
