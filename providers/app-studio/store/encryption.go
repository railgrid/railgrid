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
// message content and metadata at rest. Passing no keys returns the underlying
// store.
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

func (s *encryptedStore) CreateAssistantThread(ctx context.Context, scope Scope, thread AssistantThread, events []AssistantThreadEvent) (AssistantThread, error) {
	var err error
	thread, err = prepareAssistantThread(thread)
	if err != nil {
		return AssistantThread{}, err
	}
	thread, err = s.encryptAssistantThread(scope, thread)
	if err != nil {
		return AssistantThread{}, err
	}
	encryptedEvents := make([]AssistantThreadEvent, len(events))
	for index, event := range events {
		event.ThreadID = thread.ID
		encryptedEvents[index], err = s.encryptAssistantThreadEvent(scope, event)
		if err != nil {
			return AssistantThread{}, err
		}
	}
	created, err := s.inner.CreateAssistantThread(ctx, scope, thread, encryptedEvents)
	if err != nil {
		return AssistantThread{}, err
	}
	if err := s.decryptAssistantThread(scope, &created); err != nil {
		return AssistantThread{}, err
	}
	return created, nil
}

func (s *encryptedStore) GetAssistantThread(ctx context.Context, scope Scope, threadID string) (AssistantThread, error) {
	thread, err := s.inner.GetAssistantThread(ctx, scope, threadID)
	if err != nil {
		return AssistantThread{}, err
	}
	if err := s.decryptAssistantThread(scope, &thread); err != nil {
		return AssistantThread{}, err
	}
	return thread, nil
}

func (s *encryptedStore) ListAssistantThreads(ctx context.Context, scope Scope, actorID string, includeArchived bool, limit int, cursor string) (AssistantThreadPage, error) {
	page, err := s.inner.ListAssistantThreads(ctx, scope, actorID, includeArchived, limit, cursor)
	if err != nil {
		return AssistantThreadPage{}, err
	}
	for index := range page.Items {
		if err := s.decryptAssistantThread(scope, &page.Items[index]); err != nil {
			return AssistantThreadPage{}, err
		}
	}
	return page, nil
}

func (s *encryptedStore) UpdateAssistantThread(ctx context.Context, scope Scope, thread AssistantThread) (AssistantThread, error) {
	prepared, err := prepareAssistantThread(thread)
	if err != nil {
		return AssistantThread{}, err
	}
	prepared, err = s.encryptAssistantThread(scope, prepared)
	if err != nil {
		return AssistantThread{}, err
	}
	updated, err := s.inner.UpdateAssistantThread(ctx, scope, prepared)
	if err != nil {
		return AssistantThread{}, err
	}
	if err := s.decryptAssistantThread(scope, &updated); err != nil {
		return AssistantThread{}, err
	}
	return updated, nil
}

func (s *encryptedStore) SetAssistantThreadTitleIfEmpty(ctx context.Context, scope Scope, threadID, actorID, title string, event AssistantThreadEvent) (AssistantThread, bool, error) {
	prepared, err := prepareAssistantThread(AssistantThread{ID: threadID, ActorID: actorID, Title: title})
	if err != nil {
		return AssistantThread{}, false, err
	}
	prepared, err = s.encryptAssistantThread(scope, prepared)
	if err != nil {
		return AssistantThread{}, false, err
	}
	event.ThreadID = prepared.ID
	if len(event.Payload) == 0 || string(event.Payload) == "{}" {
		event.Payload, err = json.Marshal(map[string]any{"thread": map[string]any{"id": prepared.ID, "title": title}})
		if err != nil {
			return AssistantThread{}, false, err
		}
	}
	encryptedEvent, err := s.encryptAssistantThreadEvent(scope, event)
	if err != nil {
		return AssistantThread{}, false, err
	}
	updated, changed, err := s.inner.SetAssistantThreadTitleIfEmpty(ctx, scope, prepared.ID, prepared.ActorID, prepared.Title, encryptedEvent)
	if err != nil {
		return AssistantThread{}, false, err
	}
	if err := s.decryptAssistantThread(scope, &updated); err != nil {
		return AssistantThread{}, false, err
	}
	return updated, changed, nil
}

