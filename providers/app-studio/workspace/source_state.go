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

// Working-copy state.
//
// Everything in this file that is AUTHORITY — the dirty-path set, the source
// revision, the commit in flight, the settlement receipt — goes through the
// Ledger (ledger.go), which is backed by the project's own CR. What stays here
// is what is genuinely about the local tree: whether it exists, and what its
// bytes digest to.
//
// Before §9 Cut D.3 all of it was JSON beside the tree on a ReadWriteOnce
// volume. A replica that did not have that volume read "no dirty paths" and
// "revision 1", which is indistinguishable from a clean project at its initial
// revision — so a second replica could push an empty file list to a dev
// sandbox and a moved project could have its fence restart below what the
// agent had already seen.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
)

// RetainsSource reports whether THIS replica's tree is the one the project is
// actually at: the directory exists, and its local revision tag
// (tree_revision.go) has caught up with the ledger's revision. An empty tree
// counts, because its files may have been deliberately deleted.
//
// Before §9 Cut D.3 this compared a pod-local revision file against the
// project claim's recorded floor. Both are gone as a fence: the ledger is one
// number every replica reads, and what varies per replica is only how far this
// directory's bytes have got. A tree with no tag is unknown, and unknown is
// stale — the rebuild is cheap and serving five-edits-old source is not.
func (s *FileStore) RetainsSource(ctx context.Context, scope Scope) (bool, error) {
	if s == nil {
		return false, errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	dir, err := s.scopeDir(scope)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("workspace source is not a directory")
	}
	revision, err := s.sourceRevision(ctx, scope)
	if err != nil {
		return false, err
	}
	local, err := s.localTreeRevision(scope)
	if err != nil {
		return false, err
	}
	return local >= revision, nil
}

// HasSourceTree reports whether this replica holds a materialized tree for the
// project at all. It is the question the hydration path asks: a replica that
// has no tree must rebuild it from the last commit plus the ledger's dirty
// set, not serve an empty workspace.
func (s *FileStore) HasSourceTree(ctx context.Context, scope Scope) (bool, error) {
	if s == nil {
		return false, errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	dir, err := s.scopeDir(scope)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("workspace source is not a directory")
	}
	entries, err := s.allFiles(ctx, dir, 1)
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
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

	return s.addUncommittedPaths(ctx, scope, paths)
}

// SourceAttributions returns the known producing turns for a selected path
// set, grouped by turn. Paths without one unambiguous owner are omitted.
// Callers capture this before requesting a RepositoryCommit and persist the
// result on PendingCommit, so later source edits cannot change its owner.
func (s *FileStore) SourceAttributions(ctx context.Context, scope Scope, paths []string) ([]SourceAttribution, error) {
	if s == nil {
		return nil, errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return nil, err
	}
	record, err := ledger.Read(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("read workspace source attributions: %w", err)
	}
	selected := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		path, err := cleanProjectPath(rawPath)
		if err != nil {
			return nil, err
		}
		selected[path] = struct{}{}
	}
	byPath := make(map[string]SourcePathAttribution, len(record.SourcePathAttributions))
	for _, attribution := range record.SourcePathAttributions {
		byPath[attribution.Path] = attribution
	}
	byOwner := make(map[string]map[string]struct{})
	for path := range selected {
		attribution, ok := byPath[path]
		if !ok || attribution.ThreadID == "" || attribution.RunID == "" {
			continue
		}
		key := attribution.ThreadID + "\x00" + attribution.RunID
		if byOwner[key] == nil {
			byOwner[key] = map[string]struct{}{}
		}
		byOwner[key][path] = struct{}{}
	}
	owners := make([]string, 0, len(byOwner))
	for key := range byOwner {
		owners = append(owners, key)
	}
	sort.Strings(owners)
	out := make([]SourceAttribution, 0, len(owners))
	for _, key := range owners {
		parts := strings.SplitN(key, "\x00", 2)
		ownerPaths := make([]string, 0, len(byOwner[key]))
		for path := range byOwner[key] {
			ownerPaths = append(ownerPaths, path)
		}
		sort.Strings(ownerPaths)
		out = append(out, SourceAttribution{ThreadID: parts[0], RunID: parts[1], Paths: ownerPaths})
	}
	return out, nil
}

