/*
Copyright 2026 The Faros Authors.

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

package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

const (
	workspaceSourceStateFile      = "source-state.json"
	workspaceCommitSettlementFile = "commit-settlement.json"
)

type workspaceSourceState struct {
	UncommittedPaths []string `json:"uncommittedPaths"`
}

type workspaceCommitSettlement struct {
	WorkspaceDigest string   `json:"workspaceDigest"`
	Paths           []string `json:"paths"`
}

// UncommittedPaths returns the project source paths changed by App Studio
// since the last successful repository commit. The state follows the
// ProjectUID-scoped workspace rather than an individual assistant run.
func (s *FileStore) UncommittedPaths(ctx context.Context, scope Scope) ([]string, error) {
	if s == nil {
		return nil, errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.uncommittedPaths(ctx, scope)
}

// AddUncommittedPaths durably unions changed source paths into the current
// project incarnation's pending repository commit set.
func (s *FileStore) AddUncommittedPaths(ctx context.Context, scope Scope, paths []string) ([]string, error) {
	if s == nil {
		return nil, errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	current, err := s.uncommittedPaths(ctx, scope)
	if err != nil {
		return nil, err
	}
	pathSet := make(map[string]struct{}, len(current)+len(paths))
	for _, path := range current {
		pathSet[path] = struct{}{}
	}
	for _, raw := range paths {
		clean, err := cleanProjectPath(raw)
		if err != nil {
			return nil, err
		}
		pathSet[clean] = struct{}{}
	}
	merged := sortedWorkspaceSourcePaths(pathSet)
	if len(merged) == 0 {
		return nil, nil
	}
	dir, statePath, err := s.sourceStatePath(scope)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace source state directory: %w", err)
	}
	raw, err := json.Marshal(workspaceSourceState{UncommittedPaths: merged})
	if err != nil {
		return nil, fmt.Errorf("encode workspace source state: %w", err)
	}
	if err := writeFileAtomically(dir, statePath, raw, 0o600, false); err != nil {
		return nil, fmt.Errorf("persist workspace source state: %w", err)
	}
	return merged, nil
}

func (s *FileStore) removeUncommittedPaths(ctx context.Context, scope Scope, paths []string) error {
	current, err := s.uncommittedPaths(ctx, scope)
	if err != nil {
		return err
	}
	remove := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		clean, err := cleanProjectPath(raw)
		if err != nil {
			return err
		}
		remove[clean] = struct{}{}
	}
	remaining := make(map[string]struct{}, len(current))
	for _, path := range current {
		if _, ok := remove[path]; !ok {
			remaining[path] = struct{}{}
		}
	}
	if len(remaining) == 0 {
		_, statePath, err := s.sourceStatePath(scope)
		if err != nil {
			return err
		}
		if err := os.Remove(statePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("clear workspace source state: %w", err)
		}
		return nil
	}
	dir, statePath, err := s.sourceStatePath(scope)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(workspaceSourceState{UncommittedPaths: sortedWorkspaceSourcePaths(remaining)})
	if err != nil {
		return fmt.Errorf("encode workspace source state: %w", err)
	}
	if err := writeFileAtomically(dir, statePath, raw, 0o600, false); err != nil {
		return fmt.Errorf("persist workspace source state: %w", err)
	}
	return nil
}

// RecordCommitSettlement durably records the local cleanup still required
// after a repository commit has already succeeded. This receipt lets a later
// process repair source-state.json without repeating the external commit.
func (s *FileStore) RecordCommitSettlement(ctx context.Context, scope Scope, workspaceDigest string, paths []string) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	digest := workspaceDigest
	if digest == "" {
		return errors.New("commit settlement workspace digest is required")
	}
	pathSet := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		clean, err := cleanProjectPath(raw)
		if err != nil {
			return err
		}
		pathSet[clean] = struct{}{}
	}
	if len(pathSet) == 0 {
		return errors.New("commit settlement paths are required")
	}
	dir, settlementPath, err := s.commitSettlementPath(scope)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create workspace commit settlement directory: %w", err)
	}
	raw, err := json.Marshal(workspaceCommitSettlement{WorkspaceDigest: digest, Paths: sortedWorkspaceSourcePaths(pathSet)})
	if err != nil {
		return fmt.Errorf("encode workspace commit settlement: %w", err)
	}
	if err := writeFileAtomically(dir, settlementPath, raw, 0o600, false); err != nil {
		return fmt.Errorf("persist workspace commit settlement: %w", err)
	}
	return nil
}

// ReconcileCommitSettlement clears committed paths and the matching receipt in
// one workspace mutation critical section. The caller must first verify that
// the current file bundle still has the receipt's digest.
func (s *FileStore) ReconcileCommitSettlement(ctx context.Context, scope Scope) (bool, error) {
	if s == nil {
		return false, errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, settlementPath, err := s.commitSettlementPath(scope)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(settlementPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read workspace commit settlement for reconciliation: %w", err)
	}
	var settlement workspaceCommitSettlement
	if err := json.Unmarshal(raw, &settlement); err != nil {
		return false, fmt.Errorf("decode workspace commit settlement for reconciliation: %w", err)
	}
	currentDigest, err := s.workspaceDigest(ctx, scope, settlement.Paths)
	if err != nil {
		return false, fmt.Errorf("verify workspace commit settlement: %w", err)
	}
	if settlement.WorkspaceDigest != currentDigest {
		return false, nil
	}
	if err := s.removeUncommittedPaths(ctx, scope, settlement.Paths); err != nil {
		return false, err
	}
	if err := os.Remove(settlementPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("clear workspace commit settlement: %w", err)
	}
	return true, nil
}

// WorkspaceDigest binds an ordered path set to its current UTF-8 contents.
// The digest is computed under the same lock used by workspace mutations.
func (s *FileStore) WorkspaceDigest(ctx context.Context, scope Scope, paths []string) (string, error) {
	if s == nil {
		return "", errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.workspaceDigest(ctx, scope, paths)
}

func (s *FileStore) workspaceDigest(ctx context.Context, scope Scope, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("workspace digest paths are required")
	}
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		clean, err := cleanProjectPath(path)
		if err != nil {
			return "", err
		}
		file, err := s.ReadFile(ctx, scope, ReadOptions{Path: clean, MaxBytes: MaxWriteBytes})
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				_, _ = hash.Write([]byte(clean))
				_, _ = hash.Write([]byte{0})
				// 0xff cannot occur in a valid UTF-8 workspace file, so a
				// deletion cannot collide with an upsert of sentinel-like text.
				_, _ = hash.Write([]byte{0xff})
				_, _ = hash.Write([]byte{0})
				continue
			}
			return "", err
		}
		if file.Binary || file.Truncated {
			return "", fmt.Errorf("file %q cannot be committed as bounded UTF-8 source", clean)
		}
		_, _ = hash.Write([]byte(clean))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(file.Content))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *FileStore) uncommittedPaths(ctx context.Context, scope Scope) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, statePath, err := s.sourceStatePath(scope)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(statePath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read workspace source state: %w", err)
	}
	var state workspaceSourceState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("decode workspace source state: %w", err)
	}
	pathSet := make(map[string]struct{}, len(state.UncommittedPaths))
	for _, rawPath := range state.UncommittedPaths {
		clean, err := cleanProjectPath(rawPath)
		if err != nil {
			return nil, fmt.Errorf("invalid workspace source state: %w", err)
		}
		pathSet[clean] = struct{}{}
	}
	return sortedWorkspaceSourcePaths(pathSet), nil
}

func (s *FileStore) sourceStatePath(scope Scope) (string, string, error) {
	dir, err := s.snapshotProjectDir(scope)
	if err != nil {
		return "", "", err
	}
	return dir, filepath.Join(dir, workspaceSourceStateFile), nil
}

func (s *FileStore) commitSettlementPath(scope Scope) (string, string, error) {
	dir, err := s.snapshotProjectDir(scope)
	if err != nil {
		return "", "", err
	}
	return dir, filepath.Join(dir, workspaceCommitSettlementFile), nil
}

func sortedWorkspaceSourcePaths(pathSet map[string]struct{}) []string {
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
