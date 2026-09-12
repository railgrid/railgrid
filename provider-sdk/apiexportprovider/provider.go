/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package apiexportprovider is the multicluster-runtime provider every faros
// provider uses to reconcile resources in tenant workspaces. It is a drop-in
// for github.com/kcp-dev/multicluster-provider/apiexport with two additions
// that upstream (v0.8.0) lacks:
//
//   - A failed endpoint watch is retried. Upstream starts one watcher per URL
//     in APIExportEndpointSlice.status.endpoints[] from the slice informer's
//     add/update handler; when that watcher fails to start (kcp restarting,
//     the front proxy not yet routing, "connection refused"), it logs once and
//     skips the URL. Nothing runs the handler again until the slice itself
//     changes, so the provider keeps its lease, keeps answering /healthz, and
//     reconciles nothing in any tenant workspace until someone restarts the
//     pod. Here the URL is retried with exponential backoff, capped at
//     DefaultRetryBackoffCap, for as long as the slice publishes it; each
//     attempt is logged, and once it succeeds the engaged clusters reach the
//     manager exactly as they would have on a first-try success.
//
//   - The watch state is observable. Check reports why no tenant workspace is
//     being watched — the provider has not started, the slice has not been
//     seen, it publishes no endpoints, or an endpoint is failing — so a
//     readiness endpoint (see vwhealth.Readiness.Attach) can say so instead of
//     staying green.
//
// The engagement logic is the upstream provider's, lifted rather than wrapped
// because the watched-endpoint table is private there. Behaviour on the
// success path is unchanged; everything else in the upstream module (the
// scoped clusters, wildcard cache, event recorders, lifecycle handlers) is
// imported, not copied.
//
// Derived from github.com/kcp-dev/multicluster-provider pkg/provider and
// apiexport (Copyright The kcp Authors, Apache License 2.0).
package apiexportprovider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/multicluster-runtime/pkg/clusters"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	mcpcache "github.com/kcp-dev/multicluster-provider/pkg/cache"
	mcrecorder "github.com/kcp-dev/multicluster-provider/pkg/events/recorder"
	"github.com/kcp-dev/multicluster-provider/pkg/handlers"
	upstream "github.com/kcp-dev/multicluster-provider/pkg/provider"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

const (
	// DefaultRetryBackoff is the delay before the first retry of a failed
	// endpoint watch. It doubles on every further failure.
	DefaultRetryBackoff = time.Second
	// DefaultRetryBackoffCap bounds the delay between retries. Long enough not
	// to hammer a control plane that is coming back, short enough that a
	// provider is reconciling again within a minute of kcp returning.
	DefaultRetryBackoffCap = 30 * time.Second
)

var (
	_ multicluster.Provider         = &Provider{}
	_ multicluster.ProviderRunnable = &Provider{}
)

// Options are the options for creating a new instance of the provider. They
// mirror apiexport.Options upstream, plus the retry knobs.
type Options struct {
	// Scheme is the scheme to use for the provider. If this is nil, it defaults
	// to the client-go scheme.
	Scheme *runtime.Scheme

	// Log is the logger to use for the provider. If this is nil, it defaults
	// to the controller-runtime default logger.
	Log *logr.Logger

	// ObjectToWatch is the object type that the provider watches via a /clusters/*
	// wildcard endpoint to extract information about logical clusters joining and
	// leaving the "fleet" of (logical) clusters in kcp. If this is nil, it defaults
	// to [apisv1alpha1.APIBinding].
	ObjectToWatch client.Object

	// AddFilter is called to filter objects to engage.
	// If false is returned the object is not engaged.
	// If unset and ObjectToWatch is unset it defaults to ConditionReadyFunc("Ready").
	AddFilter func(obj client.Object) (bool, error)

	// UpdateFilter is called to filter objects to engage.
	// If false is returned the object is not engaged.
	// If unset and ObjectToWatch is unset it defaults to ConditionReadyFunc("Ready").
	UpdateFilter func(obj client.Object) (bool, error)

	// Handlers are lifecycle handlers, ran for each logical cluster in the provider
	// represented by the watched object.
	Handlers handlers.Handlers

	// RetryBackoff is the delay before the first retry of a failed endpoint
	// watch; it doubles per failure. DefaultRetryBackoff when zero.
	RetryBackoff time.Duration
	// RetryBackoffCap bounds the retry delay. DefaultRetryBackoffCap when zero.
	RetryBackoffCap time.Duration
}

