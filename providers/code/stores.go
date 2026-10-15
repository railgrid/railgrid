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
	"path/filepath"
	"time"

	"k8s.io/klog/v2"

	"github.com/railgrid/provider-code/commitbundle"
	"github.com/railgrid/provider-code/objectstore"
	"github.com/railgrid/provider-code/snapshotstore"
)

// bundleStore is what the provider needs from its commit bundle store: the
// Store the verbs and controllers use, plus the sweeper the file backend
// runs.
type bundleStore interface {
	commitbundle.Store
	commitbundle.Sweeper
}

// transientStores are the two stores the provider keeps outside kcp, and
// which backend they run on.
type transientStores struct {
	bundles   bundleStore
	snapshots snapshotstore.Store
	kind      string
}

// newTransientStores builds the stores on the backend CODE_BUNDLE_STORE
// selects. "file" keeps both under CODE_COMMIT_BUNDLE_DIR and is correct for
// exactly one replica: the replica that accepts an upload and the leader
// that consumes it must share a disk. "s3" keeps both in one bucket every
// replica reaches, which is what the chart requires for replicaCount > 1.
func newTransientStores(ctx context.Context) (transientStores, error) {
	kind, err := objectstore.SelectedStore()
	if err != nil {
		return transientStores{}, err
	}
	switch kind {
	case objectstore.StoreS3:
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		backend, err := objectstore.NewS3FromEnv(ctx)
		if err != nil {
			return transientStores{}, fmt.Errorf("s3 object store: %w", err)
		}
		bundles, err := commitbundle.NewObjectStore(backend)
		if err != nil {
			return transientStores{}, err
		}
		snapshots, err := snapshotstore.NewObjectStore(backend)
		if err != nil {
			return transientStores{}, err
		}
		return transientStores{bundles: bundles, snapshots: snapshots, kind: kind}, nil
	default:
		bundles, err := commitbundle.NewFileStoreFromEnv()
		if err != nil {
			return transientStores{}, fmt.Errorf("commit bundle store: %w", err)
		}
		snapshots, err := snapshotstore.NewFileStore(filepath.Join(bundles.Dir(), "git-snapshots"))
		if err != nil {
			return transientStores{}, fmt.Errorf("snapshot store: %w", err)
		}
		return transientStores{bundles: bundles, snapshots: snapshots, kind: kind}, nil
	}
}

// runSweepers reclaims what a crash or an abandoned request left behind,
// until ctx is done. It is the §5.8 carve-out docs/code-provider-architecture.md
// argues: the artifacts are not kcp objects, a CR is the authority for each,
// and nothing is lost if one is swept. It runs only for the file backend,
// where nothing else can expire a directory; an S3 bucket gets a lifecycle
// rule instead (see the chart README), so no replica runs a sweep over it.
func (s transientStores) runSweepers(ctx context.Context) {
	if s.kind != objectstore.StoreFile {
		return
	}
	go commitbundle.RunSweeper(ctx, s.bundles, commitbundle.DefaultSweepInterval, commitbundle.DefaultSweepMaxAge)
	logger := klog.FromContext(ctx).WithName("snapshotstore-sweeper")
	go snapshotstore.RunSweeper(ctx, s.snapshots, snapshotstore.DefaultSweepInterval, func(removed int, err error) {
		if err != nil {
			logger.Error(err, "sweep expired snapshots", "store", s.snapshots.String())
		}
		if removed > 0 {
			logger.Info("removed expired snapshots", "count", removed)
		}
	})
}
