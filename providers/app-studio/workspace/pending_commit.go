/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package workspace

// The in-flight commit.
//
// A RepositoryCommit the Code provider accepted but has not finished is the
// same ledger one step before settlement: the content digest and paths a
// commit carried, waiting for the commit to land. It lives on the Project's
// status beside the settlement receipt and the dirty set it will clear, so the
// replica that follows the commit up does not have to be the replica that sent
// it.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrPendingCommitExists means another immutable commit intent already owns
// this project's single pending-commit slot.
var ErrPendingCommitExists = errors.New("a project commit is already pending")

// ErrPendingCommitChanged means a completion callback belongs to an older
// intent than the one currently stored in the ledger.
var ErrPendingCommitChanged = errors.New("pending project commit changed")

// RecordPendingCommit durably remembers an in-flight RepositoryCommit so a
// later reconcile (or process, or replica) follows it up by name instead of
// resending.
func (s *FileStore) RecordPendingCommit(ctx context.Context, scope Scope, pending PendingCommit) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return err
	}
	if err := normalizePendingCommit(&pending, false); err != nil {
		return err
	}
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		if record.PendingCommit != nil {
			if samePendingCommit(record.PendingCommit, &pending) {
				return false, nil
			}
			return false, ErrPendingCommitExists
		}
		recorded := pending
		record.PendingCommit = &recorded
		return true, nil
	}); err != nil {
		return fmt.Errorf("persist workspace pending commit: %w", err)
	}
	return nil
}

// ReservePendingCommit durably claims the project's one pending-commit slot
// before calling Code to create a RepositoryCommit. The request ID and staged
// bundle make the external action safe to retry after a lost response.
func (s *FileStore) ReservePendingCommit(ctx context.Context, scope Scope, pending PendingCommit) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return err
	}
	pending.Name = ""
	if err := normalizePendingCommit(&pending, true); err != nil {
		return err
	}
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		if record.PendingCommit != nil {
			if samePendingIntent(record.PendingCommit, &pending) {
				return false, nil
			}
			return false, ErrPendingCommitExists
		}
		currentRevision := record.SourceRevision
		if currentRevision == 0 {
			currentRevision = 1
		}
		if currentRevision != pending.SourceRevision {
			return false, fmt.Errorf("%w: expected %d, current %d", ErrSourceRevisionConflict, pending.SourceRevision, currentRevision)
		}
		recorded := pending
		record.PendingCommit = &recorded
		return true, nil
	}); err != nil {
		return fmt.Errorf("reserve workspace pending commit: %w", err)
	}
	return nil
}

// BindPendingCommitName attaches the Code-created object to its durable
// intent. A delayed response from an older action cannot replace a newer
// intent or change the name already bound to the same request.
func (s *FileStore) BindPendingCommitName(ctx context.Context, scope Scope, requestID, name string) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	requestID, name = strings.TrimSpace(requestID), strings.TrimSpace(name)
	if requestID == "" || name == "" {
		return errors.New("pending commit request ID and name are required")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return err
	}
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		pending := record.PendingCommit
		if pending == nil || pending.RequestID != requestID {
			return false, ErrPendingCommitChanged
		}
		if pending.Name != "" {
			if pending.Name == name {
				if pending.BlockedReason != "" {
					pending.BlockedReason = ""
					pending.BlockedMessage = ""
					pending.BlockedAt = nil
					return true, nil
				}
				return false, nil
			}
			return false, ErrPendingCommitChanged
		}
		pending.Name = name
		// A successful delayed action response proves that the operation has a
		// durable object. It wins over a replay failure recorded from an older
		// attempt and returns the slot to normal watch-driven settlement.
		pending.BlockedReason = ""
		pending.BlockedMessage = ""
		pending.BlockedAt = nil
		return true, nil
	}); err != nil {
		return fmt.Errorf("bind RepositoryCommit to pending intent: %w", err)
	}
	return nil
}

// BlockPendingCommit records a permanent failure while the keyed request is
// still unnamed. A concurrent name bind wins: once Code has returned a
// RepositoryCommit, that object is followed through the watch instead.
func (s *FileStore) BlockPendingCommit(ctx context.Context, scope Scope, requestID, reason, message string) (bool, error) {
	return s.blockPendingCommit(ctx, scope, requestID, "", reason, message)
}

