// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package trigger reconciles Trigger CRs — the event-driven half of agent
// automation.
//
// A Trigger owns no URL any more: it routes from a Webhook (spec.webhookRef),
// which owns the inbound path and the delivery sink, so this reconciler
// validates — and migrates a trigger from before the Webhook kind to a
// Webhook of its own name (migrateToWebhook), since its URL is already in
// use. What it reports is the Validated condition, not an enforcement: a
// trigger with a bad spec is left to fail visibly rather than refused.
//
// Each check is a way for a trigger to look configured and never fire:
// spec.agentRef naming no Agent (the event arrives, finds no agent, and is
// dropped), spec.webhookRef naming no Webhook (nothing is ever routed to it),
// a channelRef the agent does not have (it fires, but answers elsewhere), and
// a spec.filter key the matcher does not know — ignored at delivery time, by
// design, so a typo silently filters nothing.
package trigger

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/internal/triggerfilter"
)

// Reconciler reports a Trigger's spec validity.
type Reconciler struct {
	Manager mcmanager.Manager
}

// SetupWithManager wires the reconciler into the multicluster manager. Agents
// and Webhooks are watched so that creating the missing one clears an
// UnknownAgentRef or UnknownWebhookRef without the Trigger being touched.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("agents-trigger").
		For(&agentsv1alpha1.Trigger{}).
		Watches(&agentsv1alpha1.Agent{}, triggersForAgent).
		Watches(&agentsv1alpha1.Webhook{}, triggersForWebhook).
		Complete(r)
}

// triggersForAgent enqueues the Triggers whose agentRef names the Agent the
// event came from.
func triggersForAgent(clusterName multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list agentsv1alpha1.TriggerList
		if err := cl.GetClient().List(ctx, &list); err != nil {
			klog.FromContext(ctx).Error(err, "listing triggers to re-validate")
			return nil
		}
		var reqs []reconcile.Request
		for i := range list.Items {
			if strings.TrimSpace(list.Items[i].Spec.AgentRef) == obj.GetName() {
				reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
			}
		}
		return reqs
	}))(clusterName, cl)
}

// triggersForWebhook enqueues the Triggers whose webhookRef names the Webhook
// that changed. A trigger with a dangling webhookRef otherwise stays marked
// invalid after the missing Webhook is created.
func triggersForWebhook(clusterName multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return TriggerRequestsForWebhook(ctx, cl.GetClient(), obj.GetName())
	}))(clusterName, cl)
}

// TriggerRequestsForWebhook lists the Triggers routing from the named Webhook,
// as reconcile requests. Exported because the Webhook reconciler maps Trigger
// events the other way and both must agree on what "routes from" means.
func TriggerRequestsForWebhook(ctx context.Context, c client.Reader, webhookName string) []reconcile.Request {
	var list agentsv1alpha1.TriggerList
	if err := c.List(ctx, &list); err != nil {
		klog.FromContext(ctx).Error(err, "listing triggers to re-validate")
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if strings.TrimSpace(list.Items[i].Spec.WebhookRef) == webhookName {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
		}
	}
	return reqs
}

// Reconcile handles one Trigger.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()

	var trig agentsv1alpha1.Trigger
	if err := c.Get(ctx, req.NamespacedName, &trig); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !trig.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if strings.TrimSpace(trig.Spec.WebhookRef) == "" {
		migrated, err := r.migrateToWebhook(ctx, c, &trig)
		if err != nil {
			return ctrl.Result{}, err
		}
		if migrated {
			// The spec write re-delivers the object; validation runs on the
			// fresh copy with the reference in place.
			return ctrl.Result{}, nil
		}
	}

	reason, message, err := r.validate(ctx, c, &trig)
	if err != nil {
		// A failed read is not a verdict.
		return ctrl.Result{}, err
	}

	cond := metav1.Condition{
		Type:               agentsv1alpha1.ConditionValidated,
		Status:             metav1.ConditionTrue,
		Reason:             agentsv1alpha1.ReasonValidated,
		Message:            "the trigger spec is usable",
		ObservedGeneration: trig.Generation,
	}
	if reason != "" {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, reason, message
	}
	if !meta.SetStatusCondition(&trig.Status.Conditions, cond) {
		return ctrl.Result{}, nil
	}
	if err := c.Status().Update(ctx, &trig); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil // the watch re-delivers the newer object
		}
		return ctrl.Result{}, err
	}
	klog.FromContext(ctx).V(2).Info("trigger status written", "trigger", req.Name, "cluster", req.ClusterName, "validated", cond.Status)
	return ctrl.Result{}, nil
}

