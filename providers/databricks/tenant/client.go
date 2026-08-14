// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	multicluster "sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/queryapi"
)

var (
	tablesGVR = databricksv1alpha1.SchemeGroupVersion.WithResource("tables")
)

const (
	defaultClientCacheCapacity = 128
	defaultClientCacheTTL      = 10 * time.Minute
)

type cacheRecord struct {
	createdAt time.Time
	lastUsed  uint64
}

type ClientFactory struct {
	baseHost  string
	baseTLS   rest.TLSClientConfig
	authority ClusterAuthority

	configMu      sync.RWMutex
	configured    bool
	authorityMu   sync.RWMutex
	mu            sync.Mutex
	hot           map[string]dynamic.Interface
	authHot       map[string]authorizationv1client.AuthorizationV1Interface
	hotMeta       map[string]cacheRecord
	authMeta      map[string]cacheRecord
	cacheCapacity int
	cacheTTL      time.Duration
	now           func() time.Time
	cacheClock    uint64
}

// ClusterAuthority is the provider-owned multicluster manager. Its client is
// authenticated with the provider ServiceAccount, so provider-owned Tables,
// Warehouses, Connections, and credential Secrets are resolved with provider
// authority rather than the forwarded caller token.
type ClusterAuthority interface {
	GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error)
}

func NewClientFactory(base *rest.Config) *ClientFactory {
	if base == nil {
		return nil
	}
	f := newClientFactory()
	if err := f.SetBaseConfig(base); err != nil {
		return nil
	}
	return f
}

// NewDeferredClientFactory creates a factory whose provider kubeconfig can be
// installed after the HTTP server starts. Requests fail closed until
// SetBaseConfig succeeds; this lets a controller retry recover tenant
// discovery/action dependencies without rebuilding the route handlers.
func NewDeferredClientFactory() *ClientFactory {
	return newClientFactory()
}

func newClientFactory() *ClientFactory {
	return &ClientFactory{
		hot:           make(map[string]dynamic.Interface),
		authHot:       make(map[string]authorizationv1client.AuthorizationV1Interface),
		hotMeta:       make(map[string]cacheRecord),
		authMeta:      make(map[string]cacheRecord),
		cacheCapacity: defaultClientCacheCapacity,
		cacheTTL:      defaultClientCacheTTL,
		now:           time.Now,
	}
}

// SetBaseConfig installs the provider's non-authority kubeconfig details used
// to construct caller-token clients for tenant logical clusters. Bearer and
// exec credentials are deliberately not retained: provider authority is
// supplied by the live multicluster manager, while tenant reads and SSARs use
// the forwarded caller token.
func (f *ClientFactory) SetBaseConfig(base *rest.Config) error {
	if f == nil {
		return errors.New("tenant client unavailable")
	}
	if base == nil {
		return errors.New("provider kubeconfig is unavailable")
	}
	baseHost, err := stripClusterSuffix(base.Host)
	if err != nil {
		baseHost = strings.TrimRight(base.Host, "/")
	}
	if strings.TrimSpace(baseHost) == "" {
		return errors.New("provider kubeconfig host is empty")
	}
	tls := base.TLSClientConfig
	tls.CertData = nil
	tls.CertFile = ""
	tls.KeyData = nil
	tls.KeyFile = ""
	f.configMu.Lock()
	f.baseHost = baseHost
	f.baseTLS = tls
	f.configured = true
	f.configMu.Unlock()
	// A changed bootstrap host must not leave clients for the previous host in
	// the cache. Clear under the cache lock so in-flight requests either use the
	// old config/client consistently or observe the new one on their next call.
	f.mu.Lock()
	f.hot = make(map[string]dynamic.Interface)
	f.authHot = make(map[string]authorizationv1client.AuthorizationV1Interface)
	f.hotMeta = make(map[string]cacheRecord)
	f.authMeta = make(map[string]cacheRecord)
	f.mu.Unlock()
	return nil
}

// Configured reports whether caller-token tenant clients can be constructed.
func (f *ClientFactory) Configured() bool {
	if f == nil {
		return false
	}
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	return f.configured
}

// SetAuthority wires the multicluster manager after it has been constructed.
// The HTTP server is started only after this is set when controller startup is
// available; a nil authority makes direct actions fail closed.
func (f *ClientFactory) SetAuthority(authority ClusterAuthority) {
	if f != nil {
		f.authorityMu.Lock()
		defer f.authorityMu.Unlock()
		f.authority = authority
	}
}

