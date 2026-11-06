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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/railgrid/provider-sdk/attachments"
)

const (
	DefaultAgentAttachmentWorkspaceMaxBytes = attachments.DefaultWorkspaceMaxBytes
	DefaultAgentAttachmentDraftMaxBytes     = attachments.DefaultDraftOwnerMaxBytes
	DefaultAgentAttachmentDraftMaxCount     = attachments.DefaultDraftOwnerMaxCount
)

var (
	ErrAgentAttachmentNotFound     = errors.New("agent attachment not found")
	ErrAgentAttachmentConflict     = errors.New("agent attachment already exists with different content")
	ErrAgentAttachmentForbidden    = errors.New("agent attachment ownership check failed")
	ErrAgentAttachmentImmutable    = errors.New("agent attachment is immutable after its chat run is accepted")
	ErrAgentAttachmentReceipt      = errors.New("agent attachment receipt does not match stored metadata")
	ErrAgentAttachmentQuota        = errors.New("agent attachment quota exceeded")
	ErrAgentAttachmentDraftExpired = errors.New("agent attachment draft has expired")
)

// AgentAttachmentReceipt is the portable, immutable client receipt. Session,
// owner, lifecycle state, and bytes stay in the provider store.
type AgentAttachmentReceipt = attachments.Receipt

// AgentAttachmentQuota bounds one workspace and each Agent's uncommitted
// upload drafts. A zero field means no extra hosting cap for that dimension.
type AgentAttachmentQuota struct {
	WorkspaceMaxBytes  int64
	DraftAgentMaxBytes int64
	DraftAgentMaxCount int
}

func DefaultAgentAttachmentQuota() AgentAttachmentQuota {
	return AgentAttachmentQuota{
		WorkspaceMaxBytes:  DefaultAgentAttachmentWorkspaceMaxBytes,
		DraftAgentMaxBytes: DefaultAgentAttachmentDraftMaxBytes,
		DraftAgentMaxCount: DefaultAgentAttachmentDraftMaxCount,
	}
}

// AgentAttachment is an immutable uploaded blob. A draft is bound to exactly
// one accepted chat run before its receipt is admitted to the transcript.
type AgentAttachment struct {
	ID             string
	AgentName      string
	AgentUID       string
	SessionID      string
	ActorID        string
	Filename       string
	ContentType    string
	SizeBytes      int64
	SHA256         string
	Draft          bool
	CreatedAt      time.Time
	ExpiresAt      *time.Time
	Data           []byte
	BindingID      string
	DraftExpiresAt *time.Time
}

// AgentAttachmentStore is separate from Store so transcript-only test doubles
// do not acquire blob persistence requirements.
type AgentAttachmentStore interface {
	CreateAgentAttachment(context.Context, Scope, AgentAttachment) (AgentAttachment, bool, error)
	ListAgentAttachments(context.Context, Scope, string, string, string, int) ([]AgentAttachment, error)
	GetAgentAttachment(context.Context, Scope, string, string, string, string) (AgentAttachment, error)
	DeleteAgentAttachment(context.Context, Scope, string, string, string, string) error
	BindAgentAttachments(context.Context, Scope, string, string, []AgentAttachmentReceipt, string, string, time.Time) ([]AgentAttachment, error)
	RollbackAgentAttachmentBinding(context.Context, Scope, string, string) error
	ReconcileAgentAttachmentBindings(context.Context) error
}

// AgentChatAdmissionStore persists the transcript anchor, accepted Run, and
// attachment bindings in one store transaction.
type AgentChatAdmissionStore interface {
	CreateAgentChatRun(context.Context, Scope, string, Message, Run, []AgentAttachmentReceipt, string, time.Time) error
}

type AgentAttachmentQuotaConfigurer interface {
	ConfigureAgentAttachmentQuota(AgentAttachmentQuota) error
}

func (a AgentAttachment) Receipt() AgentAttachmentReceipt {
	return AgentAttachmentReceipt{
		ID: a.ID, Filename: a.Filename, ContentType: a.ContentType,
		SizeBytes: a.SizeBytes, SHA256: a.SHA256, CreatedAt: a.CreatedAt.UTC(),
	}
}

func (a AgentAttachment) clone() AgentAttachment {
	a.Data = append([]byte(nil), a.Data...)
	if a.ExpiresAt != nil {
		at := a.ExpiresAt.UTC()
		a.ExpiresAt = &at
	}
	if a.DraftExpiresAt != nil {
		at := a.DraftExpiresAt.UTC()
		a.DraftExpiresAt = &at
	}
	return a
}

func (a AgentAttachment) validate() error {
	if err := attachments.ValidateAttachmentID(a.ID); err != nil {
		return err
	}
	if strings.TrimSpace(a.AgentName) == "" || strings.TrimSpace(a.AgentUID) == "" || strings.TrimSpace(a.SessionID) == "" || strings.TrimSpace(a.ActorID) == "" {
		return fmt.Errorf("agent attachment needs Agent, session, and actor identity")
	}
	if err := attachments.ValidateAttachmentContent(a.Filename, a.ContentType, a.Data); err != nil {
		return err
	}
	normalizedType, err := attachments.NormalizeAttachmentContentType(a.ContentType)
	if err != nil {
		return err
	}
	if a.ContentType != normalizedType {
		return fmt.Errorf("agent attachment content type is not canonical")
	}
	if a.SizeBytes != int64(len(a.Data)) {
		return fmt.Errorf("attachment size %d does not match its %d data bytes", a.SizeBytes, len(a.Data))
	}
	digest := sha256.Sum256(a.Data)
	if a.SHA256 != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("attachment SHA-256 does not match its data")
	}
	if a.CreatedAt.IsZero() {
		return fmt.Errorf("attachment creation time is required")
	}
	if a.Draft {
		if a.ExpiresAt == nil || !a.ExpiresAt.After(a.CreatedAt) || a.BindingID != "" {
			return fmt.Errorf("draft attachment needs a future expiry and no run binding")
		}
	} else if a.BindingID == "" || a.ExpiresAt != nil {
		return fmt.Errorf("accepted attachment needs a run binding and no draft expiry")
	}
	return nil
}

