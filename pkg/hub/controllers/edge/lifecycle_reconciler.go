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

package edge

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// LifecycleReconciler monitors Edge connectivity and marks stale edges as Disconnected.
type LifecycleReconciler struct {
	mgr mcmanager.Manager
}

// SetupLifecycleWithManager registers the edge lifecycle controller with the multicluster manager.
func SetupLifecycleWithManager(mgr mcmanager.Manager) error {
	r := &LifecycleReconciler{mgr: mgr}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("edge-lifecycle").
		For(&kedgev1alpha1.Edge{}).
		Complete(r)
}

// Reconcile checks an Edge's connected state and transitions its phase accordingly.
func (r *LifecycleReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("edge", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var edge kedgev1alpha1.Edge
	if err := c.Get(ctx, req.NamespacedName, &edge); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// If edge is not connected but phase is still Ready, mark it Disconnected.
	if !edge.Status.Connected && edge.Status.Phase == kedgev1alpha1.EdgePhaseReady {
		logger.Info("Edge no longer connected, marking Disconnected")
		edge.Status.Phase = kedgev1alpha1.EdgePhaseDisconnected
		if err := c.Status().Update(ctx, &edge); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating edge status: %w", err)
		}
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}