// addUncommittedPaths is the lock-free implementation used by callers that
// already hold mutationMu. Keeping the ledger update in the same critical
// section as a whole-tree replacement prevents a concurrent commit from
// observing only part of the restored path set.
func (s *FileStore) addUncommittedPaths(ctx context.Context, scope Scope, paths []string) ([]string, error) {
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return nil, err
	}
	clean := make([]string, 0, len(paths))
	for _, raw := range paths {
		path, err := cleanProjectPath(raw)
		if err != nil {
			return nil, err
		}
		clean = append(clean, path)
	}
	if len(clean) == 0 {
		record, err := ledger.Read(ctx, scope)
		if err != nil {
			return nil, err
		}
		return record.UncommittedPaths, nil
	}
	record, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		before := len(record.UncommittedPaths)
		record.UncommittedPaths = normalizeLedgerPaths(append(append([]string(nil), record.UncommittedPaths...), clean...))
		return len(record.UncommittedPaths) != before, nil
	})
	if err != nil {
		return nil, fmt.Errorf("record uncommitted paths: %w", err)
	}
	return record.UncommittedPaths, nil
}

// SourceRevision returns the durable source revision for this project
// incarnation. A missing revision is the initial revision and is represented
// as one so the infrastructure agent can reject an omitted/zero authority.
func (s *FileStore) SourceRevision(ctx context.Context, scope Scope) (uint64, error) {
	if s == nil {
		return 0, errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.sourceRevision(ctx, scope)
}

// VerifySourceRevision checks that a multi-read source snapshot still names
// the same bytes/provenance epoch. Callers read the revision before gathering
// file contents and owner metadata, then verify it before starting an external
// commit; any intervening source mutation makes the snapshot unusable.
func (s *FileStore) VerifySourceRevision(ctx context.Context, scope Scope, expected uint64) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	current, err := s.sourceRevision(ctx, scope)
	if err != nil {
		return err
	}
	if current != expected {
		return fmt.Errorf("%w: expected %d, current %d", ErrSourceRevisionConflict, expected, current)
	}
	return nil
}

func (s *FileStore) sourceRevision(ctx context.Context, scope Scope) (uint64, error) {
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return 0, err
	}
	record, err := ledger.Read(ctx, scope)
	if err != nil {
		return 0, fmt.Errorf("read workspace source revision: %w", err)
	}
	if record.SourceRevision == 0 {
		return 1, nil
	}
	return record.SourceRevision, nil
}

func (s *FileStore) bumpSourceRevisionForPaths(ctx context.Context, scope Scope, paths []string) error {
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return err
	}
	record, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		if record.SourceRevision == 0 {
			record.SourceRevision = 1
		}
		record.SourceRevision++
		origin, _ := ctx.Value(mutationOriginContextKey{}).(SourceChange)
		origin.SourceRevision = record.SourceRevision
		record.LastSourceChange = &origin
		recordSourcePathChanges(record, paths, origin)
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("advance workspace source revision: %w", err)
	}
	s.tagLocalTree(scope, record.SourceRevision)
	return nil
}

