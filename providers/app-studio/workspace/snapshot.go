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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const workspaceSnapshotDirectory = ".assistant-snapshots"

var (
	ErrSnapshotNotFound = errors.New("workspace snapshot not found")
	ErrSnapshotConflict = errors.New("workspace changed after assistant run")
)

type workspaceSnapshotEntry struct {
	Path         string `json:"path"`
	Existed      bool   `json:"existed"`
	Content      []byte `json:"content,omitempty"`
	Mode         uint32 `json:"mode,omitempty"`
	AfterExisted bool   `json:"afterExisted"`
	After        []byte `json:"after,omitempty"`
	AfterMode    uint32 `json:"afterMode,omitempty"`
}

// SnapshotRestoreResult describes a restored assistant-turn snapshot.
type SnapshotRestoreResult struct {
	SnapshotID string           `json:"snapshotID"`
	Files      []MutationResult `json:"files"`
}

type workspaceSnapshotRestorePlan struct {
	entry    workspaceSnapshotEntry
	path     string
	current  []byte
	existed  bool
	mode     fs.FileMode
	restored bool
}

type workspaceFileTooLargeError struct {
	path  string
	size  int64
	limit int
}

func (e *workspaceFileTooLargeError) Error() string {
	return fmt.Sprintf("file %q is too large to edit: %d > %d bytes", e.path, e.size, e.limit)
}

func (s *FileStore) readMutationTarget(ctx context.Context, scope Scope, clean string) ([]byte, bool, error) {
	return s.readMutationTargetLimited(ctx, scope, clean, 0)
}

// readMutationTargetLimited reads one regular workspace file while bounding
// the amount of content retained in memory. A non-zero limit rejects the file
// once the limit is exceeded; this is used by unified patches, including
// Delete File, so a deletion cannot force an unbounded snapshot/read.
func (s *FileStore) readMutationTargetLimited(ctx context.Context, scope Scope, clean string, limit int) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	dir, err := s.scopeDir(scope)
	if err != nil {
		return nil, false, err
	}
	target := filepath.Join(dir, filepath.FromSlash(clean))
	if err := ensureWithin(dir, target); err != nil {
		return nil, false, err
	}
	if err := rejectSymlinkComponents(dir, clean, true); err != nil {
		return nil, false, err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("stat %q: %w", clean, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("path %q is a symlink", clean)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("path %q is not a regular file", clean)
	}
	if limit > 0 && info.Size() > int64(limit) {
		return nil, true, &workspaceFileTooLargeError{path: clean, size: info.Size(), limit: limit}
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, false, fmt.Errorf("open %q: %w", clean, err)
	}
	defer func() { _ = f.Close() }()
	var reader io.Reader = f
	if limit > 0 {
		reader = io.LimitReader(f, int64(limit)+1)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, false, fmt.Errorf("read %q: %w", clean, err)
	}
	if limit > 0 && len(content) > limit {
		return nil, true, &workspaceFileTooLargeError{path: clean, size: int64(len(content)), limit: limit}
	}
	return content, true, nil
}

func (s *FileStore) prepareSnapshotFile(
	ctx context.Context,
	scope Scope,
	snapshotID string,
	clean string,
	content []byte,
	existed bool,
	after []byte,
	afterExisted bool,
) error {
	return s.prepareSnapshotFileWithModes(ctx, scope, snapshotID, clean, content, existed, 0, after, afterExisted, 0)
}

func (s *FileStore) prepareSnapshotFileWithModes(
	ctx context.Context,
	scope Scope,
	snapshotID string,
	clean string,
	content []byte,
	existed bool,
	mode fs.FileMode,
	after []byte,
	afterExisted bool,
	afterMode fs.FileMode,
) error {
	snapshotID = strings.TrimSpace(snapshotID)
	if snapshotID == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.snapshotDir(scope, snapshotID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create workspace snapshot %q: %w", snapshotID, err)
	}
	sum := sha256.Sum256([]byte(clean))
	entryPath := filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
	raw, err := os.ReadFile(entryPath)
	var entry workspaceSnapshotEntry
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("decode workspace snapshot entry: %w", err)
		}
	case errors.Is(err, fs.ErrNotExist):
		if existed && mode == 0 {
			mode = s.currentMutationTargetMode(scope, clean)
		}
		entry = workspaceSnapshotEntry{
			Path:    clean,
			Existed: existed,
			Content: append([]byte(nil), content...),
			Mode:    uint32(mode.Perm()),
		}
	default:
		return fmt.Errorf("read workspace snapshot entry: %w", err)
	}
	if entry.Existed && entry.Mode == 0 {
		if mode == 0 {
			mode = s.currentMutationTargetMode(scope, clean)
		}
		entry.Mode = uint32(mode.Perm())
	}
	if afterExisted && afterMode == 0 {
		afterMode = mode
		if afterMode == 0 {
			afterMode = 0o644
		}
	}
	entry.AfterExisted = afterExisted
	entry.After = append([]byte(nil), after...)
	entry.AfterMode = uint32(afterMode.Perm())
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode workspace snapshot entry: %w", err)
	}
	if err := writeFileAtomically(dir, entryPath, encoded, 0o600, false); err != nil {
		return fmt.Errorf("persist workspace snapshot entry: %w", err)
	}
	return nil
}

func (s *FileStore) currentMutationTargetMode(scope Scope, clean string) fs.FileMode {
	dir, err := s.scopeDir(scope)
	if err != nil {
		return 0
	}
	info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(clean)))
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Mode().Perm()
}

