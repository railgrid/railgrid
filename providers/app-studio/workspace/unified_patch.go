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
	"time"
)

// PatchErrorCode is a stable, safe classification for a contextual patch
// failure. Callers may expose these codes to the model and audit log.
type PatchErrorCode string

const (
	PatchErrorInvalidPatch      PatchErrorCode = "invalid_patch"
	PatchErrorContextNotFound   PatchErrorCode = "context_not_found"
	PatchErrorContextAmbiguous  PatchErrorCode = "context_ambiguous"
	PatchErrorTargetExists      PatchErrorCode = "target_exists"
	PatchErrorTargetNotFound    PatchErrorCode = "target_not_found"
	PatchErrorWorkspaceConflict PatchErrorCode = "workspace_conflict"
	PatchErrorNoChanges         PatchErrorCode = "no_changes"
	PatchErrorApplyFailed       PatchErrorCode = "apply_failed"
	PatchErrorStrategyChange    PatchErrorCode = "strategy_change_required"
)

// PatchError is a typed, model-safe patch failure. ActualChanges is populated
// only if an I/O failure could not be completely rolled back.
type PatchError struct {
	Code                   PatchErrorCode   `json:"code"`
	Path                   string           `json:"path,omitempty"`
	Hunk                   int              `json:"hunk,omitempty"`
	Matches                int              `json:"matches,omitempty"`
	Message                string           `json:"message"`
	ExpectedContext        string           `json:"expectedContext,omitempty"`
	ActualContext          string           `json:"actualContext,omitempty"`
	SourceMutationRevision uint64           `json:"sourceMutationRevision"`
	ActualChanges          []MutationResult `json:"actualChanges,omitempty"`
}

func (e *PatchError) Error() string {
	if e == nil {
		return ""
	}
	detail := strings.TrimSpace(e.Message)
	if detail == "" {
		detail = string(e.Code)
	}
	parts := []string{string(e.Code)}
	if e.Path != "" {
		parts = append(parts, fmt.Sprintf("path=%q", e.Path))
	}
	if e.Hunk > 0 {
		parts = append(parts, fmt.Sprintf("hunk=%d", e.Hunk))
	}
	if e.Matches > 0 {
		parts = append(parts, fmt.Sprintf("matches=%d", e.Matches))
	}
	return strings.Join(parts, " ") + ": " + detail
}

func newPatchError(code PatchErrorCode, filePath string, hunk, matches int, format string, args ...any) *PatchError {
	return &PatchError{
		Code:    code,
		Path:    filePath,
		Hunk:    hunk,
		Matches: matches,
		Message: fmt.Sprintf(format, args...),
	}
}

func withPatchErrorContext(err *PatchError, expected, actual string) *PatchError {
	if err == nil {
		return nil
	}
	err.ExpectedContext = boundedPatchContext(expected)
	err.ActualContext = boundedPatchContext(actual)
	return err
}