// BlockNamedPendingCommit records a permanent replay failure for the exact
// named object attached to a keyed intent. The name fence keeps a late error
// from blocking a replacement request or a newer binding.
func (s *FileStore) BlockNamedPendingCommit(ctx context.Context, scope Scope, requestID, name, reason, message string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, errors.New("blocking a named pending commit requires its RepositoryCommit name")
	}
	return s.blockPendingCommit(ctx, scope, requestID, name, reason, message)
}

func (s *FileStore) blockPendingCommit(ctx context.Context, scope Scope, requestID, expectedName, reason, message string) (bool, error) {
	if s == nil {
		return false, errors.New("project workspace store is not configured")
	}
	requestID, expectedName = strings.TrimSpace(requestID), strings.TrimSpace(expectedName)
	reason = strings.TrimSpace(reason)
	message = strings.TrimSpace(message)
	if (requestID == "" && expectedName == "") || reason == "" || message == "" {
		return false, errors.New("blocking a pending commit requires its request ID or name, reason, and message")
	}
	if len(reason) > 64 || len(message) > 512 {
		return false, errors.New("pending commit block reason or message exceeds its limit")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return false, err
	}
	blocked := false
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		blocked = false
		pending := record.PendingCommit
		if pending == nil || pending.RequestID != requestID || pending.Name != expectedName {
			return false, nil
		}
		if pending.BlockedReason == reason && pending.BlockedMessage == message && pending.BlockedAt != nil {
			return false, nil
		}
		now := time.Now().UTC()
		if pending.BlockedReason == reason && pending.BlockedAt != nil {
			now = *pending.BlockedAt
		}
		pending.BlockedReason = reason
		pending.BlockedMessage = message
		pending.BlockedAt = &now
		blocked = true
		return true, nil
	}); err != nil {
		return false, fmt.Errorf("block workspace pending commit: %w", err)
	}
	return blocked, nil
}

// RetryBlockedPendingCommit clears a block only for an explicit, fresh
// operator token on the same still-unnamed request. The token is stored on the
// intent so a stale Project annotation cannot repeatedly wake a later retry.
func (s *FileStore) RetryBlockedPendingCommit(ctx context.Context, scope Scope, requestID, token string) (bool, error) {
	return s.retryBlockedPendingCommit(ctx, scope, requestID, "", token)
}

// RetryNamedBlockedPendingCommit clears a block only for the matching named
// request. The caller still checks the existing CR before retrying its action.
func (s *FileStore) RetryNamedBlockedPendingCommit(ctx context.Context, scope Scope, requestID, name, token string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, errors.New("retrying a named blocked commit requires its RepositoryCommit name")
	}
	return s.retryBlockedPendingCommit(ctx, scope, requestID, name, token)
}

// ClearNamedPendingCommitBlock clears metadata from an exact named pending
// object after it reappears in a nonterminal phase. Its timestamp fence keeps
// an old watch event from clearing a newer block on the same request.
func (s *FileStore) ClearNamedPendingCommitBlock(ctx context.Context, scope Scope, requestID, name, reason string, blockedAt *time.Time) (bool, error) {
	if s == nil {
		return false, errors.New("project workspace store is not configured")
	}
	requestID, name, reason = strings.TrimSpace(requestID), strings.TrimSpace(name), strings.TrimSpace(reason)
	if name == "" || reason == "" || blockedAt == nil {
		return false, errors.New("clearing a named pending commit block requires its name, reason, and block timestamp")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return false, err
	}
	cleared := false
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		cleared = false
		pending := record.PendingCommit
		if pending == nil || pending.RequestID != requestID || pending.Name != name ||
			pending.BlockedReason != reason || !sameTime(pending.BlockedAt, blockedAt) {
			return false, nil
		}
		pending.BlockedReason = ""
		pending.BlockedMessage = ""
		pending.BlockedAt = nil
		cleared = true
		return true, nil
	}); err != nil {
		return false, fmt.Errorf("clear named pending commit block: %w", err)
	}
	return cleared, nil
}

