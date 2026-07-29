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

package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	cryptoRand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// EncryptionKey is one AES-GCM key. The first configured key encrypts new
// messages; all configured keys remain available to decrypt older messages.
type EncryptionKey struct {
	ID    string
	Value []byte
}

type encryptedStore struct {
	inner  Store
	active string
	keys   map[string]cipher.AEAD
}

// ParseEncryptionKeys parses comma-separated key specs in the form
// key-id:base64-encoded-aes-key. AES accepts 16, 24, or 32 byte keys.
func ParseEncryptionKeys(raw string) ([]EncryptionKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	keys := make([]EncryptionKey, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, encoded, ok := strings.Cut(part, ":")
		id = strings.TrimSpace(id)
		encoded = strings.TrimSpace(encoded)
		if !ok || id == "" || encoded == "" {
			return nil, fmt.Errorf("encryption keys must be key-id:base64-key")
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate encryption key id %q", id)
		}
		key, err := decodeBase64Key(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode encryption key %q: %w", id, err)
		}
		if _, err := aes.NewCipher(key); err != nil {
			return nil, fmt.Errorf("invalid encryption key %q: %w", id, err)
		}
		seen[id] = true
		keys = append(keys, EncryptionKey{ID: id, Value: key})
	}
	return keys, nil
}

