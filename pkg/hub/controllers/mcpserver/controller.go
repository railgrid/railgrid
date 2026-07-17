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

// Package mcpserver reconciles MCPServer objects. MCPServer is a built-in,
// core-hosted "provider": the CRD is distributed to tenant workspaces via the
// core.faros.sh APIExport/APIBinding, and this reconciler — running in-process
// in the hub against the core.faros.sh multicluster manager — provisions each
// server's long-lived identity (ServiceAccount + token Secret + RBAC) and
// publishes its endpoint URL + token reference on status. The aggregate MCP
// serving itself lives in pkg/hub/mcpaggregate.
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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/apiurl"
	"github.com/faroshq/faros-kedge/pkg/hub/mcpaggregate"
)

// mcpIdentityNamespace is the tenant-workspace namespace the per-MCPServer
// ServiceAccount + token Secret live in.
const mcpIdentityNamespace = "default"

// toolsRefreshInterval is how often a Ready MCPServer re-discovers its federated
// providers' tools and restamps status. Kept modest so the portal reflects newly
// enabled providers / changed toolsets without a manual refresh.
const toolsRefreshInterval = 60 * time.Second

// ProviderEnumerator returns the tenant's Ready, MCP-exposing providers. Wired
// from the hub's provider registry — the same enumerator the aggregate endpoint
// federates with.
type ProviderEnumerator func(ctx context.Context) []mcpaggregate.ProviderTarget

// Reconciler provisions each MCPServer's identity and publishes its status.
type Reconciler struct {
	mgr            mcmanager.Manager
	kcpConfig      *rest.Config
	hubExternalURL string
	enumerate      ProviderEnumerator
}

// SetupWithManager registers the MCPServer controller with the core.faros.sh
// multicluster manager. kcpConfig is the hub's admin config, used to build a
// direct per-tenant client for identity provisioning (the token controller
// populates legacy token Secrets written through a direct client, which the
// APIExport virtual-workspace client does not guarantee). enumerate lists the
// Ready MCP-exposing providers each server discovers its tools from.
func SetupWithManager(mgr mcmanager.Manager, kcpConfig *rest.Config, hubExternalURL string, enumerate ProviderEnumerator) error {
	r := &Reconciler{mgr: mgr, kcpConfig: kcpConfig, hubExternalURL: hubExternalURL, enumerate: enumerate}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("mcpserver").
		For(&kedgev1alpha1.MCPServer{}).
		Complete(r)
}

// Reconcile provisions the server's identity and writes status. Provisioned
// objects are owned by the MCPServer, so they are garbage-collected when it is
// deleted. Until kcp's token controller populates the token Secret, the server
// stays Provisioning and the reconcile requeues.
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

	clusterRef := string(req.ClusterName)
	if path := lookupClusterPath(ctx, c); path != "" {
		clusterRef = path
	}
	srv.Status.URL = apiurl.MCPServerURL(r.hubExternalURL, clusterRef, srv.Name)

	// Direct typed client to the tenant workspace — the proven path for legacy
	// SA-token provisioning (mirrors pkg/hub/providers ensureLegacySAToken).
	kube, err := kubernetes.NewForConfig(r.tenantConfig(string(req.ClusterName)))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building tenant client for %s: %w", req.ClusterName, err)
	}

	ref, token, tokenReady, provErr := ensureMCPIdentity(ctx, kube, &srv)
	switch {
	case provErr != nil:
		srv.Status.Phase = kedgev1alpha1.MCPServerPhaseError
		setCondition(&srv.Status.Conditions, "Ready", metav1.ConditionFalse, "ProvisioningFailed", provErr.Error(), srv.Generation)
	case !tokenReady:
		srv.Status.TokenSecretRef = ref
		srv.Status.Phase = kedgev1alpha1.MCPServerPhaseProvisioning
		setCondition(&srv.Status.Conditions, "Ready", metav1.ConditionFalse, "TokenPending", "waiting for token controller to populate the Secret", srv.Generation)
	default:
		srv.Status.TokenSecretRef = ref
		srv.Status.Phase = kedgev1alpha1.MCPServerPhaseReady
		setCondition(&srv.Status.Conditions, "Ready", metav1.ConditionTrue, "EndpointReady", "endpoint provisioned", srv.Generation)
		// Discover the tools this endpoint federates, using its OWN token so the
		// set reflects exactly what this server can reach (per-server targeted
		// tooling). Best-effort: discovery failures leave the last snapshot and
		// don't fail the reconcile.
		srv.Status.FederatedProviders = r.discoverTools(ctx, string(req.ClusterName), token)
		now := metav1.Now()
		srv.Status.ToolsRefreshedTime = &now
	}

	if err := c.Status().Update(ctx, &srv); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating MCPServer status: %w", err)
	}
	if provErr != nil {
		logger.Error(provErr, "provisioning MCP identity failed")
		return ctrl.Result{}, provErr
	}
	if !tokenReady {
		// Token controller populates asynchronously; check back shortly.
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	// Ready: periodically re-discover so status reflects newly enabled providers
	// / changed toolsets without a manual poke.
	return ctrl.Result{RequeueAfter: toolsRefreshInterval}, nil
}

