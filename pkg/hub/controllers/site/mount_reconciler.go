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

	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
	"github.com/faroshq/faros-kedge/pkg/virtual/builder"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

var workspaceGVR = schema.GroupVersionResource{
	Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces",
}

// MountReconciler watches Sites and creates mount Workspaces in kcp so that
// users can access site kube APIs through kcp workspace navigation.
type MountReconciler struct {
	mgr            mcmanager.Manager
	kcpConfig      *rest.Config
	hubExternalURL string
	siteRoutes     *builder.SiteRouteMap
}

// SetupMountWithManager registers the mount controller with the multicluster manager.
func SetupMountWithManager(mgr mcmanager.Manager, kcpConfig *rest.Config, hubExternalURL string, siteRoutes *builder.SiteRouteMap) error {
	r := &MountReconciler{
		mgr:            mgr,
		kcpConfig:      kcpConfig,
		hubExternalURL: hubExternalURL,
		siteRoutes:     siteRoutes,
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("site-mount").
		For(&kedgev1alpha1.Site{}).
		Owns(&kcptenancyv1alpha1.Workspace{}).
		Complete(r)
}

// Reconcile creates a mount workspace for a ready site and maintains the site route map.
func (r *MountReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
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

	// Only proceed if the site is ready (agent connected, mount contract met).
	if site.Status.Phase != kedgev1alpha1.SitePhaseReady {
		return ctrl.Result{}, nil
	}

	routeKey := req.ClusterName + ":" + site.Name
	tunnelKey := req.ClusterName + "/" + site.Name

	// Register in route map so the site proxy handler can find this site.
	r.siteRoutes.Set(routeKey, tunnelKey)

	// Set the mount URL on the site status if not already set.
	// This URL is served by the hub's site-proxy virtual workspace handler,
	// NOT by /clusters/ which is kcp's own workspace routing prefix.
	expectedURL := r.hubExternalURL + "/services/site-proxy/" + req.ClusterName + "/" + site.Name
	if site.Status.URL != expectedURL {
		logger.Info("Setting site mount URL", "url", expectedURL)
		site.Status.URL = expectedURL
		if err := c.Status().Update(ctx, &site); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating site URL: %w", err)
		}
	}

	// Create mount workspace in kcp via admin dynamic client.
	if err := r.ensureMountWorkspace(ctx, logger, req.ClusterName, &site); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring mount workspace: %w", err)
	}

	return ctrl.Result{}, nil
}

// ensureMountWorkspace creates a Workspace with mount.ref pointing to the Site,
// owned by the Site so that workspace is garbage-collected when the Site is deleted.
func (r *MountReconciler) ensureMountWorkspace(ctx context.Context, logger klog.Logger, clusterName string, site *kedgev1alpha1.Site) error {
	cfg := rest.CopyConfig(r.kcpConfig)
	cfg.Host = kcp.AppendClusterPath(cfg.Host, clusterName)

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
			Name:            site.Name,
			OwnerReferences: []metav1.OwnerReference{siteOwnerRef(site)},
		},
		Spec: kcptenancyv1alpha1.WorkspaceSpec{
			Mount: &kcptenancyv1alpha1.Mount{
				Reference: kcptenancyv1alpha1.ObjectReference{
					APIVersion: kedgev1alpha1.SchemeGroupVersion.String(),
					Kind:       "Site",
					Name:       site.Name,
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
		logger.V(4).Info("Mount workspace already exists", "site", site.Name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("creating mount workspace %s: %w", site.Name, err)
	}

	logger.Info("Mount workspace created", "site", site.Name, "cluster", clusterName)
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
