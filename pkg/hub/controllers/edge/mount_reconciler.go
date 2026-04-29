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

	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/apiurl"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

var workspaceGVR = schema.GroupVersionResource{
	Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces",
}

// MountReconciler watches Edges and creates mount Workspaces in kcp for
// type=kubernetes edges so that users can access edge kube APIs through kcp
// workspace navigation.
//
// Edges with spec.type=server are skipped — they have no kcp workspace.
type MountReconciler struct {
	mgr       mcmanager.Manager
	kcpConfig *rest.Config
	// hubMountURL is the internal URL used for edge.Status.URL which kcp reads
	// during mount resolution. This must be a localhost/internal address to
	// avoid CDN/proxy loops (e.g. Cloudflare loop detection) when kcp's
	// local proxy calls the edges-proxy handler.
	hubMountURL string

	// workspaceEnsureFn creates or adopts the kcp mount workspace for an edge.
	// Defaults to r.ensureMountWorkspace; injectable for unit testing.
	workspaceEnsureFn func(ctx context.Context, logger klog.Logger, clusterName string, edge *kedgev1alpha1.Edge) error
}

// SetupMountWithManager registers the edge mount controller with the multicluster manager.
func SetupMountWithManager(mgr mcmanager.Manager, kcpConfig *rest.Config, hubMountURL string) error {
	r := &MountReconciler{
		mgr:         mgr,
		kcpConfig:   kcpConfig,
		hubMountURL: hubMountURL,
	}
	r.workspaceEnsureFn = r.ensureMountWorkspace
	return mcbuilder.ControllerManagedBy(mgr).
		Named("edge-mount").
		For(&kedgev1alpha1.Edge{}).
		Owns(&kcptenancyv1alpha1.Workspace{}).
		Complete(r)
}

