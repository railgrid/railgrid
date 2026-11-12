// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package run

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// ErrAwaitingChildRuns reports that a terminal Run's descendants were asked to
// stop and have not settled yet. It is not a failure: the parent is enqueued
// again by parentOfSettledChild when a child settles, so the reconciler does
// not poll for it.
var ErrAwaitingChildRuns = errors.New("child runs have not settled yet")

// onlyAwaitingChildren reports whether err consists solely of
// ErrAwaitingChildRuns, so a real failure joined with it still backs off.
func onlyAwaitingChildren(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if !onlyAwaitingChildren(e) {
				return false
			}
		}
		return true
	}
	if errors.Is(err, ErrAwaitingChildRuns) {
		return true
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return onlyAwaitingChildren(wrapped)
	}
	return false
}

// parentOfSettledChild enqueues the parent of a Run that has just reached a
// terminal phase, in the child's own cluster. That is the event a terminal
// parent waits on while its descendants are being cancelled.
func parentOfSettledChild() mchandler.EventHandlerFunc {
	return func(name multicluster.ClusterName, _ cluster.Cluster) mchandler.EventHandler {
		return mchandler.ForCluster(handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			child, ok := obj.(*agentsv1alpha1.Run)
			if !ok || child.Spec.ParentRunRef == "" {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: child.Spec.ParentRunRef}}}
		}), name)
	}
}

// childSettled passes only updates that move a child Run into a terminal phase.
// Runs listed at informer start are reconciled themselves, and a terminal
// parent reconciles its descendants then.
var childSettled = predicate.Funcs{ //nolint:gochecknoglobals // immutable event filter
	CreateFunc:  func(event.CreateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldRun, okOld := e.ObjectOld.(*agentsv1alpha1.Run)
		newRun, okNew := e.ObjectNew.(*agentsv1alpha1.Run)
		return okOld && okNew && newRun.Spec.ParentRunRef != "" &&
			!isTerminalPhase(oldRun.Status.Phase) && isTerminalPhase(newRun.Status.Phase)
	},
}
