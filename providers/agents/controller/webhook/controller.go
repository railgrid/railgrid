// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package webhook reconciles Webhook CRs — the inbound URL and delivery sink
// that Triggers route from.
//
// status.webhookPath is the first point of this reconciler. A Webhook is
// useless until it has one: the path is its whole inbound interface and its
// only credential (an HMAC over cluster/name, so there is no token to store
// and none to leak from the CR). The portal creates Webhooks with a kube
// client, so the mint happens here, for every writer, through
// internal/webhookpath — the same derivation and the same prefix a Trigger's
// URL used to have, so a Webhook created under an old trigger's name answers
// the URL already pasted into GitHub.
//
// status.routes is the second: which Triggers name this webhook in
// spec.webhookRef. The delivery path finds them itself on every POST, so the
// list is for readers — the portal, kubectl — who otherwise could not tell a
// webhook nobody routes from from one with three triggers behind it. It is
// kept current by watching Triggers.
//
// The third is the sink's lifetime. Deliveries live in the provider store,
// keyed by the webhook's UID; a finalizer holds the object until they are
// purged, so deleting a webhook takes its traffic with it.
package webhook

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/internal/webhookpath"
)

const (
	// purgeRetryInterval spaces retries of a failed sink purge.
	purgeRetryInterval = 30 * time.Second
	// purgeGiveUp bounds how long a delete may be held open for the purge: a
	// store that is down must not leave the user with a Webhook they cannot
	// get rid of. Past it the finalizer is released and the orphaned rows are
	// logged for an operator to collect. The same bound the Agent purge uses.
	purgeGiveUp = 10 * time.Minute
)

// Reconciler mints a Webhook's inbound path, keeps its route list, reports
// spec validity, and purges its delivery sink on delete.
type Reconciler struct {
	Manager mcmanager.Manager
	// WebhookKey signs inbound URLs. It is the same key the HTTP layer checks
	// with (see internal/webhookpath.Key). Empty means the provider has no
	// key configured: no URL can be minted, and — crucially — a path already
	// stored is left alone rather than cleared, so a misconfigured replica
	// cannot revoke every existing URL.
	WebhookKey []byte
	// PurgeDeliveries empties one webhook's sink in the provider store. Nil
	// (the in-memory dev path without an executor) disables the finalizer
	// outright: one nothing could clear would make every webhook undeletable.
	PurgeDeliveries func(ctx context.Context, clusterID, webhookUID string) error

	// now is the clock the purge deadline is measured on; nil is time.Now.
	now func() time.Time
}

func (r *Reconciler) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// SetupWithManager wires the reconciler into the multicluster manager.
// Triggers are watched so that a trigger naming this webhook — or no longer
// naming it — refreshes status.routes without the Webhook being touched.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("agents-webhook").
		For(&agentsv1alpha1.Webhook{}).
		Watches(&agentsv1alpha1.Trigger{}, webhookForTrigger).
		Complete(r)
}

// webhookForTrigger enqueues the Webhook a Trigger routes from. On an update
// the map runs for the old and the new object alike, so moving a trigger from
// one webhook to another refreshes both route lists.
func webhookForTrigger(clusterName multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
		return WebhookRequestForTrigger(obj)
	}))(clusterName, cl)
}

// WebhookRequestForTrigger maps a Trigger to the Webhook its spec.webhookRef
// names, or to nothing.
func WebhookRequestForTrigger(obj client.Object) []reconcile.Request {
	trig, ok := obj.(*agentsv1alpha1.Trigger)
	if !ok {
		return nil
	}
	ref := strings.TrimSpace(trig.Spec.WebhookRef)
	if ref == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: ref}}}
}

// Reconcile handles one Webhook.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()

	var wh agentsv1alpha1.Webhook
	if err := c.Get(ctx, req.NamespacedName, &wh); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !wh.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, c, &wh, req.ClusterName.String())
	}
	if r.PurgeDeliveries != nil && !controllerutil.ContainsFinalizer(&wh, agentsv1alpha1.WebhookDeliveriesFinalizer) {
		controllerutil.AddFinalizer(&wh, agentsv1alpha1.WebhookDeliveriesFinalizer)
		if err := c.Update(ctx, &wh); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, err
		}
		// The metadata write re-delivers the object; status is written from
		// the fresh copy rather than the stale one in hand.
		return ctrl.Result{}, nil
	}

	routes, err := r.routes(ctx, c, wh.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	changed := r.reconcileWebhookPath(req.ClusterName.String(), &wh)
	if !slices.Equal(routes, wh.Status.Routes) {
		wh.Status.Routes = routes
		changed = true
	}
	cond := metav1.Condition{
		Type:               agentsv1alpha1.ConditionValidated,
		Status:             metav1.ConditionTrue,
		Reason:             agentsv1alpha1.ReasonValidated,
		Message:            "the webhook spec is usable",
		ObservedGeneration: wh.Generation,
	}
	if reason, message := validate(&wh); reason != "" {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, reason, message
	}
	if meta.SetStatusCondition(&wh.Status.Conditions, cond) {
		changed = true
	}
	if !changed {
		return ctrl.Result{}, nil
	}
	if err := c.Status().Update(ctx, &wh); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil // the watch re-delivers the newer object
		}
		return ctrl.Result{}, err
	}
	klog.FromContext(ctx).V(2).Info("webhook status written", "webhook", req.Name, "cluster", req.ClusterName,
		"path", wh.Status.WebhookPath != "", "routes", len(wh.Status.Routes))
	return ctrl.Result{}, nil
}

