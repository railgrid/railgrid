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

// Package snapshotstore keeps the git snapshots the stage-snapshot verb
// uploads until the PullRequest controller publishes them. Like commit
// bundles, a snapshot is a transient artifact: the PullRequest carries only
// its content digest, nothing is lost if it is gone, and it expires on a
// fixed TTL. The verb that stages a snapshot and the leader that reads it
// run on different replicas, so a multi-replica deployment uses the object
// store and a single replica may use a local directory.
package snapshotstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	// TTL is how long a staged snapshot stays readable. A PullRequest
	// publishes its head within minutes; anything older was abandoned.
	TTL = time.Hour
	// MaxBytes caps one serialized snapshot.
	MaxBytes = 36 << 20
	// MaxPerTenant and MaxBytesPerTenant bound what one logical cluster may
	// hold at once, counting expired entries that have not been reclaimed.
	MaxPerTenant      = 16
	MaxBytesPerTenant = 256 << 20
	// DefaultSweepInterval is how often RunSweeper reclaims expired
	// snapshots from a store that has no lifecycle rule of its own.
	DefaultSweepInterval = time.Hour
	// ttlSkew is the clock disagreement tolerated between this process and
	// an external object store. A writer treats an entry as reusable only if
	// it is at least this far from expiring, so it never hands out a ref a
	// reader on another replica then judges expired.
	ttlSkew = 2 * time.Minute
)

var (
	// ErrNotFound reports that no live snapshot matches the reference.
	ErrNotFound = errors.New("snapshot unavailable or expired")
	// ErrQuotaExceeded reports that the tenant holds its maximum.
	ErrQuotaExceeded = errors.New("tenant snapshot quota reached")
	// ErrInvalidRef reports a reference that is not a hex SHA-256.
	ErrInvalidRef = errors.New("invalid snapshot reference")
	// ErrTooLarge reports a payload above MaxBytes.
	ErrTooLarge = errors.New("snapshot exceeds limit")
)

// Store stages and reads serialized snapshots. cluster is the tenant's kcp
// logical-cluster ID; scope distinguishes who staged within that tenant (the
// repository, connection and caller), so a caller reads back only what it
// staged while the controller finds a snapshot by digest across all of a
// tenant's scopes. The reference is the hex SHA-256 of the bytes, so the
// same bytes are the same snapshot wherever they were put.
type Store interface {
	// Stage stores data and returns its reference. Staging bytes already
	// present is a no-op that returns the same reference.
	Stage(ctx context.Context, cluster, scope string, data []byte) (string, error)
	// Load returns the live snapshot ref under exactly this cluster and
	// scope, verifying its digest.
	Load(ctx context.Context, cluster, scope, ref string) ([]byte, error)
	// Find returns the live snapshot ref staged under any scope of cluster,
	// verifying its digest.
	Find(ctx context.Context, cluster, ref string) ([]byte, error)
	// Sweep removes snapshots that expired before now and reports how many.
	Sweep(ctx context.Context, now time.Time) (int, error)
	// String names the store for logs.
	String() string
}

var refPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ValidRef reports whether ref is a well-formed reference.
func ValidRef(ref string) bool {
	return refPattern.MatchString(ref)
}

// Ref returns the reference of data.
func Ref(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashKey(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func validate(cluster, scope string) error {
	if cluster == "" {
		return errors.New("snapshot cluster is required")
	}
	if scope == "" {
		return errors.New("snapshot scope is required")
	}
	return nil
}

// verify checks data against ref and its size against MaxBytes.
func verify(data []byte, ref string) error {
	if len(data) > MaxBytes {
		return ErrTooLarge
	}
	if Ref(data) != ref {
		// Bytes that do not hash to ref are not the snapshot asked for.
		return fmt.Errorf("%w: digest mismatch", ErrNotFound)
	}
	return nil
}

// RunSweeper sweeps expired snapshots once at startup and then every
// interval until ctx is done.
func RunSweeper(ctx context.Context, store Store, interval time.Duration, log func(removed int, err error)) {
	sweep := func() {
		removed, err := store.Sweep(ctx, time.Now())
		if log != nil && (removed > 0 || err != nil) {
			log(removed, err)
		}
	}
	sweep()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}
