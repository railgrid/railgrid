// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engagement

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	coordinationv1client "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
)

// Edge-engagement sharding: one coordination.k8s.io Lease per engaged edge,
// held in the provider's own kcp workspace (kcp serves Leases in every logical
// cluster). Whichever replica claims an edge's Lease syncs it; the others skip
// it, and a dead replica's edges are taken over within claimTTL.
//
// TODO(provider-contract-remediation §0.3): this is a hand-rolled
// generalization of provider-sdk/leaderelection from one lease to one per
// shard key. It stays here only until that package grows the sharding
// primitive; when it does, delete this file and pass the same TTL and renew
// interval to the SDK. Nothing outside this package should grow a dependency
// on its shape in the meantime.
const (
	// claimNamespace is where the per-edge Leases live. kcp creates the
	// "default" namespace in every logical cluster.
	claimNamespace = "default"
	// claimTTL is how long a claim survives without renewal before a peer may
	// take it over. Handover costs one TTL plus an engage.
	claimTTL = 60 * time.Second
	// renewInterval is how often the owning replica renews its claims and
	// re-asserts the engaged rows. Well inside claimTTL so a slow pass is not
	// mistaken for a dead replica.
	renewInterval = 20 * time.Second
	// leasePrefix namespaces kuery's per-edge Leases inside the provider
	// workspace's default namespace, where the controller lease also lives.
	leasePrefix = "kuery-engage-"
)

// leaseName is the Lease backing one Engagement's claim. It is the Engagement
// name under a fixed prefix — not an independent hash — so the Engagement
// reconciler can map a Lease event back to its Engagement without an index,
// which is what lets lease expiry drive the stale sweep.
func leaseName(engagementName string) string { return leasePrefix + engagementName }

// engagementNameFromLease is leaseName's inverse; ok is false for a Lease that
// is not one of ours (the controller lease, anything else in the workspace).
func engagementNameFromLease(name string) (string, bool) {
	return strings.CutPrefix(name, leasePrefix)
}

// edgeClaims implements try-acquire/renew/release over per-edge Leases.
type edgeClaims struct {
	leases   coordinationv1client.LeaseInterface
	identity string
	now      func() time.Time
}

func newEdgeClaims(cfg *rest.Config) (*edgeClaims, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("claims client: %w", err)
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("claims identity: %w", err)
	}
	return &edgeClaims{
		leases:   cs.CoordinationV1().Leases(claimNamespace),
		identity: fmt.Sprintf("%s_%d", host, os.Getpid()),
		now:      time.Now,
	}, nil
}

// tryAcquire reports whether this replica holds the claim after the attempt:
// it acquires a free or expired Lease, renews one it already holds, and
// declines a fresh foreign one. Update conflicts mean a peer moved first —
// treated as "not held" and settled on the next pass.
func (c *edgeClaims) tryAcquire(ctx context.Context, engagementName string) (bool, error) {
	name := leaseName(engagementName)
	now := metav1.NewMicroTime(c.now())
	lease, err := c.leases.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := c.leases.Create(ctx, &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       ptr.To(c.identity),
				LeaseDurationSeconds: ptr.To(int32(claimTTL.Seconds())),
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return err == nil, err
	}
	if err != nil {
		return false, err
	}

	holder := ptr.Deref(lease.Spec.HolderIdentity, "")
	if holder == c.identity {
		lease.Spec.RenewTime = &now
		if _, err := c.leases.Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}

	if !leaseExpired(lease, c.now()) {
		return false, nil
	}
	lease.Spec.HolderIdentity = ptr.To(c.identity)
	lease.Spec.AcquireTime = &now
	lease.Spec.RenewTime = &now
	lease.Spec.LeaseTransitions = ptr.To(ptr.Deref(lease.Spec.LeaseTransitions, 0) + 1)
	if _, err := c.leases.Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsConflict(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// release deletes the claim if this replica holds it, so a peer takes the edge
// over immediately instead of after claimTTL. Best-effort: an expired claim
// hands over by TTL anyway.
func (c *edgeClaims) release(ctx context.Context, engagementName string) {
	name := leaseName(engagementName)
	lease, err := c.leases.Get(ctx, name, metav1.GetOptions{})
	if err != nil || ptr.Deref(lease.Spec.HolderIdentity, "") != c.identity {
		return
	}
	_ = c.leases.Delete(ctx, name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &lease.UID},
	})
}

// leaseExpired reports whether a Lease's holder has stopped renewing. It reads
// the Lease's own declared duration when it has one, so a claim written by a
// replica running a different build is judged by the terms it was written
// under.
func leaseExpired(lease *coordinationv1.Lease, now time.Time) bool {
	if lease == nil || lease.Spec.RenewTime == nil {
		return true
	}
	ttl := claimTTL
	if seconds := ptr.Deref(lease.Spec.LeaseDurationSeconds, 0); seconds > 0 {
		ttl = time.Duration(seconds) * time.Second
	}
	return now.Sub(lease.Spec.RenewTime.Time) > ttl
}

// leaseHolder is the identity currently holding a Lease, or "" when it has
// none or has expired.
func leaseHolder(lease *coordinationv1.Lease, now time.Time) string {
	if lease == nil || leaseExpired(lease, now) {
		return ""
	}
	return ptr.Deref(lease.Spec.HolderIdentity, "")
}
