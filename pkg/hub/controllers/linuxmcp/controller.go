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

// Package linuxmcp reconciles LinuxMCP resources.
package linuxmcp

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

// ConnManager is the minimal interface the controller needs from the connection manager.
type ConnManager interface {
	HasConnection(key string) bool
}

// connKeyFn must match edgeConnKey in pkg/virtual/builder/agent_proxy_builder_v2.go.
func connKeyFn(cluster, edge string) string {
	return "edges/" + cluster + "/" + edge
}

// Reconciler reconciles LinuxMCP objects.
type Reconciler struct {
	mgr            mcmanager.Manager
	connManager    ConnManager
	hubExternalURL string
}

// SetupWithManager registers the LinuxMCP controller with the multicluster manager.
func SetupWithManager(mgr mcmanager.Manager, connManager ConnManager, hubExternalURL string) error {
	r := &Reconciler{
		mgr:            mgr,
		connManager:    connManager,
		hubExternalURL: hubExternalURL,
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("linux-mcp").
		For(&kedgev1alpha1.LinuxMCP{}).
		Complete(r)
}

// Reconcile reconciles a single LinuxMCP object.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("linuxmcp", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var lmcp kedgev1alpha1.LinuxMCP
	if err := c.Get(ctx, req.NamespacedName, &lmcp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	endpoint := apiurl.LinuxMCPURL(r.hubExternalURL, string(req.ClusterName), lmcp.Name)

	var edgeList kedgev1alpha1.EdgeList
	if err := c.List(ctx, &edgeList); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing edges in cluster %s: %w", req.ClusterName, err)
	}

	var selector labels.Selector
	if lmcp.Spec.EdgeSelector != nil {
		selector, err = metav1.LabelSelectorAsSelector(lmcp.Spec.EdgeSelector)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("parsing edgeSelector: %w", err)
		}
	} else {
		selector = labels.Everything()
	}

	connectedCount := 0
	for i := range edgeList.Items {
		edge := &edgeList.Items[i]
		// LinuxMCP only ever targets server-type edges; kubernetes edges are
		// served by KubernetesMCP.
		if edge.Spec.Type != kedgev1alpha1.EdgeTypeServer {
			continue
		}
		if !selector.Matches(labels.Set(edge.Labels)) {
			continue
		}
		if r.connManager.HasConnection(connKeyFn(string(req.ClusterName), edge.Name)) {
			connectedCount++
		}
	}

	readyCondition := metav1.Condition{
		Type:               "Ready",
		ObservedGeneration: lmcp.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if connectedCount > 0 {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "EdgesConnected"
		readyCondition.Message = fmt.Sprintf("%d server edge(s) connected", connectedCount)
	} else {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "NoEdgesConnected"
		readyCondition.Message = "No server edges matching the selector are currently connected"
	}

	patch := client.MergeFrom(lmcp.DeepCopy())
	lmcp.Status.URL = endpoint
	lmcp.Status.ConnectedEdges = connectedCount

	if existing := findCondition(lmcp.Status.Conditions, "Ready"); existing == nil ||
		existing.Status != readyCondition.Status || existing.Reason != readyCondition.Reason {
		lmcp.Status.Conditions = setCondition(lmcp.Status.Conditions, readyCondition)
	}

	if err := c.Status().Patch(ctx, &lmcp, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patching LinuxMCP status: %w", err)
	}

	logger.Info("Reconciled LinuxMCP", "URL", endpoint, "connectedEdges", connectedCount)

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