func boundedPatchContext(value string) string {
	const maxRunes = 2_000
	value = strings.Trim(value, "\r\n")
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

type patchFileState struct {
	path          string
	before        []byte
	beforeExisted bool
	after         []byte
	afterExisted  bool
	beforeMode    fs.FileMode
	afterMode     fs.FileMode
}

type preparedPatchOperation struct {
	operation patchOperation
	states    []*patchFileState
	result    MutationResult
}

func (s *FileStore) applyUnifiedPatch(ctx context.Context, scope Scope, opts PatchOptions) (MutationResult, error) {
	if s == nil {
		return MutationResult{}, errors.New("project workspace store is not configured")
	}
	parsed, err := parseUnifiedPatch(opts.Patch)
	if err != nil {
		return MutationResult{}, err
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	prepared, states, err := s.preflightUnifiedPatch(ctx, scope, parsed)
	if err != nil {
		return MutationResult{}, err
	}
	if err := s.verifyPatchBaselines(ctx, scope, states); err != nil {
		return MutationResult{}, err
	}

	applied := make([]*patchFileState, 0, len(states))
	for _, operation := range prepared {
		for _, state := range operation.states {
			if err := s.applyPatchFileState(ctx, scope, state); err != nil {
				result, patchErr := s.rollbackUnifiedPatch(ctx, scope, states, applied, state, err)
				return result, patchErr
			}
			applied = append(applied, state)
		}
	}
	return aggregatePatchMutationResults(prepared), nil
}

func (s *FileStore) preflightUnifiedPatch(ctx context.Context, scope Scope, parsed parsedPatch) ([]preparedPatchOperation, []*patchFileState, error) {
	prepared := make([]preparedPatchOperation, 0, len(parsed.operations))
	states := make([]*patchFileState, 0, len(parsed.operations)*2)
	for _, operation := range parsed.operations {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		before, existed, mode, err := s.readPatchTarget(ctx, scope, operation.path)
		if err != nil {
			return nil, nil, patchTargetError(ctx, operation.path, err)
		}
		switch operation.kind {
		case patchOperationAdd:
			if existed {
				return nil, nil, newPatchError(PatchErrorTargetExists, operation.path, 0, 0, "Add File target already exists")
			}
			if err := validateMutationContent(operation.path, operation.content); err != nil {
				return nil, nil, newPatchError(PatchErrorInvalidPatch, operation.path, 0, 0, "%v", err)
			}
			state := &patchFileState{
				path:         operation.path,
				after:        []byte(operation.content),
				afterExisted: true,
				afterMode:    0o644,
			}
			result := mutationResult("add_file", operation.path, nil, operation.content, 0)
			result.Patch = strings.Replace(result.Patch, "--- a/"+operation.path, "--- /dev/null", 1)
			prepared = append(prepared, preparedPatchOperation{operation: operation, states: []*patchFileState{state}, result: result})
			states = append(states, state)

		case patchOperationDelete:
			if !existed {
				return nil, nil, newPatchError(PatchErrorTargetNotFound, operation.path, 0, 0, "Delete File target does not exist")
			}
			if !validTextContent(string(before)) {
				return nil, nil, newPatchError(PatchErrorInvalidPatch, operation.path, 0, 0, "Delete File target must be UTF-8 text without NUL bytes")
			}
			state := &patchFileState{
				path:          operation.path,
				before:        before,
				beforeExisted: true,
				beforeMode:    mode,
			}
			result := mutationResult("delete_file", operation.path, before, "", 0)
			result.Patch = strings.Replace(result.Patch, "+++ b/"+operation.path, "+++ /dev/null", 1)
			prepared = append(prepared, preparedPatchOperation{operation: operation, states: []*patchFileState{state}, result: result})
			states = append(states, state)

		case patchOperationUpdate:
			if !existed {
				return nil, nil, newPatchError(PatchErrorTargetNotFound, operation.path, 0, 0, "Update File target does not exist")
			}
			if len(before) > MaxWriteBytes {
				return nil, nil, newPatchError(PatchErrorInvalidPatch, operation.path, 0, 0, "file is too large to patch: %d > %d bytes", len(before), MaxWriteBytes)
			}
			if !validTextContent(string(before)) {
				return nil, nil, newPatchError(PatchErrorInvalidPatch, operation.path, 0, 0, "Update File target must be UTF-8 text without NUL bytes")
			}
			next, changedHunks, err := applyPatchChunks(operation.path, string(before), operation.chunks)
			if err != nil {
				return nil, nil, err
			}
			resultPath := operation.path
			if operation.movePath != "" {
				resultPath = operation.movePath
			}
			if err := validateMutationContent(resultPath, next); err != nil {
				return nil, nil, newPatchError(PatchErrorInvalidPatch, resultPath, 0, 0, "%v", err)
			}
			if operation.movePath == "" && bytes.Equal(before, []byte(next)) {
				return nil, nil, newPatchError(PatchErrorNoChanges, operation.path, 0, 0, "Update File made no changes")
			}
			if operation.movePath == "" {
				state := &patchFileState{
					path:          operation.path,
					before:        before,
					beforeExisted: true,
					after:         []byte(next),
					afterExisted:  true,
					beforeMode:    mode,
					afterMode:     mode,
				}
				result := mutationResult("update_file", operation.path, before, next, changedHunks)
				prepared = append(prepared, preparedPatchOperation{operation: operation, states: []*patchFileState{state}, result: result})
				states = append(states, state)
				continue
			}
			moveBefore, moveExisted, _, err := s.readPatchTarget(ctx, scope, operation.movePath)
			if err != nil {
				return nil, nil, patchTargetError(ctx, operation.movePath, err)
			}
			if moveExisted {
				return nil, nil, newPatchError(PatchErrorTargetExists, operation.movePath, 0, 0, "Move to target already exists")
			}
			sourceState := &patchFileState{
				path:          operation.path,
				before:        before,
				beforeExisted: true,
				beforeMode:    mode,
			}
			destinationState := &patchFileState{
				path:          operation.movePath,
				before:        moveBefore,
				beforeExisted: false,
				after:         []byte(next),
				afterExisted:  true,
				afterMode:     mode,
			}
			result := mutationResult("move_file", operation.movePath, before, next, changedHunks)
			result.PreviousPath = operation.path
			result.Patch = strings.Replace(result.Patch, "--- a/"+operation.movePath, "--- a/"+operation.path, 1)
			// Materialize the destination before deleting the source. If source
			// removal fails, rollback removes the new destination.
			opStates := []*patchFileState{destinationState, sourceState}
			prepared = append(prepared, preparedPatchOperation{operation: operation, states: opStates, result: result})
			states = append(states, opStates...)
		}
	}
	return prepared, states, nil
}

func patchTargetError(ctx context.Context, filePath string, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	var patchErr *PatchError
	if errors.As(err, &patchErr) {
		return patchErr
	}
	var tooLarge *workspaceFileTooLargeError
	if errors.As(err, &tooLarge) {
		return newPatchError(PatchErrorInvalidPatch, filePath, 0, 0, "%v", tooLarge)
	}
	return newPatchError(PatchErrorWorkspaceConflict, filePath, 0, 0, "workspace target is not safely accessible: %v", err)
}

func (s *FileStore) readPatchTarget(ctx context.Context, scope Scope, clean string) ([]byte, bool, fs.FileMode, error) {
	content, existed, err := s.readMutationTargetLimited(ctx, scope, clean, MaxWriteBytes)
	if err != nil || !existed {
		return content, existed, 0, err
	}
	dir, err := s.scopeDir(scope)
	if err != nil {
		return nil, false, 0, err
	}
	info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(clean)))
	if err != nil {
		return nil, false, 0, fmt.Errorf("stat %q: %w", clean, err)
	}
	return content, true, info.Mode().Perm(), nil
}

