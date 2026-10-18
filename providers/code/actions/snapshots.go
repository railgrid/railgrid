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

package actions

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/snapshotstore"
	"github.com/railgrid/provider-sdk/dataplane"
)

// snapshotScope names who staged a snapshot inside a tenant: the repository,
// the connection and the caller (a ServiceAccount also carries the logical
// cluster it lives in, since two providers' accounts can spell their names
// identically). The store hashes it; only the same caller on the same
// repository and connection reads the snapshot back through loadSnapshot.
func (s *Server) snapshotScope(caller dataplane.ProxiedIdentity, in Input) string {
	return in.RepositoryUID + "\x00" + in.ConnectionUID + "\x00" + caller.User + "\x00" + caller.ClusterName()
}

// stage serves the stage-snapshot verb: it stores the serialized snapshot
// and answers with its content reference, which the caller then puts on the
// PullRequest's desired head for the controller to find.
func (s *Server) stage(ctx context.Context, caller dataplane.ProxiedIdentity, cluster string, in Input) (any, error) {
	if in.Snapshot == nil || (len(in.Snapshot.Bundle) == 0 && len(in.Snapshot.Attachments) == 0) || len(in.Snapshot.Bundle) > 25<<20 {
		return nil, errors.New("invalid snapshot upload")
	}
	if s.Snapshots == nil {
		return nil, errors.New("snapshot store is unavailable")
	}
	if err := backend.ValidateAttachments(in.Snapshot.Attachments); err != nil {
		return nil, err
	}
	data, err := json.Marshal(in.Snapshot)
	if err != nil || len(data) > MaxInputBytes {
		return nil, errors.New("snapshot upload exceeds limit")
	}
	ref, err := s.Snapshots.Stage(ctx, cluster, s.snapshotScope(caller, in), data)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bundleRef": ref}, nil
}

// loadSnapshot reads back a snapshot this caller staged for this repository
// and connection.
func (s *Server) loadSnapshot(ctx context.Context, caller dataplane.ProxiedIdentity, cluster string, in Input) (backend.Snapshot, error) {
	var snapshot backend.Snapshot
	if !snapshotstore.ValidRef(in.BundleRef) {
		return snapshot, snapshotstore.ErrInvalidRef
	}
	if s.Snapshots == nil {
		return snapshot, errors.New("snapshot store is unavailable")
	}
	data, err := s.Snapshots.Load(ctx, cluster, s.snapshotScope(caller, in), in.BundleRef)
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

// FindStaged finds a snapshot the stage-snapshot verb stored for this
// cluster, whichever caller staged it: the ref is the content's digest, so
// the same bytes are the same snapshot wherever they were put. It is how a
// controller reconciling a PullRequest's desired head reaches the bundle the
// coordinator staged for it.
func FindStaged(ctx context.Context, store snapshotstore.Store, cluster, ref string) (backend.Snapshot, error) {
	var snapshot backend.Snapshot
	if store == nil {
		return snapshot, errors.New("snapshot store is unavailable")
	}
	data, err := store.Find(ctx, cluster, ref)
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return backend.Snapshot{}, err
	}
	return snapshot, nil
}
