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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const workspaceSnapshotDirectory = ".assistant-snapshots"

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
