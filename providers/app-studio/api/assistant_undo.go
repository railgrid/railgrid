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
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/mux"

	"github.com/faroshq/provider-app-studio/workspace"
)

const projectAssistantMetadataUndo = "assistantUndo"

func (s *Server) undoProjectAssistantRun(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if s.workspaces == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "project workspace store is not configured")
		return
	}
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	runID := mux.Vars(r)["run"]
	run, err := s.store.GetAssistantRun(r.Context(), messageScope, runID)
	if err != nil || s.authorizeProjectAssistantRunActor(r.Context(), messageScope, run, id.user, false) != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", "assistant run not found")
		return
	}
	if !assistantRunTerminal(run.Status) {
		writeStatus(w, http.StatusConflict, "Conflict", "assistant run is still active")
		return
	}
	release, err := s.projectAssistantSupervisor().Reserve(messageScope)
	if err != nil {
		writeStatus(w, http.StatusConflict, "Conflict", "another assistant action is active for this project")
		return
	}
	defer release()

	restored, err := s.workspaces.RestoreSnapshot(r.Context(), projectWorkspaceScope(id, project.Name), run.ID)
	if errors.Is(err, workspace.ErrSnapshotNotFound) {
		writeStatus(w, http.StatusNotFound, "NotFound", "this assistant run has no source changes to undo")
		return
	}
	if errors.Is(err, workspace.ErrSnapshotConflict) {
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	if err != nil {
		writeProjectError(w, err)
		return
	}

	message, err := s.findProjectMessage(r.Context(), messageScope, run.ActiveMessageID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if message.Metadata == nil {
		message.Metadata = map[string]any{}
	}
	message.Metadata[projectAssistantMetadataUndo] = map[string]any{
		"runID":             run.ID,
		"status":            "workspace_restored",
		"fileCount":         len(restored.Files),
		"gitHistoryChanged": false,
	}
	message.UpdatedAt = time.Now().UTC()
	if err := s.store.AppendMessage(r.Context(), messageScope, message); err != nil {
		writeProjectError(w, err)
		return
	}
	s.scheduleDevelopmentSyncAfterMutation(id, project, projectToolWriteFile)
	writeJSON(w, http.StatusOK, map[string]any{
		"runID":     run.ID,
		"fileCount": len(restored.Files),
		"message":   projectMessageToAPI(message),
	})
}
