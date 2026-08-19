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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// These bounds match the Code provider's checkout bundle limits. Keeping
	// the same ceiling here means a restore cannot accept a tree larger than
	// the source operation that produced it.
	maxWorkspaceTreeFiles = 500
	maxWorkspaceTreeBytes = 16 << 20
	treeTransactionPrefix = workspaceTempFilePrefix + "tree-"
	treeBackupPrefix      = workspaceTempFilePrefix + "tree-backup-"
)

// ErrSourceRevisionConflict means the workspace changed after the caller's
// expected source revision was read. No tree bytes are changed on this path.
var ErrSourceRevisionConflict = errors.New("workspace source revision changed")

// ReplaceTreeOptions configures an exact replacement of the managed source
// tree. The optional expected revision is checked while holding the workspace
// mutation lock, so a checkout that took time cannot overwrite a newer edit.
type ReplaceTreeOptions struct {
	Files                  []File
	ExpectedSourceRevision *uint64
}

// ReplaceTreeResult describes the paths changed by an exact tree replacement.
// Written contains creates and content replacements; Deleted contains paths
// present before the replacement but absent from the requested tree.
type ReplaceTreeResult struct {
	Written        []string
	Deleted        []string
	SourceRevision uint64
}

type treeEntry struct {
	path       string
	targetPath string
	operation  ManagedFileOperation
	content    []byte
	before     []byte
	mode       fs.FileMode
	stagePath  string
	backupPath string
	backedUp   bool
	committed  bool
}

type treeMetadataSnapshot struct {
	revisionPath string
	revisionRaw  []byte
	revisionSet  bool
	statePath    string
	stateRaw     []byte
	stateSet     bool
}

