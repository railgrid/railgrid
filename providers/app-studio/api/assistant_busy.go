/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"time"

	"k8s.io/klog/v2"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
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
