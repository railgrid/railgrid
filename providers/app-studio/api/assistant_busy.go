/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

// AssistantBusy reports whether an assistant turn or a reserved external
// operation currently owns the project's workspace. The Project reconciler's
// commit convergence gates on this: committing mid-turn would capture a
// half-written app.
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
	if s.assistantRunManager != nil && s.assistantRunManager.busy(key) {
		return true
	}
	if s.assistantSupervisor != nil && s.assistantSupervisor.reserved(store.Scope{
		OrgUUID:       scope.OrgUUID,
		WorkspaceUUID: scope.WorkspaceUUID,
		ProjectName:   scope.ProjectName,
		ProjectUID:    scope.ProjectUID,
	}) {
		return true
	}
	return false
}
