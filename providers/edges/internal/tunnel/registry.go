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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	coordinationv1client "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
)

// Registry is the shared tunnel-ownership map that makes the edges provider
// horizontally scalable while every agent keeps exactly ONE control
// connection: when an agent's tunnel terminates on this replica, the replica
// claims the edge's Lease in the provider workspace (kcp serves Leases in
// every logical cluster); any replica can then look up which peer holds a
// tunnel and relay to it (see remote.go) instead of the agent dialing every
// replica.
//
// Two lease families, both in the provider workspace's "default" namespace:
//   - tunnel leases (edge-tunnel-<hash>): holder = the owning replica's
//     "ip:internalPort" relay address; the edge key rides an annotation so
//     List can rebuild the key→addr map.
//   - presence leases (edge-replica-<id>): holder = the same address, keyed
//     by replica ID — how the replica-addressed pickup path is resolved to a
//     peer to forward to.
//
// Owned leases are renewed by the ConnManager sweeper (30s); a lease not
// renewed within RegistryLeaseTTL is dead and its edge unreachable until the
// agent reconnects (to any replica).
type Registry struct {
	leases    coordinationv1client.LeaseInterface
	replicaID string
	selfAddr  string
	now       func() time.Time

	mu    sync.Mutex
	cache map[string]registryCacheEntry
}

type registryCacheEntry struct {
	addr    string
	ok      bool
	fetched time.Time
}

const (
	// RegistryNamespace is where the Leases live. kcp creates the "default"
	// namespace in every logical cluster.
	RegistryNamespace = "default"
	// RegistryLeaseTTL is how stale a lease may be and still count as held.
	// Must comfortably exceed the sweeper's 30s renew cadence.
	RegistryLeaseTTL = 90 * time.Second
	// registryCacheTTL bounds data-path lease reads: edgeproxy/MCP lookups
	// answer from this cache, so a burst of kubectl traffic costs one lease
	// GET per key per interval, not one per request.
	registryCacheTTL = 3 * time.Second

	// TunnelLeaseLabel marks tunnel leases ("true") so a label-selected
	// informer (the edge lifecycle reconciler's Lease watch) sees only them.
	TunnelLeaseLabel = "edges.railgrid.ai/tunnel-registry"
	// TunnelLeaseKeyAnnotation carries the edge conn key
	// ("{resource}/{cluster}/{name}", see EdgeConnKey) on a tunnel lease, since
	// the lease name is a hash of it.
	TunnelLeaseKeyAnnotation = "edges.railgrid.ai/conn-key"
	tunnelLeasePrefix        = "edge-tunnel-"
	presenceLeasePrefix      = "edge-replica-"
)

// NewRegistry builds the registry from the provider's workspace-scoped kcp
// config. replicaID must be pickup-path- and lease-name-safe (SanitizeReplicaID);
// selfAddr is this replica's relay address ("podIP:internalPort").
func NewRegistry(cfg *rest.Config, replicaID, selfAddr string) (*Registry, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("registry client: %w", err)
	}
	return &Registry{
		leases:    cs.CoordinationV1().Leases(RegistryNamespace),
		replicaID: replicaID,
		selfAddr:  selfAddr,
		now:       time.Now,
		cache:     map[string]registryCacheEntry{},
	}, nil
}

// SelfAddr returns this replica's relay address as recorded in claimed leases.
func (r *Registry) SelfAddr() string { return r.selfAddr }

// ReplicaID returns the sanitized replica identity embedded in pickup paths.
func (r *Registry) ReplicaID() string { return r.replicaID }

// SanitizeReplicaID makes an identity (pod name, hostname) safe for both a
// URL path segment and a Lease name suffix.
func SanitizeReplicaID(id string) string {
	id = strings.ToLower(id)
	var b strings.Builder
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" || len(out) > 60 {
		sum := sha256.Sum256([]byte(id))
		out = hex.EncodeToString(sum[:])[:16]
	}
	return out
}

// TunnelLeaseName is the Lease name under which the tunnel for an edge conn
// key ("{resource}/{cluster}/{name}") is claimed: a fixed prefix plus a hash of
// the key, so any component (the lifecycle reconciler included) can address
// the lease for an edge without listing.
func TunnelLeaseName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return tunnelLeasePrefix + hex.EncodeToString(sum[:])[:16]
}

// ClaimTunnel records this replica as the edge's tunnel owner. Unconditional:
// a live agent socket on this replica is ground truth, so an existing claim
// (a previous owner whose agent reconnected here) is overwritten.
func (r *Registry) ClaimTunnel(ctx context.Context, key string) error {
	err := r.upsertLease(ctx, TunnelLeaseName(key), func(lease *coordinationv1.Lease) {
		if lease.Labels == nil {
			lease.Labels = map[string]string{}
		}
		lease.Labels[TunnelLeaseLabel] = "true"
		if lease.Annotations == nil {
			lease.Annotations = map[string]string{}
		}
		lease.Annotations[TunnelLeaseKeyAnnotation] = key
	})
	r.invalidate(key)
	return err
}

// ReleaseTunnel drops the claim if this replica still holds it — the holder
// check keeps a slow disconnect cleanup from erasing a newer claim written by
// the replica the agent reconnected to.
func (r *Registry) ReleaseTunnel(ctx context.Context, key string) {
	name := TunnelLeaseName(key)
	lease, err := r.leases.Get(ctx, name, metav1.GetOptions{})
	if err != nil || ptr.Deref(lease.Spec.HolderIdentity, "") != r.selfAddr {
		return
	}
	_ = r.leases.Delete(ctx, name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &lease.UID},
	})
	r.invalidate(key)
}

