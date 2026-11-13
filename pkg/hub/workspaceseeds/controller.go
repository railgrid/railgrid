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

package workspaceseeds

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	adminv1alpha1 "github.com/railgrid/railgrid/apis/admin/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// ConditionAccepted is the WorkspaceSeed condition that says whether the hub
// applies the seed.
const ConditionAccepted = "Accepted"

// unboundRequeue is how soon a workspace is looked at again while a seed's
// binding is not Bound. The binding's own event normally arrives first; this
// covers a workspace whose core binding does not serve APIBindings yet.
const unboundRequeue = 30 * time.Second

// SetupSeedsWithManager wires the seed controller into the admin multicluster
// manager, whose export serves the WorkspaceSeeds of
// root:railgrid:system:providers. Accepted seeds go into store.
func SetupSeedsWithManager(mgr mcmanager.Manager, store *Store) error {
	if mgr == nil || store == nil {
		return fmt.Errorf("workspaceseeds: manager and store are required")
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("workspace-seeds").
		For(&adminv1alpha1.WorkspaceSeed{}).
		Complete(&seedReconciler{mgr: mgr, store: store})
}

type seedReconciler struct {
	mgr   mcmanager.Manager
	store *Store
}

// Reconcile validates one WorkspaceSeed, records the verdict on it and keeps
// the Store to the accepted set.
func (r *seedReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()
	var seed adminv1alpha1.WorkspaceSeed
	if err := c.Get(ctx, req.NamespacedName, &seed); err != nil {
		if apierrors.IsNotFound(err) {
			r.store.Delete(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !seed.DeletionTimestamp.IsZero() {
		r.store.Delete(seed.Name)
		return ctrl.Result{}, nil
	}

	condition := metav1.Condition{
		Type:               ConditionAccepted,
		Status:             metav1.ConditionTrue,
		Reason:             "Valid",
		Message:            "The seed is applied to every tenant workspace.",
		ObservedGeneration: seed.Generation,
	}
	if err := Validate(&seed.Spec); err != nil {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "Invalid"
		condition.Message = err.Error()
		r.store.Delete(seed.Name)
		klog.FromContext(ctx).Info("WorkspaceSeed refused", "seed", seed.Name, "reason", err.Error())
	} else {
		r.store.Set(seed.Name, seed.Spec)
	}

	next := seed.DeepCopy()
	next.Status.ObservedGeneration = seed.Generation
	next.Status.Bindings = int32(len(seed.Spec.Bindings))
	next.Status.Objects = int32(len(seed.Spec.Objects))
	changed := meta.SetStatusCondition(&next.Status.Conditions, condition)
	if !changed && next.Status.ObservedGeneration == seed.Status.ObservedGeneration &&
		next.Status.Bindings == seed.Status.Bindings && next.Status.Objects == seed.Status.Objects {
		return ctrl.Result{}, nil
	}
	if err := c.Status().Update(ctx, next); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("updating WorkspaceSeed status: %w", err)
	}
	return ctrl.Result{}, nil
}

// WorkspaceOps is the slice of the kcp Bootstrapper the workspace controller
// writes through. *kcp.Bootstrapper satisfies it.
type WorkspaceOps interface {
	// ResolveClusterPath returns a logical cluster's workspace path.
	ResolveClusterPath(ctx context.Context, clusterName string) (string, error)
	// EnsureWorkspaceSeed brings one workspace to one seed.
	EnsureWorkspaceSeed(ctx context.Context, clusterName, seedName string, spec adminv1alpha1.WorkspaceSeedSpec) (kcp.WorkspaceSeedResult, error)
}

// ClusterNamer lists the logical clusters a multicluster provider currently
// engages. apiexportprovider.Provider.Clusters satisfies it.
type ClusterNamer interface {
	ClusterNames() []multicluster.ClusterName
}

// SetupWorkspacesWithManager wires the workspace controller into the core
// multicluster manager. Every tenant workspace binds core.railgrid.ai, whose
// virtual workspace serves the workspace's APIBindings, so the manager engages
// every tenant workspace and an APIBinding event is the signal that one
// exists or changed: a new workspace's core binding, a seeded binding turning
// Bound, a provider being enabled. A change of the Store fans out to every
// engaged workspace. There is no timer and no fleet walk.
func SetupWorkspacesWithManager(mgr mcmanager.Manager, clusters ClusterNamer, ops WorkspaceOps, store *Store) error {
	if mgr == nil || clusters == nil || ops == nil || store == nil {
		return fmt.Errorf("workspaceseeds: manager, clusters, ops and store are required")
	}
	r := &workspaceReconciler{ops: ops, store: store, paths: map[string]string{}}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("workspace-seed-apply").
		For(&apisv1alpha2.APIBinding{}).
		WatchesRawSource(&storeSource{store: store, clusters: clusters}).
		Complete(r)
}

type workspaceReconciler struct {
	ops   WorkspaceOps
	store *Store

	pathsMu sync.RWMutex
	paths   map[string]string
}

// Reconcile brings the request's workspace to every accepted seed. The
// binding named in the request is only the trigger.
func (r *workspaceReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	seeds := r.store.List()
	if len(seeds) == 0 {
		return ctrl.Result{}, nil
	}
	clusterName := string(req.ClusterName)
	path, err := r.clusterPath(ctx, clusterName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// The workspace went away with the binding that brought us here.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("resolving workspace path: %w", err)
	}
	if _, _, ok := kcppaths.SplitWorkspacePath(path); !ok {
		// Seeds are for tenant workspaces; an organization's own workspace,
		// an edge mount or a system workspace is not one.
		return ctrl.Result{}, nil
	}

	logger := klog.FromContext(ctx).WithValues("cluster", clusterName, "workspace", path)
	var (
		errs    []error
		unbound bool
	)
	for _, seed := range seeds {
		result, err := r.ops.EnsureWorkspaceSeed(ctx, clusterName, seed.Name, seed.Spec)
		if err != nil {
			errs = append(errs, fmt.Errorf("seed %q: %w", seed.Name, err))
		}
		if result.BindingsCreated+result.BindingsChanged > 0 {
			logger.Info("Seeded workspace bindings", "seed", seed.Name, "created", result.BindingsCreated, "changed", result.BindingsChanged)
		}
		if len(result.Waiting) > 0 {
			logger.V(4).Info("Workspace seed is waiting", "seed", seed.Name, "waiting", result.Waiting)
		}
		unbound = unbound || result.Unbound > 0
	}
	if err := errors.Join(errs...); err != nil {
		return ctrl.Result{}, err
	}
	if unbound {
		return ctrl.Result{RequeueAfter: unboundRequeue}, nil
	}
	return ctrl.Result{}, nil
}

// clusterPath caches the workspace path per logical cluster; a workspace is
// never renamed or re-parented.
func (r *workspaceReconciler) clusterPath(ctx context.Context, clusterName string) (string, error) {
	r.pathsMu.RLock()
	path, ok := r.paths[clusterName]
	r.pathsMu.RUnlock()
	if ok {
		return path, nil
	}
	path, err := r.ops.ResolveClusterPath(ctx, clusterName)
	if err != nil {
		return "", err
	}
	r.pathsMu.Lock()
	r.paths[clusterName] = path
	r.pathsMu.Unlock()
	return path, nil
}

// storeSource is the workspace controller's raw source for Store changes: for
// as long as it runs, every change enqueues every engaged workspace once,
// under the core binding's name. Changes before Start need no replay: the
// informers' initial list reconciles every workspace anyway.
type storeSource struct {
	store    *Store
	clusters ClusterNamer
}

// Start implements controller-runtime's source.TypedSource.
func (s *storeSource) Start(ctx context.Context, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
	unregister := s.store.OnChange(func() {
		if ctx.Err() != nil {
			return
		}
		for _, name := range s.clusters.ClusterNames() {
			q.Add(mcreconcile.Request{
				Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: kcp.CoreBindingName}},
				ClusterName: name,
			})
		}
	})
	go func() {
		<-ctx.Done()
		unregister()
	}()
	return nil
}