// recordSourcePathChanges preserves only ownership that can be proven. A
// path changed by multiple turns in one pending git batch becomes unknown;
// when a pending commit already captured that path, a later edit starts a new
// attribution window for the next commit.
func recordSourcePathChanges(record *LedgerRecord, paths []string, origin SourceChange) {
	if record == nil || len(paths) == 0 {
		return
	}
	origin.ThreadID = strings.TrimSpace(origin.ThreadID)
	origin.RunID = strings.TrimSpace(origin.RunID)
	knownOrigin := origin.ThreadID != "" && origin.RunID != ""
	byPath := make(map[string]SourcePathAttribution, len(record.SourcePathAttributions)+len(paths))
	for _, current := range record.SourcePathAttributions {
		byPath[current.Path] = current
	}
	dirty := make(map[string]struct{}, len(record.UncommittedPaths))
	for _, path := range record.UncommittedPaths {
		dirty[path] = struct{}{}
	}
	pendingPaths := map[string]struct{}{}
	pendingOwners := map[string]SourceAttribution{}
	ambiguousPendingOwners := map[string]struct{}{}
	if record.PendingCommit != nil {
		for _, path := range record.PendingCommit.Paths {
			pendingPaths[path] = struct{}{}
		}
		for _, attribution := range record.PendingCommit.Attributions {
			for _, path := range attribution.Paths {
				if _, ambiguous := ambiguousPendingOwners[path]; ambiguous {
					continue
				}
				if _, exists := pendingOwners[path]; exists {
					delete(pendingOwners, path)
					ambiguousPendingOwners[path] = struct{}{}
					continue
				}
				pendingOwners[path] = attribution
			}
		}
	}
	for _, rawPath := range paths {
		path, err := cleanProjectPath(rawPath)
		if err != nil {
			continue
		}
		unknown := SourcePathAttribution{Path: path, SourceRevision: origin.SourceRevision}
		previous, hadPrevious := byPath[path]
		if _, isPending := pendingPaths[path]; isPending {
			captured, wasCaptured := pendingOwners[path]
			if hadPrevious && record.PendingCommit.SourceRevision > 0 && previous.SourceRevision > record.PendingCommit.SourceRevision {
				// This path already changed after the commit snapshot. Keep its
				// later owner only while all further changes come from that same
				// turn; a third producer makes the path ambiguous again.
				if knownOrigin && previous.ThreadID != "" && previous.RunID != "" &&
					previous.ThreadID == origin.ThreadID && previous.RunID == origin.RunID {
					previous.SourceRevision = origin.SourceRevision
					byPath[path] = previous
				} else {
					byPath[path] = unknown
				}
			} else if record.PendingCommit.SourceRevision > 0 && wasCaptured && captured.ThreadID != "" && captured.RunID != "" && knownOrigin {
				byPath[path] = SourcePathAttribution{Path: path, ThreadID: origin.ThreadID, RunID: origin.RunID, SourceRevision: origin.SourceRevision}
			} else {
				byPath[path] = unknown
			}
			continue
		}
		if hadPrevious {
			if knownOrigin && previous.ThreadID == origin.ThreadID && previous.RunID == origin.RunID {
				previous.SourceRevision = origin.SourceRevision
				byPath[path] = previous
			} else {
				byPath[path] = unknown
			}
			continue
		}
		if _, wasDirty := dirty[path]; wasDirty || !knownOrigin {
			byPath[path] = unknown
			continue
		}
		byPath[path] = SourcePathAttribution{Path: path, ThreadID: origin.ThreadID, RunID: origin.RunID, SourceRevision: origin.SourceRevision}
	}
	updated := make([]SourcePathAttribution, 0, len(byPath))
	for _, attribution := range byPath {
		updated = append(updated, attribution)
	}
	record.SourcePathAttributions = normalizeSourcePathAttributions(updated)
}

func removeSourcePathAttributions(current []SourcePathAttribution, remove []string) []SourcePathAttribution {
	if len(current) == 0 || len(remove) == 0 {
		return normalizeSourcePathAttributions(current)
	}
	drop := make(map[string]struct{}, len(remove))
	for _, path := range remove {
		drop[path] = struct{}{}
	}
	kept := make([]SourcePathAttribution, 0, len(current))
	for _, attribution := range current {
		if _, ok := drop[attribution.Path]; !ok {
			kept = append(kept, attribution)
		}
	}
	return normalizeSourcePathAttributions(kept)
}

// tagLocalTree records which revision this replica's bytes are at
// (tree_revision.go). It is a cache tag, never authority, so a failure to
// write it is logged-by-omission rather than failing a mutation whose bytes
// are already durable: the cost is a rebuild the next time the project is
// adopted here.
func (s *FileStore) tagLocalTree(scope Scope, revision uint64) {
	if revision == 0 {
		return
	}
	_ = s.setLocalTreeRevision(scope, revision)
}