func (s *FileStore) retryBlockedPendingCommit(ctx context.Context, scope Scope, requestID, expectedName, token string) (bool, error) {
	if s == nil {
		return false, errors.New("project workspace store is not configured")
	}
	requestID, expectedName, token = strings.TrimSpace(requestID), strings.TrimSpace(expectedName), strings.TrimSpace(token)
	if requestID == "" || token == "" || len(token) > 160 {
		return false, errors.New("retrying a blocked commit requires its request ID and a token of at most 160 characters")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return false, err
	}
	retried := false
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		retried = false
		pending := record.PendingCommit
		if pending == nil || pending.RequestID != requestID || pending.Name != expectedName || pending.BlockedReason == "" || pending.RetryToken == token {
			return false, nil
		}
		pending.BlockedReason = ""
		pending.BlockedMessage = ""
		pending.BlockedAt = nil
		pending.RetryToken = token
		retried = true
		return true, nil
	}); err != nil {
		return false, fmt.Errorf("retry blocked workspace pending commit: %w", err)
	}
	return retried, nil
}

// PendingCommit returns the in-flight RepositoryCommit, if any.
func (s *FileStore) PendingCommit(ctx context.Context, scope Scope) (PendingCommit, bool, error) {
	if s == nil {
		return PendingCommit{}, false, errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return PendingCommit{}, false, err
	}
	record, err := ledger.Read(ctx, scope)
	if err != nil {
		return PendingCommit{}, false, fmt.Errorf("read workspace pending commit: %w", err)
	}
	if record.PendingCommit == nil {
		return PendingCommit{}, false, nil
	}
	pending := *record.PendingCommit
	requestIDWasPresent := pending.RequestID != ""
	pathSet := make(map[string]struct{}, len(pending.Paths))
	for _, rawPath := range pending.Paths {
		clean, err := cleanProjectPath(rawPath)
		if err != nil {
			return PendingCommit{}, false, fmt.Errorf("invalid workspace pending commit: %w", err)
		}
		pathSet[clean] = struct{}{}
	}
	pending.Name = strings.TrimSpace(pending.Name)
	pending.RequestID = strings.TrimSpace(pending.RequestID)
	pending.RepositoryRef = strings.TrimSpace(pending.RepositoryRef)
	pending.RepositoryUID = strings.TrimSpace(pending.RepositoryUID)
	pending.BundleRef = strings.TrimSpace(pending.BundleRef)
	pending.BundleDigest = strings.TrimSpace(pending.BundleDigest)
	pending.Message = strings.TrimSpace(pending.Message)
	pending.Branch = strings.TrimSpace(pending.Branch)
	pending.BlockedReason = strings.TrimSpace(pending.BlockedReason)
	pending.BlockedMessage = strings.TrimSpace(pending.BlockedMessage)
	pending.RetryToken = strings.TrimSpace(pending.RetryToken)
	validLegacy := !requestIDWasPresent && pending.RequestID == "" && pending.Name != ""
	validIntent := pending.RequestID != "" && pending.RepositoryRef != "" && pending.RepositoryUID != "" && pending.BundleRef != "" && pending.BundleDigest != ""
	if (!validLegacy && !validIntent) || pending.WorkspaceDigest == "" || len(pathSet) == 0 {
		return PendingCommit{}, false, errors.New("invalid workspace pending commit")
	}
	pending.Paths = sortedWorkspaceSourcePaths(pathSet)
	pending.Attributions = NormalizeSourceAttributions(pending.Attributions, pending.Paths)
	return pending, true, nil
}

// ClearPendingCommit forgets the in-flight RepositoryCommit. Clearing an
// absent record is not an error.
func (s *FileStore) ClearPendingCommit(ctx context.Context, scope Scope, requestID, name string) (bool, error) {
	if s == nil {
		return false, errors.New("project workspace store is not configured")
	}
	requestID, name = strings.TrimSpace(requestID), strings.TrimSpace(name)
	if requestID == "" && name == "" {
		return false, errors.New("pending commit request ID or name is required")
	}
	if requestID != "" && name == "" {
		return false, errors.New("keyed pending commit clear requires its bound RepositoryCommit name")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return false, err
	}
	cleared := false
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		if record.PendingCommit == nil || !matchesPendingCommit(record.PendingCommit, requestID, name) {
			return false, nil
		}
		record.PendingCommit = nil
		cleared = true
		return true, nil
	}); err != nil {
		return false, fmt.Errorf("clear workspace pending commit: %w", err)
	}
	return cleared, nil
}

