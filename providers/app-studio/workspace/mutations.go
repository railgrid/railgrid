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

func validateExpectedVersion(path, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return newMutationError(MutationErrorVersionRequired, path, "expectedVersion is required; read the complete current file first")
	}
	if len([]byte(expected)) > MaxFileVersionBytes || !validTextContent(expected) {
		return newMutationError(MutationErrorInvalid, path, "expectedVersion is invalid or too large")
	}
	return nil
}

func requireExpectedVersion(path string, content []byte, expected string) error {
	if fileVersion(content) != strings.TrimSpace(expected) {
		return newMutationError(MutationErrorStale, path, "expectedVersion does not match the current file")
	}
	return nil
}

// MutationErrorCode classifies a rejected ordinary file operation. The codes
// are deliberately independent of any model command syntax so callers can
// render stable diagnostics and retry guidance.
type MutationErrorCode string

const (
	MutationErrorInvalid         MutationErrorCode = "invalid_mutation"
	MutationErrorTargetExists    MutationErrorCode = "target_exists"
	MutationErrorTargetNotFound  MutationErrorCode = "target_not_found"
	MutationErrorVersionRequired MutationErrorCode = "expected_version_required"
	MutationErrorStale           MutationErrorCode = "stale_source"
	MutationErrorAmbiguous       MutationErrorCode = "ambiguous_source"
	MutationErrorNoChanges       MutationErrorCode = "no_changes"
	MutationErrorConflict        MutationErrorCode = "workspace_conflict"
)

// MutationError is a bounded, safe error returned by ordinary file
// operations. Details never include file contents.
type MutationError struct {
	Code        MutationErrorCode `json:"code"`
	Path        string            `json:"path,omitempty"`
	Occurrences int               `json:"occurrences,omitempty"`
	Message     string            `json:"message"`
}

func (e *MutationError) Error() string {
	if e == nil {
		return "workspace mutation failed"
	}
	if e.Path == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s (path %q)", e.Code, e.Message, e.Path)
}

func newMutationError(code MutationErrorCode, path, message string) *MutationError {
	return &MutationError{Code: code, Path: path, Message: message}
}

// EditOptions follows Eino's ordinary edit contract. oldString must identify
// exactly one occurrence unless ReplaceAll is explicitly true.
type EditOptions struct {
	Path            string
	OldString       string
	NewString       string
	ReplaceAll      bool
	ExpectedVersion string
}

// DeleteOptions configures removal of one existing regular file.
type DeleteOptions struct {
	Path            string
	ExpectedVersion string
}

// MoveOptions configures a no-replace move within one project workspace.
type MoveOptions struct {
	SourcePath      string
	DestinationPath string
	ExpectedVersion string
}

