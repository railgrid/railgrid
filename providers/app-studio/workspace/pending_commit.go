/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// workspacePendingCommitFile records a RepositoryCommit the Code provider
// accepted but had not finished when the reconciler sent it. It sits next
// to the settlement receipt because it is the same ledger one step earlier:
// the content digest and paths a commit carried, waiting for the commit to
// land before they can be settled.
const workspacePendingCommitFile = "pending-commit.json"

// PendingCommit is a RepositoryCommit in flight for a project workspace.
type PendingCommit struct {
	// Name is the RepositoryCommit object's name in the tenant workspace.
	Name string `json:"name"`
	// RepositoryRef names the Repository the commit targets.
	RepositoryRef string `json:"repositoryRef"`
	// WorkspaceDigest and Paths are the workspace content the commit carried;
	// they become the settlement receipt once the commit succeeds.
	WorkspaceDigest string   `json:"workspaceDigest"`
	Paths           []string `json:"paths"`
}

// RecordPendingCommit durably remembers an in-flight RepositoryCommit so a
// later reconcile (or process) follows it up by name instead of resending.
func (s *FileStore) RecordPendingCommit(ctx context.Context, scope Scope, pending PendingCommit) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := ctx.Err(); err != nil {
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
	dir, target, err := s.pendingCommitPath(scope)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create workspace pending commit directory: %w", err)
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		return fmt.Errorf("encode workspace pending commit: %w", err)
	}
	if err := writeFileAtomically(dir, target, raw, 0o600, false); err != nil {
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
	if err := ctx.Err(); err != nil {
		return PendingCommit{}, false, err
	}
	_, target, err := s.pendingCommitPath(scope)
	if err != nil {
		return PendingCommit{}, false, err
	}
	raw, err := os.ReadFile(target)
	if errors.Is(err, fs.ErrNotExist) {
		return PendingCommit{}, false, nil
	}
	if err != nil {
		return PendingCommit{}, false, fmt.Errorf("read workspace pending commit: %w", err)
	}
	var pending PendingCommit
	if err := json.Unmarshal(raw, &pending); err != nil {
		return PendingCommit{}, false, fmt.Errorf("decode workspace pending commit: %w", err)
	}
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
	if err := ctx.Err(); err != nil {
		return err
	}
	_, target, err := s.pendingCommitPath(scope)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clear workspace pending commit: %w", err)
	}
	return nil
}

func (s *FileStore) pendingCommitPath(scope Scope) (string, string, error) {
	dir, err := s.snapshotProjectDir(scope)
	if err != nil {
		return "", "", err
	}
	return dir, filepath.Join(dir, workspacePendingCommitFile), nil
}