// routes lists the Triggers naming this webhook, sorted by name so an
// unchanged set never rewrites the status.
func (r *Reconciler) routes(ctx context.Context, c client.Reader, name string) ([]agentsv1alpha1.WebhookRoute, error) {
	var list agentsv1alpha1.TriggerList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	var out []agentsv1alpha1.WebhookRoute
	for i := range list.Items {
		if strings.TrimSpace(list.Items[i].Spec.WebhookRef) == name && list.Items[i].DeletionTimestamp.IsZero() {
			out = append(out, agentsv1alpha1.WebhookRoute{Name: list.Items[i].Name})
		}
	}
	slices.SortFunc(out, func(a, b agentsv1alpha1.WebhookRoute) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// reconcileWebhookPath makes status.webhookPath the derived path, and reports
// whether it changed. No key: say nothing rather than clearing a path that
// still works for whoever holds the URL.
func (r *Reconciler) reconcileWebhookPath(clusterID string, wh *agentsv1alpha1.Webhook) bool {
	want := webhookpath.For(r.WebhookKey, clusterID, wh.Name)
	if want == "" || want == wh.Status.WebhookPath {
		return false
	}
	wh.Status.WebhookPath = want
	return true
}

// validate returns the first problem with the webhook, as (reason, message).
// The CRD schema already enforces both rules for a kube write; the condition
// says so for an object written before the schema did, or by a writer that
// skipped it.
func validate(wh *agentsv1alpha1.Webhook) (reason, message string) {
	switch strings.TrimSpace(wh.Spec.Source) {
	case agentsv1alpha1.WebhookSourceWebhook, agentsv1alpha1.WebhookSourceGitHub:
	case "":
		return agentsv1alpha1.ReasonInvalidSpec, "spec.source is required (webhook or github)"
	default:
		return agentsv1alpha1.ReasonInvalidSource,
			fmt.Sprintf("spec.source %q is not supported (use webhook or github)", wh.Spec.Source)
	}
	if n := wh.Spec.DeliveryRetention; n != nil && (*n < 0 || *n > 1000) {
		return agentsv1alpha1.ReasonInvalidSpec,
			fmt.Sprintf("spec.deliveryRetention %d is out of range (0 to 1000)", *n)
	}
	return "", ""
}

// finalize purges the webhook's delivery sink and releases the finalizer.
// The release is unconditional past purgeGiveUp, measured from the deletion
// timestamp, for the reason the Agent's purge gives: an object nobody can
// delete is a worse problem than rows an operator can.
func (r *Reconciler) finalize(ctx context.Context, c client.Client, wh *agentsv1alpha1.Webhook, clusterID string) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("webhook", wh.Name, "cluster", clusterID)
	if !controllerutil.ContainsFinalizer(wh, agentsv1alpha1.WebhookDeliveriesFinalizer) {
		return ctrl.Result{}, nil
	}
	if r.PurgeDeliveries != nil {
		if err := r.PurgeDeliveries(ctx, clusterID, string(wh.UID)); err != nil {
			if r.clock().Sub(wh.DeletionTimestamp.Time) < purgeGiveUp {
				logger.Error(err, "purging the webhook's deliveries; will retry", "retryIn", purgeRetryInterval)
				return ctrl.Result{RequeueAfter: purgeRetryInterval}, nil
			}
			logger.Error(err, "giving up on purging the webhook's deliveries; releasing the finalizer and leaving the rows behind",
				"deadline", purgeGiveUp)
		}
	}
	controllerutil.RemoveFinalizer(wh, agentsv1alpha1.WebhookDeliveriesFinalizer)
	if err := c.Update(ctx, wh); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	logger.V(2).Info("webhook deliveries purged and finalizer released")
	return ctrl.Result{}, nil
}
