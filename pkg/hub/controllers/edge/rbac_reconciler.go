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

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// RBACReconciler provisions per-edge credentials via native ServiceAccount tokens.
type RBACReconciler struct {
	mgr            mcmanager.Manager
	hubExternalURL string
}

// SetupRBACWithManager registers the edge RBAC controller with the multicluster manager.
func SetupRBACWithManager(mgr mcmanager.Manager, hubExternalURL string) error {
	r := &RBACReconciler{
		mgr:            mgr,
		hubExternalURL: hubExternalURL,
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named(rbacControllerName).
		For(&kedgev1alpha1.Edge{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.ClusterRoleBinding{}).
		Complete(r)
}

// Reconcile provisions a ServiceAccount, RBAC, and token Secret for an Edge.
func (r *RBACReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
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

	saName := "edge-" + edge.Name
	tokenSecretName := saName + "-token"
	kubeconfigSecretName := saName + "-kubeconfig"

	// Always run through ensure* steps (idempotent). Owns() watches trigger
	// re-reconciliation when child objects are deleted.

	logger.Info("Provisioning credentials for edge")

	ownerRef := edgeOwnerRef(&edge)

	// 1. Ensure namespace.
	if err := ensureNamespace(ctx, c); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring namespace %s: %w", edgeNamespace, err)
	}

	// 2. Ensure ServiceAccount.
	if err := ensureServiceAccount(ctx, c, saName, ownerRef); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring service account: %w", err)
	}

	// 3. Ensure ClusterRole for edge agents.
	// NOTE: the ClusterRole is a shared cluster-wide resource, not owned by
	// individual edges.  Attaching per-edge ownerRefs would cause GC races.
	if err := ensureClusterRole(ctx, c); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring cluster role: %w", err)
	}

	// 4. Ensure ClusterRoleBinding for this edge's SA.
	if err := ensureClusterRoleBinding(ctx, c, saName, ownerRef); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring cluster role binding: %w", err)
	}

	// 5. Ensure token Secret (type kubernetes.io/service-account-token).
	if err := ensureTokenSecret(ctx, c, tokenSecretName, saName, ownerRef); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring token secret: %w", err)
	}

	// 6. Read the SA token. If not yet populated by kcp, requeue.
	tokenSecret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: edgeNamespace, Name: tokenSecretName}, tokenSecret); err != nil {
		return ctrl.Result{}, fmt.Errorf("getting token secret: %w", err)
	}
	token := string(tokenSecret.Data["token"])
	if token == "" {
		logger.Info("Token not yet populated, requeuing")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// 7. Create kubeconfig Secret with the SA token for the agent.
	if err := r.ensureKubeconfigSecret(ctx, c, kubeconfigSecretName, edge.Name, token, ownerRef); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring kubeconfig secret: %w", err)
	}

	logger.Info("Edge credentials provisioned", "secret", edgeNamespace+"/"+kubeconfigSecretName)
	return ctrl.Result{}, nil
}

// edgeOwnerRef returns an OwnerReference for the given Edge.
// Controller is set to true so that Owns() watches (which default to
// OnlyControllerOwner) can map child object changes back to the parent Edge.
func edgeOwnerRef(edge *kedgev1alpha1.Edge) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         kedgev1alpha1.SchemeGroupVersion.String(),
		Kind:               "Edge",
		Name:               edge.Name,
		UID:                edge.UID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}

// ensureOwnerRef checks if the object already has the expected OwnerReference
// and patches it in if missing. This adopts pre-existing objects so that Owns()
// watches can map child deletions back to the parent Edge.
func ensureOwnerRef(ctx context.Context, c client.Client, obj client.Object, ownerRef metav1.OwnerReference) error {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == ownerRef.UID {
			return nil
		}
	}
	obj.SetOwnerReferences(append(obj.GetOwnerReferences(), ownerRef))
	return c.Update(ctx, obj)
}