// Ready reports whether the factory has both caller-token client configuration
// and a live provider authority. The optional Ready method is implemented by
// the production manager wrapper; test and embedding implementations that do
// not expose it are treated as available once configured.
func (f *ClientFactory) Ready() bool {
	if f == nil || !f.Configured() {
		return false
	}
	f.authorityMu.RLock()
	authority := f.authority
	f.authorityMu.RUnlock()
	if authority == nil {
		return false
	}
	if ready, ok := authority.(interface{ Ready() bool }); ok {
		return ready.Ready()
	}
	return true
}

func (f *ClientFactory) AuthorityClient(ctx context.Context, clusterID string) (client.Client, error) {
	if f == nil {
		return nil, errors.New("provider authority client unavailable")
	}
	f.authorityMu.RLock()
	authority := f.authority
	f.authorityMu.RUnlock()
	if authority == nil {
		return nil, errors.New("provider authority client unavailable")
	}
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" || clusterID == "." || clusterID == ".." || url.PathEscape(clusterID) != clusterID {
		return nil, errors.New("invalid tenant logical-cluster ID")
	}
	cl, err := authority.GetCluster(ctx, multicluster.ClusterName(clusterID))
	if err != nil {
		return nil, fmt.Errorf("provider authority cluster %q: %w", clusterID, err)
	}
	if cl == nil || cl.GetClient() == nil {
		return nil, fmt.Errorf("provider authority cluster %q has no client", clusterID)
	}
	return cl.GetClient(), nil
}

func (f *ClientFactory) For(clusterID, token string) (dynamic.Interface, error) {
	clusterID = strings.TrimSpace(clusterID)
	cfg, err := f.configFor(clusterID, token)
	if err != nil {
		return nil, err
	}
	key := clusterID + ":" + hashToken(token)

	dyn, ok := f.dynamicFromCache(key)
	if ok {
		return dyn, nil
	}

	dyn, err = dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client for cluster %q: %w", clusterID, err)
	}

	return f.storeDynamic(key, dyn), nil
}

func (f *ClientFactory) configFor(clusterID, token string) (*rest.Config, error) {
	if f == nil {
		return nil, errors.New("tenant client unavailable")
	}
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" || clusterID == "." || clusterID == ".." || url.PathEscape(clusterID) != clusterID {
		return nil, errors.New("invalid tenant logical-cluster ID")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("no bearer token on request; cannot act on the tenant's behalf")
	}
	f.configMu.RLock()
	configured := f.configured
	baseHost := f.baseHost
	baseTLS := f.baseTLS
	f.configMu.RUnlock()
	if !configured {
		return nil, errors.New("tenant client unavailable (provider kubeconfig not set)")
	}
	return &rest.Config{Host: baseHost + "/clusters/" + clusterID, BearerToken: token, TLSClientConfig: baseTLS}, nil
}

// AuthorizationFor returns a caller-token-scoped authorization client for
// delegated SelfSubjectAccessReview checks. The provider's bootstrap
// credential is never used to authorize an action on behalf of a caller.
func (f *ClientFactory) AuthorizationFor(clusterID, token string) (authorizationv1client.AuthorizationV1Interface, error) {
	clusterID = strings.TrimSpace(clusterID)
	cfg, err := f.configFor(clusterID, token)
	if err != nil {
		return nil, err
	}
	key := clusterID + ":" + hashToken(token)
	client, ok := f.authFromCache(key)
	if ok {
		return client, nil
	}
	client, err = authorizationv1client.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("authorization client for cluster %q: %w", clusterID, err)
	}
	return f.storeAuth(key, client), nil
}

func (f *ClientFactory) cacheNow() time.Time {
	if f != nil && f.now != nil {
		return f.now()
	}
	return time.Now()
}

func (f *ClientFactory) cacheTTLValue() time.Duration {
	if f != nil && f.cacheTTL > 0 {
		return f.cacheTTL
	}
	return defaultClientCacheTTL
}

func (f *ClientFactory) cacheCapacityValue() int {
	if f != nil && f.cacheCapacity > 0 {
		return f.cacheCapacity
	}
	return defaultClientCacheCapacity
}

func (f *ClientFactory) nextCacheClockLocked() uint64 {
	f.cacheClock++
	return f.cacheClock
}

