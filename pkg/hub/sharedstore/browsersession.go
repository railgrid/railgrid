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

package sharedstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/browsersession"
)

// SessionKind is the shared-store collection holding browser sessions.
const SessionKind = "railgrid-session"

// PreviewSessionKind is deliberately separate from SessionKind. Older hub
// replicas decode session JSON without scoped fields and would otherwise
// treat a preview handle as an ordinary portal session if it were presented
// under the portal cookie name.
const PreviewSessionKind = "railgrid-preview-session-v2"

// SessionBackend adapts Store to browsersession.Backend so every hub replica
// resolves and revokes the same cookies.
//
// Revocation is a delete, not a tombstone: a handle is 32 bytes of crypto/rand,
// so the collision the in-memory backend's tombstones guard against cannot
// occur, and a delete propagates to every replica immediately instead of
// depending on a per-process marker.
type SessionBackend struct {
	store *Store
}

// NewSessionBackend builds the shared browser-session backend. config must
// target the workspace holding the entries.
func NewSessionBackend(config *rest.Config, namespace string) (*SessionBackend, error) {
	store, err := New(config, namespace, SessionKind)
	if err != nil {
		return nil, err
	}
	return &SessionBackend{store: store}, nil
}

// NewPreviewSessionBackend builds the isolated collection for short-lived,
// one-app preview sessions. It must not share SessionKind with portal logins:
// older hub replicas ignore the scoped identity fields when decoding a shared
// session record.
func NewPreviewSessionBackend(config *rest.Config, namespace string) (*SessionBackend, error) {
	store, err := New(config, namespace, PreviewSessionKind)
	if err != nil {
		return nil, err
	}
	return &SessionBackend{store: store}, nil
}

// Store exposes the underlying collection so the leader can sweep it.
func (b *SessionBackend) Store() *Store { return b.store }

// storedSession is the wire form of a session record. It deliberately mirrors
// browsersession.Record rather than embedding it, so a field added to the
// in-memory type cannot silently change the persisted encoding.
type storedSession struct {
	UserID       string    `json:"userID"`
	Email        string    `json:"email,omitempty"`
	Name         string    `json:"name,omitempty"`
	AppExpiresAt time.Time `json:"appExpiresAt,omitempty"`
	AppScope     string    `json:"appScope,omitempty"`
	RBACIdentity string    `json:"rbacIdentity,omitempty"`
	Issuer       string    `json:"issuer,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	AuthType     string    `json:"authType,omitempty"`
	IssuedAt     time.Time `json:"issuedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

func (b *SessionBackend) Put(ctx context.Context, key string, record browsersession.Record) error {
	value, err := json.Marshal(storedSession{
		UserID:       record.Identity.UserID,
		Email:        record.Identity.Email,
		Name:         record.Identity.Name,
		AppScope:     record.Identity.AppScope,
		AppExpiresAt: record.Identity.AppExpiresAt,
		RBACIdentity: record.Identity.RBACIdentity,
		Issuer:       record.Identity.Issuer,
		Subject:      record.Identity.Subject,
		AuthType:     record.Identity.AuthType,
		IssuedAt:     record.IssuedAt,
		ExpiresAt:    record.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("encoding browser session: %w", err)
	}
	return b.store.Put(ctx, key, value, record.ExpiresAt)
}

func (b *SessionBackend) Get(ctx context.Context, key string) (browsersession.Record, error) {
	value, err := b.store.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return browsersession.Record{}, browsersession.ErrNotFound
	}
	if err != nil {
		return browsersession.Record{}, err
	}
	var stored storedSession
	if err := json.Unmarshal(value, &stored); err != nil {
		// A record we cannot decode is not a session we can authorize on.
		return browsersession.Record{}, browsersession.ErrNotFound
	}
	return browsersession.Record{
		Identity: browsersession.Identity{
			UserID:       stored.UserID,
			Email:        stored.Email,
			Name:         stored.Name,
			AppScope:     stored.AppScope,
			AppExpiresAt: stored.AppExpiresAt,
			RBACIdentity: stored.RBACIdentity,
			Issuer:       stored.Issuer,
			Subject:      stored.Subject,
			AuthType:     stored.AuthType,
		},
		IssuedAt:  stored.IssuedAt,
		ExpiresAt: stored.ExpiresAt,
	}, nil
}

func (b *SessionBackend) Revoke(ctx context.Context, key string, _ time.Time) error {
	return b.store.Delete(ctx, key)
}