// RestoreSnapshot restores every file first touched by one assistant run.
func (s *FileStore) RestoreSnapshot(ctx context.Context, scope Scope, snapshotID string) (SnapshotRestoreResult, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	dir, err := s.snapshotDir(scope, snapshotID)
	if err != nil {
		return SnapshotRestoreResult{}, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return SnapshotRestoreResult{}, fmt.Errorf("%w: %q", ErrSnapshotNotFound, snapshotID)
	}
	if err != nil {
		return SnapshotRestoreResult{}, fmt.Errorf("read workspace snapshot %q: %w", snapshotID, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := SnapshotRestoreResult{SnapshotID: snapshotID}
	plans := make([]workspaceSnapshotRestorePlan, 0, len(entries))
	for _, file := range entries {
		if err := ctx.Err(); err != nil {
			return SnapshotRestoreResult{}, err
		}
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			return SnapshotRestoreResult{}, fmt.Errorf("read workspace snapshot entry: %w", err)
		}
		var entry workspaceSnapshotEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return SnapshotRestoreResult{}, fmt.Errorf("decode workspace snapshot entry: %w", err)
		}
		clean, err := cleanProjectPath(entry.Path)
		if err != nil {
			return SnapshotRestoreResult{}, fmt.Errorf("invalid workspace snapshot entry: %w", err)
		}
		current, existed, err := s.readMutationTarget(ctx, scope, clean)
		if err != nil {
			return SnapshotRestoreResult{}, err
		}
		currentMode := s.currentMutationTargetMode(scope, clean)
		matchesAfter := existed == entry.AfterExisted && bytes.Equal(current, entry.After) &&
			(!existed || entry.AfterMode == 0 || currentMode == fs.FileMode(entry.AfterMode))
		matchesBefore := existed == entry.Existed && bytes.Equal(current, entry.Content) &&
			(!existed || entry.Mode == 0 || currentMode == fs.FileMode(entry.Mode))
		if !matchesAfter && !matchesBefore {
			return SnapshotRestoreResult{}, fmt.Errorf("%w at %q; refusing to overwrite newer changes", ErrSnapshotConflict, clean)
		}
		if entry.Existed {
			if err := validateMutationContent(clean, string(entry.Content)); err != nil {
				return SnapshotRestoreResult{}, fmt.Errorf("invalid workspace snapshot content: %w", err)
			}
		}
		plans = append(plans, workspaceSnapshotRestorePlan{
			entry:    entry,
			path:     clean,
			current:  append([]byte(nil), current...),
			existed:  existed,
			mode:     currentMode,
			restored: matchesBefore,
		})
	}
	for index, plan := range plans {
		if !plan.restored {
			if err := s.restoreFileStateWithMode(ctx, scope, plan.path, plan.entry.Content, plan.entry.Existed, fs.FileMode(plan.entry.Mode)); err != nil {
				rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancelRollback()
				rollbackErrs := []error{err}
				for rollbackIndex := index; rollbackIndex >= 0; rollbackIndex-- {
					applied := plans[rollbackIndex]
					if rollbackErr := s.restoreFileStateWithMode(rollbackCtx, scope, applied.path, applied.current, applied.existed, applied.mode); rollbackErr != nil {
						rollbackErrs = append(rollbackErrs, fmt.Errorf("roll back %q: %w", applied.path, rollbackErr))
					}
				}
				return SnapshotRestoreResult{}, errors.Join(rollbackErrs...)
			}
		}
		result.Files = append(result.Files, mutationResult("restore_file", plan.path, plan.entry.After, string(plan.entry.Content), 0))
	}
	return result, nil
}

// DeleteSnapshots removes every assistant-run snapshot for one project.
func (s *FileStore) DeleteSnapshots(ctx context.Context, scope Scope) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.snapshotProjectDir(scope)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete workspace snapshots: %w", err)
	}
	return nil
}

func (s *FileStore) restoreFileState(ctx context.Context, scope Scope, clean string, content []byte, existed bool) error {
	return s.restoreFileStateWithMode(ctx, scope, clean, content, existed, 0)
}

func (s *FileStore) restoreFileStateWithMode(ctx context.Context, scope Scope, clean string, content []byte, existed bool, mode fs.FileMode) error {
	if existed {
		if err := s.writePatchFile(ctx, scope, clean, content, mode, false); err != nil {
			return err
		}
		if mode == 0 {
			return nil
		}
		dir, err := s.scopeDir(scope)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(clean))
		if err := ensureWithin(dir, target); err != nil {
			return err
		}
		if err := rejectSymlink(target, clean); err != nil {
			return err
		}
		if err := os.Chmod(target, mode.Perm()); err != nil {
			return fmt.Errorf("restore mode for %q: %w", clean, err)
		}
		return nil
	}
	scopeDir, err := s.scopeDir(scope)
	if err != nil {
		return err
	}
	target := filepath.Join(scopeDir, filepath.FromSlash(clean))
	if err := ensureWithin(scopeDir, target); err != nil {
		return err
	}
	if err := rejectSymlinkComponents(scopeDir, clean, true); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove restored file %q: %w", clean, err)
	}
	return nil
}

func (s *FileStore) snapshotDir(scope Scope, snapshotID string) (string, error) {
	dir, err := s.snapshotProjectDir(scope)
	if err != nil {
		return "", err
	}
	if err := validateScopeSegment(snapshotID); err != nil {
		return "", err
	}
	return filepath.Join(dir, snapshotID), nil
}

func (s *FileStore) snapshotProjectDir(scope Scope) (string, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return "", errors.New("project workspace store is not configured")
	}
	for _, part := range []string{scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID} {
		if err := validateScopeSegment(part); err != nil {
			return "", err
		}
	}
	if err := s.migrateLegacySnapshots(scope); err != nil {
		return "", err
	}
	return filepath.Join(
		s.root,
		workspaceSnapshotDirectory,
		scope.OrgUUID,
		scope.WorkspaceUUID,
		scope.ProjectName,
		scope.ProjectUID,
	), nil
}
