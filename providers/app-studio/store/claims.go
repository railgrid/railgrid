// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package store

// Replica claims are the fleet-wide ownership map that makes App Studio
// replica-aware (docs/app-studio-replica-awareness.md): which replica owns a
// project's assistant activity (a run or a reserved external operation), and
// which replica a project's workspace is pinned to. They ride the same store
// the runs do, so ownership and run state share one durability domain.
//
// Semantics mirror the kuery/edges Lease claims: try-acquire takes a free or
// stale claim and renews an owned one; a fresh foreign claim is declined;
// releases are owner-checked so a slow cleanup cannot erase a newer claim.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Replica claim kinds.
const (
	// ReplicaClaimKindActivity marks a project's assistant activity — an
	// attached run or a reserved external operation (hydrate, template
	// switch, delete). One per project scope; Detail carries the run ID or
	// "operation".
	ReplicaClaimKindActivity = "activity"
	// ReplicaClaimKindProject pins a project's workspace to a replica; the
	// affinity middleware forwards workspace-touching requests to OwnerAddr.
	// Revision carries the durable floor of the workspace source-revision
	// fence so it survives the project moving between replicas.
	ReplicaClaimKindProject = "project"
	// ReplicaClaimKindThumbnail serializes derived preview capture across
	// provider replicas. Detail carries the target repository commit SHA.
	ReplicaClaimKindThumbnail = "thumbnail"
)

// ReplicaClaim is one fleet-wide ownership record.
type ReplicaClaim struct {
	Key          string
	Kind         string
	ScopeKey     string
	OwnerReplica string
	OwnerAddr    string
	Detail       string
	Revision     int64
	HeartbeatAt  time.Time
}

// Live reports whether the claim was renewed within staleAfter of now.
func (c ReplicaClaim) Live(now time.Time, staleAfter time.Duration) bool {
	return !c.HeartbeatAt.IsZero() && now.Sub(c.HeartbeatAt) <= staleAfter
}

// ReplicaClaimScopeKey renders the canonical per-project scope key.
func ReplicaClaimScopeKey(scope Scope) string {
	return scope.OrgUUID + "/" + scope.WorkspaceUUID + "/" + scope.ProjectName + "/" + scope.ProjectUID
}

// ActivityClaimKey is the claim key for a project's assistant activity.
func ActivityClaimKey(scope Scope) string {
	return ReplicaClaimKindActivity + "/" + ReplicaClaimScopeKey(scope)
}

// ThumbnailClaimKey is the capture lease key for one project incarnation.
func ThumbnailClaimKey(scope Scope) string {
	return ReplicaClaimKindThumbnail + "/" + ReplicaClaimScopeKey(scope)
}

// ---- Postgres ----

const replicaClaimSchemaVersion = "replica-claims-v1"

func replicaClaimSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS app_studio_replica_claims (
			claim_key text PRIMARY KEY,
			kind text NOT NULL,
			scope_key text NOT NULL,
			owner_replica text NOT NULL,
			owner_addr text NOT NULL DEFAULT '',
			detail text NOT NULL DEFAULT '',
			revision bigint NOT NULL DEFAULT 0,
			heartbeat_at timestamptz NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS app_studio_replica_claims_scope
			ON app_studio_replica_claims (scope_key, kind)`,
	}
}

// TryClaimReplica acquires (or renews) the claim in one atomic upsert: a
// missing claim is created, an owned or stale claim is overwritten, a fresh
// foreign claim is left untouched. It returns the claim as currently held and
// whether this owner holds it. Revision is preserved across takeovers.
func (s *PostgresStore) TryClaimReplica(ctx context.Context, claim ReplicaClaim, staleAfter time.Duration) (ReplicaClaim, bool, error) {
	now := time.Now().UTC()
	cutoff := now.Add(-staleAfter)
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO app_studio_replica_claims (claim_key, kind, scope_key, owner_replica, owner_addr, detail, revision, heartbeat_at)
		VALUES ($1, $2, $3, $4, $5, $6, 0, $7)
		ON CONFLICT (claim_key) DO UPDATE SET
			owner_replica = EXCLUDED.owner_replica,
			owner_addr = EXCLUDED.owner_addr,
			detail = EXCLUDED.detail,
			heartbeat_at = EXCLUDED.heartbeat_at
		WHERE app_studio_replica_claims.owner_replica = EXCLUDED.owner_replica
			OR app_studio_replica_claims.heartbeat_at < $8
		RETURNING owner_replica, owner_addr, detail, revision, heartbeat_at`,
		claim.Key, claim.Kind, claim.ScopeKey, claim.OwnerReplica, claim.OwnerAddr, claim.Detail, now, cutoff)
	held := claim
	if err := row.Scan(&held.OwnerReplica, &held.OwnerAddr, &held.Detail, &held.Revision, &held.HeartbeatAt); err == nil {
		return held, true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ReplicaClaim{}, false, fmt.Errorf("claim %s: %w", claim.Key, err)
	}
	// Fresh foreign claim: report the current holder.
	current, ok, err := s.GetReplicaClaim(ctx, claim.Key)
	if err != nil {
		return ReplicaClaim{}, false, err
	}
	if !ok {
		// Deleted between the upsert and the read; the caller's next attempt
		// will take it.
		return ReplicaClaim{}, false, nil
	}
	return current, false, nil
}

