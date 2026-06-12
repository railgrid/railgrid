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

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/apiurl"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// pathAnnotationKey is kcp's well-known annotation on the LogicalCluster
// CR holding the human-readable workspace path (e.g.
// root:kedge:orgs:<orgUUID>:<wsUUID>). Used to publish path-form
// MCPServer URLs so the path becomes the canonical tenant identifier
// across UI + MCP (otherwise the controller would write the short
// kcp-internal cluster ID and tenant scoping would diverge between
// the two ingress paths). Lifted from
// github.com/kcp-dev/kcp/sdk/apis/core "kcp.io/path".
const pathAnnotationKey = "kcp.io/path"

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

	// Resolve the human-readable workspace path for this logical
	// cluster so the published URL uses the path form
	// (root:kedge:orgs:<orgUUID>:<wsUUID>/...) instead of the short
	// kcp ID. The path is the SAME identifier the hub's tenant
	// resolver injects on UI proxy calls, so writing it here makes
	// MCP federation and UI provisioning land in the SAME
	// kedge-tenants-<hash> namespace. Falls back to the short ID if
	// the LogicalCluster CR / annotation isn't available — that's
	// the legacy behavior and at worst keeps MCP-vs-UI divergence
	// rather than failing the reconcile.
	clusterRef := string(req.ClusterName)
	if path := lookupClusterPath(ctx, c); path != "" {
		clusterRef = path
	}
	endpoint := apiurl.MCPServerURL(r.hubExternalURL, clusterRef, srv.Name)

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

	// Ensure the per-MCPServer ServiceAccount + long-lived (legacy) token
	// Secret exist, and publish a reference to the Secret on status. The
	// token value is never read here — kcp's token controller populates the
	// Secret asynchronously, and only the portal backend dereferences it.
	tokenRef, err := ensureMCPIdentity(ctx, c, &srv)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring MCP identity: %w", err)
	}

	patch := client.MergeFrom(srv.DeepCopy())
	srv.Status.URL = endpoint
	srv.Status.KubernetesEdges = kubeConnected
	srv.Status.LinuxEdges = linuxConnected
	srv.Status.TokenSecretRef = tokenRef

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

// mcpTokenNamespace is the workspace namespace the per-MCPServer
// ServiceAccount + token Secret live in. "default" mirrors where the
// infrastructure provider's runtime identity lands
// (install.RuntimeServiceAccountNamespace).
const mcpTokenNamespace = "default"

// ensureMCPIdentity provisions, idempotently, the per-MCPServer
// ServiceAccount, its long-lived (legacy) kubernetes.io/service-account-token
// Secret, and a ClusterRoleBinding granting it access, and returns a
// reference to the token Secret. The token itself is NOT read here: kcp's
// token controller populates the Secret's "token" data key asynchronously,
// and only the portal backend (which can read Secrets) dereferences it to
// render the setup command — so the credential never lands in the MCPServer CR.
//
// All objects are owned by the MCPServer so they're garbage-collected when
// it's deleted. Unlike a TokenRequest bearer, a legacy token Secret does not
// expire, which is exactly what a long-lived MCP endpoint needs.
func ensureMCPIdentity(ctx context.Context, c client.Client, srv *kedgev1alpha1.MCPServer) (*corev1.SecretReference, error) {
	saName := srv.Name + "-mcp"
	secretName := srv.Name + "-mcp-token"

	// Owner ref so the SA + Secret are garbage-collected when the MCPServer
	// is deleted. No BlockOwnerDeletion: that protects the owner (the reverse
	// of what we want) and would require update on its finalizers subresource.
	owner := metav1.OwnerReference{
		APIVersion: kedgev1alpha1.SchemeGroupVersion.String(),
		Kind:       "MCPServer",
		Name:       srv.Name,
		UID:        srv.UID,
	}

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:            saName,
			Namespace:       mcpTokenNamespace,
			OwnerReferences: []metav1.OwnerReference{owner},
		},
	}
	if err := c.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("ensuring ServiceAccount %s/%s: %w", mcpTokenNamespace, saName, err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            secretName,
			Namespace:       mcpTokenNamespace,
			OwnerReferences: []metav1.OwnerReference{owner},
			Annotations: map[string]string{
				corev1.ServiceAccountNameKey: saName,
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}
	if err := c.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("ensuring token Secret %s/%s: %w", mcpTokenNamespace, secretName, err)
	}

	// TODO(scope-down): cluster-admin is a placeholder so the MCP endpoint
	// works end-to-end. Replace with a narrowly-scoped (Cluster)Role granting
	// only what the MCP needs (e.g. read mcpservers/<name>, list edges, and
	// whatever the served toolsets require) so a leaked token can't act as
	// admin on the tenant workspace.
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:            saName,
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      saName,
			Namespace: mcpTokenNamespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
	}
	if err := c.Create(ctx, crb); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("ensuring ClusterRoleBinding %s: %w", crb.Name, err)
	}

	return &corev1.SecretReference{Namespace: mcpTokenNamespace, Name: secretName}, nil
}

// lookupClusterPath returns the human-readable workspace path for the
// current kcp logical cluster by reading the `kcp.io/path` annotation
// off its singleton LogicalCluster CR (name "cluster"). Returns "" on
// any failure — the caller treats that as "fall back to the short
// cluster ID", so a missing annotation degrades gracefully instead of
// breaking reconcile. We use an unstructured Get rather than importing
// kcp's typed scheme to keep this controller's dependency footprint
// minimal (kcp's SDK pulls a large chunk of api machinery).
func lookupClusterPath(ctx context.Context, c client.Client) string {
	gvk := schema.GroupVersionKind{Group: "core.kcp.io", Version: "v1alpha1", Kind: "LogicalCluster"}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, client.ObjectKey{Name: "cluster"}, u); err != nil {
		return ""
	}
	return u.GetAnnotations()[pathAnnotationKey]
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
