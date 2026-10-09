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
	providerEndpointsRead time.Time
	endpointRefresh       time.Duration
	providerMu            sync.Mutex

	maxEntries int
	ttl        time.Duration

	mu    sync.Mutex
	cache map[string]*callerEntry
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
	if c == nil || c.provider == nil {
		return nil, fmt.Errorf("dataplane: this provider has no provider-scoped config; the subresource path needs WithProviderConfig")
	}
	clusterID = strings.TrimSpace(clusterID)
	if !IsClusterID(clusterID) {
		return nil, fmt.Errorf("dataplane: %q is not a kcp logical-cluster ID", clusterID)
	}
	endpoint, err := c.endpointForCluster(context.Background(), clusterID)
	if err != nil {
		return nil, err
	}
	cfg := rest.CopyConfig(c.provider)
	cfg.Host = endpoint + "/clusters/" + clusterID
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

// exportEndpoints returns every export virtual-workspace base URL, read once
// from the APIExportEndpointSlice named after the export in the provider
// workspace (which is what c.provider addresses). That slice is what the
// provider's controllers watch tenant workspaces through already, so the
// subresource path acts through exactly the same door. A sharded kcp publishes
// one URL per shard; endpointForCluster decides between them.
func (c *Callers) exportEndpoints(ctx context.Context, force bool) ([]string, error) {
	c.providerMu.Lock()
	defer c.providerMu.Unlock()
	if c.providerEndpoint != "" {
		return []string{c.providerEndpoint}, nil
	}
	fresh := time.Since(c.providerEndpointsRead) < c.endpointRefresh
	if !force && fresh && len(c.providerEndpoints) > 0 {
		return c.providerEndpoints, nil
	}
	if c.providerExport == "" {
		return nil, fmt.Errorf("dataplane: the subresource path needs the provider's export name (WithProviderConfig) or its virtual-workspace URL (WithProviderEndpoint)")
	}
	client, err := dynamic.NewForConfig(c.provider)
	if err != nil {
		return nil, fmt.Errorf("dataplane: provider client: %w", err)
	}
	slice, err := client.Resource(APIExportEndpointSlices()).Get(ctx, c.providerExport, metav1.GetOptions{})
	if err != nil {
		// A refresh that fails leaves the previous set in place: it is the best
		// thing known, and a transient read must not take the data plane down.
		if len(c.providerEndpoints) > 0 {
			return c.providerEndpoints, nil
		}
		return nil, fmt.Errorf("dataplane: reading APIExportEndpointSlice %s: %w", c.providerExport, err)
	}
	endpoints, _, err := unstructured.NestedSlice(slice.Object, "status", "endpoints")
	if err != nil {
		return nil, fmt.Errorf("dataplane: APIExportEndpointSlice %s: %w", c.providerExport, err)
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
		return nil, fmt.Errorf("dataplane: APIExportEndpointSlice %s publishes no endpoint yet", c.providerExport)
	}
	if !sameEndpoints(urls, c.providerEndpoints) {
		// A choice made against the old set may name an endpoint that is gone,
		// or miss one that now serves a consumer better: decide again.
		c.clusterEndpoint = nil
	}
	c.providerEndpoints = urls
	c.providerEndpointsRead = time.Now()
	return urls, nil
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
	endpoints, err := c.exportEndpoints(ctx, false)
	if err != nil {
		return "", err
	}
	if endpoint, ok := c.chosenEndpoint(clusterID); ok {
		return endpoint, nil
	}
	if len(endpoints) == 1 {
		// Nothing to choose between. Probing would only turn a precise error
		// from the real call ("the claim was not accepted") into a vague one.
		return endpoints[0], nil
	}

	endpoint, errs := c.probeAll(ctx, endpoints, clusterID)
	if endpoint != "" {
		return endpoint, nil
	}

	// Nothing served it. The set may simply be out of date -- a shard's URL
	// appears only once the export has a consumer there -- so look again before
	// giving up, ignoring the refresh interval.
	refreshed, rerr := c.exportEndpoints(ctx, true)
	if rerr == nil && !sameEndpoints(refreshed, endpoints) {
		if endpoint, moreErrs := c.probeAll(ctx, refreshed, clusterID); endpoint != "" {
			return endpoint, nil
		} else {
			errs = append(errs, moreErrs...)
		}
	}
	return "", fmt.Errorf("dataplane: no export virtual workspace serves cluster %s: %w", clusterID, errors.Join(errs...))
}

// chosenEndpoint returns the endpoint already settled on for clusterID.
func (c *Callers) chosenEndpoint(clusterID string) (string, bool) {
	c.providerMu.Lock()
	defer c.providerMu.Unlock()
	endpoint, ok := c.clusterEndpoint[clusterID]
	return endpoint, ok
}

// probeAll returns the first endpoint that serves clusterID, remembering it,
// or every reason none did.
func (c *Callers) probeAll(ctx context.Context, endpoints []string, clusterID string) (string, []error) {
	var errs []error
	for _, endpoint := range endpoints {
		if err := c.probeEndpoint(ctx, endpoint, clusterID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", endpoint, err))
			continue
		}
		c.providerMu.Lock()
		if c.clusterEndpoint == nil {
			c.clusterEndpoint = map[string]string{}
		}
		c.clusterEndpoint[clusterID] = endpoint
		c.providerMu.Unlock()
		return endpoint, nil
	}
	return "", errs
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
