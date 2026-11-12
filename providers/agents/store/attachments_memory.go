/*
Copyright 2026 The Railgrid Authors.

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

package store

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/railgrid/provider-sdk/attachments"
)

func (m *MemoryStore) validateCheckpointAttachmentsLocked(scope Scope, sessionID string, checkpoint *SessionCheckpoint) error {
	if checkpoint == nil {
		return nil
	}
	for _, message := range checkpoint.ReplacementHistory {
		for _, receipt := range message.InputAttachments {
			found := false
			for key, item := range m.agentAttachments {
				if hasPrefix(key, tenantKey(scope)+"|") && item.AgentName == scope.AgentName && item.SessionID == sessionID &&
					item.ActorID == message.InputAttachmentOwner && item.BindingID == message.InputAttachmentBindingID &&
					item.ID == receipt.ID && !item.Draft && attachments.ReceiptsMatch(receipt, item.Receipt()) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%w: checkpoint references an unavailable attachment", ErrSessionCheckpointStale)
			}
		}
	}
	return nil
}

func (m *MemoryStore) bumpSessionSummaryGenerationLocked(scope Scope, sessionID string) {
	key := sessionKey(scope, sessionID)
	m.summaryGenerations[key]++
	delete(m.summaries, key)
}

func (m *MemoryStore) ConfigureAgentAttachmentQuota(quota AgentAttachmentQuota) error {
	if err := agentAttachmentQuotaValid(quota); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attachmentQuota = quota
	return nil
}

func (m *MemoryStore) CreateAgentAttachment(_ context.Context, scope Scope, attachment AgentAttachment) (AgentAttachment, bool, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return AgentAttachment{}, false, err
	}
	if attachment.AgentName != scope.AgentName || !attachment.Draft || attachment.BindingID != "" {
		return AgentAttachment{}, false, fmt.Errorf("uploads must be unbound drafts for the addressed Agent")
	}
	if err := attachment.validate(); err != nil {
		return AgentAttachment{}, false, err
	}
	attachment.CreatedAt = attachment.CreatedAt.UTC()
	attachment.ContentType, _ = attachments.NormalizeAttachmentContentType(attachment.ContentType)
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	now := attachment.CreatedAt
	workspacePrefix := tenantKey(scope) + "|"
	for key, existing := range m.agentAttachments {
		if hasPrefix(key, workspacePrefix) && attachmentExpired(existing, now) {
			delete(m.agentAttachments, key)
		}
	}
	key := agentAttachmentScopeKey(scope, attachment.AgentUID, attachment.ID)
	if existing, ok := m.agentAttachments[key]; ok {
		if existing.ActorID == attachment.ActorID && existing.SessionID == attachment.SessionID &&
			attachments.UploadMetadataMatches(existing.Receipt(), attachment.Receipt()) && bytes.Equal(existing.Data, attachment.Data) {
			return existing.clone(), false, nil
		}
		return AgentAttachment{}, false, ErrAgentAttachmentConflict
	}
	if err := m.checkAgentAttachmentQuotaLocked(scope, attachment, now); err != nil {
		return AgentAttachment{}, false, err
	}
	m.agentAttachments[key] = attachment.clone()
	return attachment.clone(), true, nil
}

func (m *MemoryStore) checkAgentAttachmentQuotaLocked(scope Scope, candidate AgentAttachment, now time.Time) error {
	quota := m.attachmentQuota
	var workspaceBytes, draftBytes int64
	draftCount := 0
	workspacePrefix := tenantKey(scope) + "|"
	for key, existing := range m.agentAttachments {
		if !hasPrefix(key, workspacePrefix) || attachmentExpired(existing, now) {
			continue
		}
		workspaceBytes += existing.SizeBytes
		if existing.Draft && existing.AgentName == scope.AgentName && existing.ActorID == candidate.ActorID {
			draftBytes += existing.SizeBytes
			draftCount++
		}
	}
	if quota.WorkspaceMaxBytes > 0 && workspaceBytes+candidate.SizeBytes > quota.WorkspaceMaxBytes {
		return ErrAgentAttachmentQuota
	}
	if quota.DraftAgentMaxBytes > 0 && draftBytes+candidate.SizeBytes > quota.DraftAgentMaxBytes {
		return ErrAgentAttachmentQuota
	}
	if quota.DraftAgentMaxCount > 0 && draftCount+1 > quota.DraftAgentMaxCount {
		return ErrAgentAttachmentQuota
	}
	return nil
}

func (m *MemoryStore) ListAgentAttachments(_ context.Context, scope Scope, agentUID, sessionID, actorID string, limit int) ([]AgentAttachment, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(agentUID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(actorID) == "" {
		return nil, fmt.Errorf("agent UID, session, and actor are required")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	now := time.Now().UTC()
	workspacePrefix := tenantKey(scope) + "|"
	prefix := agentAttachmentScopeKey(scope, agentUID, "")
	var out []AgentAttachment
	cleaned := 0
	for key, item := range m.agentAttachments {
		if !hasPrefix(key, workspacePrefix) {
			continue
		}
		if attachmentExpired(item, now) {
			if cleaned < agentAttachmentDraftCleanupBatch {
				delete(m.agentAttachments, key)
				cleaned++
			}
			continue
		}
		if !hasPrefix(key, prefix) || item.SessionID != sessionID {
			continue
		}
		if item.ActorID == actorID {
			out = append(out, item.cloneMetadata())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemoryStore) GetAgentAttachment(_ context.Context, scope Scope, agentUID, sessionID, actorID, id string) (AgentAttachment, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return AgentAttachment{}, err
	}
	if strings.TrimSpace(agentUID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(actorID) == "" || strings.TrimSpace(id) == "" {
		return AgentAttachment{}, ErrAgentAttachmentNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := agentAttachmentScopeKey(scope, agentUID, id)
	item, ok := m.agentAttachments[key]
	if !ok || item.SessionID != sessionID || attachmentExpired(item, time.Now().UTC()) {
		if ok && attachmentExpired(item, time.Now().UTC()) {
			delete(m.agentAttachments, key)
		}
		return AgentAttachment{}, ErrAgentAttachmentNotFound
	}
	if item.ActorID != actorID {
		return AgentAttachment{}, ErrAgentAttachmentNotFound
	}
	return item.clone(), nil
}

func (m *MemoryStore) DeleteAgentAttachment(_ context.Context, scope Scope, agentUID, sessionID, actorID, id string) error {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := agentAttachmentScopeKey(scope, agentUID, id)
	item, ok := m.agentAttachments[key]
	if !ok || item.SessionID != sessionID || attachmentExpired(item, time.Now().UTC()) {
		if ok && attachmentExpired(item, time.Now().UTC()) {
			delete(m.agentAttachments, key)
		}
		return ErrAgentAttachmentNotFound
	}
	if item.ActorID != actorID {
		return ErrAgentAttachmentNotFound
	}
	if !item.Draft {
		return ErrAgentAttachmentImmutable
	}
	delete(m.agentAttachments, key)
	return nil
}

func (m *MemoryStore) ReconcileAgentAttachmentBindings(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	for key, item := range m.agentAttachments {
		if item.Draft || item.BindingID == "" {
			if attachmentExpired(item, now) {
				delete(m.agentAttachments, key)
			}
			continue
		}
		parts := strings.SplitN(key, "|", 3)
		if len(parts) != 3 {
			delete(m.agentAttachments, key)
			continue
		}
		if _, ok := m.runs[parts[0]+"|"+parts[1]+"|"+item.BindingID]; ok {
			continue
		}
		if item.DraftExpiresAt == nil || !item.DraftExpiresAt.After(now) {
			delete(m.agentAttachments, key)
			continue
		}
		expires := item.DraftExpiresAt.UTC()
		item.Draft, item.ExpiresAt, item.BindingID, item.DraftExpiresAt = true, &expires, "", nil
		m.agentAttachments[key] = item
	}
	return nil
}

func (m *MemoryStore) CreateAgentChatRun(_ context.Context, scope Scope, agentUID string, msg Message, run Run, receipts []AgentAttachmentReceipt, actorID string, now time.Time) error {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	if err := validateAgentAttachmentAdmission(receipts, scope, agentUID, msg.SessionID, actorID, run.ID); err != nil {
		return err
	}
	if msg.ID == "" || msg.Role != "user" || msg.RunID != run.ID || msg.SessionID != run.SessionID || run.AgentName != scope.AgentName || run.Phase == "" {
		return fmt.Errorf("atomic chat admission needs a matching user message and run")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	run.AgentUID = agentUID
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := tenantKey(scope) + "|" + run.ID
	if _, exists := m.runs[key]; exists {
		return fmt.Errorf("run %q already exists", run.ID)
	}
	attachmentRows := make([]AgentAttachment, 0, len(receipts))
	for _, receipt := range receipts {
		item, ok := m.agentAttachments[agentAttachmentScopeKey(scope, agentUID, receipt.ID)]
		if !ok || item.SessionID != msg.SessionID {
			return ErrAgentAttachmentNotFound
		}
		attachmentRows = append(attachmentRows, item)
	}
	bound, err := bindAgentAttachmentBatch(attachmentRows, receipts, actorID, run.ID, now.UTC())
	if err != nil {
		return err
	}
	m.messageSequence++
	msg.Sequence = m.messageSequence
	m.messages[sessionKey(scope, msg.SessionID)] = append(m.messages[sessionKey(scope, msg.SessionID)], msg)
	m.runs[key] = run
	m.runScopes[key] = scope
	for _, item := range bound {
		m.agentAttachments[agentAttachmentScopeKey(scope, agentUID, item.ID)] = item.clone()
	}
	return nil
}

var _ AgentAttachmentStore = (*MemoryStore)(nil)
var _ AgentChatAdmissionStore = (*MemoryStore)(nil)
var _ AgentAttachmentQuotaConfigurer = (*MemoryStore)(nil)
