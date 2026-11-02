// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package byolimits

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kubeinformers "github.com/kcp-dev/client-go/informers"
	kubeCorelisters "github.com/kcp-dev/client-go/listers/core/v1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	corelisters "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"
	virtualcontext "github.com/kcp-dev/virtual-workspace-framework/pkg/context"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

const pathAnnotation = "kcp.io/path"

type account struct {
	tokens   float64
	updated  time.Time
	requests int
	streams  int
}

// Guard is process-local. Idle accounts are reclaimed only after their token
// buckets have fully refilled, and active upgraded connections are never evicted.
type Guard struct {
	config             Config
	mu                 sync.Mutex
	accounts           map[string]*account
	now                func() time.Time
	lister             corelisters.LogicalClusterClusterLister
	synced             cache.InformerSynced
	quotaLister        kubeCorelisters.ResourceQuotaClusterLister
	quotaSynced        cache.InformerSynced
	mainInstalled      atomic.Bool
	virtualInstalled   atomic.Bool
	admissionInstalled atomic.Bool
}

// SetKubeInformers is called by kcp's public admission initializer. Resource
// quota lookup stays cluster-aware and uses only the local shard's informer.
func (g *Guard) SetKubeInformers(local, _ kubeinformers.SharedInformerFactory) {
	i := local.Core().V1().ResourceQuotas()
	g.quotaLister, g.quotaSynced = i.Lister(), i.Informer().HasSynced
}

func New(config Config) (*Guard, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if !config.Enabled() {
		return nil, fmt.Errorf("BYO limits are disabled")
	}
	return &Guard{config: config, accounts: map[string]*account{}, now: time.Now}, nil
}

// SetInformers is called during native admission initialization, before serving.
func (g *Guard) SetInformers(local kcpinformers.SharedInformerFactory) {
	i := local.Core().V1alpha1().LogicalClusters()
	g.lister, g.synced = i.Lister(), i.Informer().HasSynced
}

func (g *Guard) Ready() error {
	if g == nil {
		return fmt.Errorf("BYO limits are disabled")
	}
	if !g.admissionInstalled.Load() || !g.mainInstalled.Load() || !g.virtualInstalled.Load() {
		return fmt.Errorf("BYO native admission and request guards are not all installed")
	}
	if g.synced == nil || !g.synced() {
		return fmt.Errorf("BYO workspace ownership informer is not synchronized")
	}
	if g.quotaLister == nil || g.quotaSynced == nil || !g.quotaSynced() {
		return fmt.Errorf("consumer quota informer is not synchronized")
	}
	return nil
}

