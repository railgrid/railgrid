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

package snapshotstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/railgrid/provider-code/objectstore"
)

// objectPrefix roots every snapshot key so the bucket can be shared with
// the commit bundle store.
const objectPrefix = "git-snapshots/"

// ObjectStore keeps snapshots in an object store every replica reaches, as
// git-snapshots/<tenant hash>/<scope hash>/<ref>.json. The tenant quota is
// checked with a prefix listing right before the write, so concurrent
// stagers can overshoot it by their number; see commitbundle.ObjectStore.
type ObjectStore struct {
	backend objectstore.Backend
}

var _ Store = (*ObjectStore)(nil)

// NewObjectStore wraps backend.
func NewObjectStore(backend objectstore.Backend) (*ObjectStore, error) {
	if backend == nil {
		return nil, errors.New("object store backend is required")
	}
	return &ObjectStore{backend: backend}, nil
}

// String names the store for logs.
func (s *ObjectStore) String() string {
	if named, ok := s.backend.(fmt.Stringer); ok {
		return named.String() + "/" + strings.TrimSuffix(objectPrefix, "/")
	}
	return "object://" + strings.TrimSuffix(objectPrefix, "/")
}

func tenantPrefix(cluster string) string {
	return objectPrefix + hashKey(cluster) + "/"
}

func snapshotKey(cluster, scope, ref string) string {
	return tenantPrefix(cluster) + hashKey(scope) + "/" + ref + ".json"
}

// Stage writes data under cluster and scope, reclaiming the tenant's expired
// snapshots first and refusing once the tenant holds its quota.
func (s *ObjectStore) Stage(ctx context.Context, cluster, scope string, data []byte) (string, error) {
	if err := validate(cluster, scope); err != nil {
		return "", err
	}
	if len(data) == 0 || len(data) > MaxBytes {
		return "", ErrTooLarge
	}
	ref := Ref(data)
	key := snapshotKey(cluster, scope, ref)
	now := time.Now()
	if info, err := s.backend.Stat(ctx, key); err == nil {
		if now.Sub(info.LastModified) <= TTL-ttlSkew {
			return ref, nil
		}
	} else if !objectstore.IsNotFound(err) {
		return "", fmt.Errorf("stat snapshot: %w", err)
	}
	count, size, err := s.tenantUsage(ctx, cluster, now)
	if err != nil {
		return "", err
	}
	if count >= MaxPerTenant || size+int64(len(data)) > MaxBytesPerTenant {
		return "", ErrQuotaExceeded
	}
	if err := s.backend.Put(ctx, key, data); err != nil {
		return "", fmt.Errorf("store snapshot: %w", err)
	}
	return ref, nil
}

// tenantUsage counts a tenant's live snapshots and removes expired ones.
func (s *ObjectStore) tenantUsage(ctx context.Context, cluster string, now time.Time) (int, int64, error) {
	count := 0
	var size int64
	var expired []string
	err := s.backend.List(ctx, tenantPrefix(cluster), func(obj objectstore.Object) error {
		if now.Sub(obj.LastModified) > TTL {
			expired = append(expired, obj.Key)
			return nil
		}
		count++
		size += obj.Size
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("list snapshots: %w", err)
	}
	for _, key := range expired {
		if err := s.backend.Delete(ctx, key); err != nil {
			return 0, 0, fmt.Errorf("remove expired snapshot: %w", err)
		}
	}
	return count, size, nil
}

// Load reads the snapshot at exactly cluster/scope/ref.
func (s *ObjectStore) Load(ctx context.Context, cluster, scope, ref string) ([]byte, error) {
	if err := validate(cluster, scope); err != nil {
		return nil, err
	}
	if !ValidRef(ref) {
		return nil, ErrInvalidRef
	}
	return s.readLive(ctx, snapshotKey(cluster, scope, ref), ref, time.Now())
}

// Find reads the snapshot ref from whichever of cluster's scopes staged it.
func (s *ObjectStore) Find(ctx context.Context, cluster, ref string) ([]byte, error) {
	if cluster == "" {
		return nil, errors.New("snapshot cluster is required")
	}
	if !ValidRef(ref) {
		return nil, ErrInvalidRef
	}
	now := time.Now()
	var candidates []string
	suffix := "/" + ref + ".json"
	err := s.backend.List(ctx, tenantPrefix(cluster), func(obj objectstore.Object) error {
		if strings.HasSuffix(obj.Key, suffix) && now.Sub(obj.LastModified) <= TTL && obj.Size <= MaxBytes {
			candidates = append(candidates, obj.Key)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	// A candidate that is not the snapshot (expired, tampered) is skipped; a
	// read that failed for another reason is reported, so a caller can tell
	// a transient store error from a snapshot that is gone.
	var lastErr error
	for _, key := range candidates {
		data, err := s.readLive(ctx, key, ref, now)
		if err == nil {
			return data, nil
		}
		if !errors.Is(err, ErrNotFound) {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrNotFound
}

func (s *ObjectStore) readLive(ctx context.Context, key, ref string, now time.Time) ([]byte, error) {
	info, err := s.backend.Stat(ctx, key)
	if err != nil {
		if objectstore.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("stat snapshot: %w", err)
	}
	if now.Sub(info.LastModified) > TTL || info.Size > MaxBytes {
		return nil, ErrNotFound
	}
	data, err := s.backend.Get(ctx, key)
	if err != nil {
		if objectstore.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	if err := verify(data, ref); err != nil {
		return nil, err
	}
	return data, nil
}

// Sweep removes every snapshot that expired before now. The provider does
// not run it on a clock for this store (a bucket lifecycle rule expires
// them instead); it exists for tests and manual cleanup.
func (s *ObjectStore) Sweep(ctx context.Context, now time.Time) (int, error) {
	var expired []string
	err := s.backend.List(ctx, objectPrefix, func(obj objectstore.Object) error {
		if now.Sub(obj.LastModified) > TTL {
			expired = append(expired, obj.Key)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("list snapshots: %w", err)
	}
	removed := 0
	var errs []error
	for _, key := range expired {
		if err := s.backend.Delete(ctx, key); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}
