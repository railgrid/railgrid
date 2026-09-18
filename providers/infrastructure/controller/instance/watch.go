// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package instance

// Event sources beyond the Instance itself. The reconcile used to discover
// every change on the far side of the seam by timer — a short status-mirror
// poll against the runtime cluster, a per-reconcile Template GET and a
// per-pass tenant Secret GET. Each of those is now a watch that maps its
// events back onto Instance requests:
//
//   - runtime CRs (per-template GVR, dynamic) → the Instance that owns them,
//     through the annotations syncRuntime stamps (runtimeWatches, mapRuntimeObject);
//   - Templates in the provider workspace → every Instance of that template,
//     across all engaged tenant clusters (instanceIndex, mapTemplate);
//   - tenant Secrets through the APIExport virtual workspace → the Instance(s)
//     whose bridge reads them (mapSecret).
//
// What remains timer-driven is a long safety resync (resyncPeriod) and the
// exact lifecycle deadlines (lifecycleRequeueAfter), which are computed, not
// polled.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
)

// instanceIndex is the cluster → Instance → Template index the reconciler
// maintains for the mappers. Instances live in tenant clusters reached
// through the virtual workspace, so a Template or Secret event can't be
// mapped by a plain List against one cache; the index answers "which
// Instances use template X" and "which Instances in cluster C are named N"
// from what the reconciler has already seen. Every Instance is reconciled
// when its cluster engages (informer initial list), so the index is complete
// once the controller is warm; entries are dropped when the Instance is gone.
type instanceIndex struct {
	mu sync.RWMutex
	// byCluster: cluster name → Instance key → template name.
	byCluster map[multicluster.ClusterName]map[types.NamespacedName]string
}

func newInstanceIndex() *instanceIndex {
	return &instanceIndex{byCluster: map[multicluster.ClusterName]map[types.NamespacedName]string{}}
}

// set records (or refreshes) the template an Instance references.
func (ix *instanceIndex) set(cluster multicluster.ClusterName, key types.NamespacedName, template string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	m, ok := ix.byCluster[cluster]
	if !ok {
		m = map[types.NamespacedName]string{}
		ix.byCluster[cluster] = m
	}
	m[key] = template
}

// remove forgets an Instance that no longer exists.
func (ix *instanceIndex) remove(cluster multicluster.ClusterName, key types.NamespacedName) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	m, ok := ix.byCluster[cluster]
	if !ok {
		return
	}
	delete(m, key)
	if len(m) == 0 {
		delete(ix.byCluster, cluster)
	}
}

// byTemplate returns a request for every indexed Instance of the template,
// across all clusters.
func (ix *instanceIndex) byTemplate(template string) []mcreconcile.Request {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []mcreconcile.Request
	for cluster, m := range ix.byCluster {
		for key, tmpl := range m {
			if tmpl == template {
				out = append(out, request(cluster, key))
			}
		}
	}
	return out
}

// inCluster returns a request for every indexed Instance in the cluster; a
// non-empty name narrows it to the Instances of that name (the Instance kind
// is cluster-scoped today, but the index doesn't assume it).
func (ix *instanceIndex) inCluster(cluster multicluster.ClusterName, name string) []mcreconcile.Request {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []mcreconcile.Request
	for key := range ix.byCluster[cluster] {
		if name == "" || key.Name == name {
			out = append(out, request(cluster, key))
		}
	}
	return out
}

func request(cluster multicluster.ClusterName, key types.NamespacedName) mcreconcile.Request {
	return mcreconcile.Request{ClusterName: cluster, Request: reconcile.Request{NamespacedName: key}}
}

// mapTemplate maps a Template event (in the provider workspace) to every
// Instance referencing it and drops the Template's compiled contract from
// the cache, so the reconciles it triggers recompile against the new spec.
// A Template becoming Ready, changing its schema, or being deleted all land
// here; the per-reconcile Template read is served from the informer cache.
func (c *Controller) mapTemplate(_ context.Context, obj client.Object) []mcreconcile.Request {
	name := obj.GetName()
	c.mu.Lock()
	delete(c.contracts, name)
	c.mu.Unlock()
	return c.index.byTemplate(name)
}

// mapSecret maps a tenant Secret event (through the virtual workspace, so the
// cluster is the tenant's logical cluster) to the Instance(s) whose bridge
// reads it. Only the credentials namespace is relevant:
//
//   - "<instance>-registry" is the per-instance registry pull Secret App
//     Studio mints at promote (bridgeRegistryPullSecret) → that Instance;
//   - "cloud-credentials" is the workspace-wide BYO OIDC Secret
//     (bridgeBYOSecret) → every Instance in the workspace: any of them may
//     be the BYO one, and the reconcile is cheap when it is not.
func (c *Controller) mapSecret(cluster multicluster.ClusterName, obj client.Object) []mcreconcile.Request {
	instance, bridged := c.bridgedSecretTarget(obj)
	if !bridged {
		return nil
	}
	return c.index.inCluster(cluster, instance)
}

// secretPredicate keeps the Secret watch quiet: only the credentials
// namespace and the two bridged names reach the mapper.
func (c *Controller) secretPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		_, bridged := c.bridgedSecretTarget(obj)
		return bridged
	})
}

