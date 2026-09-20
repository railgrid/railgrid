/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package operator

// The far side of the operator's seam.
//
// The reconciler runs on the host cluster, where the InfrastructureProvider CRs
// live, and applies most of its output into a kcp workspace it reaches through
// a kubeconfig carried by the CR. Those applied objects — the provider's
// CatalogEntry and the seed Templates — used to be re-applied on a timer,
// because nothing on the host cluster changes when someone edits or deletes
// them. That is exactly the case the contract says to fix with a watch, not a
// tick (docs/provider-connectivity-contract.md § "Pillar 1 carve-outs").
//
// kcpWatcher is that watch. The workspace is only known per CR and only after
// the kubeconfig Secret resolves, so it cannot be declared on the builder: the
// reconciler registers it the first time it reaches a workspace, and every
// event there enqueues the CR that owns it.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/railgrid/provider-infrastructure/apis/v1alpha1"
)

// watchedKinds are the objects the bootstrap applies into the provider
// workspace that a client exists for, and whose drift must therefore bring the
// CR back through Reconcile: the CatalogEntry that lists the provider, and the
// seed Templates that stock its catalog. The APIExport, its schemas and the
// CachedResource are reached with the same credential but are re-applied as
// part of the same pass, so an edit to any of these three recovers all of them.
var watchedKinds = []schema.GroupVersionKind{
	{Group: "providers.railgrid.ai", Version: "v1alpha1", Kind: "CatalogEntry"},
	{Group: v1alpha1.GroupName, Version: "v1alpha1", Kind: "Template"},
}

// sourceWatcher is the slice of controller-runtime's Controller the watcher
// needs: adding a source to an already-running controller, which starts it
// immediately.
type sourceWatcher interface {
	Watch(src source.TypedSource[reconcile.Request]) error
}

// kcpWatcher starts one cache per provider workspace, the first time a CR
// pointing at it is reconciled, and feeds its events back to that CR. A CR that
// is repointed at a different workspace gets a fresh cache and the old one is
// stopped.
type kcpWatcher struct {
	// base outlives any single reconcile: the caches run for the lifetime of
	// the manager, not of the call that registered them.
	base    context.Context
	watcher sourceWatcher

	mu      sync.Mutex
	entries map[types.NamespacedName]*kcpWatchEntry
}

type kcpWatchEntry struct {
	// identity is the (host, credential) pair the running cache was built
	// with. Both matter: repointing a CR at another workspace and rotating its
	// kubeconfig both leave the existing informers watching with something
	// this CR no longer uses.
	identity string
	cancel   context.CancelFunc
}

// watchIdentity fingerprints the connection a cache was built for. The bearer
// is hashed rather than kept, so rotating a credential invalidates the cache
// without this map holding a second copy of the token.
func watchIdentity(cfg *rest.Config) string {
	return fmt.Sprintf("%s|%x", cfg.Host, sha256.Sum256([]byte(cfg.BearerToken)))
}

func newKCPWatcher(base context.Context, watcher sourceWatcher) *kcpWatcher {
	return &kcpWatcher{base: base, watcher: watcher, entries: map[types.NamespacedName]*kcpWatchEntry{}}
}

// ensure registers the provider-workspace watches for cr if they are not
// running against cfg.Host already. It is idempotent and cheap on the warm
// path (a map lookup).
func (w *kcpWatcher) ensure(ctx context.Context, cr types.NamespacedName, cfg *rest.Config) error {
	if w == nil || cfg == nil {
		return nil
	}
	identity := watchIdentity(cfg)
	w.mu.Lock()
	defer w.mu.Unlock()
	if entry, ok := w.entries[cr]; ok {
		if entry.identity == identity {
			return nil
		}
		// Repointed at another workspace, or re-credentialed: the old cache
		// watches with something this CR no longer uses.
		entry.cancel()
		delete(w.entries, cr)
	}

	// No scheme: every watched kind is read unstructured, so the cache needs
	// only the RESTMapper it builds from cfg.
	workspace, err := cache.New(rest.CopyConfig(cfg), cache.Options{Scheme: runtime.NewScheme()})
	if err != nil {
		return fmt.Errorf("provider workspace cache: %w", err)
	}
	cacheCtx, cancel := context.WithCancel(w.base)
	go func() {
		if err := workspace.Start(cacheCtx); err != nil {
			klog.FromContext(cacheCtx).Error(err, "provider workspace cache stopped", "provider", cr.String())
		}
	}()

	enqueue := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: cr}}
	})
	for _, gvk := range watchedKinds {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		kindSrc := source.Kind[client.Object](workspace, obj, enqueue, predicate.ResourceVersionChangedPredicate{})
		// Non-syncing wrapper: the controller must never block its start (or a
		// later Watch call) waiting for an informer over a workspace that may
		// be briefly unreachable. The Kind source retries on its own.
		src := source.Func(func(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
			return kindSrc.Start(ctx, q)
		})
		if err := w.watcher.Watch(src); err != nil {
			cancel()
			return fmt.Errorf("watch %s in provider workspace: %w", gvk.Kind, err)
		}
	}

	w.entries[cr] = &kcpWatchEntry{identity: identity, cancel: cancel}
	klog.FromContext(ctx).Info("watching provider workspace", "provider", cr.String(), "host", cfg.Host)
	return nil
}

// forget stops the watches for a CR that is gone.
func (w *kcpWatcher) forget(cr types.NamespacedName) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if entry, ok := w.entries[cr]; ok {
		entry.cancel()
		delete(w.entries, cr)
	}
}

// WatchProviderWorkspace starts a cache over the provider workspace and calls
// notify on every add, update or delete of the kinds the bootstrap owns
// (watchedKinds). It returns once the informers are registered; the cache runs
// until ctx is cancelled.
//
// This is the env-driven `operator` mode's half of the same idea as kcpWatcher:
// there is no CR to enqueue, so the caller re-runs its bootstrap pass instead.
// Either way the trigger is an event on the object that drifted, not a tick.
func WatchProviderWorkspace(ctx context.Context, cfg *rest.Config, notify func()) error {
	workspace, err := cache.New(rest.CopyConfig(cfg), cache.Options{Scheme: runtime.NewScheme()})
	if err != nil {
		return fmt.Errorf("provider workspace cache: %w", err)
	}
	for _, gvk := range watchedKinds {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		informer, err := workspace.GetInformer(ctx, obj)
		if err != nil {
			return fmt.Errorf("informer for %s: %w", gvk.Kind, err)
		}
		if _, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
			AddFunc:    func(any) { notify() },
			UpdateFunc: func(_, _ any) { notify() },
			DeleteFunc: func(any) { notify() },
		}); err != nil {
			return fmt.Errorf("event handler for %s: %w", gvk.Kind, err)
		}
	}
	go func() {
		if err := workspace.Start(ctx); err != nil {
			klog.FromContext(ctx).Error(err, "provider workspace cache stopped")
		}
	}()
	return nil
}