// RenewOwned refreshes this replica's presence lease and the tunnel leases
// for the keys it still holds locally. Called from the ConnManager sweeper.
func (r *Registry) RenewOwned(ctx context.Context, localKeys []string) {
	_ = r.upsertLease(ctx, presenceLeasePrefix+r.replicaID, nil)
	for _, key := range localKeys {
		name := TunnelLeaseName(key)
		lease, err := r.leases.Get(ctx, name, metav1.GetOptions{})
		if err != nil || ptr.Deref(lease.Spec.HolderIdentity, "") != r.selfAddr {
			// Missing or foreign (agent reconnected elsewhere while our socket
			// lingers): re-claiming here would fight the live owner. The local
			// dialer dies on its own when the agent side closes.
			continue
		}
		now := metav1.NewMicroTime(r.now())
		lease.Spec.RenewTime = &now
		_, _ = r.leases.Update(ctx, lease, metav1.UpdateOptions{})
	}
}

// LookupTunnel resolves the relay address of the replica holding an edge's
// tunnel. Cached for registryCacheTTL; expired leases report not-held.
func (r *Registry) LookupTunnel(ctx context.Context, key string) (string, bool) {
	r.mu.Lock()
	entry, cached := r.cache[key]
	r.mu.Unlock()
	if cached && r.now().Sub(entry.fetched) < registryCacheTTL {
		return entry.addr, entry.ok
	}
	addr, ok := r.lookupLive(ctx, key)
	r.mu.Lock()
	r.cache[key] = registryCacheEntry{addr: addr, ok: ok, fetched: r.now()}
	r.mu.Unlock()
	return addr, ok
}

func (r *Registry) lookupLive(ctx context.Context, key string) (string, bool) {
	lease, err := r.leases.Get(ctx, TunnelLeaseName(key), metav1.GetOptions{})
	if err != nil {
		return "", false
	}
	if !r.leaseFresh(lease) {
		return "", false
	}
	addr := ptr.Deref(lease.Spec.HolderIdentity, "")
	return addr, addr != ""
}

// ListTunnels returns the fleet-wide key→relay-address map of fresh tunnel
// claims — the cluster-aware Keys() backing MCP tool enumeration.
func (r *Registry) ListTunnels(ctx context.Context) map[string]string {
	list, err := r.leases.List(ctx, metav1.ListOptions{LabelSelector: TunnelLeaseLabel + "=true"})
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(list.Items))
	for i := range list.Items {
		lease := &list.Items[i]
		key := lease.Annotations[TunnelLeaseKeyAnnotation]
		addr := ptr.Deref(lease.Spec.HolderIdentity, "")
		if key == "" || addr == "" || !r.leaseFresh(lease) {
			continue
		}
		out[key] = addr
	}
	return out
}

// ReplicaAddr resolves a replica ID (from a replica-addressed pickup path) to
// its relay address via its presence lease.
func (r *Registry) ReplicaAddr(ctx context.Context, replicaID string) (string, bool) {
	if replicaID == r.replicaID {
		return r.selfAddr, true
	}
	lease, err := r.leases.Get(ctx, presenceLeasePrefix+replicaID, metav1.GetOptions{})
	if err != nil || !r.leaseFresh(lease) {
		return "", false
	}
	addr := ptr.Deref(lease.Spec.HolderIdentity, "")
	return addr, addr != ""
}

func (r *Registry) leaseFresh(lease *coordinationv1.Lease) bool {
	return leaseFreshAt(lease, r.now())
}

// leaseFreshAt is the single freshness rule for registry leases: renewed
// within RegistryLeaseTTL of now. The data path (Registry) and the status
// path (LeaseObserver) share it so they never disagree about liveness.
func leaseFreshAt(lease *coordinationv1.Lease, now time.Time) bool {
	return lease.Spec.RenewTime != nil && now.Sub(lease.Spec.RenewTime.Time) <= RegistryLeaseTTL
}

func (r *Registry) invalidate(key string) {
	r.mu.Lock()
	delete(r.cache, key)
	r.mu.Unlock()
}

// upsertLease creates or takes over a lease with holder=selfAddr, applying
// decorate (labels/annotations) on the object before writing. One conflict
// retry: the loser of a race re-reads and overwrites — last live socket wins.
func (r *Registry) upsertLease(ctx context.Context, name string, decorate func(*coordinationv1.Lease)) error {
	for attempt := 0; attempt < 2; attempt++ {
		now := metav1.NewMicroTime(r.now())
		lease, err := r.leases.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			lease = &coordinationv1.Lease{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: coordinationv1.LeaseSpec{
					HolderIdentity:       ptr.To(r.selfAddr),
					LeaseDurationSeconds: ptr.To(int32(RegistryLeaseTTL.Seconds())),
					AcquireTime:          &now,
					RenewTime:            &now,
				},
			}
			if decorate != nil {
				decorate(lease)
			}
			if _, err := r.leases.Create(ctx, lease, metav1.CreateOptions{}); err != nil {
				if apierrors.IsAlreadyExists(err) {
					continue
				}
				return err
			}
			return nil
		}
		if err != nil {
			return err
		}
		if ptr.Deref(lease.Spec.HolderIdentity, "") != r.selfAddr {
			lease.Spec.AcquireTime = &now
			lease.Spec.LeaseTransitions = ptr.To(ptr.Deref(lease.Spec.LeaseTransitions, 0) + 1)
		}
		lease.Spec.HolderIdentity = ptr.To(r.selfAddr)
		lease.Spec.LeaseDurationSeconds = ptr.To(int32(RegistryLeaseTTL.Seconds()))
		lease.Spec.RenewTime = &now
		if decorate != nil {
			decorate(lease)
		}
		if _, err := r.leases.Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("lease %s: lost two consecutive update races", name)
}
