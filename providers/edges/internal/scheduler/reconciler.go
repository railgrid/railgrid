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
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
	"github.com/faroshq/provider-edges/internal/render"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// Reconciler fans a Workload out into Placements across the tenant's
// matching KubernetesCluster edges.
type Reconciler struct {
	mgr mcmanager.Manager
}

// SetupWithManager registers the Workload scheduler with the multicluster
// manager. It watches Workload and re-enqueues on KubernetesCluster changes
// so newly connected / relabeled edges are (re)scheduled.
func SetupWithManager(mgr mcmanager.Manager) error {
	r := &Reconciler{mgr: mgr}
	klog.Info("Registering Workload scheduler controller")
	return mcbuilder.ControllerManagedBy(mgr).
		Named(controllerName).
		For(&edgesv1alpha1.Workload{}).
		Watches(&edgesv1alpha1.KubernetesCluster{}, mchandler.EnqueueRequestsFromMapFunc(r.mapEdgeToWorkloads)).
		Complete(r)
}

// Reconcile handles a single Workload reconciliation across workspaces.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("key", req.NamespacedName, "cluster", req.ClusterName)
	logger.V(4).Info("Reconciling Workload")

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
		logger.Error(err, "Failed to get Workload")
		return ctrl.Result{}, err
	}

	// List all KubernetesCluster edges in this workspace.
	var edgeList edgesv1alpha1.KubernetesClusterList
	if err := c.List(ctx, &edgeList); err != nil {
		logger.Error(err, "Failed to list KubernetesCluster edges")
		return ctrl.Result{}, fmt.Errorf("listing edges: %w", err)
	}

	matched, err := MatchEdges(edgeList.Items, vw.Spec.Placement)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("matching edges: %w", err)
	}
	selected := SelectEdges(matched, vw.Spec.Placement.Strategy)
	logger.V(4).Info("Scheduling", "edges", len(edgeList.Items), "matched", len(matched), "selected", len(selected))

	// Render the workload into a manifest bundle once (Helm charts are fetched
	// + templated here, hub-side). The same bundle is stored on every
	// Placement; the agent stamps per-placement labels at apply time. A render
	// failure (e.g. chart fetch) requeues rather than creating empty placements.
	objs, err := render.Render(ctx, &vw)
	if err != nil {
		logger.Error(err, "Failed to render workload")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	manifests, err := render.ToRawExtensions(objs)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("encoding rendered manifests: %w", err)
	}

	// List existing placements for this VW.
	var placementList edgesv1alpha1.PlacementList
	if err := c.List(ctx, &placementList,
		client.InNamespace(vw.Namespace),
		client.MatchingLabels{labelWorkload: vw.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing placements: %w", err)
	}

	desiredEdges := make(map[string]bool)
	for _, edge := range selected {
		desiredEdges[edge.Name] = true
	}

	// Delete placements for edges no longer selected.
	for i := range placementList.Items {
		p := &placementList.Items[i]
		if !desiredEdges[p.Spec.EdgeName] {
			logger.Info("Deleting stale placement", "placement", p.Name, "edge", p.Spec.EdgeName)
			if err := c.Delete(ctx, p); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to delete placement", "name", p.Name)
			}
		}
	}

	// Index existing placements by edge so we can refresh their manifests when
	// the workload changes (chart bump, values edit, replica change).
	existingByEdge := make(map[string]*edgesv1alpha1.Placement, len(placementList.Items))
	for i := range placementList.Items {
		p := &placementList.Items[i]
		existingByEdge[p.Spec.EdgeName] = p
	}

	// Create or refresh a placement per selected edge.
	for _, edge := range selected {
		if existing, ok := existingByEdge[edge.Name]; ok {
			if equality.Semantic.DeepEqual(existing.Spec.Manifests, manifests) &&
				equalReplicas(existing.Spec.Replicas, vw.Spec.Replicas) {
				continue
			}
			existing.Spec.Manifests = manifests
			existing.Spec.Replicas = vw.Spec.Replicas
			logger.Info("Refreshing placement manifests", "placement", existing.Name, "edge", edge.Name)
			if err := c.Update(ctx, existing); err != nil && !apierrors.IsConflict(err) {
				logger.Error(err, "Failed to update placement", "name", existing.Name)
			}
			continue
		}

		placement := &edgesv1alpha1.Placement{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s", vw.Name, edge.Name),
				Namespace: vw.Namespace,
				Labels: map[string]string{
					labelWorkload: vw.Name,
					labelEdge:     edge.Name,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: edgesv1alpha1.SchemeGroupVersion.String(),
						Kind:       "Workload",
						Name:       vw.Name,
						UID:        vw.UID,
					},
				},
			},
			Spec: edgesv1alpha1.PlacementObjSpec{
				WorkloadRef: corev1.ObjectReference{
					APIVersion: edgesv1alpha1.SchemeGroupVersion.String(),
					Kind:       "Workload",
					Name:       vw.Name,
					Namespace:  vw.Namespace,
					UID:        vw.UID,
				},
				EdgeName:  edge.Name,
				Replicas:  vw.Spec.Replicas,
				Manifests: manifests,
			},
		}

		logger.Info("Creating placement", "placement", placement.Name, "edge", edge.Name)
		if err := c.Create(ctx, placement); err != nil && !apierrors.IsAlreadyExists(err) {
			logger.Error(err, "Failed to create placement", "name", placement.Name)
		}
	}

	// Requeue periodically so edge reconnects are picked up even if a watch
	// event was missed (status-only changes may not always fire the mapper).
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func equalReplicas(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// mapEdgeToWorkloads re-enqueues all Workloads in the same
// workspace whenever a KubernetesCluster edge changes.
func (r *Reconciler) mapEdgeToWorkloads(ctx context.Context, obj client.Object) []reconcile.Request {
	clusterKey, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		clusterKey = multicluster.ClusterName(obj.GetAnnotations()["kcp.io/cluster"])
	}
	cl, err := r.mgr.GetCluster(ctx, clusterKey)
	if err != nil {
		klog.V(2).InfoS("mapEdgeToWorkloads: GetCluster failed", "cluster", clusterKey, "err", err)
		return nil
	}
	var vwList edgesv1alpha1.WorkloadList
	if err := cl.GetClient().List(ctx, &vwList); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(vwList.Items))
	for _, vw := range vwList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: vw.Namespace, Name: vw.Name},
		})
	}
	return requests
}
