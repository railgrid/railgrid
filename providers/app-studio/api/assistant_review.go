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
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/faroshq/provider-app-studio/store"
)

const (
	projectAssistantReviewTargetCurrentWorkspace = "current_workspace"
	projectAssistantReviewInstructionsMaxBytes   = 8 << 10
)

// assistantThreadReviewStartRequest is the App Studio adaptation of Codex's
// review/start contract. A review always gets its own durable Turn and an
// explicit target; it is never an implicit completion gate.
type assistantThreadReviewStartRequest struct {
	Target              assistantReviewTarget                  `json:"target"`
	ClientUserMessageID string                                 `json:"clientUserMessageID"`
	Skills              []string                               `json:"skills,omitempty"`
	ContextResources    []projectAssistantContextResourceInput `json:"contextResources,omitempty"`
	ContentParts        []projectAssistantContentPart          `json:"contentParts,omitempty"`
}

type assistantReviewTarget struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions,omitempty"`
}

func (r assistantThreadReviewStartRequest) turnRequest() (assistantThreadTurnCreateRequest, error) {
	r.ClientUserMessageID = strings.TrimSpace(r.ClientUserMessageID)
	r.Target.Type = strings.ToLower(strings.TrimSpace(r.Target.Type))
	r.Target.Instructions = strings.TrimSpace(r.Target.Instructions)
	if r.ClientUserMessageID == "" {
		return assistantThreadTurnCreateRequest{}, newValidationError("clientUserMessageID is required")
	}
	if r.Target.Type != projectAssistantReviewTargetCurrentWorkspace {
		return assistantThreadTurnCreateRequest{}, newValidationError("review target.type must be current_workspace")
	}
	if !utf8.ValidString(r.Target.Instructions) || len(r.Target.Instructions) > projectAssistantReviewInstructionsMaxBytes {
		return assistantThreadTurnCreateRequest{}, newValidationError(fmt.Sprintf("review target.instructions must be valid UTF-8 and at most %d bytes", projectAssistantReviewInstructionsMaxBytes))
	}
	content := r.Target.Instructions
	if content == "" {
		content = "Review the current Project workspace."
	}
	return assistantThreadTurnCreateRequest{
		Content:             content,
		ClientUserMessageID: r.ClientUserMessageID,
		CollaborationMode:   store.AssistantRunModeReview,
		Skills:              r.Skills,
		ContextResources:    r.ContextResources,
		ContentParts:        r.ContentParts,
	}, nil
}

func (s *Server) startProjectAssistantThreadReview(w http.ResponseWriter, r *http.Request) {
	c, id, project, thread, ok := s.requireOwnedAssistantThread(w, r)
	if !ok {
		return
	}
	var request assistantThreadReviewStartRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	turnRequest, err := request.turnRequest()
	if err != nil {
		writeProjectError(w, err)
		return
	}
	s.startProjectAssistantThreadExecution(w, r, c, id, project, thread, turnRequest)
}