// WaitConsumerQuotaMarker waits until the synchronized local LogicalCluster
// informer observes the trusted consumer quota marker for the requested
// cluster ID. The marker write's API response can precede informer delivery,
// so inventory/quota snapshot code must wait for this proof before proceeding.
func (g *Guard) WaitConsumerQuotaMarker(ctx context.Context, clusterID string) error {
	if g == nil {
		return fmt.Errorf("BYO limits are disabled")
	}
	if clusterID == "" || strings.ContainsAny(clusterID, ":/*%\\") {
		return fmt.Errorf("invalid logical cluster identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := g.Ready(); err != nil {
			lastErr = err
		} else {
			lc, err := g.lister.Cluster(logicalcluster.Name(clusterID)).Get(corev1alpha1.LogicalClusterName)
			if err == nil && lc.Annotations[ConsumerQuotaRequiredAnnotation] == ConsumerQuotaRequiredValue {
				return nil
			}
			if err != nil {
				lastErr = err
			} else {
				lastErr = fmt.Errorf("consumer quota marker is not observed")
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for consumer quota marker on logical cluster %s: %w (last state: %v)", clusterID, ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func organization(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("workspace has no protected canonical path")
	}
	if !strings.HasPrefix(path, kcppaths.TenantsParent+":") {
		return "", nil
	}
	org, _, _ := strings.Cut(strings.TrimPrefix(path, kcppaths.TenantsParent+":"), ":")
	if org == "" {
		return "", fmt.Errorf("invalid organization workspace path")
	}
	return org, nil
}

func (g *Guard) clusterOwner(cluster string) (string, error) {
	// kcp's bootstrap clusters are structural identities, not actor exemptions.
	if cluster == "root" || strings.HasPrefix(cluster, "system:") {
		return "", nil
	}
	if cluster == "" || strings.ContainsAny(cluster, ":/*%\\") {
		return "", fmt.Errorf("invalid logical cluster identity")
	}
	if g.synced == nil || !g.synced() || g.lister == nil {
		return "", fmt.Errorf("workspace ownership is not synchronized")
	}
	lc, err := g.lister.Cluster(logicalcluster.Name(cluster)).Get(corev1alpha1.LogicalClusterName)
	if err != nil {
		return "", fmt.Errorf("workspace ownership unavailable: %w", err)
	}
	return organization(lc.Annotations[pathAnnotation])
}

// owners charges target workspace, authenticated SA origin and resolved export
// domain. A cross-organization call is conservatively charged to both owners.
// Only context supplied by kcp authentication/routing is read, never headers.
func (g *Guard) owners(ctx context.Context) ([]string, error) {
	u, ok := request.UserFrom(ctx)
	if !ok || u == nil {
		return nil, fmt.Errorf("authenticated identity is missing")
	}
	set := map[string]bool{}
	add := func(cluster string) error {
		org, err := g.clusterOwner(cluster)
		if err != nil {
			return err
		}
		if org != "" {
			set[org] = true
		}
		return nil
	}
	if strings.HasPrefix(u.GetName(), serviceaccount.ServiceAccountUsernamePrefix) {
		origin := u.GetExtra()[serviceaccount.ClusterNameKey]
		if len(origin) != 1 || origin[0] == "" {
			return nil, fmt.Errorf("service account has no unique authenticated source cluster")
		}
		if err := add(origin[0]); err != nil {
			return nil, err
		}
	}
	domain := ""
	if name, ok := virtualcontext.VirtualWorkspaceNameFrom(ctx); ok && name == "apiexport" {
		// Dynamic domains have a different grammar in other virtual
		// workspaces (notably native workspace initializers).
		domain = string(dynamiccontext.APIDomainKeyFrom(ctx))
		if domain == "" {
			return nil, fmt.Errorf("API export virtual workspace has no resolved domain")
		}
	}
	if domain != "" {
		cluster, name, valid := strings.Cut(domain, "/")
		if !valid || name == "" || strings.Contains(name, "/") {
			return nil, fmt.Errorf("invalid resolved API export domain")
		}
		if err := add(cluster); err != nil {
			return nil, err
		}
	}
	cluster := request.ClusterFrom(ctx)
	if cluster != nil && cluster.Wildcard {
		// Native platform wildcard watches can have no export domain. They
		// remain authenticated/authorized by kcp; an org SA still pays its
		// source account above. A native wildcard is shard-scoped and kcp's
		// authorizer must authorize that scope; it is not a consumer workspace.
	} else if cluster != nil && !cluster.Name.Empty() {
		if err := add(cluster.Name.String()); err != nil {
			if !bootstrapLogicalCluster(ctx, u) {
				return nil, err
			}
		}
	} else if domain != "" {
		return nil, fmt.Errorf("virtual workspace has no resolved cluster")
	}
	orgs := make([]string, 0, len(set))
	for org := range set {
		orgs = append(orgs, org)
	}
	sort.Strings(orgs)
	return orgs, nil
}

func bootstrapIdentity(u user.Info) bool {
	if u == nil {
		return false
	}
	for _, group := range u.GetGroups() {
		switch group {
		case user.SystemPrivilegedGroup, bootstrap.SystemLogicalClusterAdmin, bootstrap.SystemExternalLogicalClusterAdmin, bootstrap.SystemKcpWorkspaceBootstrapper:
			return true
		}
	}
	return false
}

// Before a newly allocated LC exists in the informer, kcp must create/read
// that singleton. This does not exempt other bootstrap objects or admin data
// writes. Once ownership exists, even these requests are charged normally.
func bootstrapLogicalCluster(ctx context.Context, u user.Info) bool {
	i, ok := request.RequestInfoFrom(ctx)
	return ok && bootstrapIdentity(u) && i.APIGroup == "core.kcp.io" && i.Resource == "logicalclusters" && (i.Name == "cluster" || (i.Name == "" && i.Verb == "create")) && (i.Verb == "create" || i.Verb == "get")
}

func (g *Guard) acquire(orgs []string, stream bool) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	missing := 0
	requested := make(map[string]struct{}, len(orgs))
	for _, org := range orgs {
		requested[org] = struct{}{}
		if g.accounts[org] == nil {
			missing++
		}
	}
	type pending struct {
		org    string
		acct   *account
		tokens float64
	}
	validated := make([]pending, 0, len(orgs))
	for _, org := range orgs {
		a := g.accounts[org]
		if a == nil {
			a = &account{tokens: float64(g.config.Burst), updated: now}
		}
		elapsed := max(0, now.Sub(a.updated).Seconds())
		tokens := a.tokens
		if elapsed > 0 {
			tokens = min(float64(g.config.Burst), tokens+elapsed*g.config.RequestsPerSecond)
		}
		if tokens < 1 || a.requests >= g.config.MaxConcurrentRequests || (stream && a.streams >= g.config.MaxConcurrentStreams) {
			return nil, fmt.Errorf("organization request budget exhausted")
		}
		validated = append(validated, pending{org: org, acct: a, tokens: tokens})
	}
	needed := len(g.accounts) + missing - g.config.MaxOrganizations
	victims := make([]string, 0, max(0, needed))
	if needed > 0 {
		// Reclaim only when capacity is needed. A full, idle bucket has already
		// earned its next burst, so replacing it with a new full bucket cannot
		// increase that organization's rate. Never reclaim an owner in this
		// multi-organization acquisition. Keep victim selection provisional so
		// a denied acquisition leaves every account unchanged.
		for org, a := range g.accounts {
			if _, used := requested[org]; used || a.requests != 0 || a.streams != 0 || !g.fullyRefilled(a, now) {
				continue
			}
			victims = append(victims, org)
			if len(victims) == needed {
				break
			}
		}
		if len(victims) < needed {
			return nil, fmt.Errorf("organization capacity reached")
		}
	}
	for _, org := range victims {
		delete(g.accounts, org)
	}
	for _, p := range validated {
		a := p.acct
		if g.accounts[p.org] == nil {
			g.accounts[p.org] = a
		}
		a.tokens = p.tokens - 1
		a.updated = now
		a.requests++
		if stream {
			a.streams++
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			for _, org := range orgs {
				g.accounts[org].requests--
				if stream {
					g.accounts[org].streams--
				}
			}
		})
	}, nil
}