// advanceAndRecord advances the source revision and unions paths into the
// dirty set in ONE ledger update, so no crash can leave a revision that moved
// beside a dirty set that did not. expected, when non-nil, is re-checked
// against the ledger inside that update: it is the compare of the whole-tree
// replacement's compare-and-swap, and the only place it can be enforced
// against a writer on another replica.
func (s *FileStore) advanceAndRecord(ctx context.Context, scope Scope, expected *uint64, paths []string, committed bool) (uint64, error) {
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return 0, err
	}
	clean := make([]string, 0, len(paths))
	for _, raw := range paths {
		path, err := cleanProjectPath(raw)
		if err != nil {
			return 0, err
		}
		clean = append(clean, path)
	}
	record, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		current := record.SourceRevision
		if current == 0 {
			current = 1
		}
		if expected != nil && *expected != current {
			return false, fmt.Errorf("%w: expected %d, current %d", ErrSourceRevisionConflict, *expected, current)
		}
		record.SourceRevision = current + 1
		origin, _ := ctx.Value(mutationOriginContextKey{}).(SourceChange)
		origin.SourceRevision = record.SourceRevision
		record.LastSourceChange = &origin
		if committed {
			// The incoming bytes ARE the repository's, so these paths are
			// clean — including any that were dirty before, whose local edits
			// this replacement has just overwritten.
			record.UncommittedPaths = removeLedgerPaths(record.UncommittedPaths, clean)
			record.SourcePathAttributions = removeSourcePathAttributions(record.SourcePathAttributions, clean)
		} else {
			recordSourcePathChanges(record, clean, origin)
			record.UncommittedPaths = normalizeLedgerPaths(append(append([]string(nil), record.UncommittedPaths...), clean...))
		}
		return true, nil
	})
	if err != nil {
		return 0, err
	}
	s.tagLocalTree(scope, record.SourceRevision)
	return record.SourceRevision, nil
}

// EnsureSourceRevisionFloor raises the durable source revision to at least
// floor. It survives from the file-backed ledger because adoption still seeds
// a floor from the project claim; with the revision on the CR the two agree by
// construction, so this is now a no-op in the common case and a repair for a
// claim that ran ahead of a status write. Never lowers the revision.
func (s *FileStore) EnsureSourceRevisionFloor(ctx context.Context, scope Scope, floor uint64) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	if floor <= 1 {
		return nil
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return err
	}
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		if record.SourceRevision >= floor {
			return false, nil
		}
		record.SourceRevision = floor
		return true, nil
	}); err != nil {
		return fmt.Errorf("raise workspace source revision floor: %w", err)
	}
	return nil
}

// ClearUncommittedPaths removes the pending source set after the complete set
// has been committed successfully through the repository bridge.
func (s *FileStore) ClearUncommittedPaths(ctx context.Context, scope Scope) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return err
	}
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		if len(record.UncommittedPaths) == 0 && len(record.SourcePathAttributions) == 0 {
			return false, nil
		}
		record.UncommittedPaths = nil
		record.SourcePathAttributions = nil
		return true, nil
	}); err != nil {
		return fmt.Errorf("clear uncommitted paths: %w", err)
	}
	return nil
}

// RemoveUncommittedPaths removes only the paths successfully persisted by a
// repository commit. Other durable dirty paths remain available to later turns.
func (s *FileStore) RemoveUncommittedPaths(ctx context.Context, scope Scope, paths []string) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	return s.removeUncommittedPaths(ctx, scope, paths)
}

