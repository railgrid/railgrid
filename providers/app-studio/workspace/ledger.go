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

// The working-copy ledger.
//
// A project's working copy has two halves. The TREE — the bytes of the files —
// lives on whatever volume this replica has, and is rebuildable: its committed
// state is in git, and what is not committed is named by the ledger. The
// LEDGER — which paths differ from the last commit, the monotonic revision the
// development data plane fences on, the commit in flight, the receipt that
// settles it — is authority, and authority may not live on a pod-local disk.
// A second replica that reads an absent ledger reads "this project is clean",
// which is how an empty file list once reached a dev sandbox.
//
// So the ledger is an interface here, and every implementation that matters is
// backed by the project's own CR (internal/projectledger). The workspace
// package deliberately owns no durable copy of it: there is no file to fall
// back to, and a missing ledger is an error rather than a silent zero.
//
// Which CR client to use is a per-call question — the HTTP layer acts as the
// caller, the reconciler acts as itself over its APIExport virtual workspace —
// so the ledger travels on the context. The FileStore keeps an optional
// process default for tests and for single-client wiring.

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

// ErrNoLedger is returned when a working-copy ledger operation is attempted
// without a ledger in scope. It is deliberately not a fallback to disk: a
// ledger that silently degraded to pod-local state is the failure mode this
// whole design exists to remove.
var ErrNoLedger = errors.New("project workspace ledger is not configured")

// PendingCommit is a RepositoryCommit in flight for a project workspace.
type PendingCommit struct {
	// Name is the RepositoryCommit object's name in the tenant workspace.
	Name string
	// RepositoryRef names the Repository the commit targets.
	RepositoryRef string
	// WorkspaceDigest and Paths are the workspace content the commit carried;
	// they become the settlement receipt once the commit succeeds.
	WorkspaceDigest string
	Paths           []string
}

// CommitSettlement is the local cleanup a landed commit still owes the ledger.
type CommitSettlement struct {
	WorkspaceDigest string
	Paths           []string
}

// LedgerRecord is one project's complete working-copy ledger.
type LedgerRecord struct {
	// SourceRevision is the monotonic working-copy revision. Zero means the
	// working copy has never been written; readers report it as 1 so an
	// omitted or zero authority is always rejected downstream.
	SourceRevision uint64
	// UncommittedPaths are the paths that differ from the last commit.
	UncommittedPaths []string
	// PendingCommit, when set, is the RepositoryCommit being followed up.
	PendingCommit *PendingCommit
	// Settlement, when set, is the receipt a landed commit left behind.
	Settlement *CommitSettlement
}

// DeepCopy returns a record that shares nothing with the receiver, so a
// Ledger implementation can hand callers a record it also caches.
func (r LedgerRecord) DeepCopy() LedgerRecord {
	out := LedgerRecord{SourceRevision: r.SourceRevision}
	if r.UncommittedPaths != nil {
		out.UncommittedPaths = append([]string(nil), r.UncommittedPaths...)
	}
	if r.PendingCommit != nil {
		pending := *r.PendingCommit
		pending.Paths = append([]string(nil), r.PendingCommit.Paths...)
		out.PendingCommit = &pending
	}
	if r.Settlement != nil {
		settlement := *r.Settlement
		settlement.Paths = append([]string(nil), r.Settlement.Paths...)
		out.Settlement = &settlement
	}
	return out
}

// Ledger is the durable authority for a project's working-copy state.
//
// Implementations must make Update atomic: read, apply, write, and retry the
// whole cycle when another writer won the race. Two replicas editing one
// project therefore converge rather than overwrite.
type Ledger interface {
	// Read returns the current ledger for scope. A project with no ledger yet
	// reads as the zero record, not an error.
	Read(ctx context.Context, scope Scope) (LedgerRecord, error)
	// Update applies mutate to a freshly read record and persists the result.
	// mutate may be called more than once and must be free of side effects
	// outside the record it is given; returning false persists nothing. The
	// returned record is what the ledger now holds.
	Update(ctx context.Context, scope Scope, mutate func(*LedgerRecord) (bool, error)) (LedgerRecord, error)
}

type ledgerContextKey struct{}

// ContextWithLedger attaches the ledger a call should use. The HTTP layer
// attaches a caller-scoped one per request and per assistant run; the Project
// reconciler attaches its own. Nothing else needs to know which.
func ContextWithLedger(ctx context.Context, ledger Ledger) context.Context {
	if ledger == nil {
		return ctx
	}
	return context.WithValue(ctx, ledgerContextKey{}, ledger)
}