func (o *Options) defaults() {
	if o.Scheme == nil {
		o.Scheme = scheme.Scheme
	}
	if o.ObjectToWatch == nil {
		o.ObjectToWatch = &apisv1alpha1.APIBinding{}
		if o.AddFilter == nil {
			o.AddFilter = upstream.ConditionReadyFunc("Ready")
		}
		if o.UpdateFilter == nil {
			o.UpdateFilter = upstream.ConditionReadyFunc("Ready")
		}
	}
	if o.Log == nil {
		o.Log = ptr.To(log.Log.WithName("kcp-apiexport-cluster-provider"))
	}
	if o.RetryBackoff <= 0 {
		o.RetryBackoff = DefaultRetryBackoff
	}
	if o.RetryBackoffCap <= 0 {
		o.RetryBackoffCap = DefaultRetryBackoffCap
	}
	if o.RetryBackoffCap < o.RetryBackoff {
		o.RetryBackoffCap = o.RetryBackoff
	}
}

// Provider is a [sigs.k8s.io/multicluster-runtime/pkg/multicluster.Provider]
// that yields each logical cluster (in the kcp sense) exposed via the APIExport
// virtual workspace as a cluster in the multicluster-runtime sense.
type Provider struct {
	opts Options
	// config points to the control plane where the endpoint slice resides.
	config    *rest.Config
	sliceName string

	// Clusters keeps track of all engaged clusters.
	Clusters clusters.Clusters[cluster.Cluster]

	// cache for the control plane where the endpoint slice resides.
	cache cache.Cache

	// recorderManager manages per-cluster event recorder providers.
	recorderManager *mcrecorder.Manager

	// aggregateCache provides an aggregated view across all shard caches.
	aggregateCache *mcpcache.AggregateCache

	// watchEndpointFunc starts watching an endpoint. Overridden by tests.
	watchEndpointFunc func(ctx context.Context, cfg *rest.Config, aware multicluster.Aware, onStopped func(error)) (*watchedEndpoint, error)

	// mu guards everything below. The slice informer's handler and the retry
	// timers all mutate the endpoint tables.
	mu sync.Mutex
	// started flips when Start runs; stopped when it returns.
	started, stopped bool
	// sliceSeen is set once any endpoint slice event has been handled.
	sliceSeen bool
	// published is the URL set of the last endpoint slice event.
	published []string
	// watched holds the endpoints whose watcher is running.
	watched map[string]*watchedEndpoint
	// pending holds the endpoints whose watcher failed and is scheduled for a
	// retry, keyed by URL.
	pending map[string]*retryState
}

// retryState tracks one failing endpoint between attempts.
type retryState struct {
	attempts int
	lastErr  error
	timer    *time.Timer
}

