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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/faroshq/provider-app-studio/store"
)

type projectAssistantStartIdentity struct {
	Actor            string                                 `json:"actor"`
	Content          string                                 `json:"content"`
	Mode             store.AssistantRunMode                 `json:"mode"`
	Skills           []string                               `json:"skills,omitempty"`
	ContextResources []projectAssistantContextResourceInput `json:"contextResources,omitempty"`
	ContentParts     []projectAssistantContentPart          `json:"contentParts,omitempty"`
}

func projectAssistantStartRequestDigest(actor, content string, mode store.AssistantRunMode) string {
	return projectAssistantStartRequestDigestWithSkills(actor, content, mode, nil)
}

func projectAssistantStartRequestDigestWithSkills(actor, content string, mode store.AssistantRunMode, skills []string) string {
	return projectAssistantStartRequestDigestWithSelections(actor, content, mode, skills, nil)
}

func projectAssistantStartRequestDigestWithSelections(actor, content string, mode store.AssistantRunMode, skills []string, resources []projectAssistantContextResourceInput) string {
	return projectAssistantStartRequestDigestWithSelectionsAndParts(actor, content, mode, skills, resources, nil)
}

func projectAssistantStartRequestDigestWithSelectionsAndParts(actor, content string, mode store.AssistantRunMode, skills []string, resources []projectAssistantContextResourceInput, parts []projectAssistantContentPart) string {
	canonicalResources := projectAssistantContextResourceIdentities(resources)
	identity := projectAssistantStartIdentity{
		Actor: strings.TrimSpace(actor), Content: strings.TrimSpace(content), Mode: mode,
		Skills: projectAssistantCanonicalSkillIDs(skills), ContextResources: canonicalResources,
		ContentParts: projectAssistantCanonicalContentPartsForIdentity(parts, skills, resources),
	}
	raw, _ := json.Marshal(identity)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func projectAssistantActorDigest(actor string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(actor)))
	return hex.EncodeToString(sum[:])
}

func bindProjectAssistantStartRequest(run *store.AssistantRun, actor, content string) error {
	return bindProjectAssistantStartRequestWithSkills(run, actor, content, nil)
}

func bindProjectAssistantStartRequestWithSkills(run *store.AssistantRun, actor, content string, skills []string) error {
	return bindProjectAssistantStartRequestWithSelections(run, actor, content, skills, nil)
}

func bindProjectAssistantStartRequestWithSelections(run *store.AssistantRun, actor, content string, skills []string, resources []projectAssistantContextResourceInput) error {
	return bindProjectAssistantStartRequestWithSelectionsAndParts(run, actor, content, skills, resources, nil)
}

func bindProjectAssistantStartRequestWithSelectionsAndParts(run *store.AssistantRun, actor, content string, skills []string, resources []projectAssistantContextResourceInput, parts []projectAssistantContentPart) error {
	if run == nil {
		return fmt.Errorf("bind assistant start request: run is required")
	}
	canonicalParts, err := projectAssistantCanonicalContentPartsForIdentityChecked(parts, skills, resources)
	if err != nil {
		return err
	}
	var audit projectAssistantRunAudit
	if len(run.Audit) > 0 {
		if err := json.Unmarshal(run.Audit, &audit); err != nil {
			return fmt.Errorf("decode assistant run audit: %w", err)
		}
	}
	if audit.Version == 0 {
		audit.Version = projectAssistantAuditVersion
	}
	audit.StartRequestDigest = projectAssistantStartRequestDigestWithSelectionsAndParts(actor, content, run.Mode, skills, resources, canonicalParts)
	audit.ActorDigest = projectAssistantActorDigest(actor)
	raw, err := json.Marshal(audit)
	if err != nil {
		return fmt.Errorf("encode assistant run audit: %w", err)
	}
	run.Audit = raw
	return nil
}

func projectAssistantRunActorMatches(run store.AssistantRun, actor string) bool {
	var audit projectAssistantRunAudit
	return len(run.Audit) > 0 && json.Unmarshal(run.Audit, &audit) == nil &&
		audit.ActorDigest != "" && audit.ActorDigest == projectAssistantActorDigest(actor)
}

func findProjectAssistantSteeringReceipt(
	ctx context.Context,
	messages store.Store,
	scope store.Scope,
	run store.AssistantRun,
	actor string,
	content string,
	clientRequestID string,
) (store.Message, bool, error) {
	if messages == nil || !projectAssistantRunActorMatches(run, actor) {
		return store.Message{}, false, fmt.Errorf("%w: assistant steering actor does not own the expected run", store.ErrAssistantRunConflict)
	}
	wantDigest := projectAssistantStartRequestDigest(actor, content, run.Mode)
	for cursor := ""; ; {
		page, err := messages.ListMessages(ctx, scope, 250, cursor)
		if err != nil {
			return store.Message{}, false, err
		}
		for _, message := range page.Items {
			requestID, _ := message.Metadata[projectAssistantSteeringRequestMetadata].(string)
			runID, _ := message.Metadata[projectAssistantSteeringRunMetadata].(string)
			if requestID != clientRequestID || runID != run.ID {
				continue
			}
			digest, _ := message.Metadata[projectAssistantSteeringDigestMetadata].(string)
			if digest != wantDigest || message.ActorID != actor || message.Content != content {
				return store.Message{}, false, fmt.Errorf("%w: steering request ID was already used for different input", store.ErrAssistantRunConflict)
			}
			return message, true, nil
		}
		if page.NextCursor == "" {
			return store.Message{}, false, nil
		}
		cursor = page.NextCursor
	}
}