// ReplaceTree atomically replaces every managed source file in a project
// workspace with files from one repository commit. It stages all new content,
// moves replaced/deleted files into same-filesystem backups, commits the
// staged files, advances source revision once, and records every changed path
// as uncommitted. Reserved metadata, snapshots, .git, and node_modules remain
// outside the managed tree and are not touched.
func (s *FileStore) ReplaceTree(ctx context.Context, scope Scope, opts ReplaceTreeOptions) (ReplaceTreeResult, error) {
	if s == nil {
		return ReplaceTreeResult{}, errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	if err := ctx.Err(); err != nil {
		return ReplaceTreeResult{}, err
	}
	dir, err := s.scopeDir(scope)
	if err != nil {
		return ReplaceTreeResult{}, err
	}
	files, err := normalizeTreeFiles(opts.Files)
	if err != nil {
		return ReplaceTreeResult{}, err
	}
	currentRevision, err := s.sourceRevision(ctx, scope)
	if err != nil {
		return ReplaceTreeResult{}, err
	}
	metadata, err := s.snapshotTreeMetadata(scope)
	if err != nil {
		return ReplaceTreeResult{}, err
	}
	if opts.ExpectedSourceRevision != nil && *opts.ExpectedSourceRevision != currentRevision {
		return ReplaceTreeResult{}, fmt.Errorf("%w: expected %d, current %d", ErrSourceRevisionConflict, *opts.ExpectedSourceRevision, currentRevision)
	}

	current, err := s.currentTree(ctx, scope, dir)
	if err != nil {
		return ReplaceTreeResult{}, err
	}
	entries, written, deleted, changedPaths, err := planTreeReplacement(files, current)
	if err != nil {
		return ReplaceTreeResult{}, err
	}
	if len(entries) == 0 {
		return ReplaceTreeResult{SourceRevision: currentRevision}, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ReplaceTreeResult{}, fmt.Errorf("create workspace tree directory: %w", err)
	}
	stageDir, err := os.MkdirTemp(dir, treeTransactionPrefix)
	if err != nil {
		return ReplaceTreeResult{}, fmt.Errorf("stage workspace tree: %w", err)
	}
	defer func() { _ = os.RemoveAll(stageDir) }()
	backupDir, err := os.MkdirTemp(dir, treeBackupPrefix)
	if err != nil {
		return ReplaceTreeResult{}, fmt.Errorf("prepare workspace tree backup: %w", err)
	}
	defer func() { _ = os.RemoveAll(backupDir) }()

	if err := stageTreeEntries(entries, stageDir, ctx); err != nil {
		return ReplaceTreeResult{}, err
	}
	if err := s.verifyTreeBaseline(ctx, scope, dir, current); err != nil {
		return ReplaceTreeResult{}, err
	}

	for index := range entries {
		entry := &entries[index]
		if entry.operation != ManagedFileDelete && entry.operation != ManagedFileReplace {
			continue
		}
		entry.targetPath = filepath.Join(dir, filepath.FromSlash(entry.path))
		if err := ctx.Err(); err != nil {
			return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, err)
		}
		if s.managedTransactionHook != nil {
			change := ManagedFileChange{Path: entry.path, Operation: entry.operation, Content: string(entry.content)}
			if err := s.managedTransactionHook(change); err != nil {
				return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, err)
			}
		}
		entry.backupPath = filepath.Join(backupDir, fmt.Sprintf("%08d", index))
		if err := os.MkdirAll(filepath.Dir(entry.backupPath), 0o700); err != nil {
			return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, fmt.Errorf("prepare backup for %q: %w", entry.path, err))
		}
		if err := os.Rename(entry.targetPath, entry.backupPath); err != nil {
			return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, fmt.Errorf("backup %q: %w", entry.path, err))
		}
		entry.backedUp = true
		if entry.operation == ManagedFileDelete {
			entry.committed = true
		}
	}
	// Remove all old targets before creating new parent/child paths. A
	// repository commit can replace a file such as `src` with `src/main.go`;
	// doing this in one interleaved loop would leave the old file blocking the
	// new directory.
	for index := range entries {
		entry := &entries[index]
		if entry.operation != ManagedFileCreate && entry.operation != ManagedFileReplace {
			continue
		}
		entry.targetPath = filepath.Join(dir, filepath.FromSlash(entry.path))
		if err := ctx.Err(); err != nil {
			return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, err)
		}
		if entry.operation == ManagedFileCreate && s.managedTransactionHook != nil {
			change := ManagedFileChange{Path: entry.path, Operation: entry.operation, Content: string(entry.content)}
			if err := s.managedTransactionHook(change); err != nil {
				return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, err)
			}
		}
		if err := ensureWithin(dir, entry.targetPath); err != nil {
			return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, err)
		}
		if err := mkdirAllForFile(dir, entry.path); err != nil {
			return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, fmt.Errorf("create parent for %q: %w", entry.path, err))
		}
		if err := rejectSymlinkComponents(dir, entry.path, false); err != nil {
			return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, err)
		}
		// Backing up nested files can leave an empty directory where the
		// selected commit contains a file (for example src/main.go -> src).
		// Remove only that empty directory; a non-empty directory may contain
		// reserved data such as node_modules and must fail closed instead.
		if info, statErr := os.Lstat(entry.targetPath); statErr == nil && info.IsDir() {
			if err := os.Remove(entry.targetPath); err != nil {
				return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, fmt.Errorf("replace directory with file %q: %w", entry.path, err))
			}
		} else if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, fmt.Errorf("stat restore target %q: %w", entry.path, statErr))
		}
		if err := os.Rename(entry.stagePath, entry.targetPath); err != nil {
			return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, fmt.Errorf("write %q: %w", entry.path, err))
		}
		entry.committed = true
	}

	if err := s.bumpSourceRevision(ctx, scope); err != nil {
		return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, err)
	}
	if _, err := s.addUncommittedPaths(ctx, scope, changedPaths); err != nil {
		return ReplaceTreeResult{}, s.rollbackReplaceTree(entries, metadata, fmt.Errorf("record restored source paths: %w", err))
	}
	return ReplaceTreeResult{
		Written:        written,
		Deleted:        deleted,
		SourceRevision: currentRevision + 1,
	}, nil
}

type currentTreeFile struct {
	content []byte
	mode    fs.FileMode
}

