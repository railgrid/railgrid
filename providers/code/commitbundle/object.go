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

package commitbundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/railgrid/provider-code/objectstore"
)

// objectPrefix roots every bundle key so one bucket can also hold the
// snapshot store (and anything else) without the sweepers colliding.
const objectPrefix = "commit-bundles/"

// ObjectStore keeps bundles in an object store every replica reaches, which
// is what lets the replica that accepted commit_files and the leader that
// commits it be different pods. Layout mirrors FileStore:
// commit-bundles/<scope key>/<bundle name>.json.
//
// The per-scope quota is checked with a prefix listing immediately before
// the write and again right after it: an object store has no equivalent of
// the filesystem lock FileStore serialises the check with, so concurrent
// writers can all pass the first check, and the second one decides who
// yields (backOutIfOverQuota). Orphans are reclaimed by a bucket lifecycle
// rule, not by this process.
type ObjectStore struct {
	backend  objectstore.Backend
	notifier notifier
}

var _ Store = (*ObjectStore)(nil)
var _ Notifier = (*ObjectStore)(nil)
var _ Sweeper = (*ObjectStore)(nil)

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

// Notify returns a channel of arrivals published by this process.
func (s *ObjectStore) Notify(ctx context.Context) <-chan Arrival {
	return s.notifier.Notify(ctx)
}

// Put validates, canonicalizes, and stores an immutable content-addressed
// bundle.
func (s *ObjectStore) Put(ctx context.Context, scope string, files []File) (BundleRef, error) {
	return s.put(ctx, scope, files, false)
}

// PutIsolated stores a bundle under a unique name while keeping the digest
// content-derived.
func (s *ObjectStore) PutIsolated(ctx context.Context, scope string, files []File) (BundleRef, error) {
	return s.put(ctx, scope, files, true)
}

func (s *ObjectStore) put(ctx context.Context, scope string, files []File, isolated bool) (BundleRef, error) {
	if s == nil {
		return BundleRef{}, errors.New("bundle store is nil")
	}
	scope = strings.TrimSpace(scope)
	key, err := scopeKey(scope)
	if err != nil {
		return BundleRef{}, err
	}
	bundle, ref, err := buildBundle(files)
	if err != nil {
		return BundleRef{}, err
	}
	bundle.Scope = scope
	ref.Scope = scope
	if isolated {
		name, err := newIsolatedBundleName()
		if err != nil {
			return BundleRef{}, err
		}
		bundle.Name = name
		ref.Name = name
	}
	if err := ctx.Err(); err != nil {
		return BundleRef{}, err
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return BundleRef{}, fmt.Errorf("marshal bundle: %w", err)
	}
	objectKey := bundleObjectKey(key, bundle.Name)
	if !isolated {
		if _, err := s.backend.Stat(ctx, objectKey); err == nil {
			// Reusing an identical bundle: rewrite the same bytes so its age
			// resets and the orphan sweeper does not reclaim it out from
			// under this new request.
			if err := s.backend.Put(ctx, objectKey, data); err != nil {
				return BundleRef{}, fmt.Errorf("refresh bundle: %w", err)
			}
			s.notifier.announce(scope, bundle.Name)
			return ref, nil
		} else if !objectstore.IsNotFound(err) {
			return BundleRef{}, fmt.Errorf("stat bundle: %w", err)
		}
	}
	count, usedBytes, err := s.scopeUsage(ctx, key)
	if err != nil {
		return BundleRef{}, err
	}
	if scopeQuotaWouldBeExceeded(count, usedBytes, int64(len(data))) {
		return BundleRef{}, fmt.Errorf("%w: scope has %d bundles and %d serialized bytes; limits are %d bundles and %d bytes", ErrScopeQuotaExceeded, count, usedBytes, MaxScopeBundles, MaxScopeBytes)
	}
	if err := s.backend.Put(ctx, objectKey, data); err != nil {
		return BundleRef{}, fmt.Errorf("store bundle: %w", err)
	}
	if err := s.backOutIfOverQuota(ctx, key, objectKey); err != nil {
		return BundleRef{}, err
	}
	s.notifier.announce(scope, bundle.Name)
	return ref, nil
}