// FailPendingCommit clears a commit that did not land. If a path changed after
// its pending snapshot, its current bytes now combine with source that was not
// committed; discard that path's newer owner so a retry cannot falsely credit
// the later turn for the failed commit's content.
func (s *FileStore) FailPendingCommit(ctx context.Context, scope Scope, requestID, name string) (bool, error) {
	return s.failPendingCommit(ctx, scope, requestID, name, nil)
}

// FailBlockedPendingCommit clears a failed commit only if its immutable
// request/name and persisted cooldown timestamp still match the caller's
// snapshot. This prevents a stale cooldown timer from releasing a newer block.
func (s *FileStore) FailBlockedPendingCommit(ctx context.Context, scope Scope, requestID, name, reason string, blockedAt *time.Time) (bool, error) {
	requestID, name, reason = strings.TrimSpace(requestID), strings.TrimSpace(name), strings.TrimSpace(reason)
	if name == "" || reason == "" || blockedAt == nil {
		return false, errors.New("releasing a blocked pending commit requires its name, reason, and block timestamp")
	}
	fence := &pendingCommitBlockFence{reason: reason, blockedAt: *blockedAt}
	return s.failPendingCommit(ctx, scope, requestID, name, fence)
}

type pendingCommitBlockFence struct {
	reason    string
	blockedAt time.Time
}

func (s *FileStore) failPendingCommit(ctx context.Context, scope Scope, requestID, name string, fence *pendingCommitBlockFence) (bool, error) {
	if s == nil {
		return false, errors.New("project workspace store is not configured")
	}
	requestID, name = strings.TrimSpace(requestID), strings.TrimSpace(name)
	if requestID == "" && name == "" {
		return false, errors.New("pending commit request ID or name is required")
	}
	if requestID != "" && name == "" {
		return false, errors.New("keyed pending commit failure requires its bound RepositoryCommit name")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return false, err
	}
	failed := false
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		failed = false
		if record.PendingCommit == nil || !matchesPendingCommit(record.PendingCommit, requestID, name) {
			return false, nil
		}
		pending := record.PendingCommit
		if fence != nil && (pending.BlockedReason != fence.reason || !sameTime(pending.BlockedAt, &fence.blockedAt)) {
			return false, nil
		}
		captured := map[string]SourceAttribution{}
		for _, attribution := range pending.Attributions {
			for _, path := range attribution.Paths {
				if _, exists := captured[path]; exists {
					delete(captured, path)
					continue
				}
				captured[path] = attribution
			}
		}
		paths := make(map[string]struct{}, len(pending.Paths))
		for _, path := range pending.Paths {
			paths[path] = struct{}{}
		}
		for index := range record.SourcePathAttributions {
			current := &record.SourcePathAttributions[index]
			if _, ok := paths[current.Path]; !ok {
				continue
			}
			owner, wasCaptured := captured[current.Path]
			if !wasCaptured || owner.ThreadID != current.ThreadID || owner.RunID != current.RunID {
				current.ThreadID, current.RunID = "", ""
			}
		}
		record.PendingCommit = nil
		failed = true
		return true, nil
	}); err != nil {
		return false, fmt.Errorf("fail workspace pending commit: %w", err)
	}
	return failed, nil
}

