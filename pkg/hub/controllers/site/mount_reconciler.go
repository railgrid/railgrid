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

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
	"github.com/faroshq/faros-kedge/pkg/virtual/builder"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

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
	if err := r.ensureMountWorkspace(ctx, logger, req.ClusterName, site.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring mount workspace: %w", err)
	}

	return ctrl.Result{}, nil
}

// ensureMountWorkspace creates a Workspace with mount.ref pointing to the Site.
func (r *MountReconciler) ensureMountWorkspace(ctx context.Context, logger klog.Logger, clusterName, siteName string) error {
	cfg := rest.CopyConfig(r.kcpConfig)
	cfg.Host = kcp.AppendClusterPath(cfg.Host, clusterName)

	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	ws := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tenancy.kcp.io/v1alpha1",
			"kind":       "Workspace",
			"metadata": map[string]interface{}{
				"name": siteName,
			},
			"spec": map[string]interface{}{
				"mount": map[string]interface{}{
					"ref": map[string]interface{}{
						"apiVersion": kedgev1alpha1.SchemeGroupVersion.String(),
						"kind":       "Site",
						"name":       siteName,
					},
				},
			},
		},
	}

	_, err = client.Resource(workspaceGVR).Create(ctx, ws, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		logger.V(4).Info("Mount workspace already exists", "site", siteName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("creating mount workspace %s: %w", siteName, err)
	}

	logger.Info("Mount workspace created", "site", siteName, "cluster", clusterName)
	return nil
}
