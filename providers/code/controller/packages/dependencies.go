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

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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
