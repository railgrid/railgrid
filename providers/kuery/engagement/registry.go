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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
	"github.com/railgrid/provider-kuery/index"
)

// Engagement records replaced three columns on kuery's SQL clusters table —
// the tenant label, the active/stale status, and the implied lease owner.
// They live in kuery's OWN workspace as a provider-private CRD (see the
// install package), which buys three things the columns could not:
//
//   - the query path asks an API, not the index, which edges a caller may
//     see, so the SQL store holds only synced objects and is rebuildable;
//   - staleness is a reconcile driven by the per-edge Lease watch instead of
//     a one-minute list-and-sweep ticker;
//   - "who is syncing what" is answerable with kubectl.
//
// The Registry is used on every replica, for both halves of the job: every
// replica serves queries and every query needs the engaged set, and every
// replica engages edges and records what it engaged.

// StoreName and SplitStoreName are the index package's, re-exported so the
// controller's call sites read as one vocabulary.
var (
	StoreName      = index.StoreName
	SplitStoreName = index.SplitStoreName
)

// EngagementName is the Engagement object's name for one edge. The tenant's
// cluster ID stays legible (it is already a DNS-safe kcp identifier) and the
// edge name — which may be up to 253 characters and is not otherwise bounded
// here — is folded into a fixed-width digest, so the result is always a valid
// object name and is always recomputable from the pair.
func EngagementName(cluster, edge string) string {
	digest := sha256.Sum256([]byte(edge))
	return cluster + "-" + hex.EncodeToString(digest[:])[:16]
}

// NewScheme is the scheme the Registry and the Engagement reconciler share:
// kuery's own kinds plus coordination/v1, because the per-edge Leases are what
// the Engagement reconciler watches. Run adds the kcp and core kinds the
// multicluster manager additionally needs on top of this.
func NewScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(kueryv1alpha1.AddToScheme(scheme))
	utilruntime.Must(coordinationv1.AddToScheme(scheme))
	return scheme
}

// Registry reads and writes Engagement records in kuery's own workspace.
//
// Reads answer "which edges may this caller query" on the request path.
// Writes come from every replica, so they are written to be concurrent:
// Ensure tolerates a peer creating the record first and never clobbers its
// status, and SetStatus skips a write that changes nothing and treats a
// conflict as "the other write won, and mine repeats". Which replica may
// claim an edge as ITS own is decided by the edge.s sharding claim, not here.
type Registry struct {
	client client.Client
}

// NewRegistry builds a Registry from the provider kubeconfig's rest.Config,
// whose host already targets kuery's own workspace.
func NewRegistry(cfg *rest.Config) (*Registry, error) {
	if cfg == nil {
		return nil, fmt.Errorf("engagement registry: a provider rest.Config is required")
	}
	scheme := NewScheme()
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("engagement registry: building client: %w", err)
	}
	return &Registry{client: cl}, nil
}

// NewRegistryWithClient wraps an existing client — the controllers pass the
// local manager's, so their reads come from its cache.
func NewRegistryWithClient(cl client.Client) *Registry {
	return &Registry{client: cl}
}

// EngagedEdges lists the bare edge names currently synced for one tenant, by
// its kcp logical-cluster ID. This is the authority the query path scopes by:
// an edge that is not in this list is not queryable, whatever the SQL index
// still holds.
//
// Sorted, so the portal's edge selector and the query scope agree on order.
func (r *Registry) EngagedEdges(ctx context.Context, cluster string) ([]string, error) {
	list := &kueryv1alpha1.EngagementList{}
	if err := r.client.List(ctx, list, client.MatchingLabels{kueryv1alpha1.EngagementClusterLabel: cluster}); err != nil {
		return nil, fmt.Errorf("listing engagements for %s: %w", cluster, err)
	}
	edges := make([]string, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		// The label is an index, not the identity: re-check the spec so a
		// hand-edited label cannot widen a tenant's view.
		if item.Spec.Cluster != cluster || item.Status.Phase != kueryv1alpha1.EngagementPhaseEngaged {
			continue
		}
		edges = append(edges, item.Spec.Edge)
	}
	sort.Strings(edges)
	return edges, nil
}

// List returns every Engagement — what a replica reads when it needs the
// whole picture rather than its own share of it (dropping a workspace, the
// query path answering "which edges may this caller see").
func (r *Registry) List(ctx context.Context) ([]kueryv1alpha1.Engagement, error) {
	list := &kueryv1alpha1.EngagementList{}
	if err := r.client.List(ctx, list); err != nil {
		return nil, fmt.Errorf("listing engagements: %w", err)
	}
	return list.Items, nil
}

// Ensure creates the Engagement for one edge if it is absent and returns it.
// An existing record is returned untouched: its status belongs to whichever
// replica owns the edge, and the caller here may not be that replica.
func (r *Registry) Ensure(ctx context.Context, cluster, edge string) (*kueryv1alpha1.Engagement, error) {
	name := EngagementName(cluster, edge)
	existing := &kueryv1alpha1.Engagement{}
	err := r.client.Get(ctx, client.ObjectKey{Name: name}, existing)
	if err == nil {
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("reading engagement %s: %w", name, err)
	}

	created := &kueryv1alpha1.Engagement{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{kueryv1alpha1.EngagementClusterLabel: cluster},
		},
		Spec: kueryv1alpha1.EngagementSpec{Cluster: cluster, Edge: edge},
	}
	if err := r.client.Create(ctx, created); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// A peer created it between the Get and the Create.
			if err := r.client.Get(ctx, client.ObjectKey{Name: name}, existing); err == nil {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("creating engagement %s: %w", name, err)
	}
	return created, nil
}

// SetStatus writes one Engagement's status. A conflict is not an error: the
// write that won produces another event, and this one's information is a
// heartbeat that will be repeated.
func (r *Registry) SetStatus(ctx context.Context, name string, mutate func(*kueryv1alpha1.EngagementStatus)) error {
	current := &kueryv1alpha1.Engagement{}
	if err := r.client.Get(ctx, client.ObjectKey{Name: name}, current); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("reading engagement %s: %w", name, err)
	}
	before := current.Status
	mutate(&current.Status)
	if statusEqual(before, current.Status) {
		return nil
	}
	if err := r.client.Status().Update(ctx, current); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("updating engagement %s status: %w", name, err)
	}
	return nil
}

// Delete removes one Engagement — the edge is gone for good.
func (r *Registry) Delete(ctx context.Context, name string) error {
	engagement := &kueryv1alpha1.Engagement{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := r.client.Delete(ctx, engagement); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting engagement %s: %w", name, err)
	}
	return nil
}

// statusEqual avoids a write — and therefore a watch event on every replica —
// when a heartbeat changes nothing but the timestamp it was about to stamp.
// LastSeen is compared, so a genuine renewal still lands.
func statusEqual(a, b kueryv1alpha1.EngagementStatus) bool {
	if a.Phase != b.Phase || a.Owner != b.Owner || a.Message != b.Message {
		return false
	}
	switch {
	case a.LastSeen == nil && b.LastSeen == nil:
		return true
	case a.LastSeen == nil || b.LastSeen == nil:
		return false
	default:
		return a.LastSeen.Equal(b.LastSeen)
	}
}