// New creates a new kcp APIExport virtual workspace provider. cfg must point at
// the workspace containing the APIExportEndpointSlice named endpointSliceName.
func New(cfg *rest.Config, endpointSliceName string, opts Options) (*Provider, error) {
	opts.defaults()

	p := &Provider{
		opts:      opts,
		config:    cfg,
		sliceName: endpointSliceName,
		Clusters:  clusters.New[cluster.Cluster](),
		watched:   map[string]*watchedEndpoint{},
		pending:   map[string]*retryState{},
	}

	c, err := cache.New(cfg, cache.Options{
		Scheme: opts.Scheme,
		ByObject: map[client.Object]cache.ByObject{
			&apisv1alpha1.APIExportEndpointSlice{}: {
				Field: fields.SelectorFromSet(fields.Set{"metadata.name": endpointSliceName}),
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("error building cache for APIExportEndpointSlice %q: %w", endpointSliceName, err)
	}
	p.cache = c

	p.recorderManager = mcrecorder.NewManager(opts.Scheme, opts.Log.WithName("recorder-manager"))
	p.aggregateCache = mcpcache.NewAggregateCache()
	p.watchEndpointFunc = func(ctx context.Context, cfg *rest.Config, aware multicluster.Aware, onStopped func(error)) (*watchedEndpoint, error) {
		return newWatchedEndpoint(ctx, cfg, aware, &watchedEndpoint{
			opts:                 p.opts,
			onStopped:            onStopped,
			getCluster:           p.Clusters.Get,
			addCluster:           p.Clusters.Add,
			removeCluster:        p.Clusters.Remove,
			getRecorderProvider:  p.recorderManager.GetProvider,
			stopRecorderProvider: p.recorderManager.StopProvider,
		})
	}
	return p, nil
}

// Get returns the cluster with the given name as a cluster.Cluster.
func (p *Provider) Get(ctx context.Context, clusterName multicluster.ClusterName) (cluster.Cluster, error) {
	return p.Clusters.Get(ctx, clusterName)
}

// Lister returns a cache.Lister that lists objects across all shards.
func (p *Provider) Lister() mcpcache.Lister {
	return p.aggregateCache
}

// IndexField adds an indexer to the clusters managed by this provider.
func (p *Provider) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	return p.Clusters.IndexField(ctx, obj, field, extractValue)
}

// Check reports why the provider is not watching every tenant workspace the
// platform routes to it, or nil. It satisfies vwhealth.Checker so a provider can
// attach it to its readiness for the lifetime of a controller term.
//
// Unready states, in the order they occur during startup: Start has not run;
// the APIExportEndpointSlice has not been observed; it publishes no endpoints
// yet (normal right after install); an endpoint watcher is failing and being
// retried. A stopped provider reports unready too — a caller that keeps it
// attached after the manager exits is the mistake this surfaces.
func (p *Provider) Check() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.stopped:
		return errors.New("kcp apiexport provider has stopped; controllers are not running")
	case !p.started:
		return errors.New("kcp apiexport provider has not started; controllers are not running")
	case !p.sliceSeen:
		return fmt.Errorf("waiting for APIExportEndpointSlice %s to be observed", p.sliceName)
	case len(p.published) == 0:
		return fmt.Errorf("APIExportEndpointSlice %s publishes no endpoints yet", p.sliceName)
	}
	failing := make([]string, 0, len(p.pending))
	for url := range p.pending {
		failing = append(failing, url)
	}
	if len(failing) == 0 {
		return nil
	}
	sort.Strings(failing)
	parts := make([]string, 0, len(failing))
	for _, url := range failing {
		st := p.pending[url]
		parts = append(parts, fmt.Sprintf("%s (%d attempts, last error: %v)", url, st.attempts, st.lastErr))
	}
	return fmt.Errorf("not watching APIExport virtual workspace endpoint(s), retrying: %s — resources in tenant workspaces on those shards will not reconcile", strings.Join(parts, "; "))
}

