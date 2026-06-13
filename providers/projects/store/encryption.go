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
	if msg.Content == "" || msg.ContentEncrypted {
		return s.inner.AppendMessage(ctx, scope, msg)
	}
	aead := s.keys[s.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(cryptoRand.Reader, nonce); err != nil {
		return fmt.Errorf("generate message nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, []byte(msg.Content), messageAAD(scope, msg))
	payload := append(nonce, ciphertext...)
	msg.Content = base64.RawStdEncoding.EncodeToString(payload)
	msg.ContentEncrypted = true
	msg.ContentKeyID = s.active
	return s.inner.AppendMessage(ctx, scope, msg)
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
		msg.ID,
		msg.Role,
	}, "\x00"))
}

var _ Store = (*encryptedStore)(nil)
