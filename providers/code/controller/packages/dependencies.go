/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package packages

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/tenant"
)

// Dependency events enter the normal workqueue immediately, even if a previous
// reconcile scheduled a long GitHub reset delay. Bind both the client and the
// queue to the watched cluster; event contexts need not contain a cluster name.
func dependencyHandler(secret bool) mchandler.EventHandlerFunc {
	return func(name multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
		return mchandler.ForCluster(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			return repositoriesForDependency(ctx, cl.GetClient(), obj, secret)
		}), name)
	}
}

// commitHandler enqueues a RepositoryCommit's target Repository, starting its
// post-commit fast crawl window immediately instead of at the next crawl.
func commitHandler() mchandler.EventHandlerFunc {
	return func(name multicluster.ClusterName, _ cluster.Cluster) mchandler.EventHandler {
		return mchandler.ForCluster(handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			commit, ok := obj.(*codev1alpha1.RepositoryCommit)
			if !ok || commit.Spec.RepositoryRef == "" {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: commit.Spec.RepositoryRef}}}
		}), name)
	}
}

// commitSucceeded passes only updates that move a RepositoryCommit to
// Succeeded. Commits listed at informer start are covered by the normal
// Repository reconcile, which checks for recent commits itself.
var commitSucceeded = predicate.Funcs{
	CreateFunc:  func(event.CreateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldCommit, okOld := e.ObjectOld.(*codev1alpha1.RepositoryCommit)
		newCommit, okNew := e.ObjectNew.(*codev1alpha1.RepositoryCommit)
		return okOld && okNew &&
			oldCommit.Status.Phase != codev1alpha1.RepositoryCommitPhaseSucceeded &&
			newCommit.Status.Phase == codev1alpha1.RepositoryCommitPhaseSucceeded
	},
}

func repositoriesForDependency(ctx context.Context, c client.Client, obj client.Object, secret bool) []reconcile.Request {
	connections := map[string]bool{obj.GetName(): true}
	if secret {
		var list codev1alpha1.ConnectionList
		if err := c.List(ctx, &list); err != nil {
			klog.FromContext(ctx).Error(err, "packages: list credential dependencies")
			return nil
		}
		connections = make(map[string]bool)
		for _, conn := range list.Items {
			ns := conn.Spec.SecretRef.Namespace
			if ns == "" {
				ns = tenant.DefaultCredentialsNamespace()
			}
			if conn.Spec.SecretRef.Name == obj.GetName() && ns == obj.GetNamespace() {
				connections[conn.Name] = true
			}
		}
	}
	if len(connections) == 0 {
		return nil
	}
	var repos codev1alpha1.RepositoryList
	if err := c.List(ctx, &repos); err != nil {
		klog.FromContext(ctx).Error(err, "packages: list repository dependencies")
		return nil
	}
	var requests []reconcile.Request
	for _, repo := range repos.Items {
		if connections[repo.Spec.ConnectionRef] {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&repo)})
		}
	}
	return requests
}