// Start starts the provider: it watches the endpoint slice and, for every
// published virtual-workspace URL, the tenant clusters behind it. Blocks until
// ctx ends.
func (p *Provider) Start(ctx context.Context, aware multicluster.Aware) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		p.mu.Lock()
		p.stopped = true
		for url, st := range p.pending {
			st.timer.Stop()
			delete(p.pending, url)
		}
		p.mu.Unlock()

		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		p.recorderManager.Stop(ctx)
	}()

	p.mu.Lock()
	p.started = true
	p.mu.Unlock()

	informer, err := p.cache.GetInformer(ctx, &apisv1alpha1.APIExportEndpointSlice{}, cache.BlockUntilSynced(false))
	if err != nil {
		return err
	}

	handler, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			t := obj.(client.Object)
			p.opts.Log.Info("new endpointslice object", "object", t)
			p.endpointSliceUpdate(ctx, aware, t)
		},
		UpdateFunc: func(oldObj any, newObj any) {
			t := newObj.(client.Object)
			p.opts.Log.Info("updated endpointslice object", "object", t)
			p.endpointSliceUpdate(ctx, aware, t)
		},
		DeleteFunc: func(obj any) {
			p.opts.Log.Info("deleted endpointslice object", "object", obj)
			p.stopAllWatchedEndpoints()
		},
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := informer.RemoveEventHandler(handler); err != nil {
			p.opts.Log.Error(err, "failed to remove event handler")
		}
	}()

	p.opts.Log.Info("starting provider")
	return p.cache.Start(ctx)
}

// endpointSliceUpdate reconciles the watched endpoints against the URLs the
// slice publishes: it starts a watcher for every new URL (scheduling a retry
// when that fails), and stops watchers and pending retries for URLs that are
// gone.
func (p *Provider) endpointSliceUpdate(ctx context.Context, aware multicluster.Aware, obj client.Object) {
	urls, err := upstream.DefaultExtractURLsFromEndpointSlice(obj)
	if err != nil {
		p.opts.Log.Error(err, "error getting virtual workspace URLs from endpointslice object", "obj", obj, "name", obj.GetName())
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.sliceSeen = true
	p.published = slices.Clone(urls)

	for _, url := range urls {
		if _, ok := p.watched[url]; ok {
			// endpoint is already handled
			continue
		}
		if _, ok := p.pending[url]; ok {
			// a retry is already scheduled
			continue
		}
		p.tryWatchLocked(ctx, aware, url, 0)
	}

	// check which watched urls can be stopped
	for watchedURL, we := range p.watched {
		if slices.Contains(urls, watchedURL) {
			continue
		}
		// URL no longer present in endpoint slice, cancel watch
		we.cancel()
		p.aggregateCache.RemoveCache(watchedURL)
		delete(p.watched, watchedURL)
	}
	for pendingURL, st := range p.pending {
		if slices.Contains(urls, pendingURL) {
			continue
		}
		st.timer.Stop()
		delete(p.pending, pendingURL)
		p.opts.Log.Info("endpoint no longer published; giving up on retrying it", "url", pendingURL)
	}
}

// tryWatchLocked starts the watcher for url. attempts counts the failures so
// far; on another failure a retry is scheduled with the next backoff step.
// Must be called with p.mu held.
func (p *Provider) tryWatchLocked(ctx context.Context, aware multicluster.Aware, url string, attempts int) {
	if ctx.Err() != nil {
		return
	}
	cfg := rest.CopyConfig(p.config)
	cfg.Host = url

	we, err := p.watchEndpointFunc(ctx, cfg, aware, func(cause error) { p.endpointStopped(ctx, aware, url, cause) })
	if err != nil {
		attempts++
		delay := p.backoff(attempts)
		p.opts.Log.Error(err, "failed to watch endpoint; will retry", "url", url, "attempt", attempts, "retryIn", delay.String())
		st := &retryState{attempts: attempts, lastErr: err}
		st.timer = time.AfterFunc(delay, func() { p.retry(ctx, aware, url) })
		p.pending[url] = st
		return
	}
	delete(p.pending, url)
	p.watched[url] = we
	p.aggregateCache.AddCache(url, we.wildcardCache)
	if attempts > 0 {
		p.opts.Log.Info("endpoint watch established after retries", "url", url, "attempts", attempts+1)
	}
}

// retry is the timer callback for a pending endpoint.
func (p *Provider) retry(ctx context.Context, aware multicluster.Aware, url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.pending[url]
	if !ok || p.stopped || ctx.Err() != nil {
		// Dropped from the slice, or the provider is shutting down.
		return
	}
	p.opts.Log.Info("retrying endpoint watch", "url", url, "attempt", st.attempts+1)
	p.tryWatchLocked(ctx, aware, url, st.attempts)
}

// endpointStopped is invoked when a running watcher's cache exits on its own
// — the endpoint went away underneath it. The watcher is torn down and the URL
// re-enters the retry loop from the first backoff step, unless the slice has
// dropped it in the meantime.
func (p *Provider) endpointStopped(ctx context.Context, aware multicluster.Aware, url string, cause error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || ctx.Err() != nil {
		return
	}
	we, ok := p.watched[url]
	if !ok {
		return
	}
	we.cancel()
	p.aggregateCache.RemoveCache(url)
	delete(p.watched, url)
	if !slices.Contains(p.published, url) {
		return
	}
	if cause == nil {
		cause = errors.New("endpoint cache stopped")
	}
	delay := p.backoff(1)
	p.opts.Log.Error(cause, "endpoint watcher lost its connection; will re-establish", "url", url, "retryIn", delay.String())
	st := &retryState{attempts: 1, lastErr: cause}
	st.timer = time.AfterFunc(delay, func() { p.retry(ctx, aware, url) })
	p.pending[url] = st
}

// backoff returns the delay before retry number attempts (1-based):
// RetryBackoff doubled per attempt, capped at RetryBackoffCap.
func (p *Provider) backoff(attempts int) time.Duration {
	d := p.opts.RetryBackoff
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= p.opts.RetryBackoffCap {
			return p.opts.RetryBackoffCap
		}
	}
	if d > p.opts.RetryBackoffCap {
		return p.opts.RetryBackoffCap
	}
	return d
}

