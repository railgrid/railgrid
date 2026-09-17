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

// Package addonctrl derives the hub-facing half of an edge add-on: for a
// runner Addon that an AGENT has confirmed it is materializing, it publishes
// the edges Service through which the add-on is reachable.
//
// The ordering constraint is the whole point of this controller. It never
// creates a Service from the Addon alone. A Service is created only once the
// agent on the referenced edge has reported Allowed=True AND published the
// add-on's token Secret, i.e. only once the machine owner's half of the trust
// model has been exercised. Hub-side intent by itself publishes nothing — a
// tenant who can create an Addon must not be able to conjure a reachable
// endpoint on a machine that never agreed to host one.
package addonctrl

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
)

const controllerName = "addon-publisher"

// resyncInterval re-checks an Addon that is waiting for its agent. The agent
// patches status when it acts, which requeues this controller, so the resync
// only covers the token Secret appearing without an Addon status change.
const resyncInterval = time.Minute

// Connectable kinds that can host an add-on. KubernetesCluster is refused; the
// API's CEL rule refuses it too, and this is the defence for objects that
// predate the rule.
const (
	linuxServerKind       = "LinuxServer"
	macOSServerKind       = "MacOSServer"
	kubernetesClusterKind = "KubernetesCluster"
)

// tokenSecretSuffix / tokenSecretNamespace mirror what the agent's runner
// add-on publishes (pkg/agent/addons.TokenSecretSuffix). They are duplicated
// rather than imported because the agent lives in the core module and the
// provider is a separate one; the docs name the contract in one place.
const (
	tokenSecretSuffix    = "-runner-token"
	tokenSecretNamespace = "default"
)

// Reconciler publishes the edges Service derived from a runner Addon.
type Reconciler struct {
	mgr mcmanager.Manager
}

// SetupWithManager registers the add-on publisher on the multicluster manager.
func SetupWithManager(mgr mcmanager.Manager) error {
	r := &Reconciler{mgr: mgr}
	return mcbuilder.ControllerManagedBy(mgr).
		Named(controllerName).
		For(&edgesv1alpha1.Addon{}).
		Owns(&edgesv1alpha1.Service{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	addon := &edgesv1alpha1.Addon{}
	if err := c.Get(ctx, req.NamespacedName, addon); err != nil {
		// The Service carries an ownerReference to the Addon, so deletion is
		// garbage-collected rather than swept here.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.reconcileAddon(klog.NewContext(ctx, klog.FromContext(ctx).WithValues("cluster", req.ClusterName)), c, addon)
}

// reconcileAddon holds the whole decision. It takes an already-fetched Addon
// and a workspace client so the gates below can be exercised directly, without
// standing up a multicluster manager whose only contribution is that client.
func (r *Reconciler) reconcileAddon(ctx context.Context, c client.Client, addon *edgesv1alpha1.Addon) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("addon", addon.Name)

	if addon.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	// Only the runner add-on has a hub-facing endpoint today. A future type
	// that also needs one adds its branch here; a type that needs none simply
	// has no branch, and this controller leaves its status alone.
	if addon.Spec.Type != edgesv1alpha1.AddonTypeRunner {
		return ctrl.Result{}, nil
	}

	kind := addon.Spec.EdgeRef.Kind
	if kind == "" {
		kind = linuxServerKind
	}
	if kind != linuxServerKind && kind != macOSServerKind {
		return r.blocked(ctx, c, addon, edgesv1alpha1.AddonReasonUnsupportedEdgeKind,
			fmt.Sprintf("%s edges cannot host add-ons; use a LinuxServer or MacOSServer edge", kind))
	}
	exists, err := r.edgeExists(ctx, c, kind, addon.Spec.EdgeRef.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !exists {
		return r.blocked(ctx, c, addon, edgesv1alpha1.AddonReasonEdgeNotFound,
			fmt.Sprintf("%s %q does not exist in this workspace", kind, addon.Spec.EdgeRef.Name))
	}

	// Gate 1: the agent's own report. Without Allowed=True no agent has
	// accepted this add-on, so there is nothing to publish.
	if !apimeta.IsStatusConditionTrue(addon.Status.Conditions, edgesv1alpha1.AddonConditionAllowed) {
		return r.waiting(ctx, c, addon, edgesv1alpha1.AddonReasonNotAllowedYet,
			"waiting for the agent on edge "+addon.Spec.EdgeRef.Name+" to report Allowed=True; "+
				"the machine owner must start it with --allow-addon=runner")
	}

	// Gate 2: the credential the Service will use. The agent publishes it only
	// after it has actually written the add-on's local state.
	secretName := addon.Name + tokenSecretSuffix
	secret := &corev1.Secret{}
	err = c.Get(ctx, client.ObjectKey{Namespace: tokenSecretNamespace, Name: secretName}, secret)
	if apierrors.IsNotFound(err) {
		return r.waiting(ctx, c, addon, edgesv1alpha1.AddonReasonTokenSecretMissing,
			fmt.Sprintf("waiting for the agent to publish Secret %s/%s", tokenSecretNamespace, secretName))
	} else if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting token secret: %w", err)
	}

	if err := r.ensureService(ctx, c, addon, kind, secretName); err != nil {
		return ctrl.Result{}, err
	}
	logger.V(4).Info("published add-on service", "service", addon.Name)

	addon.Status.ServiceRef = &edgesv1alpha1.AddonServiceRef{Name: addon.Name}
	setCondition(&addon.Status.Conditions, edgesv1alpha1.AddonConditionPublished, metav1.ConditionTrue,
		edgesv1alpha1.AddonReasonServicePublished, "Service "+addon.Name+" points at this add-on")
	if err := c.Status().Update(ctx, addon); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating addon status: %w", err)
	}
	return ctrl.Result{RequeueAfter: resyncInterval}, nil
}

// edgeExists checks that spec.edgeRef names a real connectable. A Service
// pointing at a non-existent edge would sit Unreachable forever with no
// explanation on the object the user actually created.
func (r *Reconciler) edgeExists(ctx context.Context, c client.Client, kind, name string) (bool, error) {
	var obj client.Object
	switch kind {
	case macOSServerKind:
		obj = &edgesv1alpha1.MacOSServer{}
	default:
		obj = &edgesv1alpha1.LinuxServer{}
	}
	err := c.Get(ctx, client.ObjectKey{Name: name}, obj)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("getting %s %q: %w", kind, name, err)
	}
	return true, nil
}

