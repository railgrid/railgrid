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
	pending.Name = strings.TrimSpace(pending.Name)
	if pending.Name == "" {
		return errors.New("pending commit name is required")
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
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		recorded := pending
		record.PendingCommit = &recorded
		return true, nil
	}); err != nil {
		return fmt.Errorf("persist workspace pending commit: %w", err)
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
	if pending.Name == "" || pending.WorkspaceDigest == "" || len(pathSet) == 0 {
		return PendingCommit{}, false, errors.New("invalid workspace pending commit")
	}
	pending.Paths = sortedWorkspaceSourcePaths(pathSet)
	return pending, true, nil
}

// ClearPendingCommit forgets the in-flight RepositoryCommit. Clearing an
// absent record is not an error.
func (s *FileStore) ClearPendingCommit(ctx context.Context, scope Scope) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return err
	}
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		if record.PendingCommit == nil {
			return false, nil
		}
		record.PendingCommit = nil
		return true, nil
	}); err != nil {
		return fmt.Errorf("clear workspace pending commit: %w", err)
	}
	return nil
}
