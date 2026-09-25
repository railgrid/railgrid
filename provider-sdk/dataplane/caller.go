// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	// providerEndpoint is the export virtual-workspace base URL once known:
	// given by WithProviderEndpoint, or read from the APIExportEndpointSlice
	// named after the export in the provider workspace on first use.
	providerEndpoint string
	providerMu       sync.Mutex

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
		base:       base,
		maxEntries: DefaultCallerCacheSize,
		ttl:        DefaultCallerCacheTTL,
		cache:      map[string]*callerEntry{},
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
	endpoint, err := c.exportEndpoint(context.Background())
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

// ExportEndpoint returns the provider's export virtual-workspace base URL
// (…/services/apiexport/<cluster>/<export>), for callers that address another
// provider's kinds or verbs through this provider's own export: a claimed
// resource or custom subresource is reached at
// <endpoint>/clusters/<tenant>/apis/<group>/<version>/<resource>/<name>[/<verb>]
// with the provider's own credential.
func (c *Callers) ExportEndpoint(ctx context.Context) (string, error) {
	if c == nil || c.provider == nil {
		return "", fmt.Errorf("dataplane: this provider has no provider-scoped config; ExportEndpoint needs WithProviderConfig")
	}
	return c.exportEndpoint(ctx)
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
// spec.dependencies[].composes[] entry naming "{resource}/{verb}" with verbs
// ["*"], because kcp checks the HTTP method as the verb) — through its own
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
	endpoint, err := c.exportEndpoint(ctx)
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

// exportEndpoint returns the export virtual-workspace base URL, reading it
// once from the APIExportEndpointSlice named after the export in the provider
// workspace (which is what c.provider addresses). That slice is what the
// provider's controllers watch tenant workspaces through already, so the
// subresource path acts through exactly the same door.
func (c *Callers) exportEndpoint(ctx context.Context) (string, error) {
	c.providerMu.Lock()
	defer c.providerMu.Unlock()
	if c.providerEndpoint != "" {
		return c.providerEndpoint, nil
	}
	if c.providerExport == "" {
		return "", fmt.Errorf("dataplane: the subresource path needs the provider's export name (WithProviderConfig) or its virtual-workspace URL (WithProviderEndpoint)")
	}
	client, err := dynamic.NewForConfig(c.provider)
	if err != nil {
		return "", fmt.Errorf("dataplane: provider client: %w", err)
	}
	slice, err := client.Resource(APIExportEndpointSlices()).Get(ctx, c.providerExport, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("dataplane: reading APIExportEndpointSlice %s: %w", c.providerExport, err)
	}
	endpoints, _, err := unstructured.NestedSlice(slice.Object, "status", "endpoints")
	if err != nil {
		return "", fmt.Errorf("dataplane: APIExportEndpointSlice %s: %w", c.providerExport, err)
	}
	for _, e := range endpoints {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if u, _ := entry["url"].(string); strings.TrimSpace(u) != "" {
			c.providerEndpoint = strings.TrimRight(strings.TrimSpace(u), "/")
			return c.providerEndpoint, nil
		}
	}
	return "", fmt.Errorf("dataplane: APIExportEndpointSlice %s publishes no endpoint yet", c.providerExport)
}
