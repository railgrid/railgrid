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
	"strings"
)

const (
	// Managed transactions are deliberately bounded. Project skill lifecycle
	// requests are smaller than these limits, while the bound keeps a malformed
	// request from retaining an unbounded before/after snapshot under the lock.
	maxManagedTransactionChanges   = 128
	maxManagedTransactionBytes     = 8 << 20
	managedTransactionTempPrefix   = workspaceTempFilePrefix + "txn-"
	managedTransactionBackupPrefix = workspaceTempFilePrefix + "backup-"
)

// ManagedFileOperation identifies one operation in a bounded managed-file
// transaction. The operation names intentionally mirror the ordinary
// FileStore mutation contract.
type ManagedFileOperation string

const (
	ManagedFileCreate  ManagedFileOperation = "create"
	ManagedFileReplace ManagedFileOperation = "replace"
	ManagedFileDelete  ManagedFileOperation = "delete"
)

// ManagedFileChange is one create, replace, or delete in a managed-file
// transaction. Replace and delete require the complete current content
// version returned by ReadFile. Create is create-only and must omit
// ExpectedVersion.
type ManagedFileChange struct {
	Path            string
	Operation       ManagedFileOperation
	Content         string
	ExpectedVersion string
}

type managedTransactionEntry struct {
	change     ManagedFileChange
	clean      string
	target     string
	before     []byte
	existed    bool
	mode       fs.FileMode
	changed    bool
	stagePath  string
	backupPath string
	backedUp   bool
	committed  bool
}

// ApplyManagedTransaction applies a bounded set of file mutations while
// holding one workspace mutation lock. All targets are validated and their
// expected versions checked before the first target is changed. Non-delete
// contents are staged in the workspace filesystem, existing targets are
// renamed to same-filesystem backups, and any later failure restores entries
// in reverse order under the same lock. A successful transaction advances the
// durable source revision exactly once, even when it contains many files.
func (s *FileStore) ApplyManagedTransaction(ctx context.Context, scope Scope, changes []ManagedFileChange) ([]MutationResult, error) {
	if s == nil {
		return nil, errors.New("project workspace store is not configured")
	}
	if len(changes) == 0 {
		return nil, nil
	}
	if len(changes) > maxManagedTransactionChanges {
		return nil, newMutationError(MutationErrorInvalid, "", "managed transaction contains too many files")
	}

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := s.scopeDir(scope)
	if err != nil {
		return nil, err
	}
	entries, err := s.preflightManagedTransaction(ctx, scope, dir, changes)
	if err != nil {
		return nil, err
	}

	results := managedTransactionResults(entries)
	changed := false
	for _, entry := range entries {
		if entry.changed {
			changed = true
			break
		}
	}
	if !changed {
		return results, nil
	}

	// scopeDir intentionally does not create a source directory. Staging is
	// same-filesystem, so create the directory once before creating temporary
	// files. It is harmless if a failed first mutation leaves an empty scope
	// directory; no managed target bytes have been committed in that case.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace transaction directory: %w", err)
	}
	stageDir, err := os.MkdirTemp(dir, managedTransactionTempPrefix)
	if err != nil {
		return nil, fmt.Errorf("stage workspace transaction: %w", err)
	}
	defer func() { _ = os.RemoveAll(stageDir) }()
	backupDir, err := os.MkdirTemp(dir, managedTransactionBackupPrefix)
	if err != nil {
		return nil, fmt.Errorf("prepare workspace transaction backup: %w", err)
	}
	defer func() { _ = os.RemoveAll(backupDir) }()

	if err := stageManagedTransaction(entries, stageDir, ctx); err != nil {
		return nil, err
	}

	revisionBumped := false
	for index, entry := range entries {
		if !entry.changed {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, s.rollbackManagedTransaction(entries, err)
		}
		// Re-read immediately before each commit so the expected-version
		// contract also protects against writers outside this process that do
		// not share mutationMu.
		if err := s.verifyManagedTransactionEntry(ctx, scope, entry); err != nil {
			return nil, s.rollbackManagedTransaction(entries, err)
		}
		if s.managedTransactionHook != nil {
			if err := s.managedTransactionHook(entry.change); err != nil {
				return nil, s.rollbackManagedTransaction(entries, err)
			}
		}
		if !revisionBumped {
			if err := s.bumpSourceRevision(ctx, scope); err != nil {
				return nil, s.rollbackManagedTransaction(entries, err)
			}
			revisionBumped = true
		}
		if err := commitManagedTransactionEntry(entry, dir, backupDir, index); err != nil {
			return nil, s.rollbackManagedTransaction(entries, err)
		}
	}

	return results, nil
}

