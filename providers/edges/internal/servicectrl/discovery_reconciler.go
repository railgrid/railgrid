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

package servicectrl

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
	edgeapi "github.com/faroshq/provider-edges/internal/edgeapi"
)

// discoveryResyncInterval is how often connected edges are re-scanned.
const discoveryResyncInterval = 5 * time.Minute

// DiscoveryReconciler pulls discovered services from each connected host agent
// and materializes a Service per service. It owns discovery-derived fields
// only; user-set spec (port overrides, authSecretRef) is never clobbered.
type DiscoveryReconciler struct {
	mgr         mcmanager.Manager
	connManager ConnManager
	resource    string
	kind        string
	newObj      func() edgeapi.Connectable
}

// SetupDiscoveryWithManager registers one discovery reconciler for each host
// edge kind whose agent exposes the common service-discovery endpoint.
func SetupDiscoveryWithManager(mgr mcmanager.Manager, connManager ConnManager) error {
	configs := []struct {
		resource string
		kind     string
		newObj   func() edgeapi.Connectable
	}{
		{resource: edgesv1alpha1.LinuxServerResource, kind: "LinuxServer", newObj: edgesv1alpha1.NewLinuxServer},
		{resource: edgesv1alpha1.MacOSServerResource, kind: "MacOSServer", newObj: edgesv1alpha1.NewMacOSServer},
	}
	for _, cfg := range configs {
		r := &DiscoveryReconciler{
			mgr:         mgr,
			connManager: connManager,
			resource:    cfg.resource,
			kind:        cfg.kind,
			newObj:      cfg.newObj,
		}
		if err := mcbuilder.ControllerManagedBy(mgr).
			Named("service-discovery-" + cfg.resource).
			For(cfg.newObj()).
			Complete(r); err != nil {
			return err
		}
	}
	return nil
}

func (r *DiscoveryReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("edge", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	edge := r.newObj()
	if err := c.Get(ctx, req.NamespacedName, edge); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Only scan connected edges with a live tunnel.
	if !edge.GetConnectionStatus().Connected {
		return ctrl.Result{}, nil
	}
	key := connKey(r.resource, string(req.ClusterName), req.Name)
	dialer, ok := r.connManager.Load(key)
	if !ok {
		// Tunnel not (yet) live; retry shortly.
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	services, err := fetchServices(ctx, dialer)
	if err != nil {
		logger.V(2).Info("service discovery failed (will retry)", "err", err.Error())
		return ctrl.Result{RequeueAfter: discoveryResyncInterval}, nil
	}

	// Index existing discovered Services for this edge.
	var existing edgesv1alpha1.ServiceList
	if err := c.List(ctx, &existing, client.MatchingLabels{
		edgesv1alpha1.LabelEdge:       req.Name,
		edgesv1alpha1.LabelDiscovered: "true",
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing services: %w", err)
	}
	byName := make(map[string]*edgesv1alpha1.Service, len(existing.Items))
	for i := range existing.Items {
		// LinuxServer and MacOSServer are separate connectable resources, so
		// the same metadata.name is valid for both. Only reconcile discovered
		// Services whose edgeRef points at this controller's kind; otherwise a
		// same-named edge in the other resource would be treated as missing and
		// deleted or have its status refreshed by the wrong controller.
		if existing.Items[i].Spec.EdgeRef.Kind != r.kind &&
			(r.kind != linuxServerKind || existing.Items[i].Spec.EdgeRef.Kind != "") {
			continue
		}
		byName[existing.Items[i].Name] = &existing.Items[i]
	}

	seen := make(map[string]bool, len(services))
	for _, svc := range services {
		name := discoveredName(r.resource, req.Name, svc.Type)
		seen[name] = true
		if err := r.upsert(ctx, c, req.Name, name, svc, byName[name]); err != nil {
			logger.Error(err, "upserting service", "name", name)
		}
	}

	// Reconcile the ones that disappeared.
	for name, es := range byName {
		if seen[name] {
			continue
		}
		if err := r.handleMissing(ctx, c, es); err != nil {
			logger.Error(err, "handling missing service", "name", name)
		}
	}

	return ctrl.Result{RequeueAfter: discoveryResyncInterval}, nil
}

// upsert creates or refreshes the Service for a discovered service. On
// update it only touches discovery-derived status/labels — never user spec.
func (r *DiscoveryReconciler) upsert(ctx context.Context, c client.Client, edgeName, name string, svc discoveredService, cur *edgesv1alpha1.Service) error {
	now := metav1.Now()
	if cur == nil {
		es := &edgesv1alpha1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					edgesv1alpha1.LabelEdge:       edgeName,
					edgesv1alpha1.LabelDiscovered: "true",
				},
			},
			Spec: edgesv1alpha1.ServiceSpec{
				EdgeRef: edgesv1alpha1.ServiceEdgeRef{Kind: r.kind, Name: edgeName},
				Type:    edgesv1alpha1.ServiceType(svc.Type),
				Scheme:  schemeOrDefault(svc.Scheme),
				Port:    svc.Port,
			},
		}
		if err := c.Create(ctx, es); err != nil {
			return err
		}
		// Set status after create (status is a subresource).
		es.Status = edgesv1alpha1.ServiceStatus{
			Phase:       "Detected",
			Discovered:  true,
			Version:     svc.Version,
			InstallType: svc.InstallType,
			LastSeen:    now,
		}
		setCondition(&es.Status.Conditions, "Detected", metav1.ConditionTrue, "Discovered", "service discovered by the agent")
		return c.Status().Update(ctx, es)
	}

	// Existing: refresh discovery-derived status only.
	cur.Status.Discovered = true
	cur.Status.Version = svc.Version
	cur.Status.InstallType = svc.InstallType
	cur.Status.LastSeen = now
	if cur.Status.Phase == "" || cur.Status.Phase == "Unreachable" {
		cur.Status.Phase = "Detected"
	}
	setCondition(&cur.Status.Conditions, "Detected", metav1.ConditionTrue, "Discovered", "service discovered by the agent")
	return c.Status().Update(ctx, cur)
}

// handleMissing marks a previously discovered service undetected, and deletes it
// only if the user never configured it (no authSecretRef).
func (r *DiscoveryReconciler) handleMissing(ctx context.Context, c client.Client, es *edgesv1alpha1.Service) error {
	if es.Spec.AuthSecretRef == nil {
		return client.IgnoreNotFound(c.Delete(ctx, es))
	}
	es.Status.Phase = "Unreachable"
	setCondition(&es.Status.Conditions, "Detected", metav1.ConditionFalse, "NotDetected", "service no longer detected by the agent")
	return c.Status().Update(ctx, es)
}

// discoveredName keeps the historical LinuxServer name while qualifying MacOSServer
// names. This prevents same-named Linux and Mac edges from competing for one Service
// object in a tenant workspace.
func discoveredName(resource, edge, svcType string) string {
	if resource == edgesv1alpha1.MacOSServerResource {
		edge = "macos-edge-" + edge
	}
	return edge + "-" + strings.ToLower(svcType)
}

func schemeOrDefault(s string) edgesv1alpha1.ServiceScheme {
	if s == "https" {
		return edgesv1alpha1.ServiceSchemeHTTPS
	}
	return edgesv1alpha1.ServiceSchemeHTTP
}