func validateAgentAttachmentReceipt(receipt AgentAttachmentReceipt) error {
	if err := attachments.ValidateAttachmentID(receipt.ID); err != nil {
		return err
	}
	if strings.TrimSpace(receipt.Filename) == "" || strings.TrimSpace(receipt.ContentType) == "" || receipt.SizeBytes <= 0 || receipt.CreatedAt.IsZero() {
		return fmt.Errorf("attachment receipt is incomplete")
	}
	contentType, err := attachments.NormalizeAttachmentContentType(receipt.ContentType)
	if err != nil || contentType != receipt.ContentType {
		return fmt.Errorf("attachment receipt content type is not canonical")
	}
	if len(receipt.SHA256) != sha256.Size*2 || strings.ToLower(receipt.SHA256) != receipt.SHA256 {
		return fmt.Errorf("attachment receipt needs a lowercase SHA-256 digest")
	}
	if _, err := hex.DecodeString(receipt.SHA256); err != nil {
		return fmt.Errorf("attachment receipt needs a lowercase SHA-256 digest")
	}
	return nil
}

func validateAgentAttachmentBatch(receipts []AgentAttachmentReceipt) error {
	if len(receipts) > attachments.MaxAttachmentsPerTurn {
		return fmt.Errorf("at most %d attachments may be selected for one turn", attachments.MaxAttachmentsPerTurn)
	}
	var total int64
	seen := make(map[string]struct{}, len(receipts))
	for _, receipt := range receipts {
		if err := validateAgentAttachmentReceipt(receipt); err != nil {
			return err
		}
		if _, ok := seen[receipt.ID]; ok {
			return fmt.Errorf("attachment receipts must not repeat an ID")
		}
		seen[receipt.ID] = struct{}{}
		total += receipt.SizeBytes
		if total > attachments.MaxBytesPerTurn {
			return fmt.Errorf("selected attachments exceed the %d-byte per-turn limit", attachments.MaxBytesPerTurn)
		}
	}
	return nil
}

func agentAttachmentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func agentAttachmentScopeKey(scope Scope, agentUID, id string) string {
	return tenantKey(scope) + "|" + scope.AgentName + "|" + agentUID + "|" + id
}

func agentAttachmentQuotaValid(quota AgentAttachmentQuota) error {
	if quota.WorkspaceMaxBytes < 0 || quota.DraftAgentMaxBytes < 0 || quota.DraftAgentMaxCount < 0 {
		return fmt.Errorf("agent attachment quotas cannot be negative")
	}
	return nil
}

func validateAgentAttachmentAdmission(receipts []AgentAttachmentReceipt, scope Scope, agentUID, sessionID, actorID, bindingID string) error {
	if len(receipts) == 0 {
		return fmt.Errorf("at least one attachment receipt is required")
	}
	if err := validateAgentAttachmentBatch(receipts); err != nil {
		return err
	}
	if strings.TrimSpace(agentUID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(actorID) == "" || strings.TrimSpace(bindingID) == "" {
		return fmt.Errorf("attachment binding needs Agent UID, session, actor, and run identity")
	}
	if strings.TrimSpace(scope.AgentName) == "" {
		return fmt.Errorf("attachment binding needs an Agent name")
	}
	return nil
}

func bindAgentAttachmentBatch(items []AgentAttachment, receipts []AgentAttachmentReceipt, actorID, bindingID string, now time.Time) ([]AgentAttachment, error) {
	if len(items) != len(receipts) {
		return nil, ErrAgentAttachmentReceipt
	}
	byID := make(map[string]AgentAttachment, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	for _, receipt := range receipts {
		item, ok := byID[receipt.ID]
		if !ok || !attachments.ReceiptsMatch(receipt, item.Receipt()) {
			return nil, ErrAgentAttachmentReceipt
		}
		if item.ActorID != actorID {
			return nil, ErrAgentAttachmentForbidden
		}
		if !item.Draft {
			if item.BindingID != bindingID {
				return nil, ErrAgentAttachmentImmutable
			}
			continue
		}
		if item.ExpiresAt == nil || !item.ExpiresAt.After(now) {
			return nil, ErrAgentAttachmentDraftExpired
		}
	}
	out := make([]AgentAttachment, 0, len(receipts))
	for _, receipt := range receipts {
		item := byID[receipt.ID]
		if item.Draft {
			expiresAt := item.ExpiresAt.UTC()
			item.DraftExpiresAt = &expiresAt
			item.Draft = false
			item.BindingID = bindingID
			item.ExpiresAt = nil
			byID[receipt.ID] = item
		}
		out = append(out, byID[receipt.ID].clone())
	}
	return out, nil
}

func attachmentExpired(item AgentAttachment, now time.Time) bool {
	return item.Draft && (item.ExpiresAt == nil || !item.ExpiresAt.After(now))
}