func (s *FileStore) removeUncommittedPaths(ctx context.Context, scope Scope, paths []string) error {
	ledger, err := s.ledgerFor(ctx)
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
	if len(remove) == 0 {
		return nil
	}
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		remaining := make([]string, 0, len(record.UncommittedPaths))
		for _, path := range record.UncommittedPaths {
			if _, ok := remove[path]; !ok {
				remaining = append(remaining, path)
			}
		}
		removed := make([]string, 0, len(remove))
		for path := range remove {
			removed = append(removed, path)
		}
		attributions := removeSourcePathAttributions(record.SourcePathAttributions, removed)
		if len(remaining) == len(record.UncommittedPaths) && len(attributions) == len(record.SourcePathAttributions) {
			return false, nil
		}
		record.UncommittedPaths = remaining
		record.SourcePathAttributions = attributions
		return true, nil
	}); err != nil {
		return fmt.Errorf("clear committed paths: %w", err)
	}
	return nil
}

// RecordCommitSettlement durably records the local cleanup still required
// after a repository commit has already succeeded. This receipt lets a later
// process — on any replica — repair the dirty set without repeating the
// external commit.
func (s *FileStore) RecordCommitSettlement(ctx context.Context, scope Scope, workspaceDigest string, paths []string) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return err
	}
	if workspaceDigest == "" {
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
	settled := sortedWorkspaceSourcePaths(pathSet)
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		record.Settlement = &CommitSettlement{WorkspaceDigest: workspaceDigest, Paths: settled}
		return true, nil
	}); err != nil {
		return fmt.Errorf("persist workspace commit settlement: %w", err)
	}
	return nil
}

// PendingCommitSettlement returns a durable post-commit cleanup receipt.
func (s *FileStore) PendingCommitSettlement(ctx context.Context, scope Scope) (string, []string, bool, error) {
	if s == nil {
		return "", nil, false, errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return "", nil, false, err
	}
	record, err := ledger.Read(ctx, scope)
	if err != nil {
		return "", nil, false, fmt.Errorf("read workspace commit settlement: %w", err)
	}
	if record.Settlement == nil {
		return "", nil, false, nil
	}
	pathSet := make(map[string]struct{}, len(record.Settlement.Paths))
	for _, rawPath := range record.Settlement.Paths {
		clean, err := cleanProjectPath(rawPath)
		if err != nil {
			return "", nil, false, fmt.Errorf("invalid workspace commit settlement: %w", err)
		}
		pathSet[clean] = struct{}{}
	}
	if record.Settlement.WorkspaceDigest == "" || len(pathSet) == 0 {
		return "", nil, false, errors.New("invalid workspace commit settlement")
	}
	return record.Settlement.WorkspaceDigest, sortedWorkspaceSourcePaths(pathSet), true, nil
}

// ReconcileCommitSettlement clears committed paths and the matching receipt in
// one ledger update. The digest the receipt carries is verified against the
// current bundle first, so an edit made after the commit left stays dirty.
//
// The verification reads the local tree: a replica that does not hold the
// tree cannot settle, and correctly declines rather than clearing a dirty set
// it cannot check. The receipt stays on the CR for whichever replica can.
func (s *FileStore) ReconcileCommitSettlement(ctx context.Context, scope Scope) (bool, error) {
	if s == nil {
		return false, errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return false, err
	}
	record, err := ledger.Read(ctx, scope)
	if err != nil {
		return false, fmt.Errorf("read workspace commit settlement for reconciliation: %w", err)
	}
	if record.Settlement == nil {
		return false, nil
	}
	currentDigest, err := s.workspaceDigest(ctx, scope, record.Settlement.Paths)
	if err != nil {
		return false, fmt.Errorf("verify workspace commit settlement: %w", err)
	}
	if record.Settlement.WorkspaceDigest != currentDigest {
		return false, nil
	}
	settled := make(map[string]struct{}, len(record.Settlement.Paths))
	for _, path := range record.Settlement.Paths {
		settled[path] = struct{}{}
	}
	digest := record.Settlement.WorkspaceDigest
	applied := false
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		if record.Settlement == nil || record.Settlement.WorkspaceDigest != digest {
			// Another writer settled it between the read and this update.
			return false, nil
		}
		remaining := make([]string, 0, len(record.UncommittedPaths))
		for _, path := range record.UncommittedPaths {
			if _, ok := settled[path]; !ok {
				remaining = append(remaining, path)
			}
		}
		record.UncommittedPaths = remaining
		record.SourcePathAttributions = removeSourcePathAttributions(record.SourcePathAttributions, record.Settlement.Paths)
		record.Settlement = nil
		applied = true
		return true, nil
	}); err != nil {
		return false, fmt.Errorf("settle committed paths: %w", err)
	}
	return applied, nil
}