// migrateToWebhook gives a Trigger written before the Webhook kind existed the
// Webhook its URL now belongs to. Such a trigger owned its URL — derived from
// the cluster and ITS name — and that URL is already pasted into GitHub, so
// the Webhook is created under the trigger's name (webhookpath keys the path
// by name: same URL) and spec.webhookRef is pointed at it. Until this runs the
// trigger's URL answers 404 and every delivery sent to it is lost, which is
// why the reconciler does it rather than a release note asking the tenant to.
//
// Idempotent: a Webhook already there (this reconciler's earlier pass, or the
// tenant's own) is adopted as it stands, and only the reference is written.
// The sender flavour is not knowable any more — the legacy spec.source is
// pruned from the stored object — so the Webhook is created as a generic
// webhook; a github one is a one-field edit in the portal and changes only
// which event catalog the filter editor offers. Reports whether the trigger's
// spec was written.
func (r *Reconciler) migrateToWebhook(ctx context.Context, c client.Client, trig *agentsv1alpha1.Trigger) (bool, error) {
	wh := agentsv1alpha1.Webhook{ObjectMeta: metav1.ObjectMeta{Name: trig.Name}}
	switch err := c.Get(ctx, types.NamespacedName{Name: trig.Name}, &wh); {
	case apierrors.IsNotFound(err):
		wh.Spec.Source = agentsv1alpha1.WebhookSourceWebhook
		wh.Spec.DisplayName = trig.Name
		if err := c.Create(ctx, &wh); err != nil && !apierrors.IsAlreadyExists(err) {
			return false, fmt.Errorf("creating webhook %q for trigger %q: %w", trig.Name, trig.Name, err)
		}
	case err != nil:
		return false, err
	}
	trig.Spec.WebhookRef = trig.Name
	if err := c.Update(ctx, trig); err != nil {
		if apierrors.IsConflict(err) {
			return true, nil // the watch re-delivers the newer object
		}
		return false, fmt.Errorf("pointing trigger %q at webhook %q: %w", trig.Name, trig.Name, err)
	}
	klog.FromContext(ctx).Info("trigger migrated to a webhook of its own name", "trigger", trig.Name)
	return true, nil
}

// validate returns the first problem with the trigger, as (reason, message).
// An error is a failed read, not a verdict.
func (r *Reconciler) validate(ctx context.Context, c client.Client, trig *agentsv1alpha1.Trigger) (reason, message string, err error) {
	agentRef := strings.TrimSpace(trig.Spec.AgentRef)
	if agentRef == "" {
		return agentsv1alpha1.ReasonInvalidSpec, "spec.agentRef is required", nil
	}
	webhookRef := strings.TrimSpace(trig.Spec.WebhookRef)
	if webhookRef == "" {
		// Reached only when migrateToWebhook could not write the reference
		// (a conflict, say); the re-delivered object gets another pass.
		return agentsv1alpha1.ReasonMissingWebhookRef, "spec.webhookRef is required: name the Webhook this trigger routes from", nil
	}

	var agent agentsv1alpha1.Agent
	switch err := c.Get(ctx, types.NamespacedName{Name: agentRef}, &agent); {
	case apierrors.IsNotFound(err):
		return agentsv1alpha1.ReasonUnknownAgentRef,
			fmt.Sprintf("spec.agentRef names agent %q, which does not exist", agentRef), nil
	case err != nil:
		return "", "", err
	}

	var webhook agentsv1alpha1.Webhook
	switch err := c.Get(ctx, types.NamespacedName{Name: webhookRef}, &webhook); {
	case apierrors.IsNotFound(err):
		return agentsv1alpha1.ReasonUnknownWebhookRef,
			fmt.Sprintf("spec.webhookRef names webhook %q, which does not exist", webhookRef), nil
	case err != nil:
		return "", "", err
	}

	// A channelRef must name one of the agent's channels. Unlike the refs
	// above this one degrades rather than breaks — ResolveChannelConnection
	// falls back to the primary channel — so the trigger still fires; it just
	// answers somewhere the author did not ask for, which is worth saying.
	if role := strings.TrimSpace(trig.Spec.ChannelRef); role != "" {
		found := false
		for _, ch := range agent.Spec.Channels {
			if strings.TrimSpace(ch.Name) == role {
				found = true
				break
			}
		}
		if !found {
			return agentsv1alpha1.ReasonInvalidSpec,
				fmt.Sprintf("spec.channelRef %q is not a channel of agent %q; output falls back to the primary channel", role, agentRef), nil
		}
	}

	// Unknown filter keys last: the trigger works, it just filters less than
	// its author thinks.
	if unknown := triggerfilter.UnknownKeys(trig.Spec.Filter); len(unknown) > 0 {
		return agentsv1alpha1.ReasonUnknownFilterKey,
			fmt.Sprintf("spec.filter key(s) %s are not filter keys and are ignored (known: %s)",
				strings.Join(unknown, ", "), strings.Join(triggerfilter.KnownKeys, ", ")), nil
	}
	return "", "", nil
}
