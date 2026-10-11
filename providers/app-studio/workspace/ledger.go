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
	"time"
)

// ErrNoLedger is returned when a working-copy ledger operation is attempted
// without a ledger in scope. It is deliberately not a fallback to disk: a
// ledger that silently degraded to pod-local state is the failure mode this
// whole design exists to remove.
var ErrNoLedger = errors.New("project workspace ledger is not configured")

// ErrLedgerCapacity means a normalized workspace ledger cannot fit the
// durable status bounds. Implementations return it before attempting a status
// write, so callers can treat it as a definitive no-write result.
var ErrLedgerCapacity = errors.New("project workspace ledger capacity exceeded")

// PendingCommit is a RepositoryCommit in flight for a project workspace.
type PendingCommit struct {
	// Name is the RepositoryCommit object's name in the tenant workspace.
	// It is empty while the durable request is reserved but the Code provider
	// has not yet returned (or the caller has not yet recorded) the object.
	Name string
	// RequestID makes creation replay-safe at the Code provider. The remaining
	// request fields are the immutable inputs needed to retry after a process
	// restart without reading mutable workspace files again.
	RequestID string
	// RepositoryRef names the Repository the commit targets.
	RepositoryRef string
	RepositoryUID string
	BundleRef     string
	BundleDigest  string
	Message       string
	Branch        string
	// WorkspaceDigest and Paths are the workspace content the commit carried;
	// they become the settlement receipt once the commit succeeds.
	WorkspaceDigest string
	Paths           []string
	// SourceRevision is the durable source revision whose bytes and owners
	// were captured for this request. Later mutations are fenced from this
	// immutable commit snapshot.
	SourceRevision uint64
	// Attributions snapshots the source turns that produced the committed
	// paths. It travels with the pending RepositoryCommit so later source
	// changes cannot steal the commit event.
	Attributions []SourceAttribution
	// BlockedReason and BlockedMessage retain an immutable intent when replay is
	// permanently refused or a named RepositoryCommit failure is in its
	// automatic retry cooldown. BlockedAt persists the start of that state so
	// replicas and restarts observe the same deadline.
	BlockedReason  string
	BlockedMessage string
	BlockedAt      *time.Time
	// RetryToken is the last operator retry token consumed for this request.
	// Keeping it beside the request makes a stale Project annotation inert.
	RetryToken string
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
	// SourceMutationID identifies the writer of SourceRevision so an ambiguous
	// status-write response cannot be confused with another writer at that revision.
	SourceMutationID      string
	LastSourceChange      *SourceChange
	LastPreviewCheckpoint *SourceChange
	// SourcePathAttributions retains one unambiguous owner per source path
	// since that path last reached git. An empty owner records a path whose
	// provenance is unknown or mixed, which must stay unattributed.
	SourcePathAttributions []SourcePathAttribution
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
	out := LedgerRecord{SourceRevision: r.SourceRevision, SourceMutationID: r.SourceMutationID}
	if r.LastSourceChange != nil {
		change := *r.LastSourceChange
		out.LastSourceChange = &change
	}
	if r.LastPreviewCheckpoint != nil {
		change := *r.LastPreviewCheckpoint
		out.LastPreviewCheckpoint = &change
	}
	if r.SourcePathAttributions != nil {
		out.SourcePathAttributions = append([]SourcePathAttribution(nil), r.SourcePathAttributions...)
	}
	if r.UncommittedPaths != nil {
		out.UncommittedPaths = append([]string(nil), r.UncommittedPaths...)
	}
	if r.PendingCommit != nil {
		pending := *r.PendingCommit
		pending.Paths = append([]string(nil), r.PendingCommit.Paths...)
		pending.Attributions = cloneSourceAttributions(r.PendingCommit.Attributions)
		if r.PendingCommit.BlockedAt != nil {
			blockedAt := *r.PendingCommit.BlockedAt
			pending.BlockedAt = &blockedAt
		}
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
	record.SourcePathAttributions = normalizeSourcePathAttributions(record.SourcePathAttributions)
	if record.PendingCommit != nil {
		requestIDWasPresent := record.PendingCommit.RequestID != ""
		originalRequestID := record.PendingCommit.RequestID
		record.PendingCommit.Name = strings.TrimSpace(record.PendingCommit.Name)
		record.PendingCommit.RequestID = strings.TrimSpace(originalRequestID)
		record.PendingCommit.RepositoryRef = strings.TrimSpace(record.PendingCommit.RepositoryRef)
		record.PendingCommit.RepositoryUID = strings.TrimSpace(record.PendingCommit.RepositoryUID)
		record.PendingCommit.BundleRef = strings.TrimSpace(record.PendingCommit.BundleRef)
		record.PendingCommit.BundleDigest = strings.TrimSpace(record.PendingCommit.BundleDigest)
		record.PendingCommit.Message = strings.TrimSpace(record.PendingCommit.Message)
		record.PendingCommit.Branch = strings.TrimSpace(record.PendingCommit.Branch)
		record.PendingCommit.BlockedReason = strings.TrimSpace(record.PendingCommit.BlockedReason)
		record.PendingCommit.BlockedMessage = strings.TrimSpace(record.PendingCommit.BlockedMessage)
		record.PendingCommit.RetryToken = strings.TrimSpace(record.PendingCommit.RetryToken)
		if record.PendingCommit.BlockedReason == "" {
			record.PendingCommit.BlockedMessage = ""
			record.PendingCommit.BlockedAt = nil
		} else if record.PendingCommit.BlockedAt != nil {
			blockedAt := record.PendingCommit.BlockedAt.UTC()
			record.PendingCommit.BlockedAt = &blockedAt
		}
		record.PendingCommit.Paths = normalizeLedgerPaths(record.PendingCommit.Paths)
		record.PendingCommit.Attributions = NormalizeSourceAttributions(record.PendingCommit.Attributions, record.PendingCommit.Paths)
		validLegacy := !requestIDWasPresent && record.PendingCommit.RequestID == "" && record.PendingCommit.Name != ""
		validIntent := record.PendingCommit.RequestID != "" && record.PendingCommit.RepositoryRef != "" &&
			record.PendingCommit.RepositoryUID != "" && record.PendingCommit.BundleRef != "" && record.PendingCommit.BundleDigest != ""
		if (!validLegacy && !validIntent) || record.PendingCommit.WorkspaceDigest == "" || len(record.PendingCommit.Paths) == 0 {
			if !requestIDWasPresent {
				record.PendingCommit = nil
			} else if record.PendingCommit.RequestID == "" {
				// Preserve a malformed keyed intent, including a whitespace-only
				// request ID, so PendingCommit can reject it and prevent a newer
				// commit from replacing its durable identity.
				record.PendingCommit.RequestID = originalRequestID
			}
		}
	}
	if record.Settlement != nil {
		record.Settlement.Paths = normalizeLedgerPaths(record.Settlement.Paths)
		if record.Settlement.WorkspaceDigest == "" || len(record.Settlement.Paths) == 0 {
			record.Settlement = nil
		}
	}
}

// SourcePathAttribution records the owner of a path's current uncommitted
// source state. Empty ThreadID and RunID mean the path is deliberately
// unattributed because it is legacy, system-authored, or mixed across turns.
type SourcePathAttribution struct {
	Path           string `json:"path"`
	ThreadID       string `json:"threadID,omitempty"`
	RunID          string `json:"runID,omitempty"`
	SourceRevision uint64 `json:"sourceRevision,omitempty"`
}

// SourceAttribution groups the exact paths in a pending commit that one
// assistant turn produced. A commit can carry paths from several turns.
type SourceAttribution struct {
	ThreadID string   `json:"threadID"`
	RunID    string   `json:"runID"`
	Paths    []string `json:"paths"`
}

func cloneSourceAttributions(values []SourceAttribution) []SourceAttribution {
	if values == nil {
		return nil
	}
	out := make([]SourceAttribution, len(values))
	for index, value := range values {
		out[index] = value
		out[index].Paths = append([]string(nil), value.Paths...)
	}
	return out
}

func normalizeSourcePathAttributions(values []SourcePathAttribution) []SourcePathAttribution {
	if len(values) == 0 {
		return nil
	}
	byPath := make(map[string]SourcePathAttribution, len(values))
	ambiguous := make(map[string]struct{})
	for _, value := range values {
		path, err := cleanProjectPath(value.Path)
		if err != nil {
			continue
		}
		value.Path = path
		value.ThreadID = strings.TrimSpace(value.ThreadID)
		value.RunID = strings.TrimSpace(value.RunID)
		if value.ThreadID == "" || value.RunID == "" {
			value.ThreadID, value.RunID = "", ""
		}
		if _, alreadyAmbiguous := ambiguous[path]; alreadyAmbiguous {
			continue
		}
		if existing, ok := byPath[path]; ok {
			if existing.ThreadID != value.ThreadID || existing.RunID != value.RunID {
				delete(byPath, path)
				ambiguous[path] = struct{}{}
			}
			continue
		}
		byPath[path] = value
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]SourcePathAttribution, 0, len(paths))
	for _, path := range paths {
		out = append(out, byPath[path])
	}
	return out
}

