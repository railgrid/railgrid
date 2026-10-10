// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
)

// CallerFactory builds the per-request client a data-plane handler acts
// through. There is no provider-wide identity on this path: the only
// credential is the caller's own bearer, scoped to the caller's workspace, so
// every read and every SelfSubjectAccessReview is evaluated against the
// caller's RBAC.
type CallerFactory interface {
	// For returns a dynamic client on <hub>/clusters/{clusterID}
	// authenticating as token. Implementations cache; callers must not.
	//
	// Only the MCP class (b) still holds a caller's bearer: the hub aggregate
	// forwards it with each tool call. A verb never does — see Gate.
	For(clusterID, token string) (dynamic.Interface, error)
}

// Default bounds for the client cache. A client holds a transport and its
// connection pool, so caching matters, but an unbounded cache keyed by token
// is a memory leak with a token-shaped key.
const (
	DefaultCallerCacheSize = 512
	DefaultCallerCacheTTL  = 10 * time.Minute
	// DefaultEndpointRefresh bounds how long the published endpoint set may go
	// unchecked. It is not a cache for speed: the set changes in normal
	// operation, and a stale one sends a consumer to a shard that cannot serve
	// it.
	DefaultEndpointRefresh = time.Minute
)

// Callers is the default CallerFactory: host and CA from the provider's own
// kubeconfig, bearer from the request, target <hub>/clusters/{clusterID},
// cached per (cluster, sha256(token)) with a bounded size and TTL.
type Callers struct {
	base *rest.Config
	// provider is the provider's OWN authenticated config, kept apart from base
	// (which has every credential stripped). It is only ever used by
	// AsProvider, on the custom-subresource path, where there is no caller
	// bearer to act with. Nil until WithProviderConfig is given.
	provider *rest.Config
	// providerExport names the provider's APIExport. AsProvider acts through
	// that export's virtual workspace, never through the shard's own
	// /clusters/{id}: the provider identity has no RBAC inside a tenant
	// workspace, only the standing its export gives it.
	providerExport string
	// providerEndpoint is the export virtual-workspace base URL when it was
	// pinned with WithProviderEndpoint. Pinning skips discovery and the
	// per-cluster choice below entirely.
	providerEndpoint string
	// providerEndpoints is every URL the APIExportEndpointSlice publishes,
	// read on first use. A sharded kcp publishes one per shard, and a shard's
	// export virtual workspace serves only the logical clusters that shard
	// holds -- so which one to use depends on the consumer.
	providerEndpoints []string
	// clusterEndpoint remembers, per consumer cluster, which published
	// endpoint actually serves it, so the choice is made once.
	clusterEndpoint map[string]string
	// providerEndpointsRead is when providerEndpoints was last read, and
	// endpointRefresh how stale it may get. The set is not fixed: kcp publishes
	// a shard's URL only once the export has a consumer there, so it grows as
	// workspaces on new shards enable the provider.
	providerEndpointsRead       time.Time
	endpointRefresh             time.Duration
	providerMu                  sync.Mutex
	providerEndpointsGeneration uint64
	providerEndpointRefresh     *providerEndpointRefresh

	maxEntries int
	ttl        time.Duration

	mu    sync.Mutex
	cache map[string]*callerEntry
}

type providerEndpointRefresh struct {
	done       chan struct{}
	endpoints  []string
	generation uint64
	err        error
	canceled   bool
}

type providerEndpointSnapshot struct {
	endpoints  []string
	generation uint64
}

type callerEntry struct {
	client  dynamic.Interface
	expires time.Time
}

// CallerOption tunes the cache of the default CallerFactory.
type CallerOption func(*Callers)

// WithCallerCacheSize bounds how many clients are kept. n <= 0 restores the
// default.
func WithCallerCacheSize(n int) CallerOption {
	return func(c *Callers) {
		if n <= 0 {
			n = DefaultCallerCacheSize
		}
		c.maxEntries = n
	}
}

