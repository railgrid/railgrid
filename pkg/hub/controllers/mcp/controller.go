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

// Package mcp reconciles Kubernetes MCP resources.
package mcp

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
	mcpv1alpha1 "github.com/faroshq/faros-kedge/apis/mcp/v1alpha1"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// ConnManager is the minimal interface the controller needs from the connection manager.
type ConnManager interface {
	// HasConnection returns true if there is an active tunnel for the given key.
	HasConnection(key string) bool
}

// connKeyFn converts cluster+edge to the ConnManager key used by the agent-proxy.
// Must match edgeConnKey in pkg/virtual/builder/agent_proxy_builder_v2.go.
func connKeyFn(cluster, edge string) string {
	return cluster + "/" + edge
}

// Reconciler reconciles Kubernetes MCP objects.
type Reconciler struct {
	mgr            mcmanager.Manager
	connManager    ConnManager
	hubExternalURL string
}

// SetupWithManager registers the Kubernetes MCP controller with the multicluster manager.
func SetupWithManager(mgr mcmanager.Manager, connManager ConnManager, hubExternalURL string) error {
	r := &Reconciler{
		mgr:            mgr,
		connManager:    connManager,
		hubExternalURL: hubExternalURL,
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("kubernetes-mcp").
		For(&mcpv1alpha1.Kubernetes{}).
		Complete(r)
}

// Reconcile reconciles a single Kubernetes MCP object.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("kubernetes", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var kmcp mcpv1alpha1.Kubernetes
	if err := c.Get(ctx, req.NamespacedName, &kmcp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Compute the endpoint URL.
	// Format: {hubExternalURL}/services/mcp/{cluster}/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/{name}/mcp
	endpoint := fmt.Sprintf("%s/services/mcp/%s/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/%s/mcp",
		r.hubExternalURL, req.ClusterName, kmcp.Name)

	// List all edges in the cluster.
	var edgeList kedgev1alpha1.EdgeList
	if err := c.List(ctx, &edgeList); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing edges in cluster %s: %w", req.ClusterName, err)
	}

	// Filter edges by the selector and count connected ones.
	var selector labels.Selector
	if kmcp.Spec.EdgeSelector != nil {
		selector, err = metav1.LabelSelectorAsSelector(kmcp.Spec.EdgeSelector)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("parsing edgeSelector: %w", err)
		}
	} else {
		selector = labels.Everything()
	}

	connectedCount := 0
	for i := range edgeList.Items {
		edge := &edgeList.Items[i]
		if !selector.Matches(labels.Set(edge.Labels)) {
			continue
		}
		key := connKeyFn(req.ClusterName, edge.Name)
		if r.connManager.HasConnection(key) {
			connectedCount++
		}
	}

	// Build the Ready condition.
	readyCondition := metav1.Condition{
		Type:               "Ready",
		ObservedGeneration: kmcp.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if connectedCount > 0 {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "EdgesConnected"
		readyCondition.Message = fmt.Sprintf("%d edge(s) connected", connectedCount)
	} else {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "NoEdgesConnected"
		readyCondition.Message = "No edges matching the selector are currently connected"
	}

	// Update status if needed.
	patch := client.MergeFrom(kmcp.DeepCopy())
	kmcp.Status.URL = endpoint
	kmcp.Status.ConnectedEdges = connectedCount

	// Merge condition.
	existing := findCondition(kmcp.Status.Conditions, "Ready")
	if existing == nil || existing.Status != readyCondition.Status || existing.Reason != readyCondition.Reason {
		kmcp.Status.Conditions = setCondition(kmcp.Status.Conditions, readyCondition)
	}

	if err := c.Status().Patch(ctx, &kmcp, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patching Kubernetes MCP status: %w", err)
	}

	logger.Info("Reconciled Kubernetes MCP", "URL", endpoint, "connectedEdges", connectedCount)

	// Requeue periodically so ConnectedEdges stays fresh.
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// findCondition returns the condition with the given type, or nil.
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// setCondition replaces an existing condition of the same type or appends a new one.
func setCondition(conditions []metav1.Condition, cond metav1.Condition) []metav1.Condition {
	for i, c := range conditions {
		if c.Type == cond.Type {
			conditions[i] = cond
			return conditions
		}
	}
	return append(conditions, cond)
}