// CreateFile creates one new bounded UTF-8 text file. It is deliberately
// create-only and never uses an initial-build or turn-phase flag.
func (s *FileStore) CreateFile(ctx context.Context, scope Scope, opts CreateOptions) (MutationResult, error) {
	if s == nil {
		return MutationResult{}, errors.New("project workspace store is not configured")
	}
	if err := validateMutationContent(opts.Path, opts.Content); err != nil {
		return MutationResult{}, err
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	clean, _ := cleanProjectPath(opts.Path)
	_, existed, err := s.readMutationTarget(ctx, scope, clean)
	if err != nil {
		return MutationResult{}, err
	}
	if existed {
		return MutationResult{}, newMutationError(MutationErrorTargetExists, clean, "target already exists")
	}
	if err := s.writeMutationLocked(ctx, scope, clean, []byte(opts.Content), true); err != nil {
		return MutationResult{}, err
	}
	return mutationResult("create_file", clean, nil, opts.Content, 0), nil
}

// ReplaceFile atomically replaces one existing file only when its complete
// current content still has opts.ExpectedVersion.
func (s *FileStore) ReplaceFile(ctx context.Context, scope Scope, opts ReplaceOptions) (MutationResult, error) {
	if s == nil {
		return MutationResult{}, errors.New("project workspace store is not configured")
	}
	clean, err := cleanProjectPath(opts.Path)
	if err != nil {
		return MutationResult{}, err
	}
	if err := validateMutationContent(clean, opts.Content); err != nil {
		return MutationResult{}, err
	}
	if err := validateExpectedVersion(clean, opts.ExpectedVersion); err != nil {
		return MutationResult{}, err
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	before, existed, err := s.readMutationTargetLimited(ctx, scope, clean, MaxWriteBytes)
	if err != nil {
		var tooLarge *workspaceFileTooLargeError
		if errors.As(err, &tooLarge) {
			return MutationResult{}, newMutationError(MutationErrorInvalid, clean, tooLarge.Error())
		}
		return MutationResult{}, err
	}
	if !existed {
		return MutationResult{}, newMutationError(MutationErrorTargetNotFound, clean, "target file does not exist")
	}
	if err := requireExpectedVersion(clean, before, opts.ExpectedVersion); err != nil {
		return MutationResult{}, err
	}
	if !validTextContent(string(before)) {
		return MutationResult{}, newMutationError(MutationErrorInvalid, clean, "source file is not UTF-8 text")
	}
	if bytes.Equal(before, []byte(opts.Content)) {
		result := mutationResult("replace_file", clean, before, opts.Content, 0)
		result.Changed = false
		return result, nil
	}
	if err := s.writeMutationLocked(ctx, scope, clean, []byte(opts.Content), false); err != nil {
		return MutationResult{}, err
	}
	return mutationResult("replace_file", clean, before, opts.Content, 0), nil
}

// FileExists reports whether one regular workspace file exists without
// returning its contents. It is used by the assistant boundary to enforce a
// same-turn read before editing, deleting, or moving an existing file.
func (s *FileStore) FileExists(ctx context.Context, scope Scope, rawPath string) (bool, error) {
	if s == nil {
		return false, errors.New("project workspace store is not configured")
	}
	clean, err := cleanProjectPath(rawPath)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	dir, err := s.scopeDir(scope)
	if err != nil {
		return false, err
	}
	target := filepath.Join(dir, filepath.FromSlash(clean))
	if err := ensureWithin(dir, target); err != nil {
		return false, err
	}
	if err := rejectSymlinkComponents(dir, clean, true); err != nil {
		return false, err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %q: %w", clean, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("path %q is a symlink", clean)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("path %q is not a regular file", clean)
	}
	return true, nil
}

// EditFile replaces one exact source fragment. It reads and validates the
// complete source under the mutation lock, then performs one atomic rename.
func (s *FileStore) EditFile(ctx context.Context, scope Scope, opts EditOptions) (MutationResult, error) {
	if s == nil {
		return MutationResult{}, errors.New("project workspace store is not configured")
	}
	clean, err := cleanProjectPath(opts.Path)
	if err != nil {
		return MutationResult{}, err
	}
	if opts.OldString == "" {
		return MutationResult{}, newMutationError(MutationErrorInvalid, clean, "oldString cannot be empty")
	}
	if err := validateMutationContent(clean, opts.NewString); err != nil {
		return MutationResult{}, err
	}
	if !validTextContent(opts.OldString) {
		return MutationResult{}, newMutationError(MutationErrorInvalid, clean, "oldString must be UTF-8 text without NUL bytes")
	}
	if len([]byte(opts.OldString)) > MaxWriteBytes {
		return MutationResult{}, newMutationError(MutationErrorInvalid, clean, "oldString is too large")
	}
	if err := validateExpectedVersion(clean, opts.ExpectedVersion); err != nil {
		return MutationResult{}, err
	}

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	before, existed, err := s.readMutationTargetLimited(ctx, scope, clean, MaxWriteBytes)
	if err != nil {
		var tooLarge *workspaceFileTooLargeError
		if errors.As(err, &tooLarge) {
			return MutationResult{}, newMutationError(MutationErrorInvalid, clean, tooLarge.Error())
		}
		return MutationResult{}, err
	}
	if !existed {
		return MutationResult{}, newMutationError(MutationErrorTargetNotFound, clean, "source file does not exist")
	}
	if err := requireExpectedVersion(clean, before, opts.ExpectedVersion); err != nil {
		return MutationResult{}, err
	}
	if !validTextContent(string(before)) {
		return MutationResult{}, newMutationError(MutationErrorInvalid, clean, "source file is not UTF-8 text")
	}
	occurrences := strings.Count(string(before), opts.OldString)
	if occurrences == 0 {
		return MutationResult{}, newMutationError(MutationErrorStale, clean, "oldString was not found in the current file")
	}
	if !opts.ReplaceAll && occurrences != 1 {
		return MutationResult{}, &MutationError{Code: MutationErrorAmbiguous, Path: clean, Occurrences: occurrences, Message: "oldString matched more than one location; provide more context or set replaceAll"}
	}
	next := string(before)
	if opts.ReplaceAll {
		next = strings.ReplaceAll(next, opts.OldString, opts.NewString)
	} else {
		next = strings.Replace(next, opts.OldString, opts.NewString, 1)
	}
	if next == string(before) {
		return MutationResult{}, newMutationError(MutationErrorNoChanges, clean, "edit does not change the file")
	}
	if err := validateMutationContent(clean, next); err != nil {
		return MutationResult{}, newMutationError(MutationErrorInvalid, clean, "edited file is invalid or too large")
	}
	if err := s.writeMutationLocked(ctx, scope, clean, []byte(next), false); err != nil {
		return MutationResult{}, err
	}
	return mutationResult("edit_file", clean, before, next, occurrences), nil
}

// DeleteFile removes one existing regular file while holding the workspace
// mutation lock. The remove is preceded by a symlink-safe preflight.
func (s *FileStore) DeleteFile(ctx context.Context, scope Scope, opts DeleteOptions) (MutationResult, error) {
	if s == nil {
		return MutationResult{}, errors.New("project workspace store is not configured")
	}
	clean, err := cleanProjectPath(opts.Path)
	if err != nil {
		return MutationResult{}, err
	}
	if err := validateExpectedVersion(clean, opts.ExpectedVersion); err != nil {
		return MutationResult{}, err
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	before, existed, err := s.readMutationTargetLimited(ctx, scope, clean, MaxWriteBytes)
	if err != nil {
		var tooLarge *workspaceFileTooLargeError
		if errors.As(err, &tooLarge) {
			return MutationResult{}, newMutationError(MutationErrorInvalid, clean, tooLarge.Error())
		}
		return MutationResult{}, err
	}
	if !existed {
		return MutationResult{}, newMutationError(MutationErrorTargetNotFound, clean, "source file does not exist")
	}
	if err := requireExpectedVersion(clean, before, opts.ExpectedVersion); err != nil {
		return MutationResult{}, err
	}
	dir, err := s.scopeDir(scope)
	if err != nil {
		return MutationResult{}, err
	}
	target := filepath.Join(dir, filepath.FromSlash(clean))
	if err := rejectSymlinkComponents(dir, clean, true); err != nil {
		return MutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	if err := s.bumpSourceRevision(ctx, scope); err != nil {
		return MutationResult{}, err
	}
	if err := os.Remove(target); err != nil {
		return MutationResult{}, fmt.Errorf("delete %q: %w", clean, err)
	}
	return mutationResult("delete_file", clean, before, "", 0), nil
}

// MoveFile moves one regular file to a new project-relative path without
// replacing a destination that appears after preflight. Both endpoints are
// preflighted under the mutation lock before the source revision advances.
func (s *FileStore) MoveFile(ctx context.Context, scope Scope, opts MoveOptions) (MutationResult, error) {
	if s == nil {
		return MutationResult{}, errors.New("project workspace store is not configured")
	}
	source, err := cleanProjectPath(opts.SourcePath)
	if err != nil {
		return MutationResult{}, err
	}
	destination, err := cleanProjectPath(opts.DestinationPath)
	if err != nil {
		return MutationResult{}, err
	}
	if source == destination {
		return MutationResult{}, newMutationError(MutationErrorNoChanges, source, "source and destination are the same")
	}
	if err := validateExpectedVersion(source, opts.ExpectedVersion); err != nil {
		return MutationResult{}, err
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	before, existed, err := s.readMutationTargetLimited(ctx, scope, source, MaxWriteBytes)
	if err != nil {
		var tooLarge *workspaceFileTooLargeError
		if errors.As(err, &tooLarge) {
			return MutationResult{}, newMutationError(MutationErrorInvalid, source, tooLarge.Error())
		}
		return MutationResult{}, err
	}
	if !existed {
		return MutationResult{}, newMutationError(MutationErrorTargetNotFound, source, "source file does not exist")
	}
	if err := requireExpectedVersion(source, before, opts.ExpectedVersion); err != nil {
		return MutationResult{}, err
	}
	if !validTextContent(string(before)) {
		return MutationResult{}, newMutationError(MutationErrorInvalid, source, "source file is not UTF-8 text")
	}
	destinationExists, err := s.FileExists(ctx, scope, destination)
	if err != nil {
		return MutationResult{}, err
	}
	if destinationExists {
		return MutationResult{}, newMutationError(MutationErrorTargetExists, destination, "destination file already exists")
	}
	dir, err := s.scopeDir(scope)
	if err != nil {
		return MutationResult{}, err
	}
	sourceTarget := filepath.Join(dir, filepath.FromSlash(source))
	destinationTarget := filepath.Join(dir, filepath.FromSlash(destination))
	if err := ensureWithin(dir, sourceTarget); err != nil {
		return MutationResult{}, err
	}
	if err := ensureWithin(dir, destinationTarget); err != nil {
		return MutationResult{}, err
	}
	if err := rejectSymlinkComponents(dir, source, true); err != nil {
		return MutationResult{}, err
	}
	if err := mkdirAllForFile(dir, destination); err != nil {
		return MutationResult{}, fmt.Errorf("create parent directory for %q: %w", destination, err)
	}
	if err := rejectSymlinkComponents(dir, destination, false); err != nil {
		return MutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	if err := s.bumpSourceRevision(ctx, scope); err != nil {
		return MutationResult{}, err
	}
	if err := linkAndRemoveSource(sourceTarget, destinationTarget); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return MutationResult{}, newMutationError(MutationErrorTargetExists, destination, "destination file appeared during move")
		}
		return MutationResult{}, fmt.Errorf("move %q to %q: %w", source, destination, err)
	}
	result := mutationResult("move_file", destination, before, string(before), 0)
	result.PreviousPath = source
	result.Paths = []string{source, destination}
	return result, nil
}

// linkAndRemoveSource implements a no-replace move for regular files. A hard
// link reserves the destination atomically and fails with ErrExist if another
// writer wins the race. If source removal fails after linking, remove the new
// link so callers do not observe a partial move.
func linkAndRemoveSource(source, destination string) error {
	if err := os.Link(source, destination); err != nil {
		return err
	}
	if err := os.Remove(source); err == nil {
		return nil
	} else {
		rollbackErr := os.Remove(destination)
		if rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("rollback destination: %w", rollbackErr))
		}
		return err
	}
}

func (s *FileStore) writeMutationLocked(ctx context.Context, scope Scope, clean string, content []byte, createOnly bool) error {
	if err := s.bumpSourceRevision(ctx, scope); err != nil {
		return err
	}
	return s.writeMutationFile(ctx, scope, clean, content, 0, createOnly)
}

func (s *FileStore) writeMutationFile(ctx context.Context, scope Scope, clean string, content []byte, mode fs.FileMode, createOnly bool) error {
	dir, err := s.scopeDir(scope)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, filepath.FromSlash(clean))
	if err := ensureWithin(dir, target); err != nil {
		return err
	}
	if err := mkdirAllForFile(dir, clean); err != nil {
		return fmt.Errorf("create parent directory for %q: %w", clean, err)
	}
	if err := rejectSymlink(target, clean); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	if err := writeFileAtomically(filepath.Dir(target), target, content, mode, createOnly); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return newMutationError(MutationErrorConflict, clean, "target appeared during mutation")
		}
		return fmt.Errorf("write %q: %w", clean, err)
	}
	return nil
}