func (s *FileStore) preflightManagedTransaction(ctx context.Context, scope Scope, dir string, changes []ManagedFileChange) ([]*managedTransactionEntry, error) {
	entries := make([]*managedTransactionEntry, 0, len(changes))
	seen := make(map[string]struct{}, len(changes))
	var aggregateBytes int
	for _, change := range changes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clean, err := cleanProjectPath(change.Path)
		if err != nil {
			return nil, newMutationError(MutationErrorInvalid, change.Path, "managed transaction path is invalid")
		}
		if _, exists := seen[clean]; exists {
			return nil, newMutationError(MutationErrorInvalid, clean, "managed transaction paths must be unique")
		}
		seen[clean] = struct{}{}
		if err := rejectSymlinkComponents(dir, clean, false); err != nil {
			return nil, err
		}

		entry := &managedTransactionEntry{change: change, clean: clean, target: filepath.Join(dir, filepath.FromSlash(clean)), mode: 0o644, changed: true}
		switch change.Operation {
		case ManagedFileCreate:
			if strings.TrimSpace(change.ExpectedVersion) != "" {
				return nil, newMutationError(MutationErrorInvalid, clean, "create must not include expectedVersion")
			}
			if err := validateMutationContent(clean, change.Content); err != nil {
				return nil, newMutationError(MutationErrorInvalid, clean, "managed transaction content is invalid")
			}
			aggregateBytes += len([]byte(change.Content))
		case ManagedFileReplace:
			if err := validateExpectedVersion(clean, change.ExpectedVersion); err != nil {
				return nil, err
			}
			if err := validateMutationContent(clean, change.Content); err != nil {
				return nil, newMutationError(MutationErrorInvalid, clean, "managed transaction content is invalid")
			}
			aggregateBytes += len([]byte(change.Content))
		case ManagedFileDelete:
			if err := validateExpectedVersion(clean, change.ExpectedVersion); err != nil {
				return nil, err
			}
		default:
			return nil, newMutationError(MutationErrorInvalid, clean, "managed transaction operation is unsupported")
		}
		if aggregateBytes > maxManagedTransactionBytes {
			return nil, newMutationError(MutationErrorInvalid, clean, "managed transaction content is too large")
		}

		before, existed, err := s.readMutationTargetLimited(ctx, scope, clean, MaxWriteBytes)
		if err != nil {
			var tooLarge *workspaceFileTooLargeError
			if errors.As(err, &tooLarge) {
				return nil, newMutationError(MutationErrorInvalid, clean, tooLarge.Error())
			}
			return nil, err
		}
		entry.before, entry.existed = before, existed
		switch change.Operation {
		case ManagedFileCreate:
			if existed {
				return nil, newMutationError(MutationErrorTargetExists, clean, "target already exists")
			}
		case ManagedFileReplace, ManagedFileDelete:
			if !existed {
				return nil, newMutationError(MutationErrorTargetNotFound, clean, "target file does not exist")
			}
			if err := requireExpectedVersion(clean, before, change.ExpectedVersion); err != nil {
				return nil, err
			}
			if !validTextContent(string(before)) {
				return nil, newMutationError(MutationErrorInvalid, clean, "source file is not UTF-8 text")
			}
		}
		if existed {
			info, statErr := os.Lstat(entry.target)
			if statErr != nil {
				return nil, fmt.Errorf("stat %q: %w", clean, statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("path %q is not a regular file", clean)
			}
			entry.mode = info.Mode().Perm()
		}
		if change.Operation == ManagedFileReplace && bytes.Equal(before, []byte(change.Content)) {
			entry.changed = false
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func stageManagedTransaction(entries []*managedTransactionEntry, stageDir string, ctx context.Context) error {
	for index, entry := range entries {
		if !entry.changed || entry.change.Operation == ManagedFileDelete {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		stagePath := filepath.Join(stageDir, fmt.Sprintf("%08d", index))
		if err := writeFileAtomically(stageDir, stagePath, []byte(entry.change.Content), entry.mode, false); err != nil {
			return fmt.Errorf("stage %q: %w", entry.clean, err)
		}
		entry.stagePath = stagePath
	}
	return nil
}

func (s *FileStore) verifyManagedTransactionEntry(ctx context.Context, scope Scope, entry *managedTransactionEntry) error {
	current, existed, err := s.readMutationTargetLimited(ctx, scope, entry.clean, MaxWriteBytes)
	if err != nil {
		var tooLarge *workspaceFileTooLargeError
		if errors.As(err, &tooLarge) {
			return newMutationError(MutationErrorInvalid, entry.clean, tooLarge.Error())
		}
		return err
	}
	switch entry.change.Operation {
	case ManagedFileCreate:
		if existed {
			return newMutationError(MutationErrorConflict, entry.clean, "target appeared during transaction")
		}
	case ManagedFileReplace, ManagedFileDelete:
		if !existed {
			return newMutationError(MutationErrorTargetNotFound, entry.clean, "target file disappeared during transaction")
		}
		if err := requireExpectedVersion(entry.clean, current, entry.change.ExpectedVersion); err != nil {
			return err
		}
	}
	return nil
}

func commitManagedTransactionEntry(entry *managedTransactionEntry, dir, backupDir string, index int) error {
	if err := ensureWithin(dir, entry.target); err != nil {
		return err
	}
	if err := mkdirAllForFile(dir, entry.clean); err != nil {
		return fmt.Errorf("create parent directory for %q: %w", entry.clean, err)
	}
	if err := rejectSymlinkComponents(dir, entry.clean, true); err != nil {
		return err
	}
	if entry.existed {
		entry.backupPath = filepath.Join(backupDir, fmt.Sprintf("%08d", index))
		if err := os.Rename(entry.target, entry.backupPath); err != nil {
			return fmt.Errorf("backup %q: %w", entry.clean, err)
		}
		entry.backedUp = true
	}

	switch entry.change.Operation {
	case ManagedFileCreate:
		if err := os.Link(entry.stagePath, entry.target); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return newMutationError(MutationErrorConflict, entry.clean, "target appeared during transaction")
			}
			return fmt.Errorf("create %q: %w", entry.clean, err)
		}
		entry.committed = true
		if err := os.Remove(entry.stagePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove staged create %q: %w", entry.clean, err)
		}
	case ManagedFileReplace:
		if err := os.Rename(entry.stagePath, entry.target); err != nil {
			return fmt.Errorf("replace %q: %w", entry.clean, err)
		}
		entry.committed = true
	case ManagedFileDelete:
		// Existing delete targets were already renamed into the backup above;
		// the rename is the delete commit and leaves no live target to remove.
		entry.committed = true
	default:
		return newMutationError(MutationErrorInvalid, entry.clean, "managed transaction operation is unsupported")
	}
	return nil
}

func (s *FileStore) rollbackManagedTransaction(entries []*managedTransactionEntry, cause error) error {
	var rollbackErrs []error
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if !entry.committed && !entry.backedUp {
			continue
		}
		if entry.committed {
			if err := os.Remove(entry.target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback target %q: %w", entry.clean, err))
			}
		}
		if entry.backedUp {
			if err := mkdirAllForFile(filepath.Dir(entry.target), filepath.Base(entry.target)); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback parent %q: %w", entry.clean, err))
				continue
			}
			if err := os.Rename(entry.backupPath, entry.target); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback backup %q: %w", entry.clean, err))
			}
		}
	}
	if len(rollbackErrs) == 0 {
		return cause
	}
	return errors.Join(cause, errors.Join(rollbackErrs...))
}

func managedTransactionResults(entries []*managedTransactionEntry) []MutationResult {
	results := make([]MutationResult, 0, len(entries))
	for _, entry := range entries {
		after := entry.change.Content
		operation := "replace_file"
		switch entry.change.Operation {
		case ManagedFileCreate:
			operation = "create_file"
		case ManagedFileDelete:
			operation = "delete_file"
			after = ""
		}
		result := mutationResult(operation, entry.clean, entry.before, after, 0)
		result.Changed = entry.changed
		results = append(results, result)
	}
	return results
}
