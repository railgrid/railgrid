/*
Copyright 2026 The Railgrid Authors.

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
	"errors"
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

	adminv1alpha1 "github.com/railgrid/railgrid/apis/admin/v1alpha1"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// systemWorkspaceFinalizer gates teardown of the workspace and Secret a
// SystemWorkspace provisioned.
const systemWorkspaceFinalizer = "admin.railgrid.ai/system-workspace-cleanup"

// systemWorkspaceBlockedRetry is how often a SystemWorkspace whose name is
// taken by a workspace it does not own is looked at again. Nothing the hub
// watches changes when that workspace goes away.
const systemWorkspaceBlockedRetry = 5 * time.Minute

// SystemWorkspaceReconciler provisions what a SystemWorkspace declares: the
// workspace root:railgrid:system:<name>, the "provider" ServiceAccount in it,
// and a kubeconfig Secret in root:railgrid:system:providers. It is the
// Provider reconciler's twin for platform services that are not providers;
// see adminv1alpha1.SystemWorkspace for what differs and why.
//
// It also deletes the service's rotated-out token Secrets once their grace
// period has passed. For a provider the catalog reconciler does that; a system
// service has no CatalogEntry, so nothing else would.
type SystemWorkspaceReconciler struct {
	mgr       mcmanager.Manager
	prov      *Provisioner
	serverURL string
}

// SetupSystemWorkspaceWithManager wires the SystemWorkspace reconciler into
// the admin multicluster manager, next to the Provider reconciler.
func SetupSystemWorkspaceWithManager(mgr mcmanager.Manager, kcpConfig *rest.Config, opts CatalogReconcilerOptions) error {
	serverURL := opts.HubInternalURL
	if serverURL == "" {
		serverURL = opts.HubExternalURL
	}
	r := &SystemWorkspaceReconciler{
		mgr:       mgr,
		prov:      NewProvisioner(kcpConfig, opts.Provisioner...),
		serverURL: serverURL,
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("system-workspace-provisioner").
		For(&adminv1alpha1.SystemWorkspace{}).
		Complete(r)
}

// Reconcile provisions, or tears down, one SystemWorkspace.
func (r *SystemWorkspaceReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("systemWorkspace", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var sw adminv1alpha1.SystemWorkspace
	if err := c.Get(ctx, req.NamespacedName, &sw); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	path := kcppaths.SystemServicePath(sw.Name)
	secretName := SystemWorkspaceKubeconfigSecretName(sw.Name)

	if !sw.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&sw, systemWorkspaceFinalizer) {
			logger.Info("Tearing down system workspace", "workspace", path, "secret", secretName)
			if err := r.prov.DeleteSystemWorkspace(ctx, sw.Name); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.prov.DeleteKubeconfigSecret(ctx, kubeconfigSecretNamespace, secretName); err != nil {
				return ctrl.Result{}, fmt.Errorf("deleting kubeconfig secret: %w", err)
			}
			controllerutil.RemoveFinalizer(&sw, systemWorkspaceFinalizer)
			if err := c.Update(ctx, &sw); err != nil {
				if apierrors.IsConflict(err) {
					return ctrl.Result{Requeue: true}, nil
				}
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// A name the hub can never provision gets no finalizer: there will be
	// nothing to tear down, and the object should delete freely.
	if kcppaths.IsReservedSystemName(sw.Name) {
		return r.block(ctx, c, &sw, "NameReserved",
			fmt.Sprintf("%q is one of the hub's own workspaces under %s; choose another name.", sw.Name, kcppaths.System), 0)
	}

	if !controllerutil.ContainsFinalizer(&sw, systemWorkspaceFinalizer) {
		controllerutil.AddFinalizer(&sw, systemWorkspaceFinalizer)
		if err := c.Update(ctx, &sw); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	cluster, err := r.prov.EnsureSystemWorkspace(ctx, sw.Name)
	if errors.Is(err, ErrSystemWorkspaceNotOwned) {
		return r.block(ctx, c, &sw, "WorkspaceNotOwned",
			fmt.Sprintf("%s already exists and was not created for this SystemWorkspace; the hub leaves it alone. Choose another name, or delete that workspace.", path),
			systemWorkspaceBlockedRetry)
	}
	if err != nil {
		return r.fail(ctx, c, &sw, "WorkspaceError", fmt.Sprintf("ensuring workspace: %v", err), err)
	}
	if err := r.prov.EnsureSystemWorkspaceSA(ctx, sw.Name); err != nil {
		return r.fail(ctx, c, &sw, "ServiceAccountError", fmt.Sprintf("ensuring ServiceAccount: %v", err), err)
	}
	serverURL := sw.Spec.ServerURLOverride
	if serverURL == "" {
		serverURL = r.serverURL
	}
	kc, err := r.prov.MintSystemWorkspaceKubeconfig(ctx, sw.Name, serverURL)
	if err != nil {
		return r.fail(ctx, c, &sw, "KubeconfigError", fmt.Sprintf("minting kubeconfig: %v", err), err)
	}
	if err := r.prov.WriteSystemWorkspaceKubeconfigSecret(ctx, kubeconfigSecretNamespace, secretName, ProviderKubeconfigSecretKey, kc, sw.Name); err != nil {
		return r.fail(ctx, c, &sw, "SecretError", fmt.Sprintf("writing kubeconfig Secret: %v", err), err)
	}

	// Retire rotated-out tokens. A failure here does not undo provisioning; it
	// is retried with the next reconcile.
	var requeueAfter time.Duration
	if _, next, err := r.prov.SweepExpiredProviderTokens(ctx, path); err != nil {
		logger.Error(err, "Sweeping expired tokens failed")
		requeueAfter = time.Minute
	} else if !next.IsZero() {
		requeueAfter = max(time.Until(next), time.Second)
	}

	now := metav1.NewTime(time.Now())
	sw.Status.WorkspacePath = path
	sw.Status.WorkspaceCluster = cluster
	sw.Status.SecretRef = &adminv1alpha1.ProviderSecretRef{
		Namespace: kubeconfigSecretNamespace,
		Name:      secretName,
		Key:       ProviderKubeconfigSecretKey,
	}
	sw.Status.LastProvisioned = &now
	setCondition(&sw.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Provisioned",
		Message:            "Workspace, ServiceAccount, and kubeconfig Secret provisioned.",
		LastTransitionTime: now,
		ObservedGeneration: sw.Generation,
	})
	if err := c.Status().Update(ctx, &sw); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	logger.Info("Provisioned system workspace", "workspace", path, "cluster", cluster, "secret", secretName)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// block records a Ready=False condition for a state that retrying will not
// change by itself, and looks again after retry (never, when zero).
func (r *SystemWorkspaceReconciler) block(ctx context.Context, c client.Client, sw *adminv1alpha1.SystemWorkspace, reason, message string, retry time.Duration) (ctrl.Result, error) {
	setCondition(&sw.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.NewTime(time.Now()),
		ObservedGeneration: sw.Generation,
	})
	if err := c.Status().Update(ctx, sw); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return ctrl.Result{RequeueAfter: retry}, nil
}

// fail records a Ready=False condition and returns the cause so the item is
// retried with backoff.
func (r *SystemWorkspaceReconciler) fail(ctx context.Context, c client.Client, sw *adminv1alpha1.SystemWorkspace, reason, message string, cause error) (ctrl.Result, error) {
	setCondition(&sw.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.NewTime(time.Now()),
		ObservedGeneration: sw.Generation,
	})
	// Best-effort status write; the returned error drives the requeue.
	_ = c.Status().Update(ctx, sw)
	return ctrl.Result{}, cause
}
