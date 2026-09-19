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
// status.webhookPath is the point of this reconciler. A Trigger is useless
// until it has one: the path is the trigger's whole inbound interface and its
// only credential (an HMAC over cluster/name, so there is no token to store
// and none to leak from the CR). It used to be minted by the REST create
// handler, api/triggers.go applyTriggerCreate, as a side effect of the write —
// which meant a Trigger that arrived any other way was born inert, with no
// URL, and nothing to tell its author why nothing ever fired. The portal now
// creates Triggers with a kube client, so that "any other way" is the normal
// way, and the mint moved here where it happens for every writer.
//
// The two mints must agree exactly, because a trigger's URL is pasted into
// somebody else's system the day it is created and every later reconcile has
// to re-derive the same string or break it. Both go through
// internal/webhookpath, whose golden vectors pin the derivation.
// api/triggers.go still stamps the path on its own writes; converging on the
// same value is not a fight.
//
// The rest is validation, reported as the Validated condition rather than
// enforced: spec.source must be one the provider actually listens for, and
// spec.agentRef must name an Agent that exists — a dangling ref means the
// event arrives, finds no agent, and is dropped silently.
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
	"github.com/railgrid/provider-agents/internal/webhookpath"
)

// Reconciler mints and maintains a Trigger's inbound webhook path and reports
// spec validity.
type Reconciler struct {
	Manager mcmanager.Manager
	// WebhookKey signs inbound trigger URLs. It is the same key the HTTP
	// layer signs with (see internal/webhookpath.Key). Empty means the
	// provider has no key configured: no URL can be minted, and — crucially —
	// a path already stored is left alone rather than cleared, so a
	// misconfigured replica cannot revoke every existing trigger URL.
	WebhookKey []byte
}

// SetupWithManager wires the reconciler into the multicluster manager. Agents
// are watched so that creating the missing Agent clears an UnknownAgentRef
// without the Trigger being touched.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("agents-trigger").
		For(&agentsv1alpha1.Trigger{}).
		Watches(&agentsv1alpha1.Agent{}, triggersForAgent).
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

	reason, message, err := r.validate(ctx, c, &trig)
	if err != nil {
		// A failed read is not a verdict.
		return ctrl.Result{}, err
	}

	changed := r.reconcileWebhookPath(req.ClusterName.String(), &trig)
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
	if meta.SetStatusCondition(&trig.Status.Conditions, cond) {
		changed = true
	}
	if !changed {
		return ctrl.Result{}, nil
	}
	if err := c.Status().Update(ctx, &trig); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil // the watch re-delivers the newer object
		}
		return ctrl.Result{}, err
	}
	klog.FromContext(ctx).V(2).Info("trigger status written", "trigger", req.Name, "cluster", req.ClusterName, "webhook", trig.Status.WebhookPath != "")
	return ctrl.Result{}, nil
}

// reconcileWebhookPath makes status.webhookPath match the spec, and reports
// whether it changed.
//
// Both supported sources are delivered over the same hub-routed inbound
// endpoint (github events arrive as a webhook like any other), so both want a
// path; a source outside that set wants none, and a path left behind from a
// previous source must go — it would keep firing the trigger from an endpoint
// the spec no longer describes.
func (r *Reconciler) reconcileWebhookPath(clusterID string, trig *agentsv1alpha1.Trigger) bool {
	wantsWebhook := trig.Spec.Source == agentsv1alpha1.TriggerSourceWebhook || trig.Spec.Source == agentsv1alpha1.TriggerSourceGitHub
	if !wantsWebhook {
		if trig.Status.WebhookPath == "" {
			return false
		}
		trig.Status.WebhookPath = ""
		return true
	}
	want := webhookpath.For(r.WebhookKey, clusterID, trig.Name)
	// No key: say nothing rather than clearing a path that still works for
	// whoever holds the URL.
	if want == "" || want == trig.Status.WebhookPath {
		return false
	}
	trig.Status.WebhookPath = want
	return true
}

// validate returns the first problem with the trigger, as (reason, message).
// An error is a failed read, not a verdict. Mirrors what applyTriggerCreate /
// applyTriggerUpdate rejected at the door.
func (r *Reconciler) validate(ctx context.Context, c client.Client, trig *agentsv1alpha1.Trigger) (reason, message string, err error) {
	agentRef := strings.TrimSpace(trig.Spec.AgentRef)
	if agentRef == "" {
		return agentsv1alpha1.ReasonInvalidSpec, "spec.agentRef is required", nil
	}
	switch strings.TrimSpace(trig.Spec.Source) {
	case agentsv1alpha1.TriggerSourceWebhook, agentsv1alpha1.TriggerSourceGitHub:
	case "":
		return agentsv1alpha1.ReasonInvalidSpec, "spec.source is required", nil
	default:
		return agentsv1alpha1.ReasonInvalidSource,
			fmt.Sprintf("spec.source %q is not supported (use webhook or github)", trig.Spec.Source), nil
	}

	var agent agentsv1alpha1.Agent
	switch err := c.Get(ctx, types.NamespacedName{Name: agentRef}, &agent); {
	case apierrors.IsNotFound(err):
		return agentsv1alpha1.ReasonUnknownAgentRef,
			fmt.Sprintf("spec.agentRef names agent %q, which does not exist", agentRef), nil
	case err != nil:
		return "", "", err
	}

	if ref := strings.TrimSpace(trig.Spec.ConnectionRef); ref != "" {
		var conn agentsv1alpha1.Connection
		switch err := c.Get(ctx, types.NamespacedName{Name: ref}, &conn); {
		case apierrors.IsNotFound(err):
			return agentsv1alpha1.ReasonUnknownConnectionRef,
				fmt.Sprintf("spec.connectionRef names connection %q, which does not exist", ref), nil
		case err != nil:
			return "", "", err
		}
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
	return "", "", nil
}
