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

package edgectrl

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/railgrid/provider-edges/internal/agentidentity"
	edgeapi "github.com/railgrid/provider-edges/internal/edgeapi"
	"github.com/railgrid/provider-sdk/identityclient"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// identityFinalizer keeps the edge object around long enough to revoke its
// identity. The hub's sweep would collect an orphaned identity anyway, but
// only within one token TTL of the last refresh; a deleted edge should lose
// its credential now, not within a day.
const identityFinalizer = "edges.railgrid.ai/scoped-identity"

// RBACReconciler keeps each edge's agent identity in existence and its rules
// current, by asking the hub for it.
//
// It writes NOTHING into the tenant workspace any more. The ServiceAccount,
// the shared ClusterRole, the per-edge binding, the legacy token Secret and
// the kubeconfig Secret are all gone: they were this provider minting
// identities for itself, which is review finding M7 and which the scoped
// identity service exists to end. What is left is a request and a finalizer.
//
// The token itself is deliberately not stored anywhere. It is handed to the
// agent over the tunnel at join time and re-minted through the agent-token
// verb; a token at rest in a tenant Secret is exactly what this change
// removes.
type RBACReconciler struct {
	mgr        mcmanager.Manager
	identities *identityclient.Client
	newObj     func() edgeapi.Connectable
	kind       string
	gvr        schema.GroupVersionResource
}

// SetupRBACWithManager registers the identity controller for every connectable
// kind on the multicluster manager. A nil identities client disables it: a dev
// run without a hub still serves the tunnel, and an edge simply never gets a
// credential rather than getting a forged one.
func SetupRBACWithManager(mgr mcmanager.Manager, gvr schema.GroupVersionResource, kind string, newObj func() edgeapi.Connectable, identities *identityclient.Client) error {
	r := &RBACReconciler{mgr: mgr, identities: identities, newObj: newObj, kind: kind, gvr: gvr}
	return mcbuilder.ControllerManagedBy(mgr).
		Named(rbacControllerName + "-" + gvr.Resource).
		For(newObj()).
		Complete(r)
}

// Reconcile ensures the edge has a scoped identity with current rules, and
// revokes it on delete.
//
// The identity is re-asserted on every reconcile rather than created once: the
// hub RECONCILES a record's rules, so a rule this provider stops asking for is
// actually removed from the ClusterRole the hub owns. That was the other half
// of M7 — the old create-if-absent grant could only ever widen.
func (r *RBACReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("edge", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	edge := r.newObj()
	if err := c.Get(ctx, req.NamespacedName, edge); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	owner := agentidentity.Owner(r.gvr, r.kind, edge)
	owner.ClusterID = string(req.ClusterName)

	if !edge.GetDeletionTimestamp().IsZero() {
		if !controllerutil.ContainsFinalizer(edge, identityFinalizer) {
			return ctrl.Result{}, nil
		}
		if r.identities != nil {
			if err := r.identities.Release(ctx, string(req.ClusterName), owner); err != nil {
				// A permanent refusal must not wedge the delete: the hub's own
				// sweep collects the record when it re-probes the owner and
				// finds it gone. Anything retryable is worth retrying, because
				// revoking now is the whole point.
				var hubErr *identityclient.Error
				if !errors.As(err, &hubErr) || !hubErr.Permanent() {
					return ctrl.Result{}, fmt.Errorf("releasing the edge identity: %w", err)
				}
				logger.Error(err, "the hub refused to release this edge's identity; its sweep will collect it within one token TTL")
			}
		}
		controllerutil.RemoveFinalizer(edge, identityFinalizer)
		if err := c.Update(ctx, edge); err != nil {
			return ctrl.Result{}, fmt.Errorf("clearing the identity finalizer: %w", err)
		}
		logger.Info("Edge identity revoked")
		return ctrl.Result{}, nil
	}

	if r.identities == nil {
		logger.V(4).Info("no hub identity client; skipping edge credential provisioning")
		return ctrl.Result{}, nil
	}

	// The finalizer goes on BEFORE the identity exists. The other order leaks:
	// a delete landing between the mint and the finalizer write leaves a live
	// credential with nothing left to revoke it from.
	if !controllerutil.ContainsFinalizer(edge, identityFinalizer) {
		controllerutil.AddFinalizer(edge, identityFinalizer)
		if err := c.Update(ctx, edge); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding the identity finalizer: %w", err)
		}
	}

	// Ensure is idempotent on the owner tuple, so this re-asserts the rules and
	// discards the token it returns: the agent gets its token over the tunnel,
	// and a token this reconciler held would only be a token at rest.
	if _, err := r.identities.Ensure(ctx, identityclient.Request{
		Owner:     owner,
		ClusterID: string(req.ClusterName),
		Rules:     agentidentity.Rules(r.gvr, edge.GetName()),
		TTL:       agentidentity.TokenTTL * time.Second,
	}); err != nil {
		var hubErr *identityclient.Error
		if errors.As(err, &hubErr) && hubErr.Permanent() {
			// The rules are wrong, not the weather. Retrying the identical
			// request cannot help, and hot-looping on it would bury the hub's
			// reason in noise.
			logger.Error(err, "the hub refused this edge's identity; fix the requested rules")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("ensuring the edge identity: %w", err)
	}

	logger.V(4).Info("Edge identity ensured")
	return ctrl.Result{}, nil
}
