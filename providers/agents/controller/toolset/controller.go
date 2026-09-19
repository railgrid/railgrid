// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package toolset reconciles Toolset CRs — shared bundles of tool grants that
// many Agents link.
//
// Two things nobody was doing before:
//
//   - status.usedBy. The field has been in the API and on the portal's toolset
//     list since the kind existed, and nothing has ever written it: every
//     toolset showed "used by 0 agents" however many agents linked it. It is
//     the one number that tells a user whether deleting a toolset is safe, so
//     it is computed here from the Agents that actually reference it.
//
//   - The Validated condition. A toolset's families and connections are
//     resolved silently at run time (api/toolset.go merges them into the
//     agent's grant and skips what it cannot dial), so a typo in a family name
//     or a connection deleted out from under the bundle costs the agent a tool
//     with nothing anywhere saying why. The REST create/update handlers
//     (api/toolsets_http.go: applyToolsetCreate / applyToolsetUpdate) never
//     checked either — they only required a name — so this is a check the
//     provider gains rather than one it is keeping; the run-time behaviour is
//     unchanged, it is now merely explained.
package toolset

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
)

// Reconciler keeps a Toolset's usedBy count and Validated condition true.
type Reconciler struct {
	Manager mcmanager.Manager
}

// SetupWithManager wires the reconciler into the multicluster manager.
//
// Agents are watched because usedBy is a fact about them, not about the
// toolset: linking or unlinking a bundle changes an Agent and must be
// reflected without anyone touching the Toolset. Connections are watched
// because a bundle's connection refs are only valid while they exist.
//
// Both map to every Toolset in the cluster rather than to the ones named. An
// Agent update carries only the new object, so the toolset it just stopped
// referencing is not in it — mapping narrowly would leave that one's count
// permanently one too high. Toolsets are few per workspace and a reconcile
// that finds nothing changed writes nothing, so the coarse fan-out costs a
// list and settles in one round.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("agents-toolset").
		For(&agentsv1alpha1.Toolset{}).
		Watches(&agentsv1alpha1.Agent{}, allToolsets).
		Watches(&agentsv1alpha1.Connection{}, allToolsets).
		Complete(r)
}

// allToolsets enqueues every Toolset in the cluster the event came from.
func allToolsets(clusterName multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list agentsv1alpha1.ToolsetList
		if err := cl.GetClient().List(ctx, &list); err != nil {
			klog.FromContext(ctx).Error(err, "listing toolsets to re-reconcile")
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for i := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
		}
		return reqs
	}))(clusterName, cl)
}

// Reconcile handles one Toolset.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()

	var ts agentsv1alpha1.Toolset
	if err := c.Get(ctx, req.NamespacedName, &ts); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !ts.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	usedBy, err := countUsedBy(ctx, c, ts.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	reason, message, err := validate(ctx, c, &ts)
	if err != nil {
		// A failed read is not a verdict: leave the condition as it stands and
		// let the retry settle it.
		return ctrl.Result{}, err
	}

	changed := false
	if ts.Status.UsedBy != usedBy {
		ts.Status.UsedBy = usedBy
		changed = true
	}
	if ts.Status.ObservedGeneration != ts.Generation {
		ts.Status.ObservedGeneration = ts.Generation
		changed = true
	}
	cond := metav1.Condition{
		Type:               agentsv1alpha1.ConditionValidated,
		Status:             metav1.ConditionTrue,
		Reason:             agentsv1alpha1.ReasonValidated,
		Message:            "the toolset spec is usable",
		ObservedGeneration: ts.Generation,
	}
	if reason != "" {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, reason, message
	}
	if meta.SetStatusCondition(&ts.Status.Conditions, cond) {
		changed = true
	}
	if !changed {
		return ctrl.Result{}, nil
	}
	if err := c.Status().Update(ctx, &ts); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil // the watch re-delivers the newer object
		}
		return ctrl.Result{}, err
	}
	klog.FromContext(ctx).V(2).Info("toolset status written", "toolset", req.Name, "cluster", req.ClusterName, "usedBy", usedBy)
	return ctrl.Result{}, nil
}

// countUsedBy counts the Agents linking this toolset from either run class.
// An agent that names the same toolset in both its interactive and background
// grants is one user, not two — the number answers "who would notice if this
// went away".
func countUsedBy(ctx context.Context, c client.Client, name string) (int32, error) {
	var agents agentsv1alpha1.AgentList
	if err := c.List(ctx, &agents); err != nil {
		return 0, err
	}
	var n int32
	for i := range agents.Items {
		a := &agents.Items[i]
		if !a.DeletionTimestamp.IsZero() {
			continue
		}
		if links(a.Spec.Tools.Interactive.Toolsets, name) || links(a.Spec.Tools.Background.Toolsets, name) {
			n++
		}
	}
	return n, nil
}

func links(refs []string, name string) bool {
	for _, ref := range refs {
		if strings.TrimSpace(ref) == name {
			return true
		}
	}
	return false
}

// validate returns the first problem with the bundle, as (reason, message).
// An error is a failed read, not a verdict.
func validate(ctx context.Context, c client.Client, ts *agentsv1alpha1.Toolset) (reason, message string, err error) {
	if strings.TrimSpace(ts.Name) == "" {
		return agentsv1alpha1.ReasonInvalidSpec, "the toolset has no name", nil
	}
	for _, f := range ts.Spec.Families {
		f = strings.TrimSpace(f)
		if f == "" {
			return agentsv1alpha1.ReasonInvalidSpec, "spec.families contains an empty entry", nil
		}
		if !agentsv1alpha1.KnownToolFamilies[f] {
			return agentsv1alpha1.ReasonUnknownToolFamily, fmt.Sprintf("spec.families contains unknown family %q", f), nil
		}
	}
	for _, ref := range ts.Spec.Connections {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return agentsv1alpha1.ReasonInvalidSpec, "spec.connections contains an empty entry", nil
		}
		var conn agentsv1alpha1.Connection
		switch err := c.Get(ctx, types.NamespacedName{Name: ref}, &conn); {
		case apierrors.IsNotFound(err):
			return agentsv1alpha1.ReasonUnknownConnectionRef,
				fmt.Sprintf("spec.connections names connection %q, which does not exist", ref), nil
		case err != nil:
			return "", "", err
		}
	}
	return "", "", nil
}