func decodeBase64Key(encoded string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		value, err := enc.DecodeString(encoded)
		if err == nil {
			return value, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// NewEncryptedStore wraps an existing Store with AES-GCM encryption for
// message content at rest. Passing no keys returns the underlying store.
func NewEncryptedStore(inner Store, keys []EncryptionKey) (Store, error) {
	if inner == nil {
		return nil, fmt.Errorf("store is required")
	}
	if len(keys) == 0 {
		return inner, nil
	}
	out := &encryptedStore{
		inner:  inner,
		active: strings.TrimSpace(keys[0].ID),
		keys:   make(map[string]cipher.AEAD, len(keys)),
	}
	if out.active == "" {
		return nil, fmt.Errorf("active encryption key id is required")
	}
	for _, key := range keys {
		id := strings.TrimSpace(key.ID)
		if id == "" {
			return nil, fmt.Errorf("encryption key id is required")
		}
		block, err := aes.NewCipher(key.Value)
		if err != nil {
			return nil, fmt.Errorf("invalid encryption key %q: %w", id, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("initialize encryption key %q: %w", id, err)
		}
		if _, exists := out.keys[id]; exists {
			return nil, fmt.Errorf("duplicate encryption key id %q", id)
		}
		out.keys[id] = aead
	}
	if _, ok := out.keys[out.active]; !ok {
		return nil, fmt.Errorf("active encryption key %q is not configured", out.active)
	}
	return out, nil
}

func (s *encryptedStore) EnsureSchema(ctx context.Context) error {
	return s.inner.EnsureSchema(ctx)
}

func (s *encryptedStore) AppendMessage(ctx context.Context, scope Scope, msg Message) error {
	if err := scope.validate(); err != nil {
		return err
	}
	msg, err := s.encryptMessage(scope, msg)
	if err != nil {
		return err
	}
	return s.inner.AppendMessage(ctx, scope, msg)
}

func (s *encryptedStore) encryptMessage(scope Scope, msg Message) (Message, error) {
	if msg.Content == "" || msg.ContentEncrypted {
		return msg, nil
	}
	aead := s.keys[s.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(cryptoRand.Reader, nonce); err != nil {
		return Message{}, fmt.Errorf("generate message nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, []byte(msg.Content), messageAAD(scope, msg))
	payload := append(nonce, ciphertext...)
	msg.Content = base64.RawStdEncoding.EncodeToString(payload)
	msg.ContentEncrypted = true
	msg.ContentKeyID = s.active
	return msg, nil
}

func (s *encryptedStore) ListMessages(ctx context.Context, scope Scope, limit int, cursor string) (Page, error) {
	page, err := s.inner.ListMessages(ctx, scope, limit, cursor)
	if err != nil {
		return Page{}, err
	}
	for i := range page.Items {
		if err := s.decryptMessage(scope, &page.Items[i]); err != nil {
			return Page{}, err
		}
	}
	return page, nil
}

func (s *encryptedStore) LoadRecentMessages(ctx context.Context, scope Scope, limit int) ([]Message, error) {
	items, err := s.inner.LoadRecentMessages(ctx, scope, limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if err := s.decryptMessage(scope, &items[i]); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *encryptedStore) GetAssistantApprovalPreference(ctx context.Context, scope Scope, actor string) (AssistantApprovalPreference, error) {
	return s.inner.GetAssistantApprovalPreference(ctx, scope, actor)
}

func (s *encryptedStore) SetAssistantApprovalPreference(ctx context.Context, scope Scope, preference AssistantApprovalPreference) (AssistantApprovalPreference, error) {
	return s.inner.SetAssistantApprovalPreference(ctx, scope, preference)
}

func (s *encryptedStore) SaveAssistantRun(ctx context.Context, scope Scope, run AssistantRun) error {
	if err := scope.validate(); err != nil {
		return err
	}
	checkpoint, err := s.encryptAssistantRunBlob(scope, run, "checkpoint", run.Checkpoint)
	if err != nil {
		return err
	}
	audit, err := s.encryptAssistantRunBlob(scope, run, "audit", run.Audit)
	if err != nil {
		return err
	}
	run.Checkpoint = checkpoint
	run.Audit = audit
	return s.inner.SaveAssistantRun(ctx, scope, run)
}

func (s *encryptedStore) CreateAssistantRun(ctx context.Context, scope Scope, user Message, assistant Message, run AssistantRun) (AssistantRun, error) {
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	user, err := s.encryptMessage(scope, user)
	if err != nil {
		return AssistantRun{}, err
	}
	assistant, err = s.encryptMessage(scope, assistant)
	if err != nil {
		return AssistantRun{}, err
	}
	checkpoint, err := s.encryptAssistantRunBlob(scope, run, "checkpoint", run.Checkpoint)
	if err != nil {
		return AssistantRun{}, err
	}
	audit, err := s.encryptAssistantRunBlob(scope, run, "audit", run.Audit)
	if err != nil {
		return AssistantRun{}, err
	}
	run.Checkpoint = checkpoint
	run.Audit = audit
	created, err := s.inner.CreateAssistantRun(ctx, scope, user, assistant, run)
	if err != nil {
		return AssistantRun{}, err
	}
	if err := s.decryptAssistantRunBlobs(scope, &created); err != nil {
		return AssistantRun{}, err
	}
	return created, nil
}

func (s *encryptedStore) CreateWorkItemAndAssistantRun(ctx context.Context, scope Scope, item AssistantWorkItem, user Message, assistant Message, run AssistantRun) (AssistantWorkItem, error) {
	// A Start action may claim the just-submitted, unassigned root message.
	// Preserve its existing ciphertext so the inner store can compare its exact
	// immutable content while this wrapper verifies the plaintext identity.
	if persisted, found, err := s.findRawMessage(ctx, scope, user.ID); err != nil {
		return AssistantWorkItem{}, err
	} else if found {
		plaintext := persisted
		if err := s.decryptMessage(scope, &plaintext); err != nil {
			return AssistantWorkItem{}, err
		}
		if persisted.WorkItemID != "" || plaintext.Role != user.Role || plaintext.ActorID != user.ActorID || plaintext.Content != user.Content {
			return AssistantWorkItem{}, fmt.Errorf("%w: root message %q cannot be attached", ErrAssistantWorkItemConflict, user.ID)
		}
		user = persisted
	} else {
		var err error
		user, err = s.encryptMessage(scope, user)
		if err != nil {
			return AssistantWorkItem{}, err
		}
	}
	assistant, err := s.encryptMessage(scope, assistant)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	item.PlanGrant, err = s.encryptAssistantWorkItemGrant(scope, item, item.PlanGrant)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	item.ExecutionPlan, err = s.encryptAssistantWorkItemExecutionPlan(scope, item, item.ExecutionPlan)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	run.Checkpoint, err = s.encryptAssistantRunBlob(scope, run, "checkpoint", run.Checkpoint)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	run.Audit, err = s.encryptAssistantRunBlob(scope, run, "audit", run.Audit)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	created, err := s.inner.CreateWorkItemAndAssistantRun(ctx, scope, item, user, assistant, run)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	if err := s.decryptAssistantWorkItem(scope, &created); err != nil {
		return AssistantWorkItem{}, err
	}
	return created, nil
}

func (s *encryptedStore) PromoteAssistantRunToWorkItem(
	ctx context.Context,
	scope Scope,
	runID, actor, workItemID string,
	expectedRunRevision int64,
	now time.Time,
) (AssistantWorkItem, AssistantRun, error) {
	// Promotion does not change checkpoint or audit content. Validate and
	// decrypt those blobs before the inner atomic mutation so no missing-key
	// or corrupt-ciphertext error can be discovered only after commit.
	existing, err := s.inner.GetAssistantRun(ctx, scope, runID)
	if err != nil {
		if errors.Is(err, ErrAssistantRunNotFound) {
			return AssistantWorkItem{}, AssistantRun{}, fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, runID)
		}
		return AssistantWorkItem{}, AssistantRun{}, err
	}
	if err := s.decryptAssistantRunBlobs(scope, &existing); err != nil {
		return AssistantWorkItem{}, AssistantRun{}, err
	}
	item, run, err := s.inner.PromoteAssistantRunToWorkItem(
		ctx,
		scope,
		runID,
		actor,
		workItemID,
		expectedRunRevision,
		now,
	)
	if err != nil {
		return AssistantWorkItem{}, AssistantRun{}, err
	}
	run.Checkpoint = existing.Checkpoint
	run.Audit = existing.Audit
	// A first-time promotion always creates an empty grant, so this cannot
	// discover a ciphertext error after a mutation. On an idempotent replay
	// the inner operation is read-only and returns the authoritative grant and
	// revision together; decrypt that result to avoid racing plan changes.
	if err := s.decryptAssistantWorkItem(scope, &item); err != nil {
		return AssistantWorkItem{}, AssistantRun{}, err
	}
	return item, run, nil
}

func (s *encryptedStore) ResumeWorkItemAndCreateAssistantRun(ctx context.Context, scope Scope, workItemID, actor string, expectedRevision int64, user Message, assistant Message, run AssistantRun) (AssistantWorkItem, error) {
	var err error
	user, err = s.encryptMessage(scope, user)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	assistant, err = s.encryptMessage(scope, assistant)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	run.Checkpoint, err = s.encryptAssistantRunBlob(scope, run, "checkpoint", run.Checkpoint)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	run.Audit, err = s.encryptAssistantRunBlob(scope, run, "audit", run.Audit)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	item, err := s.inner.ResumeWorkItemAndCreateAssistantRun(ctx, scope, workItemID, actor, expectedRevision, user, assistant, run)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	if err := s.decryptAssistantWorkItem(scope, &item); err != nil {
		return AssistantWorkItem{}, err
	}
	return item, nil
}

func (s *encryptedStore) findRawMessage(ctx context.Context, scope Scope, id string) (Message, bool, error) {
	cursor := ""
	for {
		page, err := s.inner.ListMessages(ctx, scope, 500, cursor)
		if err != nil {
			return Message{}, false, err
		}
		for _, message := range page.Items {
			if message.ID == id {
				return message, true, nil
			}
		}
		if page.NextCursor == "" {
			return Message{}, false, nil
		}
		cursor = page.NextCursor
	}
}

func (s *encryptedStore) GetAssistantWorkItem(ctx context.Context, scope Scope, id string) (AssistantWorkItem, error) {
	item, err := s.inner.GetAssistantWorkItem(ctx, scope, id)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	if err := s.decryptAssistantWorkItem(scope, &item); err != nil {
		return AssistantWorkItem{}, err
	}
	return item, nil
}

func (s *encryptedStore) ListAssistantWorkItems(ctx context.Context, scope Scope) ([]AssistantWorkItem, error) {
	items, err := s.inner.ListAssistantWorkItems(ctx, scope)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if err := s.decryptAssistantWorkItem(scope, &items[i]); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *encryptedStore) CompareAndSwapAssistantWorkItem(ctx context.Context, scope Scope, item AssistantWorkItem, expectedRevision int64) error {
	if len(item.ExecutionPlan) > 0 && !json.Valid(item.ExecutionPlan) {
		return fmt.Errorf("assistant work item execution plan is not valid json")
	}
	grant, err := s.encryptAssistantWorkItemGrant(scope, item, item.PlanGrant)
	if err != nil {
		return err
	}
	item.PlanGrant = grant
	executionPlan, err := s.encryptAssistantWorkItemExecutionPlan(scope, item, item.ExecutionPlan)
	if err != nil {
		return err
	}
	item.ExecutionPlan = executionPlan
	return s.inner.CompareAndSwapAssistantWorkItem(ctx, scope, item, expectedRevision)
}

func (s *encryptedStore) SaveWorkItemExecutionPlan(ctx context.Context, scope Scope, workItemID, runID string, expectedRevision int64, executionPlanRevision string, executionPlan json.RawMessage, now time.Time) (AssistantWorkItem, error) {
	if strings.TrimSpace(workItemID) == "" || strings.TrimSpace(runID) == "" || expectedRevision < 1 || strings.TrimSpace(executionPlanRevision) == "" || len(executionPlan) == 0 {
		return AssistantWorkItem{}, fmt.Errorf("%w: work item, run, revision, and execution plan are required", ErrAssistantWorkItemConflict)
	}
	if !json.Valid(executionPlan) {
		return AssistantWorkItem{}, fmt.Errorf("work item execution plan is not valid json")
	}
	item := AssistantWorkItem{ID: workItemID}
	encryptedPlan, err := s.encryptAssistantWorkItemExecutionPlan(scope, item, executionPlan)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	saved, err := s.inner.SaveWorkItemExecutionPlan(ctx, scope, workItemID, runID, expectedRevision, executionPlanRevision, encryptedPlan, now)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	if err := s.decryptAssistantWorkItem(scope, &saved); err != nil {
		return AssistantWorkItem{}, err
	}
	return saved, nil
}

func (s *encryptedStore) ApproveWorkItemPlan(ctx context.Context, scope Scope, workItemID, runID string, expectedRevision int64, grantRevision string, planGrant json.RawMessage, now time.Time) (AssistantWorkItem, error) {
	item := AssistantWorkItem{ID: workItemID}
	grant, err := s.encryptAssistantWorkItemGrant(scope, item, planGrant)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	approved, err := s.inner.ApproveWorkItemPlan(ctx, scope, workItemID, runID, expectedRevision, grantRevision, grant, now)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	if err := s.decryptAssistantWorkItem(scope, &approved); err != nil {
		return AssistantWorkItem{}, err
	}
	return approved, nil
}

func (s *encryptedStore) RetireWorkItemPlan(ctx context.Context, scope Scope, workItemID, runID, actor string, expectedWorkItemRevision int64, expectedGrantRevision, tombstoneGrantRevision string, now time.Time) (AssistantWorkItem, error) {
	item, err := s.inner.RetireWorkItemPlan(ctx, scope, workItemID, runID, actor, expectedWorkItemRevision, expectedGrantRevision, tombstoneGrantRevision, now)
	if err != nil {
		return AssistantWorkItem{}, err
	}
	if err := s.decryptAssistantWorkItem(scope, &item); err != nil {
		return AssistantWorkItem{}, err
	}
	return item, nil
}

func (s *encryptedStore) TransitionWorkItemAndRun(ctx context.Context, scope Scope, workItemID string, expectedWorkItemRevision int64, run AssistantRun, status AssistantWorkItemStatus, reason string, now time.Time) error {
	// Terminal transitions deliberately delete the resumable checkpoint. Do not
	// encrypt an old plaintext blob only for the inner store to retain it.
	run.Checkpoint = nil
	checkpoint, err := s.encryptAssistantRunBlob(scope, run, "checkpoint", run.Checkpoint)
	if err != nil {
		return err
	}
	audit, err := s.encryptAssistantRunBlob(scope, run, "audit", run.Audit)
	if err != nil {
		return err
	}
	run.Checkpoint, run.Audit = checkpoint, audit
	return s.inner.TransitionWorkItemAndRun(ctx, scope, workItemID, expectedWorkItemRevision, run, status, reason, now)
}

func (s *encryptedStore) RequestAssistantRunStop(ctx context.Context, scope Scope, workItemID, runID string, expectedWorkItemRevision, expectedRunRevision int64, now time.Time) (AssistantRun, error) {
	run, err := s.inner.RequestAssistantRunStop(ctx, scope, workItemID, runID, expectedWorkItemRevision, expectedRunRevision, now)
	if err != nil {
		return AssistantRun{}, err
	}
	if err := s.decryptAssistantRunBlobs(scope, &run); err != nil {
		return AssistantRun{}, err
	}
	return run, nil
}

func (s *encryptedStore) LoadMessagesForWorkItem(ctx context.Context, scope Scope, workItemID string, limit int) ([]Message, error) {
	items, err := s.inner.LoadMessagesForWorkItem(ctx, scope, workItemID, limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if err := s.decryptMessage(scope, &items[i]); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *encryptedStore) LatestAssistantRunForWorkItem(ctx context.Context, scope Scope, workItemID string) (AssistantRun, error) {
	run, err := s.inner.LatestAssistantRunForWorkItem(ctx, scope, workItemID)
	if err != nil {
		return AssistantRun{}, err
	}
	if err := s.decryptAssistantRunBlobs(scope, &run); err != nil {
		return AssistantRun{}, err
	}
	return run, nil
}

func (s *encryptedStore) SaveAssistantRunSnapshot(ctx context.Context, scope Scope, run AssistantRun, messages []Message, expectedRevision int64) error {
	if err := scope.validate(); err != nil {
		return err
	}
	checkpoint, err := s.encryptAssistantRunBlob(scope, run, "checkpoint", run.Checkpoint)
	if err != nil {
		return err
	}
	audit, err := s.encryptAssistantRunBlob(scope, run, "audit", run.Audit)
	if err != nil {
		return err
	}
	run.Checkpoint = checkpoint
	run.Audit = audit
	encryptedMessages := make([]Message, len(messages))
	for i := range messages {
		encryptedMessages[i], err = s.encryptMessage(scope, messages[i])
		if err != nil {
			return err
		}
	}
	return s.inner.SaveAssistantRunSnapshot(ctx, scope, run, encryptedMessages, expectedRevision)
}

func (s *encryptedStore) CompareAndSwapAssistantRun(ctx context.Context, scope Scope, run AssistantRun, expectedRequestID string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	checkpoint, err := s.encryptAssistantRunBlob(scope, run, "checkpoint", run.Checkpoint)
	if err != nil {
		return err
	}
	audit, err := s.encryptAssistantRunBlob(scope, run, "audit", run.Audit)
	if err != nil {
		return err
	}
	run.Checkpoint = checkpoint
	run.Audit = audit
	return s.inner.CompareAndSwapAssistantRun(ctx, scope, run, expectedRequestID)
}

func (s *encryptedStore) ClaimAssistantRun(ctx context.Context, scope Scope, id string, requestID string, now time.Time) (AssistantRun, error) {
	run, err := s.inner.ClaimAssistantRun(ctx, scope, id, requestID, now)
	if err != nil {
		return AssistantRun{}, err
	}
	if err := s.decryptAssistantRunBlobs(scope, &run); err != nil {
		return AssistantRun{}, err
	}
	return run, nil
}

func (s *encryptedStore) GetAssistantRun(ctx context.Context, scope Scope, id string) (AssistantRun, error) {
	run, err := s.inner.GetAssistantRun(ctx, scope, id)
	if err != nil {
		return AssistantRun{}, err
	}
	if err := s.decryptAssistantRunBlobs(scope, &run); err != nil {
		return AssistantRun{}, err
	}
	return run, nil
}

func (s *encryptedStore) FindAssistantRunByClientRequestID(ctx context.Context, scope Scope, clientRequestID string) (AssistantRun, error) {
	run, err := s.inner.FindAssistantRunByClientRequestID(ctx, scope, clientRequestID)
	if err != nil {
		return AssistantRun{}, err
	}
	if err := s.decryptAssistantRunBlobs(scope, &run); err != nil {
		return AssistantRun{}, err
	}
	return run, nil
}

func (s *encryptedStore) LatestAssistantRun(ctx context.Context, scope Scope) (AssistantRun, error) {
	run, err := s.inner.LatestAssistantRun(ctx, scope)
	if err != nil {
		return AssistantRun{}, err
	}
	if err := s.decryptAssistantRunBlobs(scope, &run); err != nil {
		return AssistantRun{}, err
	}
	return run, nil
}

func (s *encryptedStore) DeleteProjectMessages(ctx context.Context, scope Scope) error {
	return s.inner.DeleteProjectMessages(ctx, scope)
}

func (s *encryptedStore) DeleteMessagesOlderThan(ctx context.Context, before time.Time) (int64, error) {
	return s.inner.DeleteMessagesOlderThan(ctx, before)
}

func (s *encryptedStore) Close() error {
	if closer, ok := s.inner.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (s *encryptedStore) decryptMessage(scope Scope, msg *Message) error {
	if msg == nil || !msg.ContentEncrypted {
		return nil
	}
	aead := s.keys[msg.ContentKeyID]
	if aead == nil {
		return fmt.Errorf("message %q uses unknown encryption key %q", msg.ID, msg.ContentKeyID)
	}
	payload, err := base64.RawStdEncoding.DecodeString(msg.Content)
	if err != nil {
		return fmt.Errorf("decode encrypted message %q: %w", msg.ID, err)
	}
	if len(payload) < aead.NonceSize() {
		return fmt.Errorf("encrypted message %q is too short", msg.ID)
	}
	nonce := payload[:aead.NonceSize()]
	ciphertext := payload[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, messageAAD(scope, *msg))
	if err != nil {
		return fmt.Errorf("decrypt message %q: %w", msg.ID, err)
	}
	msg.Content = string(plaintext)
	msg.ContentEncrypted = false
	return nil
}

func messageAAD(scope Scope, msg Message) []byte {
	return []byte(strings.Join([]string{
		scope.OrgUUID,
		scope.WorkspaceUUID,
		scope.ProjectName,
		scope.ProjectUID,
		msg.ID,
		msg.Role,
	}, "\x00"))
}

type encryptedAssistantRunCheckpoint struct {
	Encrypted bool   `json:"encrypted"`
	KeyID     string `json:"keyID"`
	Payload   string `json:"payload"`
}

func (s *encryptedStore) encryptAssistantRunBlob(scope Scope, run AssistantRun, label string, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	var existing encryptedAssistantRunCheckpoint
	if json.Unmarshal(plaintext, &existing) == nil && existing.Encrypted && existing.KeyID != "" && existing.Payload != "" {
		return cloneRawMessage(plaintext), nil
	}
	aead := s.keys[s.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(cryptoRand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate assistant checkpoint nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, assistantRunAAD(scope, run, label))
	payload := append(nonce, ciphertext...)
	envelope := encryptedAssistantRunCheckpoint{
		Encrypted: true,
		KeyID:     s.active,
		Payload:   base64.RawStdEncoding.EncodeToString(payload),
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode encrypted assistant checkpoint: %w", err)
	}
	return raw, nil
}

func (s *encryptedStore) decryptAssistantRunBlobs(scope Scope, run *AssistantRun) error {
	if err := s.decryptAssistantRunBlob(scope, run, "checkpoint", &run.Checkpoint); err != nil {
		return err
	}
	return s.decryptAssistantRunBlob(scope, run, "audit", &run.Audit)
}

func (s *encryptedStore) decryptAssistantRunBlob(scope Scope, run *AssistantRun, label string, value *json.RawMessage) error {
	if run == nil || value == nil || len(*value) == 0 {
		return nil
	}
	var envelope encryptedAssistantRunCheckpoint
	if err := json.Unmarshal(*value, &envelope); err != nil || !envelope.Encrypted {
		return nil
	}
	aead := s.keys[envelope.KeyID]
	if aead == nil {
		return fmt.Errorf("assistant run %q uses unknown encryption key %q", run.ID, envelope.KeyID)
	}
	payload, err := base64.RawStdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return fmt.Errorf("decode encrypted assistant checkpoint %q: %w", run.ID, err)
	}
	if len(payload) < aead.NonceSize() {
		return fmt.Errorf("encrypted assistant checkpoint %q is too short", run.ID)
	}
	nonce := payload[:aead.NonceSize()]
	ciphertext := payload[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, assistantRunAAD(scope, *run, label))
	if err != nil {
		return fmt.Errorf("decrypt assistant checkpoint %q: %w", run.ID, err)
	}
	*value = plaintext
	return nil
}

func assistantRunAAD(scope Scope, run AssistantRun, label string) []byte {
	return []byte(strings.Join([]string{
		scope.OrgUUID,
		scope.WorkspaceUUID,
		scope.ProjectName,
		scope.ProjectUID,
		run.ID,
		label,
	}, "\x00"))
}

func (s *encryptedStore) encryptAssistantWorkItemGrant(scope Scope, item AssistantWorkItem, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	var existing encryptedAssistantRunCheckpoint
	if json.Unmarshal(plaintext, &existing) == nil && existing.Encrypted && existing.KeyID != "" && existing.Payload != "" {
		return cloneRawMessage(plaintext), nil
	}
	aead := s.keys[s.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(cryptoRand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate work item grant nonce: %w", err)
	}
	payload := append(nonce, aead.Seal(nil, nonce, plaintext, assistantWorkItemAAD(scope, item))...)
	return json.Marshal(encryptedAssistantRunCheckpoint{Encrypted: true, KeyID: s.active, Payload: base64.RawStdEncoding.EncodeToString(payload)})
}

func (s *encryptedStore) decryptAssistantWorkItemGrant(scope Scope, item *AssistantWorkItem) error {
	if item == nil || len(item.PlanGrant) == 0 {
		return nil
	}
	var envelope encryptedAssistantRunCheckpoint
	if err := json.Unmarshal(item.PlanGrant, &envelope); err != nil || !envelope.Encrypted {
		return nil
	}
	aead := s.keys[envelope.KeyID]
	if aead == nil {
		return fmt.Errorf("work item %q uses unknown encryption key %q", item.ID, envelope.KeyID)
	}
	payload, err := base64.RawStdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return fmt.Errorf("decode encrypted work item grant %q: %w", item.ID, err)
	}
	if len(payload) < aead.NonceSize() {
		return fmt.Errorf("encrypted work item grant %q is too short", item.ID)
	}
	plaintext, err := aead.Open(nil, payload[:aead.NonceSize()], payload[aead.NonceSize():], assistantWorkItemAAD(scope, *item))
	if err != nil {
		return fmt.Errorf("decrypt work item grant %q: %w", item.ID, err)
	}
	item.PlanGrant = plaintext
	return nil
}

func (s *encryptedStore) encryptAssistantWorkItemExecutionPlan(scope Scope, item AssistantWorkItem, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	var existing encryptedAssistantRunCheckpoint
	if json.Unmarshal(plaintext, &existing) == nil && existing.Encrypted && existing.KeyID != "" && existing.Payload != "" {
		return cloneRawMessage(plaintext), nil
	}
	aead := s.keys[s.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(cryptoRand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate work item execution plan nonce: %w", err)
	}
	payload := append(nonce, aead.Seal(nil, nonce, plaintext, assistantWorkItemExecutionPlanAAD(scope, item))...)
	return json.Marshal(encryptedAssistantRunCheckpoint{Encrypted: true, KeyID: s.active, Payload: base64.RawStdEncoding.EncodeToString(payload)})
}

func (s *encryptedStore) decryptAssistantWorkItemExecutionPlan(scope Scope, item *AssistantWorkItem) error {
	if item == nil || len(item.ExecutionPlan) == 0 {
		return nil
	}
	var envelope encryptedAssistantRunCheckpoint
	if err := json.Unmarshal(item.ExecutionPlan, &envelope); err != nil || !envelope.Encrypted {
		return nil
	}
	aead := s.keys[envelope.KeyID]
	if aead == nil {
		return fmt.Errorf("work item %q execution plan uses unknown encryption key %q", item.ID, envelope.KeyID)
	}
	payload, err := base64.RawStdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return fmt.Errorf("decode encrypted work item execution plan %q: %w", item.ID, err)
	}
	if len(payload) < aead.NonceSize() {
		return fmt.Errorf("encrypted work item execution plan %q is too short", item.ID)
	}
	plaintext, err := aead.Open(nil, payload[:aead.NonceSize()], payload[aead.NonceSize():], assistantWorkItemExecutionPlanAAD(scope, *item))
	if err != nil {
		return fmt.Errorf("decrypt work item execution plan %q: %w", item.ID, err)
	}
	item.ExecutionPlan = plaintext
	return nil
}

func (s *encryptedStore) decryptAssistantWorkItem(scope Scope, item *AssistantWorkItem) error {
	if err := s.decryptAssistantWorkItemGrant(scope, item); err != nil {
		return err
	}
	return s.decryptAssistantWorkItemExecutionPlan(scope, item)
}

func assistantWorkItemAAD(scope Scope, item AssistantWorkItem) []byte {
	return []byte(strings.Join([]string{scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, item.ID, "plan_grant"}, "\x00"))
}

func assistantWorkItemExecutionPlanAAD(scope Scope, item AssistantWorkItem) []byte {
	return []byte(strings.Join([]string{scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, item.ID, "execution_plan"}, "\x00"))
}

var _ Store = (*encryptedStore)(nil)