// discoverTools runs federation discovery for one server with its own token and
// maps the result into the CR status shape. Returns nil when no enumerator is
// wired (e.g. minimal hubs), so status simply carries no federated providers.
func (r *Reconciler) discoverTools(ctx context.Context, cluster, token string) []kedgev1alpha1.FederatedMCPProvider {
	if r.enumerate == nil {
		return nil
	}
	targets := r.enumerate(ctx)
	discovered := mcpaggregate.DiscoverFederation(ctx, targets, token, cluster)
	out := make([]kedgev1alpha1.FederatedMCPProvider, 0, len(discovered))
	for _, p := range discovered {
		tools := make([]kedgev1alpha1.FederatedMCPTool, 0, len(p.Tools))
		for _, t := range p.Tools {
			tools = append(tools, kedgev1alpha1.FederatedMCPTool{
				Name: t.Name, Title: t.Title, Description: t.Description,
			})
		}
		out = append(out, kedgev1alpha1.FederatedMCPProvider{
			Name:        p.Name,
			DisplayName: p.DisplayName,
			Reachable:   p.Reachable,
			Message:     p.Error,
			Tools:       tools,
		})
	}
	return out
}

// tenantConfig returns an admin rest.Config scoped to the tenant logical
// cluster, so identity objects are written through a direct client.
func (r *Reconciler) tenantConfig(clusterName string) *rest.Config {
	cfg := rest.CopyConfig(r.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(r.kcpConfig.Host, clusterName)
	return cfg
}

// ensureMCPIdentity provisions, idempotently, the per-MCPServer ServiceAccount,
// its long-lived (legacy) token Secret, and a ClusterRoleBinding — all owned by
// the MCPServer for GC. It returns a reference to the token Secret and whether
// kcp's token controller has populated it yet (a short poll; if still empty the
// caller requeues rather than blocking).
func ensureMCPIdentity(ctx context.Context, cs kubernetes.Interface, srv *kedgev1alpha1.MCPServer) (*corev1.SecretReference, string, bool, error) {
	saName := srv.Name + "-mcp"
	secretName := srv.Name + "-mcp-token"

	owner := metav1.OwnerReference{
		APIVersion: kedgev1alpha1.SchemeGroupVersion.String(),
		Kind:       "MCPServer",
		Name:       srv.Name,
		UID:        srv.UID,
	}

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: mcpIdentityNamespace, OwnerReferences: []metav1.OwnerReference{owner}},
	}
	if _, err := cs.CoreV1().ServiceAccounts(mcpIdentityNamespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, "", false, fmt.Errorf("ensuring ServiceAccount %s/%s: %w", mcpIdentityNamespace, saName, err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            secretName,
			Namespace:       mcpIdentityNamespace,
			OwnerReferences: []metav1.OwnerReference{owner},
			Annotations:     map[string]string{corev1.ServiceAccountNameKey: saName},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}
	if _, err := cs.CoreV1().Secrets(mcpIdentityNamespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, "", false, fmt.Errorf("ensuring token Secret %s/%s: %w", mcpIdentityNamespace, secretName, err)
	}

	// TODO(scope-down): cluster-admin is a placeholder so the endpoint works
	// end-to-end. Replace with a narrowly-scoped role once the federated tools'
	// exact needs are pinned, so a leaked token can't act as admin.
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: saName, OwnerReferences: []metav1.OwnerReference{owner}},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: mcpIdentityNamespace}},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "cluster-admin"},
	}
	if _, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, "", false, fmt.Errorf("ensuring ClusterRoleBinding %s: %w", saName, err)
	}

	ref := &corev1.SecretReference{Namespace: mcpIdentityNamespace, Name: secretName}

	// Short poll for the token controller to populate the Secret. If it's not
	// ready within the window, report not-ready so the caller requeues — we
	// never block a worker indefinitely. On success the token value is returned
	// so the caller can discover this server's federated tools with it.
	var token string
	_ = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		got, err := cs.CoreV1().Secrets(mcpIdentityNamespace).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if tok := got.Data[corev1.ServiceAccountTokenKey]; len(tok) > 0 {
			token = string(tok)
			return true, nil
		}
		return false, nil
	})
	return ref, token, token != "", nil
}

// lookupClusterPath returns the workspace path from the singleton
// LogicalCluster CR's kcp.io/path annotation, or "" on any failure.
func lookupClusterPath(ctx context.Context, c client.Client) string {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "core.kcp.io", Version: "v1alpha1", Kind: "LogicalCluster"})
	if err := c.Get(ctx, client.ObjectKey{Name: "cluster"}, u); err != nil {
		return ""
	}
	return u.GetAnnotations()["kcp.io/path"]
}

// setCondition upserts a condition by type.
func setCondition(conditions *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, msg string, gen int64) {
	cond := metav1.Condition{
		Type: condType, Status: status, Reason: reason, Message: msg,
		ObservedGeneration: gen, LastTransitionTime: metav1.Now(),
	}
	for i := range *conditions {
		if (*conditions)[i].Type == condType {
			(*conditions)[i] = cond
			return
		}
	}
	*conditions = append(*conditions, cond)
}
