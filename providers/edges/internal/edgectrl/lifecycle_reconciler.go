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
	"encoding/json"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
	edgeapi "github.com/railgrid/provider-edges/internal/edgeapi"
	"github.com/railgrid/provider-edges/internal/tunnel"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// ConnManager is the slice of the tunnel ConnManager the lifecycle controller
// needs: a hook that fires when a tunnel is stored on or removed from THIS
// replica, so connect/disconnect reconcile immediately. Liveness itself is
// never read from it — see TunnelRegistry.
type ConnManager interface {
	OnChange(fn func(key string))
}

// TunnelRegistry is the lifecycle reconciler's only liveness input: the
// registry Lease an edge's tunnel owner claims and renews (see
// internal/tunnel.Registry / LeaseObserver). Reading the Lease rather than the
// in-process ConnManager keeps the derived status correct at any replica
// count — every replica sees the same Leases, whichever one holds the socket.
type TunnelRegistry interface {
	Observe(ctx context.Context, key string) (tunnel.TunnelObservation, error)
}

const (
	// lifecycleResync is the safety re-check for an edge with no live Lease.
	// Everything that should change its status arrives as an event (Lease
	// create/update/delete, the local ConnManager hook, agent status patches),
	// so this only guards against a missed watch event.
	lifecycleResync = 5 * time.Minute
	// leaseExpirySlack is added to the Lease expiry when scheduling the
	// staleness re-check, so the reconcile lands after the TTL has genuinely
	// elapsed rather than a few milliseconds before it.
	leaseExpirySlack = 5 * time.Second
	// minLeaseRequeue bounds the staleness re-check from below: a Lease whose
	// renewal is overdue but not yet expired must not hot-loop the reconciler.
	minLeaseRequeue = 10 * time.Second
	// tunnelEventBuffer is the per-kind buffer for local connect/disconnect
	// notifications. A full buffer drops the event — harmless, since the Lease
	// watch delivers the same transition moments later.
	tunnelEventBuffer = 1024
)

// LifecycleReconciler is the SINGLE writer of an edge's connectivity status:
// status.connected, status.phase, status.lastHeartbeatTime and the Registered
// condition (True). It derives them from the tunnel registry Lease and
// nothing else. The tunnel handler stopped writing these on tunnel open/close
// (it records only what it alone observes: joinToken clearing, hostname, SSH
// credentials / host key, URL), so there is exactly one owner per field and
// no write race between the hub handler and the reconciler.
//
// It runs on every replica (the manager is not leader-elected — see
// controller_manager.go) but every replica derives the same values from the
// same Leases and writes only on a diff, so duplicates converge rather than
// flap.
type LifecycleReconciler struct {
	mgr      mcmanager.Manager
	registry TunnelRegistry
	newObj   func() edgeapi.Connectable
	resource string
	now      func() time.Time
}

