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
	Actor            string                 `json:"actor"`
	Content          string                 `json:"content"`
	Mode             store.AssistantRunMode `json:"mode"`
	WorkItemID       string                 `json:"workItemID,omitempty"`
	WorkItemRevision int64                  `json:"workItemRevision,omitempty"`
	InitialPrompt    bool                   `json:"initialProjectPrompt,omitempty"`
}

type projectAssistantCancelReceipt struct {
	Kind            string `json:"kind"`
	ClientRequestID string `json:"clientRequestID"`
	Digest          string `json:"digest"`
}

func projectAssistantStartRequestDigest(actor, content string, mode store.AssistantRunMode, workItemID string, workItemRevision int64, initialProjectPrompt ...bool) string {
	identity := projectAssistantStartIdentity{
		Actor:            strings.TrimSpace(actor),
		Content:          strings.TrimSpace(content),
		Mode:             mode,
		WorkItemID:       strings.TrimSpace(workItemID),
		WorkItemRevision: workItemRevision,
	}
	if len(initialProjectPrompt) > 0 {
		identity.InitialPrompt = initialProjectPrompt[0]
	}
	raw, _ := json.Marshal(identity)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func bindProjectAssistantStartRequest(run *store.AssistantRun, actor, content, workItemID string, workItemRevision int64, initialProjectPrompt ...bool) error {
	if run == nil {
		return fmt.Errorf("bind assistant start request: run is required")
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
	audit.StartRequestDigest = projectAssistantStartRequestDigest(actor, content, run.Mode, workItemID, workItemRevision, initialProjectPrompt...)
	raw, err := json.Marshal(audit)
	if err != nil {
		return fmt.Errorf("encode assistant run audit: %w", err)
	}
	run.Audit = raw
	return nil
}

func validateProjectAssistantStartReplay(run store.AssistantRun, actor, content string, mode store.AssistantRunMode, workItemID string, workItemRevision int64, initialProjectPrompt ...bool) error {
	var audit projectAssistantRunAudit
	if len(run.Audit) == 0 || json.Unmarshal(run.Audit, &audit) != nil {
		return fmt.Errorf("%w: client request identity is unavailable", store.ErrAssistantRunConflict)
	}
	expected := projectAssistantStartRequestDigest(actor, content, mode, workItemID, workItemRevision, initialProjectPrompt...)
	if audit.StartRequestDigest == "" || audit.StartRequestDigest != expected {
		return fmt.Errorf("%w: client request ID was already used for different input", store.ErrAssistantRunConflict)
	}
	return nil
}

func (s *Server) recoverProjectAssistantStartReplay(ctx context.Context, scope store.Scope, createErr error, clientRequestID, actor, content string, mode store.AssistantRunMode, workItemID string, workItemRevision int64, initialProjectPrompt ...bool) (store.AssistantRun, bool) {
	if !errors.Is(createErr, store.ErrAssistantRunConflict) {
		return store.AssistantRun{}, false
	}
	prior, err := s.store.FindAssistantRunByClientRequestID(ctx, scope, clientRequestID)
	if err != nil || validateProjectAssistantStartReplay(prior, actor, content, mode, workItemID, workItemRevision, initialProjectPrompt...) != nil {
		return store.AssistantRun{}, false
	}
	return prior, true
}

func bindProjectAssistantStopRequest(run *store.AssistantRun, actor, clientRequestID string) error {
	clientRequestID = strings.TrimSpace(clientRequestID)
	if run == nil || strings.TrimSpace(actor) == "" || clientRequestID == "" {
		return newValidationError("clientRequestID is required")
	}
	digest := projectAssistantStartRequestDigest(actor, run.ID, "stop", clientRequestID, 0)
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

func projectAssistantCancelRequestReceipt(actor, workItemID, clientRequestID string, revision int64) projectAssistantCancelReceipt {
	return projectAssistantCancelReceipt{
		Kind:            "cancel_receipt",
		ClientRequestID: strings.TrimSpace(clientRequestID),
		Digest:          projectAssistantStartRequestDigest(actor, clientRequestID, "cancel", workItemID, revision),
	}
}

func encodeProjectAssistantCancelReceipt(receipt projectAssistantCancelReceipt) (json.RawMessage, error) {
	return json.Marshal(receipt)
}

func validateProjectAssistantCancelReplay(item store.AssistantWorkItem, actor, clientRequestID string, revision int64) error {
	var receipt projectAssistantCancelReceipt
	if json.Unmarshal(item.PlanGrant, &receipt) != nil {
		return fmt.Errorf("%w: cancellation request identity is unavailable", store.ErrAssistantWorkItemConflict)
	}
	expected := projectAssistantCancelRequestReceipt(actor, item.ID, clientRequestID, revision)
	if receipt.Kind != expected.Kind || receipt.ClientRequestID != expected.ClientRequestID || receipt.Digest != expected.Digest {
		return fmt.Errorf("%w: cancellation request ID was already used for different input", store.ErrAssistantWorkItemConflict)
	}
	return nil
}
