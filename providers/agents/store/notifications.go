// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrSessionOwnerConflict = errors.New("session owner is already set or session is not eligible for ownership")
	ErrSessionAccessDenied  = errors.New("caller is not authorized for this session")
	ErrNotificationConflict = errors.New("notification idempotency key was reused with different content")
	ErrNotificationInput    = errors.New("invalid notification input")
)

// SessionNotificationStore owns exact-session access and a durable notification
// feed. Notifications are intentionally separate from transcript messages.
type SessionNotificationStore interface {
	ClaimSessionOwner(context.Context, Scope, string, string, string, time.Time) error
	GrantSessionAccess(context.Context, Scope, string, string, string, string, time.Time) error
	RevokeSessionAccess(context.Context, Scope, string, string, string, string) error
	HasSessionAccess(context.Context, Scope, string, string, string) (bool, error)
	CreateSessionNotification(context.Context, Scope, string, string, string, string, string, time.Time, ...string) (SessionNotification, bool, error)
	ListSessionNotifications(context.Context, Scope, string, string, string, int) ([]SessionNotification, error)
}

// SessionNotification is a durable notification-only event. It is not a model
// message and is never replayed as transcript context.
type SessionNotification struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionID"`
	Key       string    `json:"key"`
	Producer  string    `json:"producer"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"createdAt"`
}

func notificationFingerprint(payload string) string {
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func validateSessionAccess(agentUID, sessionID, subject string) error {
	if strings.TrimSpace(agentUID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(subject) == "" {
		return fmt.Errorf("agent UID, session ID, and caller subject are required")
	}
	return nil
}
