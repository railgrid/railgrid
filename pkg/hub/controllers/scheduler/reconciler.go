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

package scheduler

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// Reconciler implements the multicluster scheduler reconciler.
type Reconciler struct {
	mgr mcmanager.Manager
}

// SetupWithManager registers the scheduler controller with the multicluster manager.
func SetupWithManager(mgr mcmanager.Manager) error {
	r := &Reconciler{mgr: mgr}
	klog.Info("Registering VirtualWorkload scheduler controller")
	return mcbuilder.ControllerManagedBy(mgr).
		Named(controllerName).
		For(&kedgev1alpha1.VirtualWorkload{}).
		Watches(&kedgev1alpha1.Site{}, mchandler.EnqueueRequestsFromMapFunc(r.mapSiteToVirtualWorkloads)).
		Complete(r)
}

// Reconcile handles a single VirtualWorkload reconciliation across workspaces.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("key", req.NamespacedName, "cluster", req.ClusterName)
	logger.V(4).Info("Reconciling VirtualWorkload")

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		logger.Error(err, "Failed to get cluster", "clusterName", req.ClusterName)
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	// Get VirtualWorkload
	var vw kedgev1alpha1.VirtualWorkload
	if err := c.Get(ctx, req.NamespacedName, &vw); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get VirtualWorkload")
		return ctrl.Result{}, err
	}

	// List all Sites in this workspace
	var siteList kedgev1alpha1.SiteList
	if err := c.List(ctx, &siteList); err != nil {
		logger.Error(err, "Failed to list Sites")
		return ctrl.Result{}, fmt.Errorf("listing sites: %w", err)
	}

	// Match and select sites
	matched, err := MatchSites(siteList.Items, vw.Spec.Placement)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("matching sites: %w", err)
	}
	selected := SelectSites(matched, vw.Spec.Placement.Strategy)
	logger.V(4).Info("Scheduling", "sites", len(siteList.Items), "matched", len(matched), "selected", len(selected))

	// List existing placements for this VW
	var placementList kedgev1alpha1.PlacementList
	if err := c.List(ctx, &placementList,
		client.InNamespace(vw.Namespace),
		client.MatchingLabels{"kedge.faros.sh/virtualworkload": vw.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing placements: %w", err)
	}

	// Build desired site set
	desiredSites := make(map[string]bool)
	for _, site := range selected {
		desiredSites[site.Name] = true
	}

	// Delete placements for sites no longer selected
	for i := range placementList.Items {
		p := &placementList.Items[i]
		if !desiredSites[p.Spec.SiteName] {
			logger.Info("Deleting stale placement", "placement", p.Name, "site", p.Spec.SiteName)
			if err := c.Delete(ctx, p); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to delete placement", "name", p.Name)
			}
		}
	}

	// Build existing site set
	existingSites := make(map[string]bool)
	for _, p := range placementList.Items {
		existingSites[p.Spec.SiteName] = true
	}

	// Create placements for newly selected sites
	for _, site := range selected {
		if existingSites[site.Name] {
			continue
		}

		placement := &kedgev1alpha1.Placement{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s", vw.Name, site.Name),
				Namespace: vw.Namespace,
				Labels: map[string]string{
					"kedge.faros.sh/virtualworkload": vw.Name,
					"kedge.faros.sh/site":            site.Name,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: kedgev1alpha1.SchemeGroupVersion.String(),
						Kind:       "VirtualWorkload",
						Name:       vw.Name,
						UID:        vw.UID,
					},
				},
			},
			Spec: kedgev1alpha1.PlacementObjSpec{
				WorkloadRef: corev1.ObjectReference{
					APIVersion: kedgev1alpha1.SchemeGroupVersion.String(),
					Kind:       "VirtualWorkload",
					Name:       vw.Name,
					Namespace:  vw.Namespace,
					UID:        vw.UID,
				},
				SiteName: site.Name,
				Replicas: vw.Spec.Replicas,
			},
		}

		logger.Info("Creating placement", "placement", placement.Name, "site", site.Name)
		if err := c.Create(ctx, placement); err != nil && !apierrors.IsAlreadyExists(err) {
			logger.Error(err, "Failed to create placement", "name", placement.Name)
		}
	}

	// Requeue periodically so site reconnects are picked up even if the watch
	// event is missed (e.g. status-only changes may not always fire the mapper).
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// mapSiteToVirtualWorkloads re-enqueues all VirtualWorkloads in the same
// workspace whenever a Site changes.
func (r *Reconciler) mapSiteToVirtualWorkloads(ctx context.Context, obj client.Object) []reconcile.Request {
	// Prefer the cluster name from the multicluster-runtime context (canonical),
	// fall back to the kcp annotation if the context value is absent.
	clusterKey, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		clusterKey = obj.GetAnnotations()["kcp.io/cluster"]
	}
	klog.V(2).InfoS("mapSiteToVirtualWorkloads", "site", obj.GetName(), "cluster", clusterKey)
	cl, err := r.mgr.GetCluster(ctx, clusterKey)
	if err != nil {
		klog.V(2).InfoS("mapSiteToVirtualWorkloads: GetCluster failed", "cluster", clusterKey, "err", err)
		return nil
	}
	var vwList kedgev1alpha1.VirtualWorkloadList
	if err := cl.GetClient().List(ctx, &vwList); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(vwList.Items))
	for _, vw := range vwList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: vw.Namespace,
				Name:      vw.Name,
			},
		})
	}
	return requests
}