func (s *FileStore) verifyPatchBaselines(ctx context.Context, scope Scope, states []*patchFileState) error {
	for _, state := range states {
		current, existed, err := s.readMutationTarget(ctx, scope, state.path)
		if err != nil {
			return patchTargetError(ctx, state.path, err)
		}
		if existed != state.beforeExisted || !bytes.Equal(current, state.before) {
			return withPatchErrorContext(
				newPatchError(PatchErrorWorkspaceConflict, state.path, 0, 0, "workspace changed after patch preflight; no patch operations were applied"),
				string(state.before),
				string(current),
			)
		}
	}
	return nil
}

func (s *FileStore) applyPatchFileState(ctx context.Context, scope Scope, state *patchFileState) error {
	if !state.afterExisted {
		return s.restoreFileState(ctx, scope, state.path, nil, false)
	}
	return s.writePatchFile(ctx, scope, state.path, state.after, state.afterMode, !state.beforeExisted)
}

func (s *FileStore) writePatchFile(ctx context.Context, scope Scope, clean string, content []byte, mode fs.FileMode, createOnly bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	if mode == 0 {
		mode = 0o644
	}
	writeFile := s.patchWriteFile
	if writeFile == nil {
		writeFile = writeFileAtomically
	}
	err = writeFile(filepath.Dir(target), target, content, mode, createOnly)
	if errors.Is(err, fs.ErrExist) {
		return newPatchError(PatchErrorWorkspaceConflict, clean, 0, 0, "target appeared after patch preflight")
	}
	if err != nil {
		return fmt.Errorf("write %q: %w", clean, err)
	}
	return nil
}