// Reconcile creates a mount workspace for a ready kubernetes edge and maintains
// the URL status field.
func (r *MountReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
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

	// Only proceed if the edge is ready (agent connected).
	if edge.Status.Phase != kedgev1alpha1.EdgePhaseReady {
		return ctrl.Result{}, nil
	}

	// Guard: server-type edges do NOT get a kcp workspace.
	// If the type changed from kubernetes to server, delete the existing workspace.
	if edge.Spec.Type == kedgev1alpha1.EdgeTypeServer {
		logger.V(4).Info("Server-type edge, ensuring no workspace exists")
		if err := r.deleteMountWorkspace(ctx, logger, req.ClusterName, edge.Name); err != nil {
			return ctrl.Result{}, fmt.Errorf("deleting mount workspace for server edge: %w", err)
		}
		// Set the SSH URL on the edge status so clients can reach the SSH endpoint.
		expectedSSHURL := apiurl.EdgeProxyURL(r.hubMountURL, req.ClusterName, edge.Name, "ssh")
		if edge.Status.URL != expectedSSHURL {
			logger.Info("Setting edge SSH URL", "url", expectedSSHURL)
			edge.Status.URL = expectedSSHURL
			if err := c.Status().Update(ctx, &edge); err != nil {
				return ctrl.Result{}, fmt.Errorf("updating edge SSH URL: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// At this point edge.Spec.Type == EdgeTypeKubernetes.

	// Set the workspace URL on the edge status if not already set.
	// The URL is served by the hub's edge-proxy virtual workspace handler.
	expectedURL := apiurl.EdgeProxyURL(r.hubMountURL, req.ClusterName, edge.Name, "k8s")
	if edge.Status.URL != expectedURL {
		logger.Info("Setting edge workspace URL", "url", expectedURL)
		edge.Status.URL = expectedURL
		if err := c.Status().Update(ctx, &edge); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating edge workspaceURL: %w", err)
		}
	}

	// Gate mount workspace creation on the Edge REST mapping being resolvable
	// in the tenant cluster. kcp's WorkspaceMounts indexer runs at the shard
	// level and uses the tenant cluster's dynamicRESTMapper to translate the
	// mount reference (kedge.faros.sh/v1alpha1 Edge) into a GVR the moment
	// the Workspace lands. If the `core.faros.sh` APIBinding hasn't finished
	// reconciling yet, the RESTMapping call errors and the indexer panics,
	// which tears down the kcp pod
	// (kcp-dev/kcp@v0.30.0/pkg/reconciler/tenancy/workspacemounts/workspace_indexes.go).
	// So we wait for discovery to expose `edges.kedge.faros.sh/v1alpha1` in
	// the tenant cluster before creating the Workspace with the mount ref.
	ready, err := r.edgeAPIReadyInCluster(ctx, req.ClusterName)
	if err != nil {
		logger.V(4).Info("edge API readiness check failed, requeuing", "err", err.Error())
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if !ready {
		logger.V(4).Info("edge APIBinding not yet discoverable in tenant cluster; requeuing")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Create mount workspace in kcp via admin dynamic client.
	if err := r.workspaceEnsureFn(ctx, logger, req.ClusterName, &edge); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring mount workspace: %w", err)
	}

	return ctrl.Result{}, nil
}

// edgeAPIReadyInCluster returns true when kcp's discovery in the given tenant
// cluster lists the `edges` resource in kedge.faros.sh/v1alpha1 — which means
// the APIBinding has been reconciled and kcp's internal dynamicRESTMapper for
// that cluster has a mapping for Kind=Edge. Any other outcome (including
// transient errors) returns false so the caller requeues.
func (r *MountReconciler) edgeAPIReadyInCluster(ctx context.Context, clusterName string) (bool, error) {
	cfg := rest.CopyConfig(r.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterName)

	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return false, fmt.Errorf("creating discovery client: %w", err)
	}
	// ServerResourcesForGroupVersion is a single GET against /apis/<gv>, cheap.
	rl, err := disc.ServerResourcesForGroupVersion(kedgev1alpha1.SchemeGroupVersion.String())
	if err != nil {
		// NotFound is the expected pre-binding state: treat as "not ready yet".
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	for _, res := range rl.APIResources {
		if res.Name == "edges" {
			return true, nil
		}
	}
	return false, nil
}

// ensureMountWorkspace creates a Workspace with mount.ref pointing to the Edge,
// owned by the Edge so that the workspace is garbage-collected when the Edge is deleted.
func (r *MountReconciler) ensureMountWorkspace(ctx context.Context, logger klog.Logger, clusterName string, edge *kedgev1alpha1.Edge) error {
	cfg := rest.CopyConfig(r.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterName)

	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	ws := &kcptenancyv1alpha1.Workspace{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kcptenancyv1alpha1.SchemeGroupVersion.String(),
			Kind:       "Workspace",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            edge.Name,
			OwnerReferences: []metav1.OwnerReference{edgeOwnerRef(edge)},
		},
		Spec: kcptenancyv1alpha1.WorkspaceSpec{
			Mount: &kcptenancyv1alpha1.Mount{
				Reference: kcptenancyv1alpha1.ObjectReference{
					APIVersion: kedgev1alpha1.SchemeGroupVersion.String(),
					Kind:       "Edge",
					Name:       edge.Name,
				},
			},
		},
	}

	u, err := toUnstructured(ws)
	if err != nil {
		return fmt.Errorf("converting workspace to unstructured: %w", err)
	}

	_, err = client.Resource(workspaceGVR).Create(ctx, u, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		logger.V(4).Info("Mount workspace already exists", "edge", edge.Name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("creating mount workspace %s: %w", edge.Name, err)
	}

	logger.Info("Mount workspace created", "edge", edge.Name, "cluster", clusterName)
	return nil
}

// deleteMountWorkspace deletes the mount workspace for an edge if it exists.
// This is called when an edge type changes from kubernetes to server.
func (r *MountReconciler) deleteMountWorkspace(ctx context.Context, logger klog.Logger, clusterName, edgeName string) error {
	cfg := rest.CopyConfig(r.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterName)

	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	err = client.Resource(workspaceGVR).Delete(ctx, edgeName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		logger.V(4).Info("Mount workspace already deleted or never existed", "edge", edgeName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("deleting mount workspace %s: %w", edgeName, err)
	}

	logger.Info("Mount workspace deleted", "edge", edgeName, "cluster", clusterName)
	return nil
}

// toUnstructured converts a typed runtime.Object to an Unstructured object.
func toUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: data}, nil
}
