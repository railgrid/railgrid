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
				return false, nil
			}
			return false, ErrPendingCommitChanged
		}
		pending.Name = name
		return true, nil
	}); err != nil {
		return fmt.Errorf("bind RepositoryCommit to pending intent: %w", err)
	}
	return nil
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
	validLegacy := pending.RequestID == "" && pending.Name != ""
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
		if record.PendingCommit == nil || !matchesPendingCommit(record.PendingCommit, requestID, name) {
			return false, nil
		}
		pending := record.PendingCommit
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
		a.SourceRevision != b.SourceRevision || strings.Join(a.Paths, "\x00") != strings.Join(b.Paths, "\x00") {
		return false
	}
	return sameAttributions(a.Attributions, b.Attributions)
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
