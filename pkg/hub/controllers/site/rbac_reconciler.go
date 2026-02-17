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

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
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

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

const (
	// siteAgentClusterRole is the ClusterRole name for site agents.
	siteAgentClusterRole = "kedge-site-agent"
)

// RBACReconciler provisions per-site credentials via native ServiceAccount tokens.
type RBACReconciler struct {
	mgr            mcmanager.Manager
	hubExternalURL string
}

// SetupRBACWithManager registers the site RBAC controller with the multicluster manager.
func SetupRBACWithManager(mgr mcmanager.Manager, hubExternalURL string) error {
	r := &RBACReconciler{
		mgr:            mgr,
		hubExternalURL: hubExternalURL,
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named(rbacControllerName).
		For(&kedgev1alpha1.Site{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.ClusterRole{}).
		Owns(&rbacv1.ClusterRoleBinding{}).
		Complete(r)
}

// Reconcile provisions a ServiceAccount, RBAC, and token Secret for a Site.
func (r *RBACReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
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

	saName := "site-" + site.Name
	tokenSecretName := saName + "-token"
	kubeconfigSecretName := saName + "-kubeconfig"
	secretRef := siteNamespace + "/" + kubeconfigSecretName

	// Always run through ensure* steps (idempotent). Owns() watches trigger
	// re-reconciliation when child objects are deleted.

	logger.Info("Provisioning credentials for site")

	ownerRef := siteOwnerRef(&site)

	// 1. Ensure namespace.
	if err := ensureNamespace(ctx, c); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring namespace %s: %w", siteNamespace, err)
	}

	// 2. Ensure ServiceAccount.
	if err := ensureServiceAccount(ctx, c, saName, ownerRef); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring service account: %w", err)
	}

	// 3. Ensure ClusterRole for site agents.
	if err := ensureClusterRole(ctx, c, ownerRef); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring cluster role: %w", err)
	}

	// 4. Ensure ClusterRoleBinding for this site's SA.
	if err := ensureClusterRoleBinding(ctx, c, saName, ownerRef); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring cluster role binding: %w", err)
	}

	// 5. Ensure token Secret (type kubernetes.io/service-account-token).
	if err := ensureTokenSecret(ctx, c, tokenSecretName, saName, ownerRef); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring token secret: %w", err)
	}

	// 6. Read the SA token. If not yet populated by kcp, requeue.
	tokenSecret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: siteNamespace, Name: tokenSecretName}, tokenSecret); err != nil {
		return ctrl.Result{}, fmt.Errorf("getting token secret: %w", err)
	}
	token := string(tokenSecret.Data["token"])
	if token == "" {
		logger.Info("Token not yet populated, requeuing")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// 7. Create kubeconfig Secret with the SA token for the agent.
	if err := r.ensureKubeconfigSecret(ctx, c, kubeconfigSecretName, site.Name, token, ownerRef); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring kubeconfig secret: %w", err)
	}

	// 8. Update Site status with the secret reference.
	logger.Info("Updating site credentials reference", "secret", secretRef)
	site.Status.CredentialsSecretRef = secretRef
	if site.Status.Phase == "" {
		site.Status.Phase = kedgev1alpha1.SitePhaseNotReady
	}
	if err := c.Status().Update(ctx, &site); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating site status: %w", err)
	}

	logger.Info("Site credentials provisioned", "secret", secretRef)
	return ctrl.Result{}, nil
}

// siteOwnerRef returns an OwnerReference for the given Site.
// Controller is set to true so that Owns() watches (which default to
// OnlyControllerOwner) can map child object changes back to the parent Site.
func siteOwnerRef(site *kedgev1alpha1.Site) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         kedgev1alpha1.SchemeGroupVersion.String(),
		Kind:               "Site",
		Name:               site.Name,
		UID:                site.UID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}

// ensureOwnerRef checks if the object already has the expected OwnerReference
// and patches it in if missing. This adopts pre-existing objects so that Owns()
// watches can map child deletions back to the parent Site.
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
	if err := c.Get(ctx, client.ObjectKey{Name: siteNamespace}, ns); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: siteNamespace},
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func ensureServiceAccount(ctx context.Context, c client.Client, name string, ownerRef metav1.OwnerReference) error {
	sa := &corev1.ServiceAccount{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: siteNamespace, Name: name}, sa); err == nil {
		return ensureOwnerRef(ctx, c, sa, ownerRef)
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       siteNamespace,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// desiredAgentRules returns the PolicyRules that the site agent ClusterRole should have.
func desiredAgentRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{"kedge.faros.sh"},
			Resources: []string{"sites", "sites/status"},
			Verbs:     []string{"get", "list", "watch", "update", "patch"},
		},
		{
			APIGroups: []string{"kedge.faros.sh"},
			Resources: []string{"placements", "placements/status"},
			Verbs:     []string{"get", "list", "watch", "update", "patch"},
		},
		{
			APIGroups: []string{"kedge.faros.sh"},
			Resources: []string{"virtualworkloads", "virtualworkloads/status"},
			Verbs:     []string{"get", "list", "watch"},
		},
	}
}

func ensureClusterRole(ctx context.Context, c client.Client, ownerRef metav1.OwnerReference) error {
	desired := desiredAgentRules()
	cr := &rbacv1.ClusterRole{}
	if err := c.Get(ctx, client.ObjectKey{Name: siteAgentClusterRole}, cr); err == nil {
		if err := ensureOwnerRef(ctx, c, cr, ownerRef); err != nil {
			return err
		}
		// Re-read after potential ownerRef update.
		if err := c.Get(ctx, client.ObjectKey{Name: siteAgentClusterRole}, cr); err != nil {
			return err
		}
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
			Name:            siteAgentClusterRole,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
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
	crbName := "kedge-site-" + saName
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
			Name:     siteAgentClusterRole,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: siteNamespace,
			},
		},
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func ensureTokenSecret(ctx context.Context, c client.Client, secretName, saName string, ownerRef metav1.OwnerReference) error {
	existing := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: siteNamespace, Name: secretName}, existing); err == nil {
		return ensureOwnerRef(ctx, c, existing, ownerRef)
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: siteNamespace,
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

func (r *RBACReconciler) ensureKubeconfigSecret(ctx context.Context, c client.Client, name, siteName, token string, ownerRef metav1.OwnerReference) error {
	existing := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: siteNamespace, Name: name}, existing); err == nil {
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
			"site-agent": {
				Token: token,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"kedge": {
				Cluster:  "kedge",
				AuthInfo: "site-agent",
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
			Namespace: siteNamespace,
			Labels: map[string]string{
				"kedge.faros.sh/site": siteName,
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
