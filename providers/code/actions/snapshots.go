// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-sdk/dataplane"
)

var snapshotDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var snapshotMu sync.Mutex

const snapshotTTL = time.Hour

// Staging is a bounded artifact upload endpoint, separate from the small action
// inputs. Its opaque handle is scoped to tenant, resource identities and
// caller. The caller is the identity kcp stamped on the request — there is no
// bearer on an action — qualified by the logical cluster a ServiceAccount
// lives in, because two providers' ServiceAccounts spell their names
// identically and must not share a staging directory.
func (s *Server) snapshotScope(caller dataplane.ProxiedIdentity, cluster string, in Input) string {
	root := s.SnapshotDir
	if root == "" {
		root = filepath.Join(os.TempDir(), "railgrid-code-snapshots")
	}
	tenant := sha256.Sum256([]byte(cluster))
	scope := sha256.Sum256([]byte(in.RepositoryUID + "\x00" + in.ConnectionUID + "\x00" + caller.User + "\x00" + caller.ClusterName()))
	return filepath.Join(root, hex.EncodeToString(tenant[:]), hex.EncodeToString(scope[:]))
}
func (s *Server) stage(caller dataplane.ProxiedIdentity, cluster string, in Input) (any, error) {
	if in.Snapshot == nil || len(in.Snapshot.Bundle) == 0 || len(in.Snapshot.Bundle) > 25<<20 {
		return nil, errors.New("invalid snapshot upload")
	}
	data, err := json.Marshal(in.Snapshot)
	if err != nil || len(data) > MaxInputBytes {
		return nil, errors.New("snapshot upload exceeds limit")
	}
	digest := sha256.Sum256(data)
	ref := hex.EncodeToString(digest[:])
	scope := s.snapshotScope(caller, cluster, in)
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	if err = os.MkdirAll(scope, 0700); err != nil {
		return nil, err
	}
	tenantRoot := filepath.Dir(scope)
	count := 0
	var size int64
	// The private tenant directory contains only scoped snapshots written here.
	err = filepath.WalkDir(tenantRoot, func(path string, entry os.DirEntry, walkErr error) error {
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
		if time.Since(info.ModTime()) > snapshotTTL {
			return os.Remove(path)
		}
		count++
		size += info.Size()
		return nil
	})
	if err != nil {
		return nil, err
	}
	path := filepath.Join(scope, ref+".json")
	if _, err = os.Stat(path); err == nil {
		return map[string]any{"bundleRef": ref}, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if count >= 16 || size+int64(len(data)) > 256<<20 {
		return nil, errors.New("tenant snapshot quota reached")
	}
	temp, err := os.CreateTemp(scope, "upload-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err = temp.Write(data); err != nil {
		_ = temp.Close()
		return nil, err
	}
	if err = temp.Close(); err != nil {
		return nil, err
	}
	if err = os.Rename(temp.Name(), path); err != nil {
		return nil, err
	}
	return map[string]any{"bundleRef": ref}, nil
}
func (s *Server) loadSnapshot(caller dataplane.ProxiedIdentity, cluster string, in Input) (backend.Snapshot, error) {
	var snapshot backend.Snapshot
	if !snapshotDigest.MatchString(in.BundleRef) {
		return snapshot, errors.New("invalid snapshot reference")
	}
	file, err := os.Open(filepath.Join(s.snapshotScope(caller, cluster, in), in.BundleRef+".json"))
	if err != nil {
		return snapshot, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) > snapshotTTL || info.Size() > MaxInputBytes {
		return snapshot, errors.New("snapshot unavailable or expired")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxInputBytes+1))
	if err != nil || len(data) > MaxInputBytes {
		return snapshot, errors.New("snapshot exceeds limit")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != in.BundleRef {
		return snapshot, errors.New("snapshot digest mismatch")
	}
	if err = json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}