// NormalizeSourceAttributions bounds attribution groups to committedPaths,
// sorts their paths, and drops any path claimed by conflicting owners.
func NormalizeSourceAttributions(values []SourceAttribution, committedPaths []string) []SourceAttribution {
	if len(values) == 0 || len(committedPaths) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(committedPaths))
	for _, rawPath := range committedPaths {
		path, err := cleanProjectPath(rawPath)
		if err == nil {
			allowed[path] = struct{}{}
		}
	}
	type owner struct{ threadID, runID string }
	byOwner := make(map[owner]map[string]struct{}, len(values))
	ownerByPath := make(map[string]owner)
	ambiguous := make(map[string]struct{})
	for _, value := range values {
		key := owner{threadID: strings.TrimSpace(value.ThreadID), runID: strings.TrimSpace(value.RunID)}
		if key.threadID == "" || key.runID == "" {
			continue
		}
		for _, rawPath := range value.Paths {
			path, err := cleanProjectPath(rawPath)
			if err != nil {
				continue
			}
			if _, ok := allowed[path]; !ok {
				continue
			}
			if _, conflicted := ambiguous[path]; conflicted {
				continue
			}
			if existing, ok := ownerByPath[path]; ok && existing != key {
				delete(byOwner[existing], path)
				delete(ownerByPath, path)
				ambiguous[path] = struct{}{}
				continue
			}
			ownerByPath[path] = key
			if byOwner[key] == nil {
				byOwner[key] = map[string]struct{}{}
			}
			byOwner[key][path] = struct{}{}
		}
	}
	owners := make([]owner, 0, len(byOwner))
	for key := range byOwner {
		if len(byOwner[key]) > 0 {
			owners = append(owners, key)
		}
	}
	sort.Slice(owners, func(i, j int) bool {
		if owners[i].threadID != owners[j].threadID {
			return owners[i].threadID < owners[j].threadID
		}
		return owners[i].runID < owners[j].runID
	})
	out := make([]SourceAttribution, 0, len(owners))
	for _, key := range owners {
		paths := make([]string, 0, len(byOwner[key]))
		for path := range byOwner[key] {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		out = append(out, SourceAttribution{ThreadID: key.threadID, RunID: key.runID, Paths: paths})
	}
	return out
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

// SourceChange records the immutable turn attribution of a source revision.
type SourceChange struct {
	ThreadID       string `json:"threadID,omitempty"`
	RunID          string `json:"runID,omitempty"`
	SourceRevision uint64 `json:"sourceRevision"`
}

type mutationOriginContextKey struct{}

// ContextWithMutationOrigin attaches server-owned turn provenance to a short
// workspace transaction. It is never parsed from client-provided headers.
func ContextWithMutationOrigin(ctx context.Context, threadID, runID string) context.Context {
	return context.WithValue(ctx, mutationOriginContextKey{}, SourceChange{ThreadID: threadID, RunID: runID})
}

// LastSourceChange reads provenance from the same durable ledger as its revision.
func (s *FileStore) LastSourceChange(ctx context.Context, scope Scope) (*SourceChange, error) {
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return nil, err
	}
	record, err := ledger.Read(ctx, scope)
	if err != nil {
		return nil, err
	}
	return record.DeepCopy().LastSourceChange, nil
}

func (s *FileStore) LastPreviewCheckpoint(ctx context.Context, scope Scope) (*SourceChange, error) {
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return nil, err
	}
	record, err := ledger.Read(ctx, scope)
	if err != nil {
		return nil, err
	}
	return record.DeepCopy().LastPreviewCheckpoint, nil
}

// RecordPreviewCheckpoint records only the snapshot acknowledged by the runtime.
func (s *FileStore) RecordPreviewCheckpoint(ctx context.Context, scope Scope, source SourceChange) error {
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return err
	}
	_, err = ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		copy := source
		record.LastPreviewCheckpoint = &copy
		return true, nil
	})
	return err
}
