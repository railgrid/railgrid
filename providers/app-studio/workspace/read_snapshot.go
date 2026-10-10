/*
Copyright 2026 The Railgrid Authors.

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
)

// ReadSnapshot is a read-only view of the project files and source revision
// while FileStore.WithReadSnapshot holds the store's mutation lock. Use its
// read methods only inside that callback; their context is canceled when the
// callback returns.
type ReadSnapshot struct {
	SourceRevision uint64
	Files          FileList

	store *FileStore
	ctx   context.Context
	scope Scope
}

// ReadFile reads one file from this snapshot using the ordinary bounded-read
// and binary-detection rules.
func (s ReadSnapshot) ReadFile(filePath string, maxBytes int) (FileContent, error) {
	if s.store == nil || s.ctx == nil {
		return FileContent{}, errors.New("workspace read snapshot is not active")
	}
	return s.store.ReadFile(s.ctx, s.scope, ReadOptions{Path: filePath, MaxBytes: maxBytes})
}

// ReadFileBytes reads the complete bytes of one file from this snapshot.
func (s ReadSnapshot) ReadFileBytes(filePath string, limit int64) ([]byte, error) {
	if s.store == nil || s.ctx == nil {
		return nil, errors.New("workspace read snapshot is not active")
	}
	return s.store.ReadFileBytes(s.ctx, s.scope, filePath, limit)
}

// WithReadSnapshot lists the project files, reads their source revision, and
// allows the callback to read file content while FileStore mutations are
// serialized. This gives callers that need a consistent list-and-content
// view one bounded traversal and one read per selected file instead of
// re-listing and re-reading the whole workspace to detect an in-process
// mutation between independent operations.
//
// Keep the callback local and bounded. It must not call methods such as
// SourceRevision that acquire mutationMu; the snapshot already carries that
// revision.
func (s *FileStore) WithReadSnapshot(
	ctx context.Context,
	scope Scope,
	opts ListOptions,
	visit func(ReadSnapshot) error,
) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	if visit == nil {
		return errors.New("workspace read snapshot callback is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.scopeDir(scope)
	if err != nil {
		return err
	}

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	revision, err := s.sourceRevision(ctx, scope)
	if err != nil {
		return err
	}
	limit := boundedPositive(opts.Limit, DefaultListLimit, MaxListLimit)
	files, err := s.allFiles(ctx, dir, limit+1)
	if err != nil {
		return err
	}
	truncated := len(files) > limit
	if truncated {
		files = files[:limit]
	}
	readerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	return visit(ReadSnapshot{
		SourceRevision: revision,
		Files:          FileList{Files: files, Truncated: truncated, Limit: limit},
		store:          s,
		ctx:            readerCtx,
		scope:          scope,
	})
}