// WorkspaceDigest binds an ordered path set to its current contents, text and
// binary alike. The digest is computed under the same lock used by workspace
// mutations.
//
// Entries are "path \0 body \0". A text body is the file bytes (unchanged
// from the text-only digest, so settlement receipts stay valid); a deletion is
// the single byte 0xff; a binary body is 0xfe, the 8-byte big-endian length,
// and the file's SHA-256. Neither marker byte can start UTF-8 text, so the
// three forms never collide.
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
		if err := s.digestWorkspaceFile(ctx, scope, hash, clean); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *FileStore) digestWorkspaceFile(ctx context.Context, scope Scope, hash io.Writer, clean string) error {
	_, f, _, err := s.openRegularFile(ctx, scope, clean)
	if errors.Is(err, fs.ErrNotExist) {
		_, _ = hash.Write([]byte(clean))
		_, _ = hash.Write([]byte{0})
		// 0xff cannot occur in valid UTF-8 text, so a deletion cannot collide
		// with an upsert of sentinel-like text.
		_, _ = hash.Write([]byte{0xff})
		_, _ = hash.Write([]byte{0})
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	// Classify first (streaming, constant memory), then hash in the chosen
	// form so text digests stay byte-identical to the text-only encoding.
	detector := &textDetector{}
	fileHash := sha256.New()
	size, err := io.Copy(io.MultiWriter(detector, fileHash), contextReader{ctx: ctx, r: f})
	if err != nil {
		return fmt.Errorf("digest %q: %w", clean, err)
	}
	_, _ = hash.Write([]byte(clean))
	_, _ = hash.Write([]byte{0})
	if detector.Text() {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("digest %q: %w", clean, err)
		}
		if _, err := io.Copy(hash, contextReader{ctx: ctx, r: f}); err != nil {
			return fmt.Errorf("digest %q: %w", clean, err)
		}
	} else {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(size))
		_, _ = hash.Write([]byte{0xfe})
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(fileHash.Sum(nil))
	}
	_, _ = hash.Write([]byte{0})
	return nil
}

func (s *FileStore) uncommittedPaths(ctx context.Context, scope Scope) ([]string, error) {
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return nil, err
	}
	record, err := ledger.Read(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("read uncommitted paths: %w", err)
	}
	pathSet := make(map[string]struct{}, len(record.UncommittedPaths))
	for _, rawPath := range record.UncommittedPaths {
		clean, err := cleanProjectPath(rawPath)
		if err != nil {
			return nil, fmt.Errorf("invalid workspace source state: %w", err)
		}
		pathSet[clean] = struct{}{}
	}
	return sortedWorkspaceSourcePaths(pathSet), nil
}

func sortedWorkspaceSourcePaths(pathSet map[string]struct{}) []string {
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// InitializeRepositorySource queues the complete source tree for an explicitly
// attached repository, so a project whose files predate its repository gets a
// first commit carrying all of them.
//
// It no longer keeps its own receipt: the receipt was a file on the workspace
// volume, and the union it performs is idempotent anyway. What makes it
// once-only is the Project annotation the caller stamps, which the reconciler
// removes after this returns — one durable record instead of two that could
// disagree across replicas.
func (s *FileStore) InitializeRepositorySource(ctx context.Context, scope Scope) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	tree, err := s.scopeDir(scope)
	if err != nil {
		return err
	}
	var paths []string
	if err := s.walkFiles(ctx, tree, func(file FileInfo) error {
		paths = append(paths, file.Path)
		return nil
	}); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	_, err = s.addUncommittedPaths(ctx, scope, paths)
	return err
}