// bridgedSecretTarget reports whether the bridge reads this tenant Secret
// and, when it is a per-instance one, which Instance it belongs to (empty
// for the workspace-wide cloud-credentials Secret).
func (c *Controller) bridgedSecretTarget(obj client.Object) (instance string, bridged bool) {
	if obj.GetNamespace() != c.cfg.CredentialsNamespace {
		return "", false
	}
	name := obj.GetName()
	if name == cloudCredentialsSecret {
		return "", true
	}
	if inst, ok := strings.CutSuffix(name, registryPullSecretName("")); ok && inst != "" {
		return inst, true
	}
	return "", false
}

// mapRuntimeObject maps a runtime-cluster CR event back to the tenant
// Instance it was materialized from, through the annotations syncRuntime
// stamps. A CR without them (written before the annotations existed, or by
// someone else) maps to nothing; the safety resync converges its annotations
// on the next pass of the owning Instance.
func mapRuntimeObject(_ context.Context, obj client.Object) []mcreconcile.Request {
	ann := obj.GetAnnotations()
	cluster := ann[infrav1alpha1.RailgridInstanceClusterAnnotation]
	name := ann[infrav1alpha1.RailgridInstanceNameAnnotation]
	if cluster == "" || name == "" {
		return nil
	}
	return []mcreconcile.Request{request(multicluster.ClusterName(cluster), types.NamespacedName{
		Namespace: ann[infrav1alpha1.RailgridInstanceNamespaceAnnotation],
		Name:      name,
	})}
}

// instanceAnnotations are the annotations syncRuntime stamps on the runtime
// CR so mapRuntimeObject can invert it.
func instanceAnnotations(tenant string, inst *unstructured.Unstructured) map[string]string {
	out := map[string]string{
		infrav1alpha1.RailgridInstanceClusterAnnotation: tenant,
		infrav1alpha1.RailgridInstanceNameAnnotation:    inst.GetName(),
	}
	if ns := inst.GetNamespace(); ns != "" {
		out[infrav1alpha1.RailgridInstanceNamespaceAnnotation] = ns
	}
	return out
}

// sourceWatcher is the slice of the controller the registrar needs: adding
// a source, before or after the controller started. controller-runtime
// starts a source added to a running controller immediately.
type sourceWatcher interface {
	Watch(src source.TypedSource[mcreconcile.Request]) error
}

// runtimeWatchRegistrar starts one watch per runtime GVR on the runtime
// cluster's cache, the first time an Instance of a Template with that GVR
// is reconciled (or finalized), and never twice. The set of GVRs is dynamic —
// each Template declares its own instanceCRD — so they can't be declared on
// the builder up front.
//
// Registration is deliberately non-blocking: the source is wrapped so the
// controller never waits for the informer to sync (the CRD behind a GVR is
// established by kro asynchronously; controller-runtime's Kind source polls
// until it exists).
//
// TODO: watches are never unregistered. controller-runtime informers can be
// removed from a cache (cache.RemoveInformer), but the controller keeps no
// handle on the started source; dropping a GVR when its Template is deleted
// would need a cache restart or a per-GVR context. A retired Template's
// informer idles on an empty (or absent) resource, which is cheap, so this
// is left for a later change.
type runtimeWatchRegistrar struct {
	watcher sourceWatcher
	cache   cache.Cache

	mu         sync.Mutex
	registered map[schema.GroupVersionResource]struct{}
}

func newRuntimeWatchRegistrar(watcher sourceWatcher, cache cache.Cache) *runtimeWatchRegistrar {
	return &runtimeWatchRegistrar{
		watcher:    watcher,
		cache:      cache,
		registered: map[schema.GroupVersionResource]struct{}{},
	}
}

// ensure registers the watch for gvr/kind if it isn't yet. Returns whether
// this call registered it.
func (r *runtimeWatchRegistrar) ensure(ctx context.Context, gvr schema.GroupVersionResource, kind string) (bool, error) {
	if gvr.Resource == "" || kind == "" {
		return false, fmt.Errorf("runtime watch: incomplete target %s kind %q", gvr, kind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.registered[gvr]; ok {
		return false, nil
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
	kindSrc := source.TypedKind[client.Object, mcreconcile.Request](
		r.cache, obj,
		handler.TypedEnqueueRequestsFromMapFunc[client.Object, mcreconcile.Request](mapRuntimeObject),
		predicate.ResourceVersionChangedPredicate{},
	)
	// Non-syncing wrapper: never hold the controller's start on a CRD kro
	// hasn't established yet. The Kind source retries the informer lookup
	// itself.
	src := source.TypedFunc[mcreconcile.Request](func(ctx context.Context, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
		return kindSrc.Start(ctx, q)
	})
	if err := r.watcher.Watch(src); err != nil {
		return false, fmt.Errorf("runtime watch %s: %w", gvr, err)
	}
	r.registered[gvr] = struct{}{}
	klog.FromContext(ctx).Info("watching runtime resource", "gvr", gvr.String(), "kind", kind)
	return true, nil
}

// templateReady reports whether the Template controller marked the Template
// Ready — the RGD is applied and the runtime CRD is expected to exist, so
// the runtime GVR is worth watching.
func templateReady(tmpl *infrav1alpha1.Template) bool {
	for _, cond := range tmpl.Status.Conditions {
		if cond.Type == infrav1alpha1.ConditionReady {
			return cond.Status == "True"
		}
	}
	return false
}