func (s *encryptedStore) DeleteAssistantThread(ctx context.Context, scope Scope, threadID, actorID string) error {
	return s.inner.DeleteAssistantThread(ctx, scope, threadID, actorID)
}

func (s *encryptedStore) UpdateAssistantThreadWithEvent(ctx context.Context, scope Scope, thread AssistantThread, event AssistantThreadEvent, expectedSequence int64) (AssistantThread, AssistantThreadEvent, error) {
	var err error
	thread, err = prepareAssistantThread(thread)
	if err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, err
	}
	thread, err = s.encryptAssistantThread(scope, thread)
	if err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, err
	}
	event.ThreadID = thread.ID
	encrypted, err := s.encryptAssistantThreadEvent(scope, event)
	if err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, err
	}
	updated, created, err := s.inner.UpdateAssistantThreadWithEvent(ctx, scope, thread, encrypted, expectedSequence)
	if err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, err
	}
	if err := s.decryptAssistantThread(scope, &updated); err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, err
	}
	if err := s.decryptAssistantThreadEvent(scope, &created); err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, err
	}
	return updated, created, nil
}

func (s *encryptedStore) CreateAssistantTurn(ctx context.Context, scope Scope, turn AssistantTurn, events []AssistantThreadEvent) (AssistantTurn, error) {
	var err error
	turn, err = s.encryptAssistantTurn(scope, turn)
	if err != nil {
		return AssistantTurn{}, err
	}
	encryptedEvents := make([]AssistantThreadEvent, len(events))
	for index, event := range events {
		event.ThreadID, event.TurnID = turn.ThreadID, turn.ID
		encryptedEvents[index], err = s.encryptAssistantThreadEvent(scope, event)
		if err != nil {
			return AssistantTurn{}, err
		}
	}
	created, err := s.inner.CreateAssistantTurn(ctx, scope, turn, encryptedEvents)
	if err != nil {
		return AssistantTurn{}, err
	}
	if err := s.decryptAssistantTurn(scope, &created); err != nil {
		return AssistantTurn{}, err
	}
	return created, nil
}

func (s *encryptedStore) GetAssistantTurn(ctx context.Context, scope Scope, threadID, turnID string) (AssistantTurn, error) {
	turn, err := s.inner.GetAssistantTurn(ctx, scope, threadID, turnID)
	if err != nil {
		return AssistantTurn{}, err
	}
	if err := s.decryptAssistantTurn(scope, &turn); err != nil {
		return AssistantTurn{}, err
	}
	return turn, nil
}

func (s *encryptedStore) FindAssistantTurnByClientUserMessageID(ctx context.Context, scope Scope, threadID, clientUserMessageID string) (AssistantTurn, error) {
	turn, err := s.inner.FindAssistantTurnByClientUserMessageID(ctx, scope, threadID, clientUserMessageID)
	if err != nil {
		return AssistantTurn{}, err
	}
	if err := s.decryptAssistantTurn(scope, &turn); err != nil {
		return AssistantTurn{}, err
	}
	return turn, nil
}

func (s *encryptedStore) ActiveAssistantTurn(ctx context.Context, scope Scope, threadID string) (AssistantTurn, error) {
	turn, err := s.inner.ActiveAssistantTurn(ctx, scope, threadID)
	if err != nil {
		return AssistantTurn{}, err
	}
	if err := s.decryptAssistantTurn(scope, &turn); err != nil {
		return AssistantTurn{}, err
	}
	return turn, nil
}

func (s *encryptedStore) SaveAssistantTurn(ctx context.Context, scope Scope, turn AssistantTurn) error {
	encrypted, err := s.encryptAssistantTurn(scope, turn)
	if err != nil {
		return err
	}
	return s.inner.SaveAssistantTurn(ctx, scope, encrypted)
}