// WithProviderConfig lets the factory also act as the provider itself, which
// the custom-subresource path requires: the shard stamps the caller's identity
// but hands over no credential, so visibility is decided by an access review
// run by the provider and the handler then acts as the provider. cfg keeps its
// credentials; it is never used for a request that carries a bearer.
func WithProviderConfig(cfg *rest.Config, exportName string) CallerOption {
	return func(c *Callers) {
		if cfg != nil {
			c.provider = rest.CopyConfig(cfg)
		}
		c.providerExport = strings.TrimSpace(exportName)
	}
}

// WithProviderEndpoint pins the export virtual-workspace base URL AsProvider
// acts through (…/services/apiexport/<cluster>/<export>), instead of reading
// it from the APIExportEndpointSlice on first use. For a provider that already
// discovered it, and for tests.
func WithProviderEndpoint(vwURL string) CallerOption {
	return func(c *Callers) {
		c.providerEndpoint = strings.TrimRight(strings.TrimSpace(vwURL), "/")
	}
}

// WithCallerCacheTTL bounds how long a client is reused. Keep it well under
// the lifetime of the tokens in play, so a revoked or rotated credential
// stops being backed by a warm transport. d <= 0 restores the default.
func WithCallerCacheTTL(d time.Duration) CallerOption {
	return func(c *Callers) {
		if d <= 0 {
			d = DefaultCallerCacheTTL
		}
		c.ttl = d
	}
}

// NewCallerFactory builds the default CallerFactory from the provider's own
// rest.Config — typically the one loaded from the provider kubeconfig the hub
// minted.
//
// Only the server-facing half of base survives: host (with any /clusters/…
// suffix removed so a per-request one can be attached), proxy and dial
// settings, CA, ServerName, Insecure, timeouts. Every credential is dropped
// by DropCredentials, so a client this factory returns can never act as the
// provider even if the request carries no token — it fails instead.
func NewCallerFactory(base *rest.Config, opts ...CallerOption) (*Callers, error) {
	if base == nil {
		return nil, fmt.Errorf("dataplane: caller factory needs a base rest.Config")
	}
	stripped := DropCredentials(base)
	host, err := stripClusterSuffix(stripped.Host)
	if err != nil {
		return nil, err
	}
	stripped.Host = host
	return newCallers(stripped, opts...), nil
}

// NewHubCallerFactory builds the default CallerFactory from a hub base URL and
// a CA bundle, for a provider that has no kubeconfig at serve time. It is the
// same target tenantaccess.RESTConfig builds. insecure relaxes server
// verification for in-cluster hub certificates (the RAILGRID_HUB_INSECURE
// knob) and is never appropriate in production.
func NewHubCallerFactory(hubBase string, caCertData []byte, insecure bool, opts ...CallerOption) (*Callers, error) {
	hubBase = strings.TrimRight(strings.TrimSpace(hubBase), "/")
	if hubBase == "" {
		return nil, fmt.Errorf("dataplane: hub base URL is empty")
	}
	host, err := stripClusterSuffix(hubBase)
	if err != nil {
		return nil, err
	}
	tls := rest.TLSClientConfig{CAData: caCertData, Insecure: insecure}
	if insecure {
		// rest refuses a config that both trusts a CA and skips verification.
		tls.CAData = nil
	}
	return newCallers(&rest.Config{Host: host, TLSClientConfig: tls}, opts...), nil
}

