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

package providers

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	adminv1alpha1 "github.com/faroshq/faros-kedge/apis/admin/v1alpha1"
)

// providerFinalizer gates teardown of the provisioned sub-workspace + Secret
// when a Provider CR is deleted.
const providerFinalizer = "admin.kedge.faros.sh/cleanup"

// kubeconfigSecretNamespace is the namespace in root:kedge:system:providers the
// minted kubeconfig Secret is written to.
const kubeconfigSecretNamespace = "default"

// ProviderReconciler provisions the kcp-side scaffolding declared by a Provider
// CR: the per-provider sub-workspace, the "provider" ServiceAccount, and a
// minted kubeconfig written into a Secret in root:kedge:system:providers. It is the
// level-driven replacement for the former imperative admin "onboard" call — it
// chains the same Provisioner steps and then persists the kubeconfig instead of
// returning it over HTTP.
//
// Scope is deliberately narrow: workspace + ServiceAccount + Secret, nothing
// else. The provider's APIExport / schemas / endpoint slice / bind grant remain
// the provider's own `init` responsibility.
type ProviderReconciler struct {
	mgr  mcmanager.Manager
	prov *Provisioner
	// providerServerURL is baked into minted kubeconfigs when the Provider
	// does not override it: the provider-internal URL when set, otherwise the
	// hub external URL.
	providerServerURL string
}

// SetupProviderWithManager wires the Provider provisioning reconciler into the
// admin multicluster manager (bound to admin.kedge.faros.sh, whose Provider
// objects live in root:kedge:system:providers). kcpConfig is the admin
// rest.Config used to create sub-workspaces and write the kubeconfig Secret
// with admin credentials.
func SetupProviderWithManager(mgr mcmanager.Manager, kcpConfig *rest.Config, opts CatalogReconcilerOptions) error {
	serverURL := opts.ProviderInternalURL
	if serverURL == "" {
		serverURL = opts.HubExternalURL
	}
	r := &ProviderReconciler{
		mgr:               mgr,
		prov:              NewProvisioner(kcpConfig),
		providerServerURL: serverURL,
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("provider-provisioner").
		For(&adminv1alpha1.Provider{}).
		Complete(r)
}

// Reconcile provisions (or tears down) one Provider.
func (r *ProviderReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("provider", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var p adminv1alpha1.Provider
	if err := c.Get(ctx, req.NamespacedName, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	secretName := p.Spec.SecretName
	if secretName == "" {
		secretName = p.Name + "-kubeconfig"
	}

	// Teardown path.
	if !p.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&p, providerFinalizer) {
			logger.Info("Tearing down provider", "workspace", providersParentWorkspace+":"+p.Name, "secret", secretName)
			if err := r.prov.DeleteProviderWorkspace(ctx, p.Name); err != nil {
				return ctrl.Result{}, fmt.Errorf("deleting provider workspace: %w", err)
			}
			if err := r.prov.DeleteKubeconfigSecret(ctx, kubeconfigSecretNamespace, secretName); err != nil {
				return ctrl.Result{}, fmt.Errorf("deleting kubeconfig secret: %w", err)
			}
			controllerutil.RemoveFinalizer(&p, providerFinalizer)
			if err := c.Update(ctx, &p); err != nil {
				if apierrors.IsConflict(err) {
					return ctrl.Result{Requeue: true}, nil
				}
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure the finalizer is present before we create anything, so a delete
	// that races provisioning still triggers teardown.
	if !controllerutil.ContainsFinalizer(&p, providerFinalizer) {
		controllerutil.AddFinalizer(&p, providerFinalizer)
		if err := c.Update(ctx, &p); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Provision (idempotent), mirroring the former admin.Service.Onboard chain.
	cluster, err := r.prov.EnsureProviderWorkspace(ctx, p.Name)
	if err != nil {
		return r.fail(ctx, c, &p, "WorkspaceError", fmt.Sprintf("ensuring provider workspace: %v", err), err)
	}
	if err := r.prov.EnsureProviderSA(ctx, p.Name); err != nil {
		return r.fail(ctx, c, &p, "ServiceAccountError", fmt.Sprintf("ensuring provider ServiceAccount: %v", err), err)
	}
	// The CatalogEntry APIExport binding is created automatically by the
	// `provider` WorkspaceType's defaultAPIBindings (see
	// config/kcp/workspacetype-provider.yaml) when the sub-workspace is created,
	// so the provider can self-register its CatalogEntry from inside its
	// workspace. The provisioning (Provider) export is NOT bound there.
	serverURL := p.Spec.ServerURLOverride
	if serverURL == "" {
		serverURL = r.providerServerURL
	}
	kc, err := r.prov.MintProviderKubeconfig(ctx, p.Name, serverURL)
	if err != nil {
		return r.fail(ctx, c, &p, "KubeconfigError", fmt.Sprintf("minting kubeconfig: %v", err), err)
	}
	if err := r.prov.WriteKubeconfigSecret(ctx, kubeconfigSecretNamespace, secretName, ProviderKubeconfigSecretKey, kc, p.Name); err != nil {
		return r.fail(ctx, c, &p, "SecretError", fmt.Sprintf("writing kubeconfig Secret: %v", err), err)
	}

	now := metav1.NewTime(time.Now())
	p.Status.WorkspacePath = providersParentWorkspace + ":" + p.Name
	p.Status.WorkspaceCluster = cluster
	p.Status.SecretRef = &adminv1alpha1.ProviderSecretRef{
		Namespace: kubeconfigSecretNamespace,
		Name:      secretName,
		Key:       ProviderKubeconfigSecretKey,
	}
	p.Status.LastProvisioned = &now
	setCondition(&p.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Provisioned",
		Message:            "Workspace, ServiceAccount, and kubeconfig Secret provisioned.",
		LastTransitionTime: now,
		ObservedGeneration: p.Generation,
	})
	if err := c.Status().Update(ctx, &p); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	logger.Info("Provisioned provider", "workspace", p.Status.WorkspacePath, "cluster", cluster, "secret", secretName)
	return ctrl.Result{}, nil
}

// fail sets a False Ready condition with the given reason/message, persists
// status (best-effort), and returns the underlying error so the item requeues.
func (r *ProviderReconciler) fail(ctx context.Context, c client.Client, p *adminv1alpha1.Provider, reason, message string, cause error) (ctrl.Result, error) {
	setCondition(&p.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.NewTime(time.Now()),
		ObservedGeneration: p.Generation,
	})
	// Best-effort status write; the returned error drives the requeue.
	_ = c.Status().Update(ctx, p)
	return ctrl.Result{}, cause
}
