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

package tunnel

import (
	"context"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TunnelObservation is what the registry Lease says about one edge's tunnel.
// It is the ONLY liveness input to the edge lifecycle reconciler: the tunnel
// handler no longer writes connectivity to Edge status, it only claims and
// renews the lease, and the reconciler derives status from it.
type TunnelObservation struct {
	// Held is true when a lease exists, names a holder, and was renewed within
	// RegistryLeaseTTL — i.e. some replica terminates a live tunnel for the edge.
	Held bool
	// Holder is the lease holder identity: the relay address of the replica
	// that terminates the tunnel. Set whenever a lease exists, fresh or not.
	Holder string
	// RenewTime is the lease's last renewal (zero when there is no lease). While
	// Held it is the edge's liveness timestamp (status.lastHeartbeatTime).
	RenewTime time.Time
	// ExpiresAt is RenewTime + RegistryLeaseTTL: when a lease that stops being
	// renewed stops counting as held. Zero when there is no lease.
	ExpiresAt time.Time
}

// LeaseObserver reads tunnel liveness from the registry Leases through a
// controller-runtime reader over the provider workspace — normally the local
// manager's cache-backed client, so reconciles triggered by a Lease watch read
// the same informer that fired them. It is correct at any replica count: every
// replica reads the same leases, whichever one terminates the tunnel.
type LeaseObserver struct {
	reader client.Reader
	now    func() time.Time
}

// NewLeaseObserver returns an observer reading Leases via reader, which must
// address the provider workspace (where Registry writes them).
func NewLeaseObserver(reader client.Reader) *LeaseObserver {
	return &LeaseObserver{reader: reader, now: time.Now}
}

// Observe returns the lease-derived liveness of the tunnel for key. A missing
// lease is a valid observation (not held), not an error.
func (o *LeaseObserver) Observe(ctx context.Context, key string) (TunnelObservation, error) {
	lease := &coordinationv1.Lease{}
	err := o.reader.Get(ctx, client.ObjectKey{Namespace: RegistryNamespace, Name: TunnelLeaseName(key)}, lease)
	if apierrors.IsNotFound(err) {
		return TunnelObservation{}, nil
	}
	if err != nil {
		return TunnelObservation{}, fmt.Errorf("reading tunnel lease for %s: %w", key, err)
	}
	obs := TunnelObservation{Holder: ptr.Deref(lease.Spec.HolderIdentity, "")}
	if lease.Spec.RenewTime != nil {
		obs.RenewTime = lease.Spec.RenewTime.Time
		obs.ExpiresAt = obs.RenewTime.Add(RegistryLeaseTTL)
	}
	obs.Held = obs.Holder != "" && leaseFreshAt(lease, o.now())
	return obs, nil
}
