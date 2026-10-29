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

package metering

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	adminv1alpha1 "github.com/railgrid/railgrid/apis/admin/v1alpha1"
)

const (
	// ProviderBindingName is the APIBinding every platform provider workspace
	// carries to metering's provider-facing export, which is also the
	// export's name: it serves UsageRecord and MeteringConfig, the two kinds
	// a provider writes to report usage.
	ProviderBindingName = "metering-provider"
	// ProviderBindingLabel marks the bindings this reconciler owns.
	ProviderBindingLabel = "metering.railgrid.ai/managed-by"
	// bindingSettle paces the re-check of a binding kcp has not reported
	// Bound yet. The admin manager serves Provider objects, not the
	// APIBindings in provider workspaces, so the phase cannot be watched.
	bindingSettle = 30 * time.Second
)

var gvrAPIBindings = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}

// ProviderGetter reads one Provider from the cluster the request names; the
// multicluster manager implements it and tests substitute a map.
type ProviderGetter interface {
	Get(ctx context.Context, clusterName multicluster.ClusterName, key types.NamespacedName) (*adminv1alpha1.Provider, error)
}

// ProviderOptions configures the reconciler.
type ProviderOptions struct {
	// Discovery is the feature gate: until metering's init has published its
	// endpoints in the metering workspace, no provider workspace is touched.
	Discovery Installed
	// Providers reads Provider objects; defaults to the manager's clients.
	Providers ProviderGetter
	// Workspaces returns a client scoped to a kcp workspace path, for the
	// APIBinding in each provider's workspace.
	Workspaces func(path string) (dynamic.Interface, error)
	Logger     klog.Logger
}

// ProviderReconciler binds metering's provider export into every platform
// provider workspace once metering is installed. The binding is what lets a
// provider emit UsageRecords (docs/usage-contract.md in contrib-metering):
// metering's controller engages any workspace that binds the export, so the
// binding is the whole wiring. The reconciler is additive. It never removes
// a binding, because kcp refuses to delete one while bound objects remain
// and the records in a provider workspace are its usage history.
type ProviderReconciler struct {
	opts ProviderOptions
}

// SetupProviderReconciler registers the reconciler with the hub's admin
// multicluster manager, where Provider objects live.
func SetupProviderReconciler(mgr mcmanager.Manager, opts ProviderOptions) error {
	if opts.Discovery == nil || opts.Workspaces == nil {
		return fmt.Errorf("metering provider reconciler: Discovery and Workspaces are required")
	}
	if opts.Providers == nil {
		opts.Providers = managerProviders{mgr: mgr}
	}
	r := &ProviderReconciler{opts: opts}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("metering-provider-binding").
		For(&adminv1alpha1.Provider{}).
		Complete(r)
}

type managerProviders struct {
	mgr mcmanager.Manager
}

func (m managerProviders) Get(ctx context.Context, clusterName multicluster.ClusterName, key types.NamespacedName) (*adminv1alpha1.Provider, error) {
	cl, err := m.mgr.GetCluster(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("getting cluster %s: %w", clusterName, err)
	}
	p := &adminv1alpha1.Provider{}
	if err := cl.GetClient().Get(ctx, key, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Reconcile is the per-Provider step.
func (r *ProviderReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := r.opts.Logger.WithValues("provider", req.Name)
	endpoints, ok := r.opts.Discovery.Current()
	if !ok {
		return ctrl.Result{RequeueAfter: notInstalledRetry}, nil
	}
	p, err := r.opts.Providers.Get(ctx, req.ClusterName, req.NamespacedName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// The provisioner deletes the workspace, and the binding with it.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !p.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if p.Status.WorkspacePath == "" {
		// Not provisioned yet; the provisioner's status update re-triggers
		// this reconciler.
		return ctrl.Result{}, nil
	}
	ws, err := r.opts.Workspaces(p.Status.WorkspacePath)
	if err != nil {
		return ctrl.Result{}, err
	}
	bound, err := r.ensureBinding(ctx, ws, endpoints.Workspace, logger.WithValues("workspace", p.Status.WorkspacePath))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("binding %s in %s: %w", ProviderBindingName, p.Status.WorkspacePath, err)
	}
	if !bound {
		return ctrl.Result{RequeueAfter: bindingSettle}, nil
	}
	return ctrl.Result{}, nil
}

// ensureBinding creates the binding when it is missing and reports whether
// kcp has it Bound. An existing binding that points elsewhere is left alone
// and logged: kcp does not let a binding change export, and the object is
// not this reconciler's.
func (r *ProviderReconciler) ensureBinding(ctx context.Context, ws dynamic.Interface, meteringWorkspace string, logger klog.Logger) (bool, error) {
	res := ws.Resource(gvrAPIBindings)
	existing, err := res.Get(ctx, ProviderBindingName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := res.Create(ctx, providerBinding(meteringWorkspace), metav1.CreateOptions{FieldManager: FieldManager}); err != nil && !apierrors.IsAlreadyExists(err) {
			return false, err
		}
		logger.Info("metering provider export bound into provider workspace", "export", meteringWorkspace+":"+ProviderBindingName)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	path, _, _ := unstructured.NestedString(existing.Object, "spec", "reference", "export", "path")
	name, _, _ := unstructured.NestedString(existing.Object, "spec", "reference", "export", "name")
	if path != meteringWorkspace || name != ProviderBindingName {
		logger.Info("metering binding points at another export; leaving it", "export", path+":"+name, "want", meteringWorkspace+":"+ProviderBindingName)
		return true, nil
	}
	phase, _, _ := unstructured.NestedString(existing.Object, "status", "phase")
	if phase != "Bound" {
		logger.V(2).Info("metering binding not bound yet", "phase", phase)
		return false, nil
	}
	return true, nil
}

func providerBinding(meteringWorkspace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(gvrAPIBindings.GroupVersion().String())
	u.SetKind("APIBinding")
	u.SetName(ProviderBindingName)
	u.SetLabels(map[string]string{ProviderBindingLabel: FieldManager})
	_ = unstructured.SetNestedMap(u.Object, map[string]any{
		"reference": map[string]any{
			"export": map[string]any{"path": meteringWorkspace, "name": ProviderBindingName},
		},
	}, "spec")
	return u
}