// RenewReplicaClaim refreshes the heartbeat when this owner still holds the
// claim, reporting whether it did.
func (s *PostgresStore) RenewReplicaClaim(ctx context.Context, claimKey, ownerReplica string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE app_studio_replica_claims SET heartbeat_at = $1 WHERE claim_key = $2 AND owner_replica = $3`,
		time.Now().UTC(), claimKey, ownerReplica)
	if err != nil {
		return false, fmt.Errorf("renew claim %s: %w", claimKey, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReleaseReplicaClaim drops the claim if this owner still holds it.
func (s *PostgresStore) ReleaseReplicaClaim(ctx context.Context, claimKey, ownerReplica string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM app_studio_replica_claims WHERE claim_key = $1 AND owner_replica = $2`,
		claimKey, ownerReplica)
	if err != nil {
		return fmt.Errorf("release claim %s: %w", claimKey, err)
	}
	return nil
}

// GetReplicaClaim reads one claim; ok=false when absent.
func (s *PostgresStore) GetReplicaClaim(ctx context.Context, claimKey string) (ReplicaClaim, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT claim_key, kind, scope_key, owner_replica, owner_addr, detail, revision, heartbeat_at
		FROM app_studio_replica_claims WHERE claim_key = $1`, claimKey)
	var c ReplicaClaim
	if err := row.Scan(&c.Key, &c.Kind, &c.ScopeKey, &c.OwnerReplica, &c.OwnerAddr, &c.Detail, &c.Revision, &c.HeartbeatAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReplicaClaim{}, false, nil
		}
		return ReplicaClaim{}, false, fmt.Errorf("get claim %s: %w", claimKey, err)
	}
	return c, true, nil
}

// LiveReplicaClaims lists the claims for a scope renewed within staleAfter.
func (s *PostgresStore) LiveReplicaClaims(ctx context.Context, scopeKey string, staleAfter time.Duration) ([]ReplicaClaim, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT claim_key, kind, scope_key, owner_replica, owner_addr, detail, revision, heartbeat_at
		FROM app_studio_replica_claims WHERE scope_key = $1 AND heartbeat_at >= $2`,
		scopeKey, time.Now().UTC().Add(-staleAfter))
	if err != nil {
		return nil, fmt.Errorf("live claims %s: %w", scopeKey, err)
	}
	defer func() { _ = rows.Close() }()
	var out []ReplicaClaim
	for rows.Next() {
		var c ReplicaClaim
		if err := rows.Scan(&c.Key, &c.Kind, &c.ScopeKey, &c.OwnerReplica, &c.OwnerAddr, &c.Detail, &c.Revision, &c.HeartbeatAt); err != nil {
			return nil, fmt.Errorf("live claims %s: %w", scopeKey, err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// BumpReplicaClaimRevision raises the claim's revision floor (never lowers)
// while this owner holds it.
func (s *PostgresStore) BumpReplicaClaimRevision(ctx context.Context, claimKey, ownerReplica string, revision int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE app_studio_replica_claims SET revision = GREATEST(revision, $1) WHERE claim_key = $2 AND owner_replica = $3`,
		revision, claimKey, ownerReplica)
	if err != nil {
		return fmt.Errorf("bump claim %s: %w", claimKey, err)
	}
	return nil
}

// ---- Memory ----

func (m *MemoryStore) TryClaimReplica(_ context.Context, claim ReplicaClaim, staleAfter time.Duration) (ReplicaClaim, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.replicaClaims == nil {
		m.replicaClaims = map[string]ReplicaClaim{}
	}
	now := time.Now().UTC()
	current, exists := m.replicaClaims[claim.Key]
	if exists && current.OwnerReplica != claim.OwnerReplica && current.Live(now, staleAfter) {
		return current, false, nil
	}
	claim.Revision = current.Revision // preserved across takeovers
	claim.HeartbeatAt = now
	m.replicaClaims[claim.Key] = claim
	return claim, true, nil
}

func (m *MemoryStore) RenewReplicaClaim(_ context.Context, claimKey, ownerReplica string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.replicaClaims[claimKey]
	if !exists || current.OwnerReplica != ownerReplica {
		return false, nil
	}
	current.HeartbeatAt = time.Now().UTC()
	m.replicaClaims[claimKey] = current
	return true, nil
}

func (m *MemoryStore) ReleaseReplicaClaim(_ context.Context, claimKey, ownerReplica string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, exists := m.replicaClaims[claimKey]; exists && current.OwnerReplica == ownerReplica {
		delete(m.replicaClaims, claimKey)
	}
	return nil
}

func (m *MemoryStore) GetReplicaClaim(_ context.Context, claimKey string) (ReplicaClaim, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.replicaClaims[claimKey]
	return c, ok, nil
}

func (m *MemoryStore) LiveReplicaClaims(_ context.Context, scopeKey string, staleAfter time.Duration) ([]ReplicaClaim, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := time.Now().UTC()
	var out []ReplicaClaim
	for _, c := range m.replicaClaims {
		if c.ScopeKey == scopeKey && c.Live(now, staleAfter) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (m *MemoryStore) BumpReplicaClaimRevision(_ context.Context, claimKey, ownerReplica string, revision int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, exists := m.replicaClaims[claimKey]; exists && current.OwnerReplica == ownerReplica && revision > current.Revision {
		current.Revision = revision
		m.replicaClaims[claimKey] = current
	}
	return nil
}

// ---- Encryption wrapper (claims carry no user content — pure delegation) ----

func (e *encryptedStore) TryClaimReplica(ctx context.Context, claim ReplicaClaim, staleAfter time.Duration) (ReplicaClaim, bool, error) {
	return e.inner.TryClaimReplica(ctx, claim, staleAfter)
}

func (e *encryptedStore) RenewReplicaClaim(ctx context.Context, claimKey, ownerReplica string) (bool, error) {
	return e.inner.RenewReplicaClaim(ctx, claimKey, ownerReplica)
}

func (e *encryptedStore) ReleaseReplicaClaim(ctx context.Context, claimKey, ownerReplica string) error {
	return e.inner.ReleaseReplicaClaim(ctx, claimKey, ownerReplica)
}

func (e *encryptedStore) GetReplicaClaim(ctx context.Context, claimKey string) (ReplicaClaim, bool, error) {
	return e.inner.GetReplicaClaim(ctx, claimKey)
}

func (e *encryptedStore) LiveReplicaClaims(ctx context.Context, scopeKey string, staleAfter time.Duration) ([]ReplicaClaim, error) {
	return e.inner.LiveReplicaClaims(ctx, scopeKey, staleAfter)
}

func (e *encryptedStore) BumpReplicaClaimRevision(ctx context.Context, claimKey, ownerReplica string, revision int64) error {
	return e.inner.BumpReplicaClaimRevision(ctx, claimKey, ownerReplica, revision)
}