func newCallers(base *rest.Config, opts ...CallerOption) *Callers {
	c := &Callers{
		base:            base,
		maxEntries:      DefaultCallerCacheSize,
		ttl:             DefaultCallerCacheTTL,
		endpointRefresh: DefaultEndpointRefresh,
		cache:           map[string]*callerEntry{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// DropCredentials returns a copy of cfg with every way of authenticating as
// its owner removed: bearer token and token file, client certificate and key
// (inline and on disk), basic auth, auth and exec providers, impersonation,
// and any transport or transport wrapper that could carry a credential of its
// own. What remains describes only which server to talk to and how to verify
// it.
//
// It is exported because "the provider's connection, minus the provider's
// identity" is the exact input every caller-scoped client needs, and getting
// the list wrong is a confused-deputy bug.
func DropCredentials(cfg *rest.Config) *rest.Config {
	out := rest.CopyConfig(cfg)
	out.BearerToken = ""
	out.BearerTokenFile = ""
	out.Username = ""
	out.Password = ""
	out.AuthProvider = nil
	out.ExecProvider = nil
	out.Impersonate = rest.ImpersonationConfig{}
	// Rebuilt field by field rather than nulled out, so a new credential
	// field in rest.TLSClientConfig fails the build here instead of silently
	// surviving into a caller's client.
	out.TLSClientConfig = rest.TLSClientConfig{
		Insecure:   cfg.Insecure,
		ServerName: cfg.ServerName,
		CAFile:     cfg.CAFile,
		CAData:     cfg.CAData,
		NextProtos: cfg.NextProtos,
	}
	// A pre-built transport or a wrapper may authenticate on its own, and a
	// rest.Config carrying either alongside a bearer token is rejected at
	// build time anyway.
	out.Transport = nil
	out.WrapTransport = nil
	return out
}

// For implements CallerFactory.
func (c *Callers) For(clusterID, token string) (dynamic.Interface, error) {
	if c == nil {
		return nil, fmt.Errorf("dataplane: caller factory is unavailable")
	}
	clusterID = strings.TrimSpace(clusterID)
	if !IsClusterID(clusterID) {
		// A workspace path here would be minted into a URL the hub proxy
		// answers with 403; fail before building the client.
		return nil, fmt.Errorf("dataplane: %q is not a kcp logical-cluster ID", clusterID)
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: cannot act on the tenant's behalf", ErrNoBearer)
	}

	key := clusterID + "\x00" + hashToken(token)
	now := time.Now()

	c.mu.Lock()
	if entry, ok := c.cache[key]; ok && entry.expires.After(now) {
		client := entry.client
		c.mu.Unlock()
		return client, nil
	}
	c.mu.Unlock()

	cfg := rest.CopyConfig(c.base)
	cfg.Host = c.base.Host + "/clusters/" + clusterID
	cfg.BearerToken = token

	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dataplane: caller client for cluster %q: %w", clusterID, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.cache[key]; ok && entry.expires.After(now) {
		return entry.client, nil
	}
	c.evictLocked(now)
	c.cache[key] = &callerEntry{client: client, expires: now.Add(c.ttl)}
	return client, nil
}

// evictLocked makes room for one entry: expired entries first, then, if the
// cache is still full, the entry closest to expiry.
func (c *Callers) evictLocked(now time.Time) {
	if len(c.cache) < c.maxEntries {
		return
	}
	for key, entry := range c.cache {
		if !entry.expires.After(now) {
			delete(c.cache, key)
		}
	}
	for len(c.cache) >= c.maxEntries {
		oldestKey, oldest := "", time.Time{}
		for key, entry := range c.cache {
			if oldest.IsZero() || entry.expires.Before(oldest) {
				oldestKey, oldest = key, entry.expires
			}
		}
		if oldestKey == "" {
			return
		}
		delete(c.cache, oldestKey)
	}
}

// hashToken is the cache key for a bearer: a raw credential never sits in a
// map key, a log line or a heap dump we might hand to someone.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// stripClusterSuffix turns "https://hub:9443/clusters/root:a:b" into
// "https://hub:9443" so a per-request /clusters/{id} can be attached. A host
// without the suffix (direct hub addressing in local dev) comes back
// unchanged.
func stripClusterSuffix(host string) (string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("dataplane: parse base host %q: %w", host, err)
	}
	if idx := strings.Index(u.Path, "/clusters/"); idx >= 0 {
		u.Path = u.Path[:idx]
		return strings.TrimRight(u.String(), "/"), nil
	}
	return strings.TrimRight(host, "/"), nil
}

// AsProvider implements ProviderCallerFactory: a client acting as the
// provider in clusterID. It refuses when no provider config was given, so a
// provider that never opted into the subresource path cannot reach it by
// accident.
func (c *Callers) AsProvider(clusterID string) (dynamic.Interface, error) {
	return c.AsProviderWithRateLimiter(clusterID, nil)
}

// AsProviderContext is AsProvider with request cancellation propagated through
// export endpoint discovery and shard probes. It is an optional extension to
// ProviderCallerFactory so existing implementations remain source-compatible.
func (c *Callers) AsProviderContext(ctx context.Context, clusterID string) (dynamic.Interface, error) {
	return c.AsProviderWithRateLimiterContext(ctx, clusterID, nil)
}

// AsProviderWithRateLimiter preserves the provider's export and tenant scope
// while allowing a caller to share an explicit request budget across clients.
// Nil retains the configured/default limiter. This does not cache authorization.
func (c *Callers) AsProviderWithRateLimiter(clusterID string, limiter flowcontrol.RateLimiter) (dynamic.Interface, error) {
	return c.AsProviderWithRateLimiterContext(context.Background(), clusterID, limiter)
}

// AsProviderWithRateLimiterContext is AsProviderWithRateLimiter with request
// cancellation propagated through export endpoint discovery and shard probes.
// The older method remains available for callers without a request context.
func (c *Callers) AsProviderWithRateLimiterContext(ctx context.Context, clusterID string, limiter flowcontrol.RateLimiter) (dynamic.Interface, error) {
	if c == nil || c.provider == nil {
		return nil, fmt.Errorf("dataplane: this provider has no provider-scoped config; the subresource path needs WithProviderConfig")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	clusterID = strings.TrimSpace(clusterID)
	if !IsClusterID(clusterID) {
		return nil, fmt.Errorf("dataplane: %q is not a kcp logical-cluster ID", clusterID)
	}
	endpoint, err := c.endpointForCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	cfg := rest.CopyConfig(c.provider)
	cfg.Host = endpoint + "/clusters/" + clusterID
	if limiter != nil {
		cfg.RateLimiter = limiter
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dataplane: provider client for cluster %q: %w", clusterID, err)
	}
	return client, nil
}

// APIExportEndpointSlices is the kcp resource a provider reads its export
// virtual-workspace URL from.
func APIExportEndpointSlices() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices"}
}

// ExportEndpointForCluster returns the export virtual-workspace base URL
// (…/services/apiexport/<cluster>/<export>), for callers that address another
// provider's kinds or verbs through this provider's own export: a claimed
// resource or custom subresource is reached at
// <endpoint>/clusters/<tenant>/apis/<group>/<version>/<resource>/<name>[/<verb>]
// with the provider's own credential.
//
// It takes the consumer cluster because the answer depends on it: a sharded kcp
// publishes one endpoint per shard and each serves only the clusters its shard
// holds. An accessor that did not take one used to exist, and every caller of
// it appended /clusters/{id} to a URL chosen without reference to {id}.
func (c *Callers) ExportEndpointForCluster(ctx context.Context, clusterID string) (string, error) {
	if c == nil || c.provider == nil {
		return "", fmt.Errorf("dataplane: this provider has no provider-scoped config; ExportEndpointForCluster needs WithProviderConfig")
	}
	if !IsClusterID(clusterID) {
		return "", fmt.Errorf("dataplane: %q is not a kcp logical-cluster ID", clusterID)
	}
	return c.endpointForCluster(ctx, clusterID)
}

// ProviderRESTConfig returns a copy of the provider's own rest.Config with its
// host replaced by target, keeping the credential and TLS settings. It is how
// a provider builds a client for a path under its export virtual workspace.
func (c *Callers) ProviderRESTConfig(target string) (*rest.Config, error) {
	if c == nil || c.provider == nil {
		return nil, fmt.Errorf("dataplane: this provider has no provider-scoped config; ProviderRESTConfig needs WithProviderConfig")
	}
	cfg := rest.CopyConfig(c.provider)
	cfg.Host = target
	return cfg, nil
}

// ExportVerbURL is the URL at which this provider calls a verb another
// provider serves — a custom subresource it has CLAIMED (a
// spec.requires[].resources[] entry naming "{resource}/{verb}", whose
// generated claim spells verbs ["*"] because kcp checks the HTTP method as the
// verb) — through its own
// export virtual workspace:
//
//	<endpoint>/clusters/{tenant}/apis/{group}/{version}/{resource}/{name}/{verb}[/{tail}]
//
// kcp authorizes the call against the claim the tenant accepted, forwards it
// to the owning provider impersonating THIS provider, and that provider's gate
// treats it as a foreign provider whose claim is the authorization. The
// caller's end-user identity is not carried: cross-provider work is done as
// the provider, not on a user's behalf. Use ProviderHTTPClient for the
// credential.
func (c *Callers) ExportVerbURL(ctx context.Context, gvr schema.GroupVersionResource, r Request) (string, error) {
	if c == nil || c.provider == nil {
		return "", fmt.Errorf("dataplane: this provider has no provider-scoped config; ExportVerbURL needs WithProviderConfig")
	}
	endpoint, err := c.endpointForCluster(ctx, r.ClusterID)
	if err != nil {
		return "", err
	}
	return SubresourceURL(endpoint, gvr.Group, gvr.Version, r)
}

// ProviderHTTPClient returns an http.Client authenticating as the provider
// itself, for the raw HTTP a verb call is (ExportVerbURL). The TLS and
// credential settings are the provider kubeconfig's; the client has no
// timeout, so a caller bounds each request with its context.
func (c *Callers) ProviderHTTPClient() (*http.Client, error) {
	if c == nil || c.provider == nil {
		return nil, fmt.Errorf("dataplane: this provider has no provider-scoped config; ProviderHTTPClient needs WithProviderConfig")
	}
	client, err := rest.HTTPClientFor(c.provider)
	if err != nil {
		return nil, fmt.Errorf("dataplane: provider HTTP client: %w", err)
	}
	return client, nil
}

// providerEndpointSnapshotForContext reads the export's published endpoint set
// once and shares an in-flight read with concurrent callers. The state mutex
// protects only the cache and flight pointer; endpoint HTTP requests happen
// after releasing it so each waiter can honor its own context cancellation.
// A forced caller joins any active read; if that read uses the stale fallback,
// the unchanged refresh timestamp makes the next caller retry the read.
func (c *Callers) providerEndpointSnapshotForContext(ctx context.Context, force bool) (providerEndpointSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return providerEndpointSnapshot{}, err
		}
		c.providerMu.Lock()
		if err := ctx.Err(); err != nil {
			c.providerMu.Unlock()
			return providerEndpointSnapshot{}, err
		}
		if c.providerEndpoint != "" {
			snapshot := providerEndpointSnapshot{endpoints: []string{c.providerEndpoint}, generation: c.providerEndpointsGeneration}
			c.providerMu.Unlock()
			return snapshot, nil
		}
		if c.providerExport == "" {
			c.providerMu.Unlock()
			return providerEndpointSnapshot{}, fmt.Errorf("dataplane: the subresource path needs the provider's export name (WithProviderConfig) or its virtual-workspace URL (WithProviderEndpoint)")
		}
		if refresh := c.providerEndpointRefresh; refresh != nil {
			c.providerMu.Unlock()
			select {
			case <-ctx.Done():
				return providerEndpointSnapshot{}, ctx.Err()
			case <-refresh.done:
			}
			if err := ctx.Err(); err != nil {
				return providerEndpointSnapshot{}, err
			}
			if refresh.canceled {
				// The refresh owner's context is not the waiter's context. Let an
				// active waiter take over instead of inheriting that cancellation.
				continue
			}
			if refresh.err != nil {
				return providerEndpointSnapshot{}, refresh.err
			}
			return providerEndpointSnapshot{
				endpoints:  append([]string(nil), refresh.endpoints...),
				generation: refresh.generation,
			}, nil
		}
		fresh := time.Since(c.providerEndpointsRead) < c.endpointRefresh
		if !force && fresh && len(c.providerEndpoints) > 0 {
			snapshot := providerEndpointSnapshot{endpoints: append([]string(nil), c.providerEndpoints...), generation: c.providerEndpointsGeneration}
			c.providerMu.Unlock()
			return snapshot, nil
		}

		refresh := &providerEndpointRefresh{done: make(chan struct{})}
		c.providerEndpointRefresh = refresh
		staleEndpoints := append([]string(nil), c.providerEndpoints...)
		providerConfig := rest.CopyConfig(c.provider)
		providerExport := c.providerExport
		c.providerMu.Unlock()

		endpoints, getFailed, err := readProviderExportEndpoints(ctx, providerConfig, providerExport)
		refreshCanceled := ctx.Err() != nil
		staleFallback := false
		if refreshCanceled {
			err = ctx.Err()
		} else if err != nil && getFailed && len(staleEndpoints) > 0 {
			// Preserve the old endpoint set after a genuine dependency GET error.
			// Cancellation is handled above and must never return stale data.
			endpoints = staleEndpoints
			err = nil
			staleFallback = true
		}

		c.providerMu.Lock()
		if err == nil {
			if !staleFallback {
				if !sameEndpoints(endpoints, c.providerEndpoints) {
					// A choice made against the old set may name an endpoint that is
					// gone, or miss one that now serves a consumer better.
					c.clusterEndpoint = nil
					c.providerEndpointsGeneration++
				}
				c.providerEndpoints = append([]string(nil), endpoints...)
				c.providerEndpointsRead = time.Now()
			}
			refresh.endpoints = append([]string(nil), endpoints...)
			refresh.generation = c.providerEndpointsGeneration
		}
		refresh.err = err
		refresh.canceled = refreshCanceled
		if c.providerEndpointRefresh == refresh {
			c.providerEndpointRefresh = nil
		}
		close(refresh.done)
		c.providerMu.Unlock()

		if err != nil {
			return providerEndpointSnapshot{}, err
		}
		return providerEndpointSnapshot{
			endpoints:  append([]string(nil), refresh.endpoints...),
			generation: refresh.generation,
		}, nil
	}
}