func (s *encryptedStore) SaveAssistantTurnWithEvent(ctx context.Context, scope Scope, turn AssistantTurn, event AssistantThreadEvent, expectedSequence int64) error {
	var err error
	turn, err = s.encryptAssistantTurn(scope, turn)
	if err != nil {
		return err
	}
	event.ThreadID, event.TurnID = turn.ThreadID, turn.ID
	event, err = s.encryptAssistantThreadEvent(scope, event)
	if err != nil {
		return err
	}
	return s.inner.SaveAssistantTurnWithEvent(ctx, scope, turn, event, expectedSequence)
}

func (s *encryptedStore) AppendAssistantThreadEvent(ctx context.Context, scope Scope, event AssistantThreadEvent, expectedSequence int64) (AssistantThreadEvent, error) {
	encrypted, err := s.encryptAssistantThreadEvent(scope, event)
	if err != nil {
		return AssistantThreadEvent{}, err
	}
	created, err := s.inner.AppendAssistantThreadEvent(ctx, scope, encrypted, expectedSequence)
	if err != nil {
		return AssistantThreadEvent{}, err
	}
	if err := s.decryptAssistantThreadEvent(scope, &created); err != nil {
		return AssistantThreadEvent{}, err
	}
	return created, nil
}

func (s *encryptedStore) ListAssistantThreadEvents(ctx context.Context, scope Scope, threadID string, afterSequence int64, limit int) ([]AssistantThreadEvent, error) {
	events, err := s.inner.ListAssistantThreadEvents(ctx, scope, threadID, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	for index := range events {
		if err := s.decryptAssistantThreadEvent(scope, &events[index]); err != nil {
			return nil, err
		}
	}
	return events, nil
}

// Thread titles are part of the user-authored transcript and must follow the
// same at-rest protection as thread event payloads.  The title column remains
// text for compatibility with existing schemas; an encrypted assistant-run
// envelope is serialized into that column and transparently decoded on reads.
func (s *encryptedStore) encryptAssistantThread(scope Scope, thread AssistantThread) (AssistantThread, error) {
	if thread.Title == "" {
		return thread, nil
	}
	run := AssistantRun{ID: strings.TrimSpace(thread.ID)}
	payload, err := s.encryptAssistantRunBlob(scope, run, "thread-title", []byte(thread.Title))
	if err != nil {
		return AssistantThread{}, fmt.Errorf("encrypt assistant thread title: %w", err)
	}
	thread.Title = string(payload)
	return thread, nil
}

func (s *encryptedStore) decryptAssistantThread(scope Scope, thread *AssistantThread) error {
	if thread == nil || thread.Title == "" {
		return nil
	}
	payload := json.RawMessage(thread.Title)
	var envelope encryptedAssistantRunCheckpoint
	if err := json.Unmarshal(payload, &envelope); err != nil || !envelope.Encrypted {
		// Titles written before envelope encryption remain readable and are
		// migrated to ciphertext on the next create/update.
		return nil
	}
	run := AssistantRun{ID: strings.TrimSpace(thread.ID)}
	if err := s.decryptAssistantRunBlob(scope, &run, "thread-title", &payload); err != nil {
		return fmt.Errorf("decrypt assistant thread title: %w", err)
	}
	thread.Title = string(payload)
	return nil
}

func (s *encryptedStore) encryptAssistantTurn(scope Scope, turn AssistantTurn) (AssistantTurn, error) {
	run := AssistantRun{ID: turn.ThreadID + "/" + turn.ID}
	var err error
	turn.Checkpoint, err = s.encryptAssistantRunBlob(scope, run, "turn-checkpoint", turn.Checkpoint)
	if err != nil {
		return AssistantTurn{}, err
	}
	turn.Error, err = s.encryptAssistantRunBlob(scope, run, "turn-error", turn.Error)
	if err != nil {
		return AssistantTurn{}, err
	}
	return turn, nil
}

func (s *encryptedStore) decryptAssistantTurn(scope Scope, turn *AssistantTurn) error {
	if turn == nil {
		return nil
	}
	run := AssistantRun{ID: turn.ThreadID + "/" + turn.ID}
	if err := s.decryptAssistantRunBlob(scope, &run, "turn-checkpoint", &turn.Checkpoint); err != nil {
		return err
	}
	return s.decryptAssistantRunBlob(scope, &run, "turn-error", &turn.Error)
}

func (s *encryptedStore) encryptAssistantThreadEvent(scope Scope, event AssistantThreadEvent) (AssistantThreadEvent, error) {
	run := AssistantRun{ID: event.ThreadID + "/" + event.TurnID}
	payload, err := s.encryptAssistantRunBlob(scope, run, "thread-event:"+event.Type+":"+event.ItemID+":"+event.RequestID, event.Payload)
	if err != nil {
		return AssistantThreadEvent{}, err
	}
	event.Payload = payload
	return event, nil
}

func (s *encryptedStore) decryptAssistantThreadEvent(scope Scope, event *AssistantThreadEvent) error {
	if event == nil {
		return nil
	}
	run := AssistantRun{ID: event.ThreadID + "/" + event.TurnID}
	return s.decryptAssistantRunBlob(scope, &run, "thread-event:"+event.Type+":"+event.ItemID+":"+event.RequestID, &event.Payload)
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
	if msg.Content != "" && !msg.ContentEncrypted {
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
	}
	if err := s.encryptMessageMetadata(scope, &msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

const encryptedMessageMetadataKey = "__appStudioEncryptedMetadata"

type encryptedMessageMetadata struct {
	Version int    `json:"version"`
	KeyID   string `json:"keyID"`
	Payload string `json:"payload"`
}

func (s *encryptedStore) encryptMessageMetadata(scope Scope, msg *Message) error {
	if msg == nil || len(msg.Metadata) == 0 {
		return nil
	}
	if _, encrypted := parseEncryptedMessageMetadata(msg.Metadata); encrypted {
		return nil
	}
	plaintext, err := json.Marshal(msg.Metadata)
	if err != nil {
		return fmt.Errorf("encode message %q metadata: %w", msg.ID, err)
	}
	aead := s.keys[s.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(cryptoRand.Reader, nonce); err != nil {
		return fmt.Errorf("generate message metadata nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, messageMetadataAAD(scope, *msg))
	payload := append(nonce, ciphertext...)
	msg.Metadata = map[string]any{
		encryptedMessageMetadataKey: encryptedMessageMetadata{
			Version: 1,
			KeyID:   s.active,
			Payload: base64.RawStdEncoding.EncodeToString(payload),
		},
	}
	return nil
}

func parseEncryptedMessageMetadata(metadata map[string]any) (encryptedMessageMetadata, bool) {
	if len(metadata) != 1 {
		return encryptedMessageMetadata{}, false
	}
	rawEnvelope, exists := metadata[encryptedMessageMetadataKey]
	if !exists {
		return encryptedMessageMetadata{}, false
	}
	encodedEnvelope, err := json.Marshal(rawEnvelope)
	if err != nil {
		return encryptedMessageMetadata{}, false
	}
	var envelope encryptedMessageMetadata
	if err := json.Unmarshal(encodedEnvelope, &envelope); err != nil ||
		envelope.Version != 1 || envelope.KeyID == "" || envelope.Payload == "" {
		return encryptedMessageMetadata{}, false
	}
	return envelope, true
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

func (s *encryptedStore) CreateProjectBootstrapPermit(ctx context.Context, scope Scope, actor, promptDigest string) error {
	return s.inner.CreateProjectBootstrapPermit(ctx, scope, actor, promptDigest)
}

func (s *encryptedStore) ConsumeProjectBootstrapPermit(ctx context.Context, scope Scope, actor, promptDigest, clientRequestID string, now time.Time) (bool, error) {
	return s.inner.ConsumeProjectBootstrapPermit(ctx, scope, actor, promptDigest, clientRequestID, now)
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
	terminalError, err := s.encryptAssistantRunBlob(scope, run, "error", run.Error)
	if err != nil {
		return err
	}
	run.Checkpoint = checkpoint
	run.Audit = audit
	run.Error = terminalError
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
	terminalError, err := s.encryptAssistantRunBlob(scope, run, "error", run.Error)
	if err != nil {
		return AssistantRun{}, err
	}
	run.Checkpoint = checkpoint
	run.Audit = audit
	run.Error = terminalError
	created, err := s.inner.CreateAssistantRun(ctx, scope, user, assistant, run)
	if err != nil {
		return AssistantRun{}, err
	}
	if err := s.decryptAssistantRunBlobs(scope, &created); err != nil {
		return AssistantRun{}, err
	}
	return created, nil
}

func (s *encryptedStore) RequestAssistantRunStopWithAssistantMessage(ctx context.Context, scope Scope, runID string, expectedRunRevision int64, assistant Message, now time.Time) (AssistantRun, error) {
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	assistant, err := s.encryptMessage(scope, assistant)
	if err != nil {
		return AssistantRun{}, err
	}
	run, err := s.inner.RequestAssistantRunStopWithAssistantMessage(ctx, scope, runID, expectedRunRevision, assistant, now)
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
	terminalError, err := s.encryptAssistantRunBlob(scope, run, "error", run.Error)
	if err != nil {
		return err
	}
	run.Checkpoint = checkpoint
	run.Audit = audit
	run.Error = terminalError
	encryptedMessages := make([]Message, len(messages))
	for i := range messages {
		encryptedMessages[i], err = s.encryptMessage(scope, messages[i])
		if err != nil {
			return err
		}
	}
	return s.inner.SaveAssistantRunSnapshot(ctx, scope, run, encryptedMessages, expectedRevision)
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

func (s *encryptedStore) AppendAssistantRunEvent(ctx context.Context, scope Scope, event AssistantRunEvent, expectedSequence int64) (AssistantRunEvent, error) {
	event, err := prepareAssistantRunEvent(scope, event, expectedSequence)
	if err != nil {
		return AssistantRunEvent{}, err
	}
	event.Payload, err = s.encryptAssistantRunBlob(scope, AssistantRun{ID: event.RunID}, assistantRunEventBlobLabel(event), event.Payload)
	if err != nil {
		return AssistantRunEvent{}, err
	}
	saved, err := s.inner.AppendAssistantRunEvent(ctx, scope, event, expectedSequence)
	if err != nil {
		return AssistantRunEvent{}, err
	}
	if err := s.decryptAssistantRunEvent(scope, &saved); err != nil {
		return AssistantRunEvent{}, err
	}
	return saved, nil
}

func (s *encryptedStore) ListAssistantRunEvents(ctx context.Context, scope Scope, runID string, afterSequence int64, limit int) ([]AssistantRunEvent, error) {
	events, err := s.inner.ListAssistantRunEvents(ctx, scope, runID, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	for i := range events {
		if err := s.decryptAssistantRunEvent(scope, &events[i]); err != nil {
			return nil, err
		}
	}
	return events, nil
}

func (s *encryptedStore) AppendAssistantConversationItem(ctx context.Context, scope Scope, item AssistantConversationItem) (AssistantConversationItem, error) {
	prepared, err := prepareAssistantConversationItem(scope, item)
	if err != nil {
		return AssistantConversationItem{}, err
	}
	if existing, found, err := s.findAssistantConversationItem(ctx, scope, prepared.RunID, prepared.ID); err != nil {
		return AssistantConversationItem{}, err
	} else if found {
		if !assistantConversationItemsMatch(existing, prepared) {
			return AssistantConversationItem{}, ErrAssistantConversationItemConflict
		}
		return existing, nil
	}
	run := AssistantRun{ID: prepared.RunID}
	prepared.Payload, err = s.encryptAssistantRunBlob(scope, run, "conversation:"+prepared.ID, prepared.Payload)
	if err != nil {
		return AssistantConversationItem{}, err
	}
	created, err := s.inner.AppendAssistantConversationItem(ctx, scope, prepared)
	if errors.Is(err, ErrAssistantConversationItemConflict) {
		existing, found, findErr := s.findAssistantConversationItem(ctx, scope, item.RunID, item.ID)
		if findErr != nil {
			return AssistantConversationItem{}, findErr
		}
		if found && assistantConversationItemsMatch(existing, item) {
			return existing, nil
		}
	}
	if err != nil {
		return AssistantConversationItem{}, err
	}
	if err := s.decryptAssistantRunBlob(scope, &run, "conversation:"+created.ID, &created.Payload); err != nil {
		return AssistantConversationItem{}, err
	}
	return created, nil
}

func (s *encryptedStore) findAssistantConversationItem(ctx context.Context, scope Scope, runID, itemID string) (AssistantConversationItem, bool, error) {
	after := int64(0)
	for {
		items, err := s.inner.ListAssistantConversationItems(ctx, scope, after, 500)
		if err != nil {
			return AssistantConversationItem{}, false, err
		}
		for _, item := range items {
			if item.RunID != runID || item.ID != itemID {
				continue
			}
			run := AssistantRun{ID: item.RunID}
			if err := s.decryptAssistantRunBlob(scope, &run, "conversation:"+item.ID, &item.Payload); err != nil {
				return AssistantConversationItem{}, false, err
			}
			return item, true, nil
		}
		if len(items) < 500 {
			return AssistantConversationItem{}, false, nil
		}
		after = items[len(items)-1].Sequence
	}
}

func (s *encryptedStore) ListAssistantConversationItems(ctx context.Context, scope Scope, afterSequence int64, limit int) ([]AssistantConversationItem, error) {
	items, err := s.inner.ListAssistantConversationItems(ctx, scope, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		run := AssistantRun{ID: items[i].RunID}
		if err := s.decryptAssistantRunBlob(scope, &run, "conversation:"+items[i].ID, &items[i].Payload); err != nil {
			return nil, err
		}
	}
	return items, nil
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
	if msg == nil {
		return nil
	}
	if msg.ContentEncrypted {
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
	}
	return s.decryptMessageMetadata(scope, msg)
}

func (s *encryptedStore) decryptMessageMetadata(scope Scope, msg *Message) error {
	if msg == nil || len(msg.Metadata) == 0 {
		return nil
	}
	_, exists := msg.Metadata[encryptedMessageMetadataKey]
	if !exists {
		return nil
	}
	if len(msg.Metadata) != 1 {
		return fmt.Errorf("encrypted message %q metadata envelope contains plaintext fields", msg.ID)
	}
	envelope, ok := parseEncryptedMessageMetadata(msg.Metadata)
	if !ok {
		return fmt.Errorf("encrypted message %q metadata envelope is invalid", msg.ID)
	}
	aead := s.keys[envelope.KeyID]
	if aead == nil {
		return fmt.Errorf("message %q metadata uses unknown encryption key %q", msg.ID, envelope.KeyID)
	}
	payload, err := base64.RawStdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return fmt.Errorf("decode encrypted message %q metadata: %w", msg.ID, err)
	}
	if len(payload) < aead.NonceSize() {
		return fmt.Errorf("encrypted message %q metadata is too short", msg.ID)
	}
	plaintext, err := aead.Open(
		nil,
		payload[:aead.NonceSize()],
		payload[aead.NonceSize():],
		messageMetadataAAD(scope, *msg),
	)
	if err != nil {
		return fmt.Errorf("decrypt message %q metadata: %w", msg.ID, err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(plaintext, &metadata); err != nil {
		return fmt.Errorf("decode message %q metadata: %w", msg.ID, err)
	}
	msg.Metadata = metadata
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

func messageMetadataAAD(scope Scope, msg Message) []byte {
	return append(messageAAD(scope, msg), []byte("\x00metadata")...)
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
	if err := s.decryptAssistantRunBlob(scope, run, "audit", &run.Audit); err != nil {
		return err
	}
	return s.decryptAssistantRunBlob(scope, run, "error", &run.Error)
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

func assistantRunEventBlobLabel(event AssistantRunEvent) string {
	return fmt.Sprintf("event:%d:%s", event.Sequence, event.Type)
}

func (s *encryptedStore) decryptAssistantRunEvent(scope Scope, event *AssistantRunEvent) error {
	if event == nil {
		return nil
	}
	run := AssistantRun{ID: event.RunID}
	return s.decryptAssistantRunBlob(scope, &run, assistantRunEventBlobLabel(*event), &event.Payload)
}