// LedgerFromContext returns the ledger attached to ctx, if any.
func LedgerFromContext(ctx context.Context) (Ledger, bool) {
	ledger, ok := ctx.Value(ledgerContextKey{}).(Ledger)
	return ledger, ok && ledger != nil
}

// SetLedger installs the process-wide default ledger — the one used when a
// call arrives with none on its context. Production leaves it nil: the client
// to write a project's CR with depends on who is asking.
func (s *FileStore) SetLedger(ledger Ledger) {
	if s == nil {
		return
	}
	s.ledger = ledger
}

// ledgerFor resolves the ledger for one call: the context's, else the store's
// default, else ErrNoLedger.
func (s *FileStore) ledgerFor(ctx context.Context) (Ledger, error) {
	if s == nil {
		return nil, errors.New("project workspace store is not configured")
	}
	if ledger, ok := LedgerFromContext(ctx); ok {
		return ledger, nil
	}
	if s.ledger != nil {
		return s.ledger, nil
	}
	return nil, ErrNoLedger
}

// NewMemoryLedger returns an in-process ledger. It is the ledger tests use and
// the one a REST-only local run falls back to; it is NOT durable and must
// never be wired into a deployment that has a control plane to write to.
func NewMemoryLedger() *MemoryLedger {
	return &MemoryLedger{records: map[string]LedgerRecord{}}
}

// MemoryLedger is an in-process Ledger. Its Update is atomic under its own
// mutex, which is all a single process needs.
type MemoryLedger struct {
	mu      sync.Mutex
	records map[string]LedgerRecord
}

// LedgerKey is the identity a ledger keys a project by. The ProjectUID is part
// of it so a recreated Project never inherits the deleted one's dirty set.
func LedgerKey(scope Scope) string {
	return strings.Join([]string{scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID}, "/")
}

// Read implements Ledger.
func (l *MemoryLedger) Read(ctx context.Context, scope Scope) (LedgerRecord, error) {
	if err := ctx.Err(); err != nil {
		return LedgerRecord{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.records[LedgerKey(scope)].DeepCopy(), nil
}

// Update implements Ledger.
func (l *MemoryLedger) Update(ctx context.Context, scope Scope, mutate func(*LedgerRecord) (bool, error)) (LedgerRecord, error) {
	if err := ctx.Err(); err != nil {
		return LedgerRecord{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := LedgerKey(scope)
	record := l.records[key].DeepCopy()
	changed, err := mutate(&record)
	if err != nil {
		return LedgerRecord{}, err
	}
	if !changed {
		return l.records[key].DeepCopy(), nil
	}
	NormalizeLedgerRecord(&record)
	l.records[key] = record
	return record.DeepCopy(), nil
}

// Forget drops a project's ledger. Only the teardown path uses it: a deleted
// project's CR takes its ledger with it, and this keeps the test ledger in
// step with that.
func (l *MemoryLedger) Forget(scope Scope) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.records, LedgerKey(scope))
}

// NormalizeLedgerRecord canonicalizes a record before it is persisted: paths
// sorted and deduplicated, empty sub-records dropped. Implementations call it
// so what a reader gets back does not depend on which writer produced it.
func NormalizeLedgerRecord(record *LedgerRecord) {
	if record == nil {
		return
	}
	record.UncommittedPaths = normalizeLedgerPaths(record.UncommittedPaths)
	if record.PendingCommit != nil {
		record.PendingCommit.Name = strings.TrimSpace(record.PendingCommit.Name)
		record.PendingCommit.Paths = normalizeLedgerPaths(record.PendingCommit.Paths)
		if record.PendingCommit.Name == "" || record.PendingCommit.WorkspaceDigest == "" || len(record.PendingCommit.Paths) == 0 {
			record.PendingCommit = nil
		}
	}
	if record.Settlement != nil {
		record.Settlement.Paths = normalizeLedgerPaths(record.Settlement.Paths)
		if record.Settlement.WorkspaceDigest == "" || len(record.Settlement.Paths) == 0 {
			record.Settlement = nil
		}
	}
}

// removeLedgerPaths returns current without any path in remove.
func removeLedgerPaths(current, remove []string) []string {
	if len(current) == 0 || len(remove) == 0 {
		return normalizeLedgerPaths(current)
	}
	drop := make(map[string]struct{}, len(remove))
	for _, path := range remove {
		drop[path] = struct{}{}
	}
	kept := make([]string, 0, len(current))
	for _, path := range current {
		if _, ok := drop[path]; !ok {
			kept = append(kept, path)
		}
	}
	return normalizeLedgerPaths(kept)
}

func normalizeLedgerPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}