func (p *Provider) stopAllWatchedEndpoints() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for watchedURL, we := range p.watched {
		we.cancel()
		p.aggregateCache.RemoveCache(watchedURL)
	}
	p.watched = map[string]*watchedEndpoint{}
	for url, st := range p.pending {
		st.timer.Stop()
		delete(p.pending, url)
	}
	p.published = nil
}

type watchedEndpoint struct {
	cancel        context.CancelFunc
	opts          Options
	config        *rest.Config
	wildcardCache mcpcache.WildcardCache
	logger        *logr.Logger
	// onStopped is called if the wildcard cache exits while its context is
	// still live.
	onStopped func(error)

	getCluster           func(ctx context.Context, name multicluster.ClusterName) (cluster.Cluster, error)
	addCluster           func(ctx context.Context, name multicluster.ClusterName, cl cluster.Cluster, aware multicluster.Aware) error
	removeCluster        func(name multicluster.ClusterName)
	getRecorderProvider  func(cfg *rest.Config, clusterName logicalcluster.Name) (*mcrecorder.Provider, error)
	stopRecorderProvider func(ctx context.Context, clusterName logicalcluster.Name)
}

func newWatchedEndpoint(ctx context.Context, cfg *rest.Config, aware multicluster.Aware, we *watchedEndpoint) (*watchedEndpoint, error) {
	ctx, cancel := context.WithCancel(ctx)
	we.cancel = cancel
	we.config = cfg

	wildcardCache, err := mcpcache.NewWildcardCache(we.config, cache.Options{Scheme: we.opts.Scheme})
	if err != nil {
		we.cancel()
		return nil, fmt.Errorf("error starting cache: %w", err)
	}
	we.wildcardCache = wildcardCache

	logger := we.opts.Log.WithValues("url", cfg.Host)
	we.logger = &logger

	if err := we.start(ctx, aware); err != nil {
		we.cancel()
		return nil, fmt.Errorf("error starting endpoint watcher: %w", err)
	}

	return we, nil
}