// SetupLifecycleWithManager registers the lifecycle controller for one
// connectable kind. It reconciles on:
//   - the edge itself (For);
//   - tunnel registry Leases in the provider workspace, mapped to the edge
//     through the conn-key annotation (a second, single-cluster source over
//     the local manager's cache — the provider workspace is not one of the
//     tenant clusters the multicluster provider engages);
//   - local ConnManager store/delete notifications, so connect/disconnect on
//     this replica reconcile without waiting for the Lease watch.
func SetupLifecycleWithManager(mgr mcmanager.Manager, gvr schema.GroupVersionResource, newObj func() edgeapi.Connectable, connManager ConnManager) error {
	local := mgr.GetLocalManager()
	r := &LifecycleReconciler{
		mgr:      mgr,
		registry: tunnel.NewLeaseObserver(local.GetClient()),
		newObj:   newObj,
		resource: gvr.Resource,
		now:      time.Now,
	}

	leaseOnly, err := predicate.LabelSelectorPredicate(metav1.LabelSelector{
		MatchLabels: map[string]string{tunnel.TunnelLeaseLabel: "true"},
	})
	if err != nil {
		return fmt.Errorf("lease predicate: %w", err)
	}
	leases := source.TypedKind[client.Object, mcreconcile.Request](
		local.GetCache(), &coordinationv1.Lease{},
		handler.TypedEnqueueRequestsFromMapFunc[client.Object, mcreconcile.Request](r.mapLease),
		leaseOnly,
	)

	events := make(chan event.TypedGenericEvent[string], tunnelEventBuffer)
	connManager.OnChange(func(key string) {
		select {
		case events <- event.TypedGenericEvent[string]{Object: key}:
		default:
			klog.Background().V(2).Info("dropping tunnel change notification (buffer full); the Lease watch will deliver it", "key", key)
		}
	})
	tunnels := source.TypedChannel[string, mcreconcile.Request](events, handler.TypedFuncs[string, mcreconcile.Request]{
		GenericFunc: func(_ context.Context, e event.TypedGenericEvent[string], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
			if req, ok := r.requestForKey(e.Object); ok {
				q.Add(req)
			}
		},
	})

	return mcbuilder.ControllerManagedBy(mgr).
		Named("lifecycle-" + gvr.Resource).
		For(newObj()).
		WatchesRawSource(leases).
		WatchesRawSource(tunnels).
		Complete(r)
}

// mapLease turns a tunnel registry Lease into the request for the edge whose
// conn key it carries, if that edge is of this controller's kind.
func (r *LifecycleReconciler) mapLease(_ context.Context, obj client.Object) []mcreconcile.Request {
	req, ok := r.requestForKey(obj.GetAnnotations()[tunnel.TunnelLeaseKeyAnnotation])
	if !ok {
		return nil
	}
	return []mcreconcile.Request{req}
}

// requestForKey maps a conn key ("{resource}/{cluster}/{name}") to a request,
// or false when it is malformed or names another kind.
func (r *LifecycleReconciler) requestForKey(key string) (mcreconcile.Request, bool) {
	resource, cluster, name, ok := tunnel.ParseEdgeConnKey(key)
	if !ok || resource != r.resource {
		return mcreconcile.Request{}, false
	}
	return mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: name}},
		ClusterName: multicluster.ClusterName(cluster),
	}, true
}

// Reconcile resolves the tenant cluster and hands the edge to reconcileEdge.
func (r *LifecycleReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	ctx = klog.NewContext(ctx, klog.FromContext(ctx).WithValues("edge", req.Name, "cluster", req.ClusterName))

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
	return r.reconcileEdge(ctx, c, edge, string(req.ClusterName))
}

