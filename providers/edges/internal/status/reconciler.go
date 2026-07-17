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

package status

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// Reconciler aggregates Placement statuses into their parent Workload.
type Reconciler struct {
	mgr mcmanager.Manager
}

// SetupWithManager registers the status aggregator with the multicluster manager.
func SetupWithManager(mgr mcmanager.Manager) error {
	r := &Reconciler{mgr: mgr}
	return mcbuilder.ControllerManagedBy(mgr).
		Named(controllerName).
		For(&edgesv1alpha1.Workload{}).
		Watches(&edgesv1alpha1.Placement{}, mchandler.EnqueueRequestsFromMapFunc(mapPlacementToVW)).
		Complete(r)
}

// Reconcile aggregates Placement statuses into the parent Workload status.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("key", req.NamespacedName, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var vw edgesv1alpha1.Workload
	if err := c.Get(ctx, req.NamespacedName, &vw); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	var placementList edgesv1alpha1.PlacementList
	if err := c.List(ctx, &placementList,
		client.InNamespace(vw.Namespace),
		client.MatchingLabels{edgesv1alpha1.LabelWorkload: vw.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing placements: %w", err)
	}

	vw.Status = AggregateStatus(placementList.Items)
	logger.V(4).Info("Updating Workload status", "readyReplicas", vw.Status.ReadyReplicas, "phase", vw.Status.Phase)
	if err := c.Status().Update(ctx, &vw); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating Workload status: %w", err)
	}

	return ctrl.Result{}, nil
}

// mapPlacementToVW maps a Placement event to the parent Workload.
func mapPlacementToVW(_ context.Context, obj client.Object) []reconcile.Request {
	vwName := obj.GetLabels()[edgesv1alpha1.LabelWorkload]
	if vwName == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: vwName},
	}}
}