func validateProjectAssistantStartReplay(run store.AssistantRun, actor, content string, mode store.AssistantRunMode) error {
	return validateProjectAssistantStartReplayWithSkills(run, actor, content, mode, nil)
}

func validateProjectAssistantStartReplayWithSkills(run store.AssistantRun, actor, content string, mode store.AssistantRunMode, skills []string) error {
	return validateProjectAssistantStartReplayWithSelections(run, actor, content, mode, skills, nil)
}

func validateProjectAssistantStartReplayWithSelections(run store.AssistantRun, actor, content string, mode store.AssistantRunMode, skills []string, resources []projectAssistantContextResourceInput) error {
	return validateProjectAssistantStartReplayWithSelectionsAndParts(run, actor, content, mode, skills, resources, nil)
}

func validateProjectAssistantStartReplayWithSelectionsAndParts(run store.AssistantRun, actor, content string, mode store.AssistantRunMode, skills []string, resources []projectAssistantContextResourceInput, parts []projectAssistantContentPart) error {
	var audit projectAssistantRunAudit
	if len(run.Audit) == 0 || json.Unmarshal(run.Audit, &audit) != nil {
		return fmt.Errorf("%w: client request identity is unavailable", store.ErrAssistantRunConflict)
	}
	canonicalParts, err := projectAssistantCanonicalContentPartsForIdentityChecked(parts, skills, resources)
	if err != nil {
		return err
	}
	expected := projectAssistantStartRequestDigestWithSelectionsAndParts(actor, content, mode, skills, resources, canonicalParts)
	if audit.StartRequestDigest == "" || audit.StartRequestDigest != expected {
		return fmt.Errorf("%w: client request ID was already used for different input", store.ErrAssistantRunConflict)
	}
	return nil
}

func (s *Server) recoverProjectAssistantStartReplay(ctx context.Context, scope store.Scope, createErr error, clientRequestID, actor, content string, mode store.AssistantRunMode) (store.AssistantRun, bool) {
	return s.recoverProjectAssistantStartReplayWithSkills(ctx, scope, createErr, clientRequestID, actor, content, mode, nil)
}

func (s *Server) recoverProjectAssistantStartReplayWithSkills(ctx context.Context, scope store.Scope, createErr error, clientRequestID, actor, content string, mode store.AssistantRunMode, skills []string) (store.AssistantRun, bool) {
	return s.recoverProjectAssistantStartReplayWithSelections(ctx, scope, createErr, clientRequestID, actor, content, mode, skills, nil)
}

func (s *Server) recoverProjectAssistantStartReplayWithSelections(ctx context.Context, scope store.Scope, createErr error, clientRequestID, actor, content string, mode store.AssistantRunMode, skills []string, resources []projectAssistantContextResourceInput) (store.AssistantRun, bool) {
	return s.recoverProjectAssistantStartReplayWithSelectionsAndParts(ctx, scope, createErr, clientRequestID, actor, content, mode, skills, resources, nil)
}

func (s *Server) recoverProjectAssistantStartReplayWithSelectionsAndParts(ctx context.Context, scope store.Scope, createErr error, clientRequestID, actor, content string, mode store.AssistantRunMode, skills []string, resources []projectAssistantContextResourceInput, parts []projectAssistantContentPart) (store.AssistantRun, bool) {
	if !errors.Is(createErr, store.ErrAssistantRunConflict) {
		return store.AssistantRun{}, false
	}
	prior, err := s.store.FindAssistantRunByClientRequestID(ctx, scope, clientRequestID)
	if err != nil || validateProjectAssistantStartReplayWithSelectionsAndParts(prior, actor, content, mode, skills, resources, parts) != nil {
		return store.AssistantRun{}, false
	}
	return prior, true
}

func bindProjectAssistantStopRequest(run *store.AssistantRun, actor, clientRequestID string) error {
	clientRequestID = strings.TrimSpace(clientRequestID)
	if run == nil || strings.TrimSpace(actor) == "" || clientRequestID == "" {
		return newValidationError("clientRequestID is required")
	}
	digest := projectAssistantStartRequestDigest(actor, run.ID+":"+clientRequestID, "stop")
	var audit projectAssistantRunAudit
	if len(run.Audit) > 0 {
		if err := json.Unmarshal(run.Audit, &audit); err != nil {
			return fmt.Errorf("decode assistant run audit: %w", err)
		}
	}
	if audit.StopRequestID != "" {
		if audit.StopRequestID != clientRequestID || audit.StopRequestDigest != digest {
			return fmt.Errorf("%w: stop request ID was already used for different input", store.ErrAssistantRunConflict)
		}
		return nil
	}
	if audit.Version == 0 {
		audit.Version = projectAssistantAuditVersion
	}
	audit.StopRequestID = clientRequestID
	audit.StopRequestDigest = digest
	raw, err := json.Marshal(audit)
	if err != nil {
		return fmt.Errorf("encode assistant run audit: %w", err)
	}
	run.Audit = raw
	return nil
}