func (f *ClientFactory) dynamicFromCache(key string) (dynamic.Interface, bool) {
	if f == nil {
		return nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hot == nil {
		return nil, false
	}
	dyn, ok := f.hot[key]
	if !ok {
		return nil, false
	}
	now := f.cacheNow()
	if f.hotMeta == nil {
		f.hotMeta = make(map[string]cacheRecord)
	}
	record := f.hotMeta[key]
	if record.createdAt.IsZero() {
		record.createdAt = now
	}
	if now.Sub(record.createdAt) >= f.cacheTTLValue() {
		delete(f.hot, key)
		delete(f.hotMeta, key)
		return nil, false
	}
	record.lastUsed = f.nextCacheClockLocked()
	f.hotMeta[key] = record
	return dyn, true
}

func (f *ClientFactory) storeDynamic(key string, dyn dynamic.Interface) dynamic.Interface {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hot == nil {
		f.hot = make(map[string]dynamic.Interface)
	}
	if f.hotMeta == nil {
		f.hotMeta = make(map[string]cacheRecord)
	}
	for existingKey := range f.hot {
		if _, ok := f.hotMeta[existingKey]; !ok {
			f.hotMeta[existingKey] = cacheRecord{createdAt: f.cacheNow(), lastUsed: f.nextCacheClockLocked()}
		}
	}
	if existing, ok := f.hot[key]; ok {
		record := f.hotMeta[key]
		if record.createdAt.IsZero() {
			record.createdAt = f.cacheNow()
		}
		record.lastUsed = f.nextCacheClockLocked()
		f.hotMeta[key] = record
		return existing
	}
	f.hot[key] = dyn
	f.hotMeta[key] = cacheRecord{createdAt: f.cacheNow(), lastUsed: f.nextCacheClockLocked()}
	f.evictDynamicLocked()
	return dyn
}

func (f *ClientFactory) evictDynamicLocked() {
	for len(f.hot) > f.cacheCapacityValue() {
		oldestKey, _ := oldestCacheKey(f.hotMeta)
		if oldestKey == "" {
			return
		}
		delete(f.hot, oldestKey)
		delete(f.hotMeta, oldestKey)
	}
}

func (f *ClientFactory) authFromCache(key string) (authorizationv1client.AuthorizationV1Interface, bool) {
	if f == nil {
		return nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.authHot == nil {
		return nil, false
	}
	client, ok := f.authHot[key]
	if !ok {
		return nil, false
	}
	now := f.cacheNow()
	if f.authMeta == nil {
		f.authMeta = make(map[string]cacheRecord)
	}
	record := f.authMeta[key]
	if record.createdAt.IsZero() {
		record.createdAt = now
	}
	if now.Sub(record.createdAt) >= f.cacheTTLValue() {
		delete(f.authHot, key)
		delete(f.authMeta, key)
		return nil, false
	}
	record.lastUsed = f.nextCacheClockLocked()
	f.authMeta[key] = record
	return client, true
}

func (f *ClientFactory) storeAuth(key string, value authorizationv1client.AuthorizationV1Interface) authorizationv1client.AuthorizationV1Interface {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.authHot == nil {
		f.authHot = make(map[string]authorizationv1client.AuthorizationV1Interface)
	}
	if f.authMeta == nil {
		f.authMeta = make(map[string]cacheRecord)
	}
	for existingKey := range f.authHot {
		if _, ok := f.authMeta[existingKey]; !ok {
			f.authMeta[existingKey] = cacheRecord{createdAt: f.cacheNow(), lastUsed: f.nextCacheClockLocked()}
		}
	}
	if existing, ok := f.authHot[key]; ok {
		record := f.authMeta[key]
		if record.createdAt.IsZero() {
			record.createdAt = f.cacheNow()
		}
		record.lastUsed = f.nextCacheClockLocked()
		f.authMeta[key] = record
		return existing
	}
	f.authHot[key] = value
	f.authMeta[key] = cacheRecord{createdAt: f.cacheNow(), lastUsed: f.nextCacheClockLocked()}
	f.evictAuthLocked()
	return value
}

func (f *ClientFactory) evictAuthLocked() {
	for len(f.authHot) > f.cacheCapacityValue() {
		oldestKey, _ := oldestCacheKey(f.authMeta)
		if oldestKey == "" {
			return
		}
		delete(f.authHot, oldestKey)
		delete(f.authMeta, oldestKey)
	}
}

func oldestCacheKey(records map[string]cacheRecord) (string, uint64) {
	var oldestKey string
	var oldest uint64
	for key, record := range records {
		if oldestKey == "" || record.lastUsed < oldest {
			oldestKey, oldest = key, record.lastUsed
		}
	}
	return oldestKey, oldest
}

func (f *ClientFactory) TableResolverForRequest(r *http.Request) queryapi.TableResolver {
	if f == nil {
		return queryapi.UnavailableResolver{Message: "tenant client unavailable (provider kubeconfig not set)"}
	}
	ident := identityFromRequest(r)
	return tableResolver{factory: f, identity: ident}
}

type identity struct {
	tenantPath string
	clusterID  string
	token      string
}

func identityFromRequest(r *http.Request) identity {
	return identity{
		tenantPath: r.Header.Get("X-Faros-Tenant"),
		clusterID:  r.Header.Get("X-Faros-Cluster"),
		token:      bearerToken(r),
	}
}

func bearerToken(r *http.Request) string {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

type tableResolver struct {
	factory  *ClientFactory
	identity identity
}

func (r tableResolver) ListTables(ctx context.Context) (map[string]queryapi.TableRef, error) {
	tables, _, err := r.ListTablesBounded(ctx, queryapi.MaxTableListItems)
	return tables, err
}

// ListTablesBounded asks the tenant KCP API for one bounded page and returns
// whether another item/page exists. Table names come from KCP metadata, never
// from the spec's Databricks full name, so MCP tableRef values remain exact
// resource handles.
func (r tableResolver) ListTablesBounded(ctx context.Context, limit int) (map[string]queryapi.TableRef, bool, error) {
	if limit < 1 {
		limit = queryapi.MaxTableListItems
	}
	dyn, err := r.dynamicClient()
	if err != nil {
		return nil, false, err
	}
	list, err := dyn.Resource(tablesGVR).List(ctx, metav1.ListOptions{Limit: int64(limit) + 1})
	if err != nil {
		return nil, false, err
	}
	truncated := len(list.Items) > limit || list.GetContinue() != ""
	items := list.Items
	if len(items) > limit {
		items = items[:limit]
	}
	out := make(map[string]queryapi.TableRef, len(items))
	for _, item := range items {
		ref, ok := tableRefFromObject(item)
		if ok {
			out[item.GetName()] = ref
		}
	}
	return out, truncated, nil
}

func (r tableResolver) GetTable(ctx context.Context, name string) (queryapi.TableRef, bool, error) {
	dyn, err := r.dynamicClient()
	if err != nil {
		return queryapi.TableRef{}, false, err
	}
	item, err := dyn.Resource(tablesGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return queryapi.TableRef{}, false, nil
	}
	if err != nil {
		return queryapi.TableRef{}, false, err
	}
	ref, ok := tableRefFromObject(*item)
	if !ok {
		return queryapi.TableRef{}, false, nil
	}
	return ref, true, nil
}

func (r tableResolver) dynamicClient() (dynamic.Interface, error) {
	if r.identity.tenantPath == "" {
		return nil, errors.New("no tenant identity on this request; bearer token did not resolve to a workspace")
	}
	if r.identity.clusterID == "" {
		return nil, errors.New("no workspace cluster on this request (X-Faros-Cluster missing)")
	}
	if r.factory == nil {
		return nil, errors.New("tenant client unavailable (provider kubeconfig not set)")
	}
	return r.factory.For(r.identity.clusterID, r.identity.token)
}

// GetTableSchema returns the controller-cached column schema from the Table
// resource's status. Callers (describe_table) surface it so clients never have
// to guess column names against the bounded query contract.
func (r tableResolver) GetTableSchema(ctx context.Context, name string) ([]queryapi.QueryColumn, string, error) {
	dyn, err := r.dynamicClient()
	if err != nil {
		return nil, "", err
	}
	item, err := dyn.Resource(tablesGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	rawColumns, _, _ := unstructured.NestedSlice(item.Object, "status", "columns")
	columns := make([]queryapi.QueryColumn, 0, len(rawColumns))
	for _, raw := range rawColumns {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		columnName, _ := entry["name"].(string)
		if strings.TrimSpace(columnName) == "" {
			continue
		}
		columnType, _ := entry["type"].(string)
		columns = append(columns, queryapi.QueryColumn{Name: columnName, Type: columnType})
	}
	refreshedAt, _, _ := unstructured.NestedString(item.Object, "status", "refreshedAt")
	return columns, refreshedAt, nil
}

func tableRefFromObject(item unstructured.Unstructured) (queryapi.TableRef, bool) {
	catalog, _, _ := unstructured.NestedString(item.Object, "spec", "catalog")
	schemaName, _, _ := unstructured.NestedString(item.Object, "spec", "schema")
	table, _, _ := unstructured.NestedString(item.Object, "spec", "table")
	if strings.TrimSpace(catalog) == "" || strings.TrimSpace(schemaName) == "" || strings.TrimSpace(table) == "" {
		return queryapi.TableRef{}, false
	}
	return queryapi.TableRef{Catalog: catalog, Schema: schemaName, Table: table}, true
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func stripClusterSuffix(host string) (string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("parse base kubeconfig host %q: %w", host, err)
	}
	idx := strings.Index(u.Path, "/clusters/")
	if idx < 0 {
		return strings.TrimRight(host, "/"), nil
	}
	u.Path = u.Path[:idx]
	return strings.TrimRight(u.String(), "/"), nil
}