// reconcileEdge holds the whole decision for one already-fetched edge, against
// a workspace client, so it can be exercised without a multicluster manager.
func (r *LifecycleReconciler) reconcileEdge(ctx context.Context, c client.Client, edge edgeapi.Connectable, cluster string) (ctrl.Result, error) {
	logger := klog.FromContext(ctx)
	name := edge.GetName()

	// Ensure the self-name label so a Workload placement can select this one
	// edge deterministically (the marketplace deploys to a chosen edge).
	if edge.GetLabels()[edgesv1alpha1.LabelName] != name {
		labels := edge.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[edgesv1alpha1.LabelName] = name
		edge.SetLabels(labels)
		if err := c.Update(ctx, edge); err != nil {
			return ctrl.Result{}, fmt.Errorf("stamping name label: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	obs, err := r.registry.Observe(ctx, tunnel.EdgeConnKey(r.resource, cluster, name))
	if err != nil {
		return ctrl.Result{}, err
	}
	now := r.now()
	cs := edge.GetConnectionStatus()

	// Derive the desired connectivity fields from the Lease alone.
	desired := *cs
	var result ctrl.Result
	if obs.Held {
		desired.Connected = true
		desired.Phase = edgeapi.ConnectionPhaseReady
		// status.lastHeartbeatTime is typed at seconds precision; the Lease
		// renewTime is a MicroTime. Truncate so an unchanged renewal never
		// reads as a diff.
		hb := metav1.NewTime(obs.RenewTime.Truncate(time.Second))
		desired.LastHeartbeatTime = &hb
		// Re-check at Lease expiry so a tunnel owner that dies without
		// releasing its Lease (no delete event) is still noticed once the
		// renewals stop.
		result.RequeueAfter = max(obs.ExpiresAt.Sub(now)+leaseExpirySlack, minLeaseRequeue)
	} else {
		desired.Connected = false
		// Only a Ready edge becomes Disconnected; a never-connected edge keeps
		// its provisioning phase (empty / Scheduling) untouched.
		if cs.Phase == edgeapi.ConnectionPhaseReady {
			desired.Phase = edgeapi.ConnectionPhaseDisconnected
		}
		// lastHeartbeatTime keeps the last renewal seen: "last heard from".
		result.RequeueAfter = lifecycleResync
	}

	// Registered=True is a one-way transition set the first time a live tunnel
	// is observed; it is never flipped back here (the token reconciler seeds
	// it False when it issues a bootstrap token). Conditions are a list, so a
	// merge patch cannot address one entry; use an RV-checked Update for this
	// rare transition and the cheap field-level merge patch for everything
	// else.
	if obs.Held && !meta.IsStatusConditionTrue(cs.Conditions, edgeapi.ConnectionConditionRegistered) {
		cs.Connected, cs.Phase, cs.LastHeartbeatTime = desired.Connected, desired.Phase, desired.LastHeartbeatTime
		meta.SetStatusCondition(&cs.Conditions, metav1.Condition{
			Type:               edgeapi.ConnectionConditionRegistered,
			Status:             metav1.ConditionTrue,
			Reason:             "AgentRegistered",
			Message:            "Agent has registered; its tunnel is held by replica " + obs.Holder + ".",
			LastTransitionTime: metav1.NewTime(now),
		})
		if err := c.Status().Update(ctx, edge); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating edge status: %w", err)
		}
		logger.Info("Edge registered and connected", "holder", obs.Holder)
		return result, nil
	}

	patch := connectivityPatch(cs, &desired)
	if patch == nil {
		return result, nil
	}
	if err := c.Status().Patch(ctx, edge, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching edge status: %w", err)
	}
	if desired.Connected != cs.Connected {
		if desired.Connected {
			logger.Info("Edge connected", "holder", obs.Holder)
		} else {
			logger.Info("Edge has no live tunnel lease, marking Disconnected",
				"lastHolder", obs.Holder, "lastRenew", obs.RenewTime)
		}
	}
	return result, nil
}

// connectivityPatch builds a JSON merge patch carrying ONLY the connectivity
// fields this reconciler owns that differ between current and desired, or nil
// when nothing changed. Merge-patching just these fields is what lets the
// agent-side edge_reporter (agentVersion, labels, allowedAddons, ...) and the
// tunnel handler (hostname, sshCredentials, URL, ...) keep patching theirs
// without a resourceVersion fight.
func connectivityPatch(current, desired *edgeapi.ConnectionStatus) []byte {
	fields := map[string]interface{}{}
	if desired.Connected != current.Connected {
		fields["connected"] = desired.Connected
	}
	if desired.Phase != current.Phase {
		fields["phase"] = string(desired.Phase)
	}
	if desired.LastHeartbeatTime != nil &&
		(current.LastHeartbeatTime == nil || !current.LastHeartbeatTime.Time.Equal(desired.LastHeartbeatTime.Time)) {
		fields["lastHeartbeatTime"] = *desired.LastHeartbeatTime
	}
	if len(fields) == 0 {
		return nil
	}
	patch, _ := json.Marshal(map[string]interface{}{"status": fields})
	return patch
}
