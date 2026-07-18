/*
Copyright 2026 The Faros Authors.

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
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
	edgeapi "github.com/faroshq/provider-edges/internal/edgeapi"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// ConnManager is the minimal interface the controller needs to verify tunnel liveness.
type ConnManager interface {
	HasConnection(key string) bool
}

// connKey must match edgeConnKey in the tunnel package (agent_proxy_builder_v2.go):
// "{resource}/{cluster}/{name}".
func connKey(resource, cluster, name string) string {
	return resource + "/" + cluster + "/" + name
}

// LifecycleReconciler monitors connectivity and marks stale edges as Disconnected.
type LifecycleReconciler struct {
	mgr         mcmanager.Manager
	connManager ConnManager
	newObj      func() edgeapi.Connectable
	resource    string
}

// SetupLifecycleWithManager registers the lifecycle controller for every
// connectable kind on the multicluster manager.
func SetupLifecycleWithManager(mgr mcmanager.Manager, gvr schema.GroupVersionResource, newObj func() edgeapi.Connectable, connManager ConnManager) error {
	r := &LifecycleReconciler{mgr: mgr, connManager: connManager, newObj: newObj, resource: gvr.Resource}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("lifecycle-" + gvr.Resource).
		For(newObj()).
		Complete(r)
}

// staleHeartbeatThreshold is how long an Edge can go without a hub-stamped
// heartbeat before this reconciler considers the tunnel stale even when
// ConnManager still reports a live connection. Sized as 3× the agent-proxy
// heartbeat interval (30s) — see edgeHeartbeatInterval in pkg/virtual/builder.
const staleHeartbeatThreshold = 90 * time.Second

// Reconcile reconciles status.connected/phase against the in-process tunnel registry.
//
// status.connected is only flipped to true by the agent-proxy handler when a
// revdial tunnel is established, and is supposed to be flipped to false when
// that tunnel closes. On hub cold restart (in-memory ConnManager is empty
// while etcd still says connected=true), or when an agent dies ungracefully
// and socket-close detection races reconnect, the status drifts. This
// reconciler corrects the drift by cross-checking ConnManager — which already
// fast-path-evicts closed dialers on Load and sweeps every 30s — and by
// flipping edges whose hub-stamped lastHeartbeatTime has gone stale.
func (r *LifecycleReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
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

	// Ensure the self-name label so a Workload placement can select this one
	// edge deterministically (the marketplace deploys to a chosen edge).
	if edge.GetLabels()[edgesv1alpha1.LabelName] != req.Name {
		labels := edge.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[edgesv1alpha1.LabelName] = req.Name
		edge.SetLabels(labels)
		if err := c.Update(ctx, edge); err != nil {
			return ctrl.Result{}, fmt.Errorf("stamping name label: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	cs := edge.GetConnectionStatus()

	hasTunnel := r.connManager.HasConnection(connKey(r.resource, string(req.ClusterName), req.Name))
	heartbeatStale := cs.LastHeartbeatTime != nil &&
		time.Since(cs.LastHeartbeatTime.Time) > staleHeartbeatThreshold

	switch {
	case cs.Connected && !hasTunnel:
		logger.Info("Edge has no live tunnel, marking Disconnected")
		cs.Connected = false
		cs.Phase = edgeapi.ConnectionPhaseDisconnected
		if err := c.Status().Update(ctx, edge); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating edge status: %w", err)
		}
	case cs.Connected && heartbeatStale:
		// connManager still reports a tunnel, but the hub-side heartbeat
		// goroutine hasn't stamped lastHeartbeatTime within the threshold.
		// That means revdial pongs have stopped flowing — treat the edge as
		// disconnected even though the dialer entry hasn't been evicted yet.
		logger.Info("Edge heartbeat stale, marking Disconnected",
			"lastHeartbeat", cs.LastHeartbeatTime.Time,
			"age", time.Since(cs.LastHeartbeatTime.Time).Round(time.Second))
		cs.Connected = false
		cs.Phase = edgeapi.ConnectionPhaseDisconnected
		if err := c.Status().Update(ctx, edge); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating edge status: %w", err)
		}
	case !cs.Connected && cs.Phase == edgeapi.ConnectionPhaseReady:
		logger.Info("Edge no longer connected, marking Disconnected")
		cs.Phase = edgeapi.ConnectionPhaseDisconnected
		if err := c.Status().Update(ctx, edge); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating edge status: %w", err)
		}
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}
