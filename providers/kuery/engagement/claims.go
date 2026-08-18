// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engagement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
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
// held in the provider's own kcp workspace (kcp serves Leases in every
// logical cluster — the same mechanism provider-sdk/leaderelection uses,
// generalized from one lease to one per edge). Whichever replica claims an
// edge's Lease syncs it; the others skip it and periodically re-check so a
// dead replica's edges are taken over within claimTTL.
const (
	// claimNamespace is where the per-edge Leases live. kcp creates the
	// "default" namespace in every logical cluster.
	claimNamespace = "default"
	// claimTTL is how long a claim survives without renewal before a peer
	// may take it over. Handover cost is one TTL plus an engage.
	claimTTL = 60 * time.Second
	// renewInterval is the owning replica's reconcile requeue: each pass
	// renews the claim AND re-asserts the kuery cluster row (status=active +
	// tenant label), healing the transient markStale a previous owner's
	// disengage left behind.
	renewInterval = 20 * time.Second
)

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

// claimName derives a Lease name from the path-based store name. Hashed:
// workspace paths contain characters Lease names cannot.
func claimName(storeName string) string {
	sum := sha256.Sum256([]byte(storeName))
	return "kuery-engage-" + hex.EncodeToString(sum[:])[:16]
}

// tryAcquire reports whether this replica holds the edge's claim after the
// attempt: it acquires a free or expired Lease, renews one it already holds,
// and declines a fresh foreign one. Update conflicts mean a peer moved first
// — treated as "not held" and settled on the next reconcile.
func (c *edgeClaims) tryAcquire(ctx context.Context, storeName string) (bool, error) {
	name := claimName(storeName)
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

	expired := lease.Spec.RenewTime == nil ||
		c.now().Sub(lease.Spec.RenewTime.Time) > claimTTL
	if !expired {
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

// release deletes the claim if this replica holds it, so a peer takes the
// edge over immediately instead of after claimTTL. Best-effort: an expired
// claim hands over by TTL anyway.
func (c *edgeClaims) release(ctx context.Context, storeName string) {
	name := claimName(storeName)
	lease, err := c.leases.Get(ctx, name, metav1.GetOptions{})
	if err != nil || ptr.Deref(lease.Spec.HolderIdentity, "") != c.identity {
		return
	}
	_ = c.leases.Delete(ctx, name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &lease.UID},
	})
}