func readProviderExportEndpoints(ctx context.Context, providerConfig *rest.Config, providerExport string) ([]string, bool, error) {
	client, err := dynamic.NewForConfig(providerConfig)
	if err != nil {
		return nil, false, fmt.Errorf("dataplane: provider client: %w", err)
	}
	slice, err := client.Resource(APIExportEndpointSlices()).Get(ctx, providerExport, metav1.GetOptions{})
	if err != nil {
		return nil, true, fmt.Errorf("dataplane: reading APIExportEndpointSlice %s: %w", providerExport, err)
	}
	endpoints, _, err := unstructured.NestedSlice(slice.Object, "status", "endpoints")
	if err != nil {
		return nil, false, fmt.Errorf("dataplane: APIExportEndpointSlice %s: %w", providerExport, err)
	}
	var urls []string
	for _, e := range endpoints {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if u, _ := entry["url"].(string); strings.TrimSpace(u) != "" {
			urls = append(urls, strings.TrimRight(strings.TrimSpace(u), "/"))
		}
	}
	if len(urls) == 0 {
		return nil, false, fmt.Errorf("dataplane: APIExportEndpointSlice %s publishes no endpoint yet", providerExport)
	}
	return urls, false, nil
}

// sameEndpoints compares the published set regardless of the order kcp
// happened to list it in.
func sameEndpoints(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	return sets.New(a...).Equal(sets.New(b...))
}