// desiredService is the Service an add-on publishes: a generic HTTP service on
// the edge host's loopback, authenticated with the token the agent published.
// It is a plain proxy target — "generic" means no MCP tool bundle is exposed
// for it, which is correct: the runner protocol is driven by a Factory Worker,
// not by an AI client poking at tools.
func desiredService(addon *edgesv1alpha1.Addon, kind, secretName string) *edgesv1alpha1.Service {
	port := int32(8787)
	if addon.Spec.Runner != nil && addon.Spec.Runner.Port > 0 {
		port = addon.Spec.Runner.Port
	}
	return &edgesv1alpha1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: addon.Name,
			Labels: map[string]string{
				edgesv1alpha1.LabelEdge: addon.Spec.EdgeRef.Name,
				LabelAddon:              addon.Name,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         edgesv1alpha1.SchemeGroupVersion.String(),
				Kind:               "Addon",
				Name:               addon.Name,
				UID:                addon.UID,
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: edgesv1alpha1.ServiceSpec{
			EdgeRef: edgesv1alpha1.ServiceEdgeRef{Kind: kind, Name: addon.Spec.EdgeRef.Name},
			// Loopback: the runner refuses to bind anywhere else, so naming any
			// other host here could only ever produce a broken Service.
			Host:   "127.0.0.1",
			Type:   edgesv1alpha1.ServiceTypeGeneric,
			Scheme: edgesv1alpha1.ServiceSchemeHTTP,
			Port:   port,
			Auth:   edgesv1alpha1.ServiceAuthSecret,
			AuthSecretRef: &corev1.SecretReference{
				Name:      secretName,
				Namespace: tokenSecretNamespace,
			},
		},
	}
}

// LabelAddon ties a derived Service back to the Addon it came from.
const LabelAddon = "edges.railgrid.ai/addon"

func (r *Reconciler) ensureService(ctx context.Context, c client.Client, addon *edgesv1alpha1.Addon, kind, secretName string) error {
	desired := desiredService(addon, kind, secretName)
	existing := &edgesv1alpha1.Service{}
	err := c.Get(ctx, client.ObjectKey{Name: desired.Name}, existing)
	if apierrors.IsNotFound(err) {
		return c.Create(ctx, desired)
	} else if err != nil {
		return fmt.Errorf("getting service %q: %w", desired.Name, err)
	}
	// Refuse to hijack a Service the user (or discovery) created under the same
	// name. Silently rewriting someone else's proxy target to a code-execution
	// endpoint would be the worst kind of surprise.
	if !ownedByAddon(existing, addon) {
		return fmt.Errorf("service %q already exists and is not owned by Addon %q", desired.Name, addon.Name)
	}
	existing.Labels = desired.Labels
	existing.OwnerReferences = desired.OwnerReferences
	existing.Spec = desired.Spec
	return c.Update(ctx, existing)
}

func ownedByAddon(svc *edgesv1alpha1.Service, addon *edgesv1alpha1.Addon) bool {
	for _, ref := range svc.OwnerReferences {
		if ref.Kind == "Addon" && ref.Name == addon.Name && ref.UID == addon.UID {
			return true
		}
	}
	return false
}

// blocked reports a reference this controller will never serve: Published
// stays False, no Service is created, and the phase says so.
func (r *Reconciler) blocked(ctx context.Context, c client.Client, addon *edgesv1alpha1.Addon, reason, message string) (ctrl.Result, error) {
	addon.Status.Phase = edgesv1alpha1.AddonPhaseBlocked
	addon.Status.Message = message
	addon.Status.ServiceRef = nil
	setCondition(&addon.Status.Conditions, edgesv1alpha1.AddonConditionPublished, metav1.ConditionFalse, reason, message)
	if err := c.Status().Update(ctx, addon); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating addon status: %w", err)
	}
	return ctrl.Result{}, nil
}

// waiting reports that the agent has not (yet) done its half. Published stays
// False and no Service exists. The PHASE is deliberately left alone: it belongs
// to the agent, which knows more about why than this controller does.
func (r *Reconciler) waiting(ctx context.Context, c client.Client, addon *edgesv1alpha1.Addon, reason, message string) (ctrl.Result, error) {
	addon.Status.ServiceRef = nil
	setCondition(&addon.Status.Conditions, edgesv1alpha1.AddonConditionPublished, metav1.ConditionFalse, reason, message)
	if err := c.Status().Update(ctx, addon); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating addon status: %w", err)
	}
	return ctrl.Result{RequeueAfter: resyncInterval}, nil
}

// setCondition upserts a status condition, bumping LastTransitionTime only when
// the status value changes.
func setCondition(conditions *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(conditions, metav1.Condition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
}
