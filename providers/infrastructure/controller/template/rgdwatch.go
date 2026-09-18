/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package template

// Runtime-cluster watch on the ResourceGraphDefinitions the kro backend
// authors. kro accepts or rejects an RGD asynchronously (status.state
// Active/Inactive, with GraphAccepted / KindReady / ControllerReady
// conditions); without a watch that verdict only reached the Template's
// BackendReady condition on the next unrelated Template event. The RGD is
// mapped back to its Template by the railgrid.ai/template label the backend
// sets, falling back to the RGD name (the backend names them 1:1).

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/railgrid/provider-infrastructure/kro"
)

// rgdGVK is kro's ResourceGraphDefinition kind on the runtime cluster.
var rgdGVK = schema.GroupVersionKind{Group: "kro.run", Version: "v1alpha1", Kind: "ResourceGraphDefinition"}

const (
	// rgdManagedByLabel / rgdManagedByValue is how the kro backend marks the
	// RGDs it authors (backend/kro/rgd.go); hand-applied RGDs on the runtime
	// cluster are not ours to map.
	rgdManagedByLabel = "app.kubernetes.io/managed-by"
	rgdManagedByValue = "railgrid-infrastructure"
)

// rgdSource builds the RGD watch over the runtime cluster cache. It is
// wrapped non-syncing so the Template controller's start never waits on the
// kro CRDs being installed on the runtime cluster: the Kind source keeps
// polling for the informer until they are.
func rgdSource(runtimeCache cache.Cache) source.TypedSource[reconcile.Request] {
	rgd := &unstructured.Unstructured{}
	rgd.SetGroupVersionKind(rgdGVK)
	kindSrc := source.TypedKind[client.Object, reconcile.Request](
		runtimeCache, rgd,
		handler.TypedEnqueueRequestsFromMapFunc[client.Object, reconcile.Request](mapRGDToTemplate),
		predicate.NewPredicateFuncs(railgridRGD),
	)
	return source.TypedFunc[reconcile.Request](func(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
		return kindSrc.Start(ctx, q)
	})
}

// railgridRGD reports whether the RGD was authored by this provider.
func railgridRGD(obj client.Object) bool {
	return obj.GetLabels()[rgdManagedByLabel] == rgdManagedByValue
}

// mapRGDToTemplate maps an RGD event to the Template it was derived from.
func mapRGDToTemplate(_ context.Context, obj client.Object) []reconcile.Request {
	if !railgridRGD(obj) {
		return nil
	}
	name := obj.GetLabels()[kro.LabelTemplate]
	if name == "" {
		name = obj.GetName()
	}
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name}}}
}