func normalizeTreeFiles(files []File) (map[string]File, error) {
	if len(files) > maxWorkspaceTreeFiles {
		return nil, fmt.Errorf("workspace tree contains too many files: %d > %d", len(files), maxWorkspaceTreeFiles)
	}
	result := make(map[string]File, len(files))
	var total int
	for _, file := range files {
		clean, err := cleanProjectPath(file.Path)
		if err != nil {
			return nil, err
		}
		if _, exists := result[clean]; exists {
			return nil, fmt.Errorf("workspace tree contains duplicate path %q", clean)
		}
		if err := validateMutationContent(clean, file.Content); err != nil {
			return nil, err
		}
		total += len([]byte(file.Content))
		if total > maxWorkspaceTreeBytes {
			return nil, fmt.Errorf("workspace tree is too large: %d > %d bytes", total, maxWorkspaceTreeBytes)
		}
		result[clean] = File{Path: clean, Content: file.Content}
	}
	paths := make([]string, 0, len(result))
	for clean := range result {
		paths = append(paths, clean)
	}
	sort.Strings(paths)
	for i := 1; i < len(paths); i++ {
		if strings.HasPrefix(paths[i], paths[i-1]+"/") {
			return nil, fmt.Errorf("workspace tree contains both file %q and child path %q", paths[i-1], paths[i])
		}
	}
	return result, nil
}

func (s *FileStore) currentTree(ctx context.Context, scope Scope, dir string) (map[string]currentTreeFile, error) {
	list, err := s.allFiles(ctx, dir, maxWorkspaceTreeFiles+1)
	if err != nil {
		return nil, err
	}
	if len(list) > maxWorkspaceTreeFiles {
		return nil, fmt.Errorf("existing workspace tree contains too many files: %d > %d", len(list), maxWorkspaceTreeFiles)
	}
	result := make(map[string]currentTreeFile, len(list))
	for _, file := range list {
		clean, err := cleanProjectPath(file.Path)
		if err != nil {
			return nil, err
		}
		if err := rejectSymlinkComponents(dir, clean, true); err != nil {
			return nil, err
		}
		content, existed, err := s.readMutationTargetLimited(ctx, scope, clean, MaxWriteBytes)
		if err != nil {
			return nil, err
		}
		if !existed {
			return nil, fmt.Errorf("workspace file %q disappeared while preparing replacement", clean)
		}
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(clean)))
		if err != nil {
			return nil, fmt.Errorf("stat %q: %w", clean, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("path %q is not a regular file", clean)
		}
		result[clean] = currentTreeFile{content: content, mode: info.Mode().Perm()}
	}
	return result, nil
}

