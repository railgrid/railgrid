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

// Package mcpserver reconciles MCPServer aggregate resources.  Counterpart to
// the per-kind kubernetes-mcp and linux-mcp controllers; this one is
// edge-type agnostic and surfaces both kube and server edge counts in
// status.
package mcpserver

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/apiurl"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// ConnManager is the minimal contract from the hub connection manager.
type ConnManager interface {
	HasConnection(key string) bool
}

// connKeyFn must mirror edgeConnKey in pkg/virtual/builder/agent_proxy_builder_v2.go
// so the controller checks the same set of active tunnels the handler will
// later forward requests through.
func connKeyFn(cluster, edge string) string {
	return "edges/" + cluster + "/" + edge
}

// Reconciler reconciles MCPServer objects.
type Reconciler struct {
	mgr            mcmanager.Manager
	connManager    ConnManager
	hubExternalURL string
}

// SetupWithManager registers the MCPServer controller with the multicluster
// manager (same provider/scheme used by the per-kind controllers).
func SetupWithManager(mgr mcmanager.Manager, connManager ConnManager, hubExternalURL string) error {
	r := &Reconciler{
		mgr:            mgr,
		connManager:    connManager,
		hubExternalURL: hubExternalURL,
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("mcpserver").
		For(&kedgev1alpha1.MCPServer{}).
		Complete(r)
}

// Reconcile sets status.URL plus the per-kind connected counts.  Periodic
// requeue keeps the counts fresh as edges come and go (same cadence as the
// per-kind reconcilers, kept consistent on purpose).
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("mcpserver", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var srv kedgev1alpha1.MCPServer
	if err := c.Get(ctx, req.NamespacedName, &srv); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	endpoint := apiurl.MCPServerURL(r.hubExternalURL, string(req.ClusterName), srv.Name)

	var edgeList kedgev1alpha1.EdgeList
	if err := c.List(ctx, &edgeList); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing edges in cluster %s: %w", req.ClusterName, err)
	}

	var selector labels.Selector
	if srv.Spec.EdgeSelector != nil {
		selector, err = metav1.LabelSelectorAsSelector(srv.Spec.EdgeSelector)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("parsing edgeSelector: %w", err)
		}
	} else {
		selector = labels.Everything()
	}

	var kubeConnected, linuxConnected int
	for i := range edgeList.Items {
		edge := &edgeList.Items[i]
		if !selector.Matches(labels.Set(edge.Labels)) {
			continue
		}
		if !r.connManager.HasConnection(connKeyFn(string(req.ClusterName), edge.Name)) {
			continue
		}
		switch edge.Spec.Type {
		case kedgev1alpha1.EdgeTypeKubernetes:
			kubeConnected++
		case kedgev1alpha1.EdgeTypeServer:
			linuxConnected++
		}
	}
	totalConnected := kubeConnected + linuxConnected

	readyCondition := metav1.Condition{
		Type:               "Ready",
		ObservedGeneration: srv.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if totalConnected > 0 {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "EdgesConnected"
		readyCondition.Message = fmt.Sprintf("%d edge(s) connected (kube=%d, linux=%d)",
			totalConnected, kubeConnected, linuxConnected)
	} else {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "NoEdgesConnected"
		readyCondition.Message = "No edges matching the selector are currently connected"
	}

	patch := client.MergeFrom(srv.DeepCopy())
	srv.Status.URL = endpoint
	srv.Status.KubernetesEdges = kubeConnected
	srv.Status.LinuxEdges = linuxConnected

	if existing := findCondition(srv.Status.Conditions, "Ready"); existing == nil ||
		existing.Status != readyCondition.Status || existing.Reason != readyCondition.Reason {
		srv.Status.Conditions = setCondition(srv.Status.Conditions, readyCondition)
	}

	if err := c.Status().Patch(ctx, &srv, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patching MCPServer status: %w", err)
	}

	logger.Info("Reconciled MCPServer", "URL", endpoint,
		"kubeEdges", kubeConnected, "linuxEdges", linuxConnected)

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

func setCondition(conditions []metav1.Condition, cond metav1.Condition) []metav1.Condition {
	for i, c := range conditions {
		if c.Type == cond.Type {
			conditions[i] = cond
			return conditions
		}
	}
	return append(conditions, cond)
}