func ensureNamespace(ctx context.Context, c client.Client) error {
	ns := &corev1.Namespace{}
	if err := c.Get(ctx, client.ObjectKey{Name: edgeNamespace}, ns); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: edgeNamespace},
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func ensureServiceAccount(ctx context.Context, c client.Client, name string, ownerRef metav1.OwnerReference) error {
	sa := &corev1.ServiceAccount{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: edgeNamespace, Name: name}, sa); err == nil {
		return ensureOwnerRef(ctx, c, sa, ownerRef)
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       edgeNamespace,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// desiredAgentRules returns the PolicyRules that the edge agent ClusterRole should have.
func desiredAgentRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{"kedge.faros.sh"},
			Resources: []string{"edges", "edges/status"},
			// "proxy" verb is required for accessing edge clusters through edges-proxy handler
			Verbs: []string{"get", "list", "watch", "update", "patch", "proxy"},
		},
		{
			APIGroups: []string{"kedge.faros.sh"},
			Resources: []string{"placements"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{"kedge.faros.sh"},
			Resources: []string{"virtualworkloads", "virtualworkloads/status"},
			Verbs:     []string{"get", "list", "watch"},
		},
	}
}

// ensureClusterRole creates or updates the shared kedge-edge-agent ClusterRole.
// It intentionally carries no owner reference so that it is never garbage-collected
// when an individual edge is deleted; the role is a cluster-wide shared resource.
func ensureClusterRole(ctx context.Context, c client.Client) error {
	desired := desiredAgentRules()
	cr := &rbacv1.ClusterRole{}
	if err := c.Get(ctx, client.ObjectKey{Name: edgeAgentClusterRole}, cr); err == nil {
		if !rulesEqual(cr.Rules, desired) {
			cr.Rules = desired
			return c.Update(ctx, cr)
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.Create(ctx, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: edgeAgentClusterRole,
		},
		Rules: desired,
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// rulesEqual compares two PolicyRule slices for equality.
func rulesEqual(a, b []rbacv1.PolicyRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !ruleEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func ruleEqual(a, b rbacv1.PolicyRule) bool {
	return slicesEqual(a.APIGroups, b.APIGroups) &&
		slicesEqual(a.Resources, b.Resources) &&
		slicesEqual(a.Verbs, b.Verbs) &&
		slicesEqual(a.ResourceNames, b.ResourceNames) &&
		slicesEqual(a.NonResourceURLs, b.NonResourceURLs)
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func ensureClusterRoleBinding(ctx context.Context, c client.Client, saName string, ownerRef metav1.OwnerReference) error {
	crbName := "kedge-edge-" + saName
	crb := &rbacv1.ClusterRoleBinding{}
	if err := c.Get(ctx, client.ObjectKey{Name: crbName}, crb); err == nil {
		return ensureOwnerRef(ctx, c, crb, ownerRef)
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:            crbName,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     edgeAgentClusterRole,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: edgeNamespace,
			},
		},
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func ensureTokenSecret(ctx context.Context, c client.Client, secretName, saName string, ownerRef metav1.OwnerReference) error {
	existing := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: edgeNamespace, Name: secretName}, existing); err == nil {
		return ensureOwnerRef(ctx, c, existing, ownerRef)
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: edgeNamespace,
			Annotations: map[string]string{
				"kubernetes.io/service-account.name": saName,
			},
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (r *RBACReconciler) ensureKubeconfigSecret(ctx context.Context, c client.Client, name, edgeName, token string, ownerRef metav1.OwnerReference) error {
	existing := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: edgeNamespace, Name: name}, existing); err == nil {
		return ensureOwnerRef(ctx, c, existing, ownerRef)
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	kubeconfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"kedge": {
				Server:                r.hubExternalURL,
				InsecureSkipTLSVerify: true,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"edge-agent": {
				Token: token,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"kedge": {
				Cluster:  "kedge",
				AuthInfo: "edge-agent",
			},
		},
		CurrentContext: "kedge",
	}

	kubeconfigBytes, err := clientcmd.Write(kubeconfig)
	if err != nil {
		return fmt.Errorf("marshaling kubeconfig: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: edgeNamespace,
			Labels: map[string]string{
				"kedge.faros.sh/edge": edgeName,
			},
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Data: map[string][]byte{
			"kubeconfig": kubeconfigBytes,
			"token":      []byte(token),
			"server":     []byte(r.hubExternalURL),
		},
	}

	if err := c.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}
