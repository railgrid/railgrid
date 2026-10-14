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
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileStore keeps snapshots under one directory as
// <dir>/<tenant hash>/<scope hash>/<ref>.json. It serves one replica only.
type FileStore struct {
	dir string
	mu  sync.Mutex
}

var _ Store = (*FileStore)(nil)

// NewFileStore roots a store at dir, creating it if needed.
func NewFileStore(dir string) (*FileStore, error) {
	if dir == "" {
		return nil, errors.New("snapshot directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create snapshot directory: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

// String names the store for logs.
func (s *FileStore) String() string { return "file://" + s.dir }

func (s *FileStore) tenantDir(cluster string) string {
	return filepath.Join(s.dir, hashKey(cluster))
}

func (s *FileStore) scopeDir(cluster, scope string) string {
	return filepath.Join(s.tenantDir(cluster), hashKey(scope))
}

// Stage writes data under cluster and scope, reclaiming the tenant's expired
// snapshots first and refusing once the tenant holds its quota.
func (s *FileStore) Stage(ctx context.Context, cluster, scope string, data []byte) (string, error) {
	if err := validate(cluster, scope); err != nil {
		return "", err
	}
	if len(data) == 0 || len(data) > MaxBytes {
		return "", ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ref := Ref(data)
	dir := s.scopeDir(cluster, scope)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	count, size, err := s.tenantUsage(s.tenantDir(cluster), time.Now())
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, ref+".json")
	if _, err := os.Stat(path); err == nil {
		return ref, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if count >= MaxPerTenant || size+int64(len(data)) > MaxBytesPerTenant {
		return "", ErrQuotaExceeded
	}
	temp, err := os.CreateTemp(dir, "upload-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return "", err
	}
	return ref, nil
}

// tenantUsage counts a tenant's live snapshots, removing expired ones as it
// goes. The tenant directory holds only files this store wrote.
func (s *FileStore) tenantUsage(tenantRoot string, now time.Time) (int, int64, error) {
	count := 0
	var size int64
	err := filepath.WalkDir(tenantRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("invalid snapshot storage entry")
		}
		if now.Sub(info.ModTime()) > TTL {
			return os.Remove(path)
		}
		count++
		size += info.Size()
		return nil
	})
	return count, size, err
}

// Load reads the snapshot at exactly cluster/scope/ref.
func (s *FileStore) Load(ctx context.Context, cluster, scope, ref string) ([]byte, error) {
	if err := validate(cluster, scope); err != nil {
		return nil, err
	}
	if !ValidRef(ref) {
		return nil, ErrInvalidRef
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return readLive(filepath.Join(s.scopeDir(cluster, scope), ref+".json"), ref, time.Now())
}

// Find reads the snapshot ref from whichever of cluster's scopes staged it.
func (s *FileStore) Find(ctx context.Context, cluster, ref string) ([]byte, error) {
	if cluster == "" {
		return nil, errors.New("snapshot cluster is required")
	}
	if !ValidRef(ref) {
		return nil, ErrInvalidRef
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tenantRoot := s.tenantDir(cluster)
	entries, err := os.ReadDir(tenantRoot)
	if err != nil {
		return nil, ErrNotFound
	}
	now := time.Now()
	var lastErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := readLive(filepath.Join(tenantRoot, entry.Name(), ref+".json"), ref, now)
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

// readLive reads one snapshot file, refusing one that is expired, oversized
// or whose bytes do not hash to ref.
func readLive(path, ref string, now time.Time) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrNotFound
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) > TTL || info.Size() > MaxBytes {
		return nil, ErrNotFound
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if err := verify(data, ref); err != nil {
		return nil, err
	}
	return data, nil
}

// Sweep removes every snapshot that expired before now.
func (s *FileStore) Sweep(ctx context.Context, now time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	err := filepath.WalkDir(s.dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) <= TTL {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		removed++
		return nil
	})
	return removed, err
}
