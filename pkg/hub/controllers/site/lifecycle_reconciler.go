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

package site

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

// LifecycleReconciler monitors Site heartbeats and marks stale sites as Disconnected.
type LifecycleReconciler struct {
	mgr mcmanager.Manager
}

// SetupLifecycleWithManager registers the site lifecycle controller with the multicluster manager.
func SetupLifecycleWithManager(mgr mcmanager.Manager) error {
	r := &LifecycleReconciler{mgr: mgr}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("site-lifecycle").
		For(&kedgev1alpha1.Site{}).
		Complete(r)
}

// Reconcile checks a Site's heartbeat and marks it disconnected if stale.
func (r *LifecycleReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("site", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var site kedgev1alpha1.Site
	if err := c.Get(ctx, req.NamespacedName, &site); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if site.Status.LastHeartbeatTime == nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	elapsed := time.Since(site.Status.LastHeartbeatTime.Time)
	if elapsed > HeartbeatTimeout && site.Status.Phase != kedgev1alpha1.SitePhaseDisconnected {
		logger.Info("Site heartbeat stale, marking disconnected", "elapsed", elapsed)
		site.Status.Phase = kedgev1alpha1.SitePhaseDisconnected
		site.Status.TunnelConnected = false
		if err := c.Status().Update(ctx, &site); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating site status: %w", err)
		}
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}