func (g *Guard) fullyRefilled(a *account, now time.Time) bool {
	elapsed := max(0, now.Sub(a.updated).Seconds())
	return a.tokens+elapsed*g.config.RequestsPerSecond >= float64(g.config.Burst)
}

type accountedKey struct{}

func streaming(r *http.Request) bool {
	i, ok := request.RequestInfoFrom(r.Context())
	// RequestInfo is produced by kcp's route parser. An arbitrary Upgrade
	// header on an ordinary write must not buy the longer stream deadline.
	return ok && (i.Verb == "watch" || i.Subresource != "" && i.Subresource != "status" && i.Subresource != "scale")
}

func deny(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, err.Error(), code)
}

// Wrap must run inside kcp authentication and authorization, after cluster and
// export-domain resolution. It is shared by native and virtual API dispatch.
func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(accountedKey{}) == g {
			next.ServeHTTP(w, r)
			return
		}
		orgs, err := g.owners(r.Context())
		if err != nil {
			deny(w, http.StatusServiceUnavailable, err)
			return
		}
		if len(orgs) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		stream := streaming(r)
		release, err := g.acquire(orgs, stream)
		if err != nil {
			deny(w, http.StatusTooManyRequests, err)
			return
		}
		if r.ContentLength > g.config.MaxRequestBytes {
			release()
			deny(w, http.StatusRequestEntityTooLarge, fmt.Errorf("organization request exceeds byte limit"))
			return
		}
		duration := g.config.RequestTimeout
		if stream {
			duration = g.config.StreamTimeout
		}
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), accountedKey{}, g), duration)
		defer cancel()
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, g.config.MaxRequestBytes)
		lifetime := newLifetime(w, time.Now().Add(duration), release)
		defer lifetime.finish()
		if err := lifetime.prepare(); err != nil {
			deny(w, http.StatusServiceUnavailable, fmt.Errorf("organization request deadlines unavailable: %w", err))
			return
		}
		next.ServeHTTP(lifetime.writer(), r)
	})
}
