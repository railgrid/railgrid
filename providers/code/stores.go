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

package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/railgrid/provider-code/commitbundle"
	"github.com/railgrid/provider-code/objectstore"
	"github.com/railgrid/provider-code/snapshotstore"
)

// bundleStore is what the provider needs from its commit bundle store: the
// Store the verbs and controllers use, plus the sweeper the leader runs.
type bundleStore interface {
	commitbundle.Store
	commitbundle.Sweeper
}

// newTransientStores builds the two stores the provider keeps outside kcp,
// on the backend CODE_BUNDLE_STORE selects. "file" keeps both under
// CODE_COMMIT_BUNDLE_DIR and is correct for exactly one replica: the replica
// that accepts an upload and the leader that consumes it must share a disk.
// "s3" keeps both in one bucket every replica reaches, which is what the
// chart requires for replicaCount > 1.
func newTransientStores(ctx context.Context) (bundleStore, snapshotstore.Store, error) {
	kind, err := objectstore.SelectedStore()
	if err != nil {
		return nil, nil, err
	}
	switch kind {
	case objectstore.StoreS3:
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		backend, err := objectstore.NewS3FromEnv(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("s3 object store: %w", err)
		}
		bundles, err := commitbundle.NewObjectStore(backend)
		if err != nil {
			return nil, nil, err
		}
		snapshots, err := snapshotstore.NewObjectStore(backend)
		if err != nil {
			return nil, nil, err
		}
		return bundles, snapshots, nil
	default:
		bundles, err := commitbundle.NewFileStoreFromEnv()
		if err != nil {
			return nil, nil, fmt.Errorf("commit bundle store: %w", err)
		}
		snapshots, err := snapshotstore.NewFileStore(filepath.Join(bundles.Dir(), "git-snapshots"))
		if err != nil {
			return nil, nil, fmt.Errorf("snapshot store: %w", err)
		}
		return bundles, snapshots, nil
	}
}

// runSweepers reclaims what a crash or an abandoned request left behind in
// either store, until ctx is done. Bundles are deleted once consumed and
// snapshots expire on a TTL, so this is a backstop, and with a shared backend
// one replica (the leader) running it is enough.
func runSweepers(ctx context.Context, bundles bundleStore, snapshots snapshotstore.Store) {
	go commitbundle.RunSweeper(ctx, bundles, commitbundle.DefaultSweepInterval, commitbundle.DefaultSweepMaxAge)
	go snapshotstore.RunSweeper(ctx, snapshots, time.Hour, func(removed int, err error) {
		if err != nil {
			log.Printf("snapshot sweeper (%s): %v", snapshots, err)
		}
		if removed > 0 {
			log.Printf("snapshot sweeper (%s): removed %d expired snapshot(s)", snapshots, removed)
		}
	})
}