// backOutIfOverQuota closes the check-then-write race. After its own write
// a writer lists the scope again; if concurrent writers pushed it over a
// limit, the objects whose keys sort last are the ones that do not fit, and
// a writer whose own object is among them deletes it and reports the quota.
// Every racer sees the same listing, or a prefix of it, and applies the same
// rule, so they agree on who yields without a lock. A listing that fails or
// is truncated leaves the stored object alone: a successful write is not
// failed on a re-check the store could not complete.
func (s *ObjectStore) backOutIfOverQuota(ctx context.Context, scopeKey, ownKey string) error {
	type entry struct {
		key  string
		size int64
	}
	var entries []entry
	const maxScanEntries = MaxScopeBundles*4 + 64
	scanned := 0
	err := s.backend.List(ctx, objectPrefix+scopeKey+"/", func(obj objectstore.Object) error {
		scanned++
		if strings.HasSuffix(obj.Key, ".json") {
			entries = append(entries, entry{key: obj.Key, size: obj.Size})
		}
		if scanned >= maxScanEntries {
			return errStopListing
		}
		return nil
	})
	if err != nil {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	count := len(entries)
	var usedBytes int64
	for _, e := range entries {
		usedBytes += e.size
	}
	for count > 0 && (count > MaxScopeBundles || usedBytes > MaxScopeBytes) {
		last := entries[count-1]
		if last.key == ownKey {
			if err := s.backend.Delete(ctx, ownKey); err != nil {
				return fmt.Errorf("%w: concurrent writers exceeded the scope limits, and backing out failed: %v", ErrScopeQuotaExceeded, err)
			}
			return fmt.Errorf("%w: concurrent writers exceeded the scope limits", ErrScopeQuotaExceeded)
		}
		count--
		usedBytes -= last.size
	}
	return nil
}

// errStopListing ends a prefix listing early once a limit is known to be hit.
var errStopListing = errors.New("stop listing")

// scopeUsage counts the bundles stored under one scope and their serialized
// bytes, stopping as soon as either limit is reached.
func (s *ObjectStore) scopeUsage(ctx context.Context, scopeKey string) (int, int64, error) {
	var count int
	var usedBytes int64
	const maxScanEntries = MaxScopeBundles*4 + 64
	scanned := 0
	err := s.backend.List(ctx, objectPrefix+scopeKey+"/", func(obj objectstore.Object) error {
		scanned++
		if !strings.HasSuffix(obj.Key, ".json") {
			return nil
		}
		count++
		if obj.Size > MaxScopeBytes || usedBytes > MaxScopeBytes-obj.Size {
			usedBytes = MaxScopeBytes + 1
		} else {
			usedBytes += obj.Size
		}
		if count >= MaxScopeBundles || usedBytes > MaxScopeBytes || scanned >= maxScanEntries {
			return errStopListing
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopListing) {
		return 0, 0, fmt.Errorf("list scoped bundles: %w", err)
	}
	if scanned >= maxScanEntries {
		// An unexpectedly crowded scope could otherwise make every Put list
		// without bound. Fail closed; the sweeper reclaims stale entries.
		return MaxScopeBundles, MaxScopeBytes, nil
	}
	return count, usedBytes, nil
}

// Get loads a bundle and verifies its digest when digest is provided.
func (s *ObjectStore) Get(ctx context.Context, scope, name, digest string) (*Bundle, error) {
	if s == nil {
		return nil, errors.New("bundle store is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := scopeKey(scope)
	if err != nil {
		return nil, err
	}
	if err := validateBundleName(name); err != nil {
		return nil, err
	}
	data, err := s.backend.Get(ctx, bundleObjectKey(key, name))
	if err != nil {
		if objectstore.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s", errBundleNotFound, name)
		}
		return nil, fmt.Errorf("read bundle %q: %w", name, err)
	}
	var bundle Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("decode bundle %q: %w", name, err)
	}
	if bundle.Name != name {
		return nil, fmt.Errorf("bundle %q has stored name %q", name, bundle.Name)
	}
	if bundle.Scope != strings.TrimSpace(scope) {
		return nil, fmt.Errorf("bundle %q scope mismatch", name)
	}
	if strings.TrimSpace(digest) != "" && bundle.Digest != digest {
		return nil, fmt.Errorf("bundle %q digest mismatch: got %s want %s", name, bundle.Digest, digest)
	}
	return &bundle, nil
}

// Delete removes a bundle once its consumer is done with it. A missing
// bundle is already gone and is treated as success.
func (s *ObjectStore) Delete(ctx context.Context, scope, name, digest string) error {
	if s == nil {
		return errors.New("bundle store is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := scopeKey(scope)
	if err != nil {
		return err
	}
	if err := validateBundleName(name); err != nil {
		return err
	}
	if strings.TrimSpace(digest) != "" {
		if _, err := s.Get(ctx, scope, name, digest); err != nil {
			if errors.Is(err, errBundleNotFound) {
				return nil
			}
			return err
		}
	}
	if err := s.backend.Delete(ctx, bundleObjectKey(key, name)); err != nil && !objectstore.IsNotFound(err) {
		return fmt.Errorf("delete bundle %q: %w", name, err)
	}
	return nil
}

// Sweep removes bundles last written more than maxAge before now. The
// provider does not run it on a clock for this store (a bucket lifecycle
// rule expires orphans instead); it exists for tests and manual cleanup.
func (s *ObjectStore) Sweep(ctx context.Context, now time.Time, maxAge time.Duration) (int, error) {
	if s == nil {
		return 0, errors.New("bundle store is nil")
	}
	cutoff := now.Add(-maxAge)
	var stale []string
	err := s.backend.List(ctx, objectPrefix, func(obj objectstore.Object) error {
		if strings.HasSuffix(obj.Key, ".json") && obj.LastModified.Before(cutoff) {
			stale = append(stale, obj.Key)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("list bundles: %w", err)
	}
	removed := 0
	var errs []error
	for _, key := range stale {
		if err := s.backend.Delete(ctx, key); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", key, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

func bundleObjectKey(scopeKey, name string) string {
	return objectPrefix + scopeKey + "/" + name + ".json"
}
