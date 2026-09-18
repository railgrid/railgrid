// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package agent reconciles Agent CRs. An Agent has no host-side resource to
// manage; the reconciler exists so that every Agent reads as Ready. The create
// handler stamps the phase, but agents created before that existed (or whose
// stamp failed) stayed at status {} forever, which reads as "not ready" to
// anyone polling. Suspended (or any other non-empty phase) is left alone.
package agent

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// Reconciler stamps phase Ready on agents that have no phase.
type Reconciler struct {
	Manager mcmanager.Manager
}

// SetupWithManager wires the reconciler into the multicluster manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("agents-agent").
		For(&agentsv1alpha1.Agent{}).
		Complete(r)
}

// Reconcile stamps one Agent.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()

	var agent agentsv1alpha1.Agent
	if err := c.Get(ctx, req.NamespacedName, &agent); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if agent.Status.Phase != "" || !agent.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	agent.Status.Phase = agentsv1alpha1.AgentPhaseReady
	if err := c.Status().Update(ctx, &agent); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil // the watch re-delivers the newer object
		}
		return ctrl.Result{}, err
	}
	klog.FromContext(ctx).V(2).Info("agent marked Ready", "agent", req.Name, "cluster", req.ClusterName)
	return ctrl.Result{}, nil
}
