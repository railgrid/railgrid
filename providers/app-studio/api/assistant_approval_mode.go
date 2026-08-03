// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

type projectAssistantApprovalModeResponse struct {
	Mode      store.AssistantApprovalMode `json:"mode"`
	UpdatedAt time.Time                   `json:"updatedAt,omitempty"`
}

type patchProjectAssistantApprovalModeRequest struct {
	Mode store.AssistantApprovalMode `json:"mode"`
}

func (s *Server) getProjectAssistantApprovalMode(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(id.user) == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "authenticated user is required")
		return
	}
	preference, err := s.store.GetAssistantApprovalPreference(
		r.Context(),
		projectMessageScope(id.orgUUID, id.workspaceUUID, project),
		id.user,
	)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectAssistantApprovalModeResponse{
		Mode:      preference.Mode,
		UpdatedAt: preference.UpdatedAt,
	})
}

func (s *Server) patchProjectAssistantApprovalMode(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(id.user) == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "authenticated user is required")
		return
	}
	var request patchProjectAssistantApprovalModeRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	mode, err := store.NormalizeAssistantApprovalMode(request.Mode)
	if err != nil || strings.TrimSpace(string(request.Mode)) == "" {
		writeProjectError(w, newValidationError("mode must be on_request, always_ask, or never"))
		return
	}
	preference, err := s.store.SetAssistantApprovalPreference(
		r.Context(),
		projectMessageScope(id.orgUUID, id.workspaceUUID, project),
		store.AssistantApprovalPreference{
			ActorID:   id.user,
			Mode:      mode,
			UpdatedAt: time.Now().UTC(),
		},
	)
	if err != nil {
		if errors.Is(err, store.ErrAssistantApprovalModeInvalid) {
			writeProjectError(w, newValidationError("mode must be on_request, always_ask, or never"))
			return
		}
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectAssistantApprovalModeResponse{
		Mode:      preference.Mode,
		UpdatedAt: preference.UpdatedAt,
	})
}

func (s *Server) captureProjectAssistantApprovalMode(ctx context.Context, scope store.Scope, actor string, run *store.AssistantRun) error {
	if run == nil {
		return nil
	}
	preference, err := s.store.GetAssistantApprovalPreference(ctx, scope, actor)
	if err != nil {
		return err
	}
	run.ApprovalMode = preference.Mode
	var audit projectAssistantRunAudit
	if len(run.Audit) > 0 {
		if err := json.Unmarshal(run.Audit, &audit); err != nil {
			return err
		}
	}
	audit.ApprovalMode = preference.Mode
	run.Audit, err = json.Marshal(audit)
	return err
}

func projectAssistantApprovalModeFromRun(run store.AssistantRun) store.AssistantApprovalMode {
	if strings.TrimSpace(string(run.ApprovalMode)) != "" {
		if mode, err := store.NormalizeAssistantApprovalMode(run.ApprovalMode); err == nil {
			return mode
		}
	}
	var audit projectAssistantRunAudit
	if len(run.Audit) > 0 && json.Unmarshal(run.Audit, &audit) == nil {
		if mode, err := store.NormalizeAssistantApprovalMode(audit.ApprovalMode); err == nil {
			return mode
		}
	}
	return store.AssistantApprovalModeOnRequest
}