func (s *FileStore) rollbackUnifiedPatch(
	ctx context.Context,
	scope Scope,
	allStates []*patchFileState,
	applied []*patchFileState,
	failed *patchFileState,
	applyErr error,
) (MutationResult, error) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	rollbackErrs := []error{}
	for index := len(applied) - 1; index >= 0; index-- {
		state := applied[index]
		if err := s.restorePatchFileState(rollbackCtx, scope, state); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("roll back %q: %w", state.path, err))
		}
	}
	actual := s.currentPatchDeltas(rollbackCtx, scope, allStates)
	patchErr := newPatchError(PatchErrorApplyFailed, failed.path, 0, 0, "patch application failed: %v", applyErr)
	patchErr.ActualChanges = append([]MutationResult(nil), actual...)
	if len(rollbackErrs) > 0 {
		patchErr.Message += "; rollback was incomplete: " + errors.Join(rollbackErrs...).Error()
	}
	return aggregateActualPatchChanges(actual), patchErr
}

func (s *FileStore) restorePatchFileState(ctx context.Context, scope Scope, state *patchFileState) error {
	if !state.beforeExisted {
		return s.restoreFileState(ctx, scope, state.path, nil, false)
	}
	return s.writePatchFile(ctx, scope, state.path, state.before, state.beforeMode, false)
}

func (s *FileStore) currentPatchDeltas(ctx context.Context, scope Scope, states []*patchFileState) []MutationResult {
	actual := []MutationResult{}
	for _, state := range states {
		current, existed, err := s.readMutationTarget(ctx, scope, state.path)
		if err != nil {
			continue
		}
		if existed == state.beforeExisted && bytes.Equal(current, state.before) {
			continue
		}
		result := mutationResult("actual_change", state.path, state.before, string(current), 0)
		if !existed {
			result.Size = 0
		}
		actual = append(actual, result)
	}
	return actual
}

func aggregatePatchMutationResults(prepared []preparedPatchOperation) MutationResult {
	files := make([]MutationResult, 0, len(prepared))
	for _, operation := range prepared {
		files = append(files, operation.result)
	}
	return aggregateActualPatchChanges(files)
}

func aggregateActualPatchChanges(files []MutationResult) MutationResult {
	result := MutationResult{Operation: "apply_patch", Files: append([]MutationResult(nil), files...)}
	patches := make([]string, 0, len(files))
	seenPaths := make(map[string]struct{}, len(files)*2)
	for _, file := range files {
		for _, candidate := range []string{file.PreviousPath, file.Path} {
			if candidate == "" {
				continue
			}
			if _, ok := seenPaths[candidate]; ok {
				continue
			}
			seenPaths[candidate] = struct{}{}
			result.Paths = append(result.Paths, candidate)
		}
		result.Size += file.Size
		result.Replacements += file.Replacements
		result.Additions += file.Additions
		result.Deletions += file.Deletions
		if strings.TrimSpace(file.Patch) != "" {
			patches = append(patches, strings.TrimRight(file.Patch, "\n"))
		}
	}
	if len(files) == 1 {
		result.Path = files[0].Path
		result.PreviousPath = files[0].PreviousPath
	}
	result.Patch = strings.Join(patches, "\n")
	if result.Patch != "" {
		result.Patch += "\n"
	}
	return result
}
