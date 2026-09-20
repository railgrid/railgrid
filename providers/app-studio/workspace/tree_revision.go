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

// The local tree's revision tag.
//
// This file is the one number that legitimately stays beside the tree, and it
// is worth being precise about why. The working-copy ledger (ledger.go) is
// AUTHORITY: it says what revision the project IS at, and lives on the
// project's own CR so every replica agrees. This tag is a CACHE TAG: it says
// what revision the bytes in THIS directory were written at. Losing it costs a
// rebuild; it can never cost correctness, and nothing reads it as the truth
// about the project.
//
// It exists because moving the revision to the control plane took away the
// comparison that used to detect a stale tree. When both numbers were
// pod-local, "my revision is behind the claim's" meant "my files are old".
// Now the ledger's revision is the same number everywhere, so a replica
// holding a tree from five edits ago would compare equal to itself. The tag
// restores the comparison honestly: local tag < ledger revision means another
// replica moved the project on and this tree must be rebuilt.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const workspaceTreeRevisionFile = "tree-revision.json"

type workspaceTreeRevision struct {
	Revision uint64 `json:"revision"`
}

func (s *FileStore) treeRevisionPath(scope Scope) (string, string, error) {
	dir, err := s.snapshotProjectDir(scope)
	if err != nil {
		return "", "", err
	}
	return dir, filepath.Join(dir, workspaceTreeRevisionFile), nil
}

// localTreeRevision reports the ledger revision this replica's tree was last
// written at. Zero means "unknown", which is always treated as stale.
func (s *FileStore) localTreeRevision(scope Scope) (uint64, error) {
	_, target, err := s.treeRevisionPath(scope)
	if err != nil {
		return 0, err
	}
	raw, err := os.ReadFile(target)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read local tree revision: %w", err)
	}
	var tag workspaceTreeRevision
	if err := json.Unmarshal(raw, &tag); err != nil {
		// A corrupt tag is an unknown tag: rebuild rather than guess.
		return 0, nil
	}
	return tag.Revision, nil
}

// setLocalTreeRevision tags this replica's tree with the revision its bytes
// are at. A failure here is reported, but callers treat it as non-fatal: the
// bytes are written either way, and an untagged tree is merely rebuilt next
// time it is adopted.
func (s *FileStore) setLocalTreeRevision(scope Scope, revision uint64) error {
	dir, target, err := s.treeRevisionPath(scope)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(workspaceTreeRevision{Revision: revision})
	if err != nil {
		return fmt.Errorf("encode local tree revision: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create local tree revision directory: %w", err)
	}
	if err := writeFileAtomically(dir, target, raw, 0o600, false); err != nil {
		return fmt.Errorf("persist local tree revision: %w", err)
	}
	return nil
}

// LocalTreeRevision is localTreeRevision for callers outside the mutation
// path (the replica-affinity adoption check).
func (s *FileStore) LocalTreeRevision(ctx context.Context, scope Scope) (uint64, error) {
	if s == nil {
		return 0, errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return s.localTreeRevision(scope)
}