func normalizePendingCommit(pending *PendingCommit, reservation bool) error {
	if pending == nil {
		return errors.New("pending commit is required")
	}
	pending.Name = strings.TrimSpace(pending.Name)
	pending.RequestID = strings.TrimSpace(pending.RequestID)
	pending.RepositoryRef = strings.TrimSpace(pending.RepositoryRef)
	pending.RepositoryUID = strings.TrimSpace(pending.RepositoryUID)
	pending.BundleRef = strings.TrimSpace(pending.BundleRef)
	pending.BundleDigest = strings.TrimSpace(pending.BundleDigest)
	pending.Message = strings.TrimSpace(pending.Message)
	pending.Branch = strings.TrimSpace(pending.Branch)
	pending.BlockedReason = strings.TrimSpace(pending.BlockedReason)
	pending.BlockedMessage = strings.TrimSpace(pending.BlockedMessage)
	pending.RetryToken = strings.TrimSpace(pending.RetryToken)
	if len(pending.BlockedReason) > 64 || len(pending.BlockedMessage) > 512 || len(pending.RetryToken) > 160 {
		return errors.New("pending commit block metadata exceeds its limit")
	}
	if pending.WorkspaceDigest == "" {
		return errors.New("pending commit workspace digest is required")
	}
	pathSet := make(map[string]struct{}, len(pending.Paths))
	for _, raw := range pending.Paths {
		clean, err := cleanProjectPath(raw)
		if err != nil {
			return err
		}
		pathSet[clean] = struct{}{}
	}
	if len(pathSet) == 0 {
		return errors.New("pending commit paths are required")
	}
	pending.Paths = sortedWorkspaceSourcePaths(pathSet)
	pending.Attributions = NormalizeSourceAttributions(pending.Attributions, pending.Paths)
	if pending.RequestID != "" {
		if pending.RepositoryRef == "" || pending.RepositoryUID == "" || pending.BundleRef == "" || pending.BundleDigest == "" {
			return errors.New("keyed pending commit requires repository identity and staged bundle")
		}
		if reservation && pending.Name != "" {
			return errors.New("new pending commit reservation cannot name an uncreated RepositoryCommit")
		}
	} else if pending.Name == "" || reservation {
		return errors.New("pending commit name or replay-safe request ID is required")
	}
	return nil
}

func matchesPendingCommit(pending *PendingCommit, requestID, name string) bool {
	if pending == nil {
		return false
	}
	if requestID != "" {
		return name != "" && pending.RequestID == requestID && pending.Name == name
	}
	return pending.RequestID == "" && name != "" && pending.Name == name
}

func samePendingCommit(a, b *PendingCommit) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.RequestID != b.RequestID || a.Name != b.Name || a.RepositoryRef != b.RepositoryRef ||
		a.RepositoryUID != b.RepositoryUID || a.BundleRef != b.BundleRef || a.BundleDigest != b.BundleDigest ||
		a.Message != b.Message || a.Branch != b.Branch || a.WorkspaceDigest != b.WorkspaceDigest ||
		a.SourceRevision != b.SourceRevision || a.BlockedReason != b.BlockedReason || a.BlockedMessage != b.BlockedMessage ||
		a.RetryToken != b.RetryToken || !sameTime(a.BlockedAt, b.BlockedAt) ||
		strings.Join(a.Paths, "\x00") != strings.Join(b.Paths, "\x00") {
		return false
	}
	return sameAttributions(a.Attributions, b.Attributions)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// SameIntent compares the immutable inputs of a reserved commit. The returned
// RepositoryCommit name is excluded because it is attached after reservation.
func (p PendingCommit) SameIntent(other PendingCommit) bool {
	return samePendingIntent(&p, &other)
}

func samePendingIntent(a, b *PendingCommit) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.RequestID != b.RequestID || a.RepositoryRef != b.RepositoryRef || a.RepositoryUID != b.RepositoryUID ||
		a.BundleRef != b.BundleRef || a.BundleDigest != b.BundleDigest || a.Message != b.Message || a.Branch != b.Branch ||
		a.WorkspaceDigest != b.WorkspaceDigest || a.SourceRevision != b.SourceRevision || strings.Join(a.Paths, "\x00") != strings.Join(b.Paths, "\x00") {
		return false
	}
	return sameAttributions(a.Attributions, b.Attributions)
}

func sameAttributions(a, b []SourceAttribution) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ThreadID != b[i].ThreadID || a[i].RunID != b[i].RunID || strings.Join(a[i].Paths, "\x00") != strings.Join(b[i].Paths, "\x00") {
			return false
		}
	}
	return true
}