func planTreeReplacement(files map[string]File, current map[string]currentTreeFile) ([]treeEntry, []string, []string, []string, error) {
	paths := make([]string, 0, len(files)+len(current))
	seen := make(map[string]struct{}, len(files)+len(current))
	for filePath := range files {
		seen[filePath] = struct{}{}
		paths = append(paths, filePath)
	}
	for filePath := range current {
		if _, ok := seen[filePath]; !ok {
			paths = append(paths, filePath)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		leftDepth := strings.Count(paths[i], "/")
		rightDepth := strings.Count(paths[j], "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return paths[i] < paths[j]
	})
	entries := make([]treeEntry, 0, len(paths))
	written := []string{}
	deleted := []string{}
	changed := []string{}
	for _, filePath := range paths {
		file, desired := files[filePath]
		before, existed := current[filePath]
		switch {
		case desired && !existed:
			entries = append(entries, treeEntry{path: filePath, operation: ManagedFileCreate, content: []byte(file.Content), mode: 0o644})
			written = append(written, filePath)
			changed = append(changed, filePath)
		case desired && existed && !bytes.Equal(before.content, []byte(file.Content)):
			entries = append(entries, treeEntry{path: filePath, operation: ManagedFileReplace, content: []byte(file.Content), before: before.content, mode: before.mode})
			written = append(written, filePath)
			changed = append(changed, filePath)
		case !desired && existed:
			entries = append(entries, treeEntry{path: filePath, operation: ManagedFileDelete, before: before.content, mode: before.mode})
			deleted = append(deleted, filePath)
			changed = append(changed, filePath)
		}
	}
	sort.Strings(written)
	sort.Strings(deleted)
	sort.Strings(changed)
	return entries, written, deleted, changed, nil
}

func stageTreeEntries(entries []treeEntry, stageDir string, ctx context.Context) error {
	for index := range entries {
		entry := &entries[index]
		if entry.operation == ManagedFileDelete {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entry.stagePath = filepath.Join(stageDir, fmt.Sprintf("%08d", index))
		if err := writeFileAtomically(stageDir, entry.stagePath, entry.content, entry.mode, false); err != nil {
			return fmt.Errorf("stage %q: %w", entry.path, err)
		}
	}
	return nil
}

func (s *FileStore) verifyTreeBaseline(ctx context.Context, scope Scope, dir string, current map[string]currentTreeFile) error {
	observed, err := s.currentTree(ctx, scope, dir)
	if err != nil {
		return err
	}
	if len(observed) != len(current) {
		return fmt.Errorf("%w: managed file set changed during restore", ErrMutationConflict)
	}
	for filePath, before := range current {
		after, ok := observed[filePath]
		if !ok || !bytes.Equal(before.content, after.content) {
			return fmt.Errorf("%w: %s", ErrMutationConflict, filePath)
		}
	}
	return nil
}

func (s *FileStore) snapshotTreeMetadata(scope Scope) (treeMetadataSnapshot, error) {
	_, revisionPath, err := s.sourceRevisionPath(scope)
	if err != nil {
		return treeMetadataSnapshot{}, err
	}
	_, statePath, err := s.sourceStatePath(scope)
	if err != nil {
		return treeMetadataSnapshot{}, err
	}
	revisionRaw, revisionSet, err := readTreeMetadataFile(revisionPath)
	if err != nil {
		return treeMetadataSnapshot{}, fmt.Errorf("snapshot workspace source revision: %w", err)
	}
	stateRaw, stateSet, err := readTreeMetadataFile(statePath)
	if err != nil {
		return treeMetadataSnapshot{}, fmt.Errorf("snapshot workspace source state: %w", err)
	}
	return treeMetadataSnapshot{
		revisionPath: revisionPath,
		revisionRaw:  revisionRaw,
		revisionSet:  revisionSet,
		statePath:    statePath,
		stateRaw:     stateRaw,
		stateSet:     stateSet,
	}, nil
}

func readTreeMetadataFile(path string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func (s *FileStore) rollbackReplaceTree(entries []treeEntry, metadata treeMetadataSnapshot, cause error) error {
	rollbackErr := s.rollbackTree(entries, cause)
	if err := restoreTreeMetadataFile(metadata.revisionPath, metadata.revisionRaw, metadata.revisionSet); err != nil {
		rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore workspace source revision: %w", err))
	}
	if err := restoreTreeMetadataFile(metadata.statePath, metadata.stateRaw, metadata.stateSet); err != nil {
		rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore workspace source state: %w", err))
	}
	return rollbackErr
}

func restoreTreeMetadataFile(path string, raw []byte, exists bool) error {
	if !exists {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomically(filepath.Dir(path), path, raw, 0o600, false)
}

func (s *FileStore) rollbackTree(entries []treeEntry, cause error) error {
	var rollbackErrs []error
	// Remove every newly committed target before restoring any backup. This
	// ordering matters for file/directory transitions: restoring an old `src`
	// file cannot succeed while a newly-created `src/main.go` still exists.
	for index := len(entries) - 1; index >= 0; index-- {
		entry := &entries[index]
		if entry.committed && entry.operation != ManagedFileDelete {
			if err := os.Remove(entry.targetPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback target %q: %w", entry.path, err))
			}
		}
	}
	for index := len(entries) - 1; index >= 0; index-- {
		entry := &entries[index]
		if entry.backedUp {
			if err := os.MkdirAll(filepath.Dir(entry.targetPath), 0o755); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback parent %q: %w", entry.path, err))
				continue
			}
			if info, err := os.Lstat(entry.targetPath); err == nil && info.IsDir() {
				if removeErr := os.Remove(entry.targetPath); removeErr != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback directory %q: %w", entry.path, removeErr))
					continue
				}
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("stat rollback target %q: %w", entry.path, err))
				continue
			}
			if err := os.Rename(entry.backupPath, entry.targetPath); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback backup %q: %w", entry.path, err))
			}
		}
	}
	if len(rollbackErrs) == 0 {
		return cause
	}
	return errors.Join(cause, errors.Join(rollbackErrs...))
}