// endpointForCluster picks the export virtual-workspace URL that serves
// clusterID.
//
// On one shard there is one URL and nothing to decide. On a sharded
// installation the slice publishes one per shard, and a shard's export virtual
// workspace only serves the logical clusters that shard holds -- so taking the
// first published URL works for the consumers that happen to live on that
// shard and fails for all the others, with kcp falling back to plain RBAC on
// the provider's ServiceAccount and refusing:
//
//	subjectaccessreviews.authorization.k8s.io is forbidden: User
//	"system:serviceaccount:default:provider" cannot create resource
//	"subjectaccessreviews" ... access denied
//
// Nothing in the slice says which shard holds which cluster, so find out by
// asking: a SubjectAccessReview is exactly the standing the APIExport's
// accepted claim grants, so the endpoint where one can be created is the
// endpoint that serves this consumer. The review is not persisted and its
// answer is irrelevant here -- only whether the request was allowed at all.
func (c *Callers) endpointForCluster(ctx context.Context, clusterID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		snapshot, err := c.providerEndpointSnapshotForContext(ctx, false)
		if err != nil {
			return "", err
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if endpoint, ok, changed := c.chosenEndpointForGeneration(clusterID, snapshot.generation); changed {
			continue
		} else if ok {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return endpoint, nil
		}
		if len(snapshot.endpoints) == 1 {
			// Nothing to choose between. Probing would only turn a precise error
			// from the real call ("the claim was not accepted") into a vague one.
			if !c.endpointGenerationMatches(snapshot.generation) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return snapshot.endpoints[0], nil
		}

		endpoint, errs, changed := c.probeAll(ctx, snapshot.endpoints, clusterID, snapshot.generation)
		if changed {
			continue
		}
		if endpoint != "" {
			if !c.endpointGenerationMatches(snapshot.generation) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return endpoint, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}

		// Nothing served it. The set may simply be out of date -- a shard's URL
		// appears only once the export has a consumer there -- so look again before
		// giving up, ignoring the refresh interval.
		refreshed, refreshErr := c.providerEndpointSnapshotForContext(ctx, true)
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if refreshErr == nil && !sameEndpoints(refreshed.endpoints, snapshot.endpoints) {
			endpoint, moreErrs, changed := c.probeAll(ctx, refreshed.endpoints, clusterID, refreshed.generation)
			if changed {
				continue
			}
			if endpoint != "" {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				return endpoint, nil
			}
			errs = append(errs, moreErrs...)
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("dataplane: no export virtual workspace serves cluster %s: %w", clusterID, errors.Join(errs...))
	}
}

func (c *Callers) chosenEndpointForGeneration(clusterID string, generation uint64) (string, bool, bool) {
	c.providerMu.Lock()
	defer c.providerMu.Unlock()
	if c.providerEndpointsGeneration != generation {
		return "", false, true
	}
	endpoint, ok := c.clusterEndpoint[clusterID]
	return endpoint, ok, false
}

func (c *Callers) endpointGenerationMatches(generation uint64) bool {
	c.providerMu.Lock()
	defer c.providerMu.Unlock()
	return c.providerEndpointsGeneration == generation
}

// probeAll returns the first endpoint that serves clusterID, remembering it,
// or every reason none did.
func (c *Callers) probeAll(ctx context.Context, endpoints []string, clusterID string, generation uint64) (string, []error, bool) {
	var errs []error
	for _, endpoint := range endpoints {
		if err := ctx.Err(); err != nil {
			return "", append(errs, err), false
		}
		if err := c.probeEndpoint(ctx, endpoint, clusterID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", endpoint, err))
			continue
		}
		c.providerMu.Lock()
		if c.providerEndpointsGeneration != generation {
			c.providerMu.Unlock()
			return "", errs, true
		}
		if c.clusterEndpoint == nil {
			c.clusterEndpoint = map[string]string{}
		}
		c.clusterEndpoint[clusterID] = endpoint
		c.providerMu.Unlock()
		return endpoint, nil, false
	}
	c.providerMu.Lock()
	changed := c.providerEndpointsGeneration != generation
	c.providerMu.Unlock()
	return "", errs, changed
}

// probeEndpoint reports whether endpoint serves clusterID, by creating the
// review the provider would create anyway.
func (c *Callers) probeEndpoint(ctx context.Context, endpoint, clusterID string) error {
	cfg := rest.CopyConfig(c.provider)
	cfg.Host = endpoint + "/clusters/" + clusterID
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	review := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authorization.k8s.io/v1",
		"kind":       "SubjectAccessReview",
		"spec": map[string]any{
			// Anonymous, and an attribute nothing is expected to allow: the
			// answer is not used, so the review stays a pure capability check.
			"user":               "system:anonymous",
			"resourceAttributes": map[string]any{"verb": "get", "resource": "subjectaccessreviews", "group": "authorization.k8s.io"},
		},
	}}
	_, err = client.Resource(SubjectAccessReviews()).Create(ctx, review, metav1.CreateOptions{})
	return err
}