func (we *watchedEndpoint) start(ctx context.Context, aware multicluster.Aware) error {
	informer, err := we.wildcardCache.GetInformer(ctx, we.opts.ObjectToWatch, cache.BlockUntilSynced(false))
	if err != nil {
		return fmt.Errorf("failed to get %T informer: %w", we.opts.ObjectToWatch, err)
	}

	handler, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			t := obj.(client.Object)
			we.logger.Info("new endpoint object", "object", t)
			if filter := we.opts.AddFilter; filter != nil {
				accept, err := filter(t)
				if err != nil {
					we.logger.Error(err, "error in filter function")
					return
				}
				if !accept {
					return
				}
			}
			if err := we.update(ctx, t, aware); err != nil {
				we.logger.Error(err, "unexpected error handling add event")
			}
			we.opts.Handlers.RunOnAdd(t)
		},
		UpdateFunc: func(oldObj any, newObj any) {
			t := newObj.(client.Object)
			we.logger.Info("updated endpoint object", "object", t)
			if filter := we.opts.UpdateFilter; filter != nil {
				accept, err := filter(t)
				if err != nil {
					we.logger.Error(err, "error in filter function")
					return
				}
				if !accept {
					return
				}
			}
			if err := we.update(ctx, t, aware); err != nil {
				we.logger.Error(err, "unexpected error handling update event")
			}
			we.opts.Handlers.RunOnUpdate(oldObj.(client.Object), t)
		},
		DeleteFunc: func(obj any) {
			t, ok := obj.(client.Object)
			if !ok {
				tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown)
				if !ok {
					we.logger.Error(nil, "couldn't get object from tombstone", "obj", obj)
					return
				}
				t, ok = tombstone.Obj.(client.Object)
				if !ok {
					we.logger.Error(nil, "tombstone contained object that is not expected", "obj", obj)
					return
				}
			}
			we.logger.Info("deleted endpoint object", "object", obj)
			clusterName := logicalcluster.From(t)

			shInf, _, _, err := we.wildcardCache.GetSharedInformer(we.opts.ObjectToWatch)
			if err != nil {
				we.logger.Error(err, "failed to get shared informer for delete check", "cluster", clusterName)
				return
			}

			keys, err := shInf.GetIndexer().IndexKeys(kcpcache.ClusterIndexName, clusterName.String())
			if err != nil {
				we.logger.Error(err, "failed to get index keys", "cluster", clusterName)
				return
			}

			if len(keys) > 0 {
				return
			}

			we.stopRecorderProvider(ctx, clusterName)
			we.removeCluster(multicluster.ClusterName(clusterName.String()))
			we.opts.Handlers.RunOnDelete(t)
		},
	})
	if err != nil {
		return fmt.Errorf("error adding event handler: %w", err)
	}

	go func() {
		defer informer.RemoveEventHandler(handler) //nolint:errcheck
		err := we.wildcardCache.Start(ctx)
		if err != nil {
			we.logger.Error(err, "error in cache for endpoint")
		}
		// A cache that returns while its context is live has stopped on its
		// own; hand the endpoint back to the provider so it is re-established
		// instead of silently gone.
		if ctx.Err() == nil && we.onStopped != nil {
			we.onStopped(err)
		}
	}()

	return nil
}

func (we *watchedEndpoint) update(ctx context.Context, obj client.Object, aware multicluster.Aware) error {
	clusterName := logicalcluster.From(obj)

	// check if cluster already exists before creating. There is small chance for race but its ok.
	if _, err := we.getCluster(ctx, multicluster.ClusterName(clusterName.String())); err == nil {
		return nil
	}

	recorder, err := we.getRecorderProvider(we.config, clusterName)
	if err != nil {
		return fmt.Errorf("failed to get broadcaster: %w", err)
	}

	// create new scoped cluster.
	cl, err := mcpcache.NewScopedCluster(we.config, clusterName, we.wildcardCache, we.opts.Scheme, recorder)
	if err != nil {
		return fmt.Errorf("failed to create cluster %q: %w", clusterName, err)
	}

	if err := we.addCluster(ctx, multicluster.ClusterName(clusterName.String()), cl, aware); err != nil {
		return fmt.Errorf("failed to add cluster %q: %w", clusterName, err)
	}

	return nil
}
