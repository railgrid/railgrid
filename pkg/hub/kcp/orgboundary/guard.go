// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package orgboundary installs the native KCP boundary for organization-owned
// provider credentials. It is compiled into both the embedded shard and the
// supported standalone Railgrid KCP command; it does not patch KCP.
package orgboundary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	apislisters "github.com/kcp-dev/sdk/client/listers/apis/v1alpha2"
	corelisters "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

const (
	// DefaultMaxRequestBytes covers existing provider transfers (up to 96 MiB).
	// Ordinary KCP object decoders retain their smaller object limits.
	DefaultMaxRequestBytes int64 = 96 << 20
	// DefaultRequestsPerSecond is the sustained per-organization request rate.
	DefaultRequestsPerSecond = 100.0
	// DefaultBurst is the per-organization token bucket burst.
	DefaultBurst = 200
	// DefaultMaxConcurrentRequests bounds active API calls and streams per org.
	DefaultMaxConcurrentRequests = 256
	// DefaultMaxTrackedOrganizations bounds the process-local limiter table.
	DefaultMaxTrackedOrganizations = 2048
	// DefaultStreamLifetime caps a long-running request such as watch.
	DefaultStreamLifetime = 30 * time.Minute
	limiterIdleTTL        = 10 * time.Minute
	logicalClusterPathKey = "kcp.io/path"
)

// Config controls process-local request admission. It does not claim to
// enforce persistent storage quotas or a fleet-wide rate budget.
type Config struct {
	MaxRequestBytes       int64
	RequestsPerSecond     float64
	Burst                 int
	MaxConcurrentRequests int
	MaxTrackedOrgs        int
	StreamLifetime        time.Duration
}

// DefaultConfig returns the bounded baseline used by embedded and standalone
// Railgrid KCP. Operators may construct lower limits for a smaller shard.
func DefaultConfig() Config {
	return Config{
		MaxRequestBytes:       DefaultMaxRequestBytes,
		RequestsPerSecond:     DefaultRequestsPerSecond,
		Burst:                 DefaultBurst,
		MaxConcurrentRequests: DefaultMaxConcurrentRequests,
		MaxTrackedOrgs:        DefaultMaxTrackedOrganizations,
		StreamLifetime:        DefaultStreamLifetime,
	}
}

func (c Config) validate() error {
	if c.MaxRequestBytes <= 0 || c.RequestsPerSecond <= 0 || c.Burst <= 0 ||
		c.MaxConcurrentRequests <= 0 || c.MaxTrackedOrgs <= 0 || c.StreamLifetime <= 0 {
		return fmt.Errorf("organization boundary limits must all be positive")
	}
	if c.MaxTrackedOrgs > 1<<20 {
		return fmt.Errorf("organization boundary max tracked organizations must be between 1 and 1048576")
	}
	return nil
}

type orgAccount struct {
	tokens   float64
	updated  time.Time
	lastUsed time.Time
	active   int
}

// Guard shares ownership checks and request limits across the KCP main API and
// virtual-workspace APIs. Account state is process-local and bounded.
type Guard struct {
	config Config
	now    func() time.Time

	mu       sync.Mutex
	accounts map[string]*orgAccount

	clusters       corelisters.LogicalClusterClusterLister
	synced         cache.InformerSynced
	bindings       apislisters.APIBindingClusterLister
	bindingsSynced cache.InformerSynced
}

// New constructs a Guard with the supplied request limits.
func New(config Config) (*Guard, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Guard{config: config, now: time.Now, accounts: map[string]*orgAccount{}}, nil
}

// SetInformers connects the guard to KCP's trusted local LogicalCluster cache.
// Authentication extras contain logical-cluster IDs, never canonical paths;
// the ID is resolved here before any organization decision is made.
func (g *Guard) SetInformers(local kcpinformers.SharedInformerFactory) {
	if local == nil {
		return
	}
	informer := local.Core().V1alpha1().LogicalClusters().Informer()
	g.clusters = local.Core().V1alpha1().LogicalClusters().Lister()
	g.synced = informer.HasSynced
	bindingInformer := local.Apis().V1alpha2().APIBindings().Informer()
	g.bindings = local.Apis().V1alpha2().APIBindings().Lister()
	g.bindingsSynced = bindingInformer.HasSynced
}

func (g *Guard) ready() bool {
	return g != nil && g.clusters != nil && g.synced != nil && g.synced()
}

func (g *Guard) clusterPath(cluster logicalcluster.Name) (string, error) {
	if cluster.Empty() {
		return "", fmt.Errorf("request has no concrete logical cluster")
	}
	if !g.ready() {
		return "", fmt.Errorf("logical-cluster ownership cache is not synchronized")
	}
	lc, err := g.clusters.Cluster(cluster).Get(corev1alpha1.LogicalClusterName)
	if err != nil {
		return "", fmt.Errorf("logical-cluster ownership is unavailable: %w", err)
	}
	path := lc.Annotations[logicalClusterPathKey]
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("logical-cluster has no canonical path")
	}
	return path, nil
}

type sourceIdentity struct {
	user             user.Info
	path             string
	org              string
	isServiceAccount bool
	isOrgProvider    bool
	logicalClusterID logicalcluster.Name
}

func (g *Guard) sourceIdentity(info user.Info) (sourceIdentity, error) {
	identity := sourceIdentity{user: info}
	if info == nil || !strings.HasPrefix(info.GetName(), authserviceaccount.ServiceAccountUsernamePrefix) {
		return identity, nil
	}
	if serviceaccounts.IsRetiredDelegationUsername(info.GetName()) {
		return identity, fmt.Errorf("retired delegated service-account credentials must be reminted")
	}
	identity.isServiceAccount = true
	extra := info.GetExtra()[authserviceaccount.ClusterNameKey]
	if len(extra) != 1 || strings.TrimSpace(extra[0]) == "" {
		return identity, fmt.Errorf("service-account credential has no unique authenticated source logical-cluster ID")
	}
	// KCP authenticates this extra from the ServiceAccount token issuer. It is
	// an opaque logical-cluster ID; treating it as a path would make every
	// tenant ServiceAccount appear to originate at platform scope.
	identity.logicalClusterID = logicalcluster.Name(extra[0])
	path, err := g.clusterPath(identity.logicalClusterID)
	if err != nil {
		return identity, fmt.Errorf("resolve authenticated service-account source: %w", err)
	}
	identity.path = path
	identity.org, err = organizationForPath(path)
	if err != nil {
		return identity, fmt.Errorf("resolve authenticated service-account organization: %w", err)
	}
	_, _, identity.isOrgProvider = kcppaths.SplitOrgProviderPath(path)
	return identity, nil
}

func organizationForPath(path string) (string, error) {
	if !strings.HasPrefix(path, kcppaths.TenantsParent+":") {
		return "", nil
	}
	rest := strings.TrimPrefix(path, kcppaths.TenantsParent+":")
	org, _, _ := strings.Cut(rest, ":")
	if org == "" || strings.ContainsAny(org, ":/*%\\") {
		return "", fmt.Errorf("logical-cluster has an invalid organization path")
	}
	return org, nil
}

type scope struct {
	source sourceIdentity
	target string
	// A wildcard target is accepted only on the source org's own APIExport
	// virtual workspace. This preserves scoped controller watches.
	wildcard bool
}

func (g *Guard) requestScope(r *http.Request, info user.Info) (scope, error) {
	source, err := g.sourceIdentity(info)
	if err != nil {
		return scope{}, err
	}
	targetID, wildcard, inVirtualClusterPath := virtualTargetFromPath(r.URL.Path)
	if inVirtualClusterPath && targetID != "*" {
		path, err := g.clusterPath(logicalcluster.Name(targetID))
		if err != nil {
			if source.org != "" {
				return scope{}, fmt.Errorf("resolve virtual-workspace target: %w", err)
			}
		} else if org, err := organizationForPath(path); err != nil {
			return scope{}, err
		} else {
			return g.finishScope(r, source, org, false)
		}
	} else if inVirtualClusterPath && wildcard {
		if source.org != "" {
			exportID, exportName, ok := apiExportFromRequest(r)
			if !ok {
				return scope{}, fmt.Errorf("organization ServiceAccount cannot address an unscoped wildcard virtual workspace")
			}
			exportPath, err := g.clusterPath(logicalcluster.Name(exportID))
			if err != nil {
				return scope{}, fmt.Errorf("resolve wildcard APIExport owner: %w", err)
			}
			exportOrg, err := organizationForPath(exportPath)
			if err != nil || exportOrg != source.org {
				return scope{}, fmt.Errorf("organization ServiceAccount may watch only its own organization's APIExport virtual workspace")
			}
			if err := g.ensureWildcardExportBindingsAreOrgScoped(exportID, exportPath, exportName, source.org); err != nil {
				return scope{}, err
			}
			return scope{source: source, target: source.org, wildcard: true}, nil
		}
		return scope{source: source, wildcard: true}, nil
	}

	if cluster := request.ClusterFrom(r.Context()); cluster != nil {
		if cluster.Wildcard {
			if source.org != "" {
				exportID, exportName, ok := apiExportFromRequest(r)
				if !ok {
					return scope{}, fmt.Errorf("organization ServiceAccount cannot address an unscoped wildcard workspace")
				}
				exportPath, err := g.clusterPath(logicalcluster.Name(exportID))
				if err != nil {
					return scope{}, fmt.Errorf("resolve wildcard APIExport owner: %w", err)
				}
				exportOrg, err := organizationForPath(exportPath)
				if err != nil || exportOrg != source.org {
					return scope{}, fmt.Errorf("organization ServiceAccount may watch only its own organization's APIExport virtual workspace")
				}
				if err := g.ensureWildcardExportBindingsAreOrgScoped(exportID, exportPath, exportName, source.org); err != nil {
					return scope{}, err
				}
				return scope{source: source, target: source.org, wildcard: true}, nil
			}
			return scope{source: source, wildcard: true}, nil
		}
		if !cluster.Name.Empty() {
			path, err := g.clusterPath(cluster.Name)
			if err != nil {
				if source.org != "" {
					return scope{}, err
				}
			} else {
				org, err := organizationForPath(path)
				if err != nil {
					return scope{}, err
				}
				return g.finishScope(r, source, org, false)
			}
		}
	}

	if source.org != "" {
		if requestInfo, ok := request.RequestInfoFrom(r.Context()); ok && requestInfo.IsResourceRequest {
			return scope{}, fmt.Errorf("organization ServiceAccount request has no target workspace")
		}
	}
	return scope{source: source}, nil
}

func (g *Guard) finishScope(r *http.Request, source sourceIdentity, targetOrg string, wildcard bool) (scope, error) {
	if source.org != "" && targetOrg != source.org {
		return scope{}, fmt.Errorf("organization ServiceAccount may address only its source organization")
	}
	return scope{source: source, target: targetOrg, wildcard: wildcard}, nil
}

// virtualTargetFromPath recognizes the target portion of KCP's native
// APIExport VW route: /services/apiexport/<export-cluster>/<export>/clusters/<target>.
func virtualTargetFromPath(path string) (targetID string, wildcard, found bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) >= 2 && segments[0] == "clusters" {
		return segments[1], segments[1] == "*", true
	}
	if len(segments) >= 6 && segments[0] == "services" && segments[1] == "apiexport" && segments[4] == "clusters" {
		return segments[5], segments[5] == "*", true
	}
	return "", false, false
}

func apiExportFromRequest(r *http.Request) (clusterID, exportName string, found bool) {
	if r == nil {
		return "", "", false
	}
	if clusterID, exportName, found = strings.Cut(string(dynamiccontext.APIDomainKeyFrom(r.Context())), "/"); found && clusterID != "" && exportName != "" {
		return clusterID, exportName, true
	}
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segments) >= 5 && segments[0] == "services" && segments[1] == "apiexport" && segments[2] != "" && segments[3] != "" {
		return segments[2], segments[3], true
	}
	return "", "", false
}

// ensureWildcardExportBindingsAreOrgScoped prevents wildcard APIExport
// requests from reaching legacy or otherwise mis-scoped consumer workspaces.
// KCP deliberately skips its per-binding authorization check for wildcard
// requests, so the trusted local binding cache provides the tenant fence.
func (g *Guard) ensureWildcardExportBindingsAreOrgScoped(exportID, exportPath, exportName, sourceOrg string) error {
	if g.bindings == nil || g.bindingsSynced == nil || !g.bindingsSynced() {
		return fmt.Errorf("API binding ownership cache is not synchronized")
	}
	bindings, err := g.bindings.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("list API bindings for wildcard scope: %w", err)
	}
	for _, binding := range bindings {
		if !bindingReferencesExport(binding, exportID, exportPath, exportName) {
			continue
		}
		consumerID := logicalcluster.From(binding).String()
		consumerPath, err := g.clusterPath(logicalcluster.Name(consumerID))
		if err != nil {
			return fmt.Errorf("resolve wildcard APIExport consumer: %w", err)
		}
		consumerOrg, err := organizationForPath(consumerPath)
		if err != nil || consumerOrg != sourceOrg {
			return fmt.Errorf("organization ServiceAccount cannot wildcard-access an APIExport bound outside its organization")
		}
	}
	return nil
}

func bindingReferencesExport(binding *apisv1alpha2.APIBinding, exportID, exportPath, exportName string) bool {
	if binding == nil || binding.Spec.Reference.Export == nil || binding.Spec.Reference.Export.Name != exportName {
		return false
	}
	if binding.Status.APIExportClusterName != "" {
		return binding.Status.APIExportClusterName == exportID
	}
	if binding.Spec.Reference.Export.Path != "" {
		return binding.Spec.Reference.Export.Path == exportID || binding.Spec.Reference.Export.Path == exportPath
	}
	return logicalcluster.From(binding).String() == exportID
}

func hasImpersonationHeaders(header http.Header) bool {
	for key := range header {
		key = http.CanonicalHeaderKey(key)
		if key == "Impersonate-User" || key == "Impersonate-Uid" || key == "Impersonate-Group" || strings.HasPrefix(key, "Impersonate-Extra-") {
			return true
		}
	}
	return false
}

// Authenticate wraps KCP's configured request authenticator. The check runs
// immediately after native authentication and before KCP handles any
// Impersonate-* header.
type boundaryAuthenticator struct {
	delegate authenticator.Request
	guard    *Guard
}

func (a boundaryAuthenticator) AuthenticateRequest(r *http.Request) (*authenticator.Response, bool, error) {
	response, authenticated, err := a.delegate.AuthenticateRequest(r)
	if err != nil || !authenticated || response == nil || response.User == nil {
		return response, authenticated, err
	}
	source, err := a.guard.sourceIdentity(response.User)
	if err != nil {
		return nil, false, err
	}
	if source.org == "" {
		return response, true, nil
	}
	if hasImpersonationHeaders(r.Header) {
		return nil, false, errTenantServiceAccountImpersonation
	}
	// KCP's TokenReview storage authenticates the reviewed bearer token by
	// calling the configured authenticator with a synthetic request that has
	// headers but no URL. Source identity and retired-credential checks above
	// still apply, but a TokenReview has no HTTP target workspace to scope.
	if r.URL == nil {
		return response, true, nil
	}
	if _, err := a.guard.requestScope(r, response.User); err != nil {
		return nil, false, err
	}
	return response, true, nil
}

func (g *Guard) acquire(orgs []string) (func(), bool) {
	unique := map[string]struct{}{}
	for _, org := range orgs {
		if org != "" {
			unique[org] = struct{}{}
		}
	}
	keys := make([]string, 0, len(unique))
	for org := range unique {
		keys = append(keys, org)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return func() {}, true
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, org := range keys {
		if g.accounts[org] == nil && len(g.accounts) >= g.config.MaxTrackedOrgs {
			g.evictIdleLocked(now)
			if len(g.accounts) >= g.config.MaxTrackedOrgs {
				return nil, false
			}
		}
	}
	for _, org := range keys {
		a := g.accounts[org]
		if a == nil {
			a = &orgAccount{tokens: float64(g.config.Burst), updated: now, lastUsed: now}
			g.accounts[org] = a
		}
		elapsed := now.Sub(a.updated).Seconds()
		if elapsed > 0 {
			a.tokens = min(float64(g.config.Burst), a.tokens+elapsed*g.config.RequestsPerSecond)
			a.updated = now
		}
		if a.active >= g.config.MaxConcurrentRequests || a.tokens < 1 {
			return nil, false
		}
	}
	for _, org := range keys {
		a := g.accounts[org]
		a.tokens--
		a.active++
		a.lastUsed = now
	}
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, org := range keys {
			if a := g.accounts[org]; a != nil && a.active > 0 {
				a.active--
				a.lastUsed = g.now()
			}
		}
	}, true
}

func (g *Guard) evictIdleLocked(now time.Time) {
	for org, a := range g.accounts {
		if a.active == 0 && now.Sub(a.lastUsed) >= limiterIdleTTL {
			delete(g.accounts, org)
		}
	}
}

// Wrap applies request body, per-organization rate/concurrency, stream
// lifetime, APIBinding, and provider-publication controls. It is installed
// inside each main and virtual-workspace handler chain so KCP's authenticated
// user and target workspace are available in context.
func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > g.config.MaxRequestBytes {
			writeStatus(w, http.StatusRequestEntityTooLarge, metav1.StatusReasonRequestEntityTooLarge, "request body exceeds the configured size limit")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, g.config.MaxRequestBytes)

		info, _ := request.RequestInfoFrom(r.Context())
		infoUser, _ := request.UserFrom(r.Context())
		s, err := g.requestScope(r, infoUser)
		if err != nil {
			writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden, err.Error())
			return
		}
		if s.source.isServiceAccount && isWriteVerb(info) {
			if s.source.isOrgProvider && orgProviderPublicationWrite(info) {
				writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden, "organization provider credentials cannot publish APIs, endpoints, or RBAC")
				return
			}
			if s.source.org != "" && serviceAccountTokenMint(info) {
				writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden, "tenant service-account credentials cannot mint service-account tokens")
				return
			}
			if s.source.org != "" && apiBindingWrite(info) {
				if !safeAPIBindingWrite(r, info, s.source.org) {
					writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden, "tenant service-account credentials may bind only an APIExport in their own organization")
					return
				}
			}
		}

		if !trustedForRateLimit(infoUser, s.source) {
			release, allowed := g.acquire([]string{s.source.org, s.target})
			if !allowed {
				writeStatus(w, http.StatusTooManyRequests, metav1.StatusReasonTooManyRequests, "organization request rate or concurrency limit exceeded")
				return
			}
			defer release()
		}

		ctx, cancel := context.WithTimeout(r.Context(), g.config.StreamLifetime)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// trustedForRateLimit exempts platform control-plane identities from tenant
// request budgets. Tenant-origin ServiceAccounts are never exempt, regardless
// of username or groups. This handler sees KCP's effective post-impersonation
// user, so customer identities remain chargeable after the Hub proxy.
func trustedForRateLimit(info user.Info, source sourceIdentity) bool {
	if info == nil || source.org != "" {
		return false
	}
	if source.isServiceAccount {
		return source.path != ""
	}
	if info.GetName() == "system:admin" || info.GetName() == "system:kcp:bootstrapper" {
		return true
	}
	for _, group := range info.GetGroups() {
		if group == "system:masters" {
			return true
		}
	}
	return false
}

func isWriteVerb(info *request.RequestInfo) bool {
	if info == nil {
		return false
	}
	switch info.Verb {
	case "create", "update", "patch", "delete", "deletecollection":
		return true
	default:
		return false
	}
}

func orgProviderPublicationWrite(info *request.RequestInfo) bool {
	if info == nil {
		return false
	}
	switch info.APIGroup {
	case "apis.kcp.io", "apiextensions.k8s.io", "rbac.authorization.k8s.io", "cache.kcp.io", "dataplane.railgrid.ai":
		return true
	case "providers.railgrid.ai":
		return info.Resource == "catalogentries"
	default:
		return false
	}
}

func serviceAccountTokenMint(info *request.RequestInfo) bool {
	return info != nil && info.APIGroup == "" && info.Resource == "serviceaccounts" && info.Subresource == "token"
}

func apiBindingWrite(info *request.RequestInfo) bool {
	return info != nil && info.APIGroup == "apis.kcp.io" && info.Resource == "apibindings"
}

func safeAPIBindingWrite(r *http.Request, info *request.RequestInfo, sourceOrg string) bool {
	if sourceOrg == "" || info.Verb == "delete" || info.Verb == "deletecollection" || info.Verb == "patch" {
		return false
	}
	if info.Verb != "create" && info.Verb != "update" {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, DefaultMaxRequestBytes+1))
	if err != nil || int64(len(body)) > DefaultMaxRequestBytes {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var object struct {
		Spec struct {
			Reference struct {
				Export *struct {
					Path string `json:"path"`
				} `json:"export"`
			} `json:"reference"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &object); err != nil {
		return false
	}
	if object.Spec.Reference.Export == nil || object.Spec.Reference.Export.Path == "" {
		return true
	}
	org, err := organizationForPath(object.Spec.Reference.Export.Path)
	return err == nil && org == sourceOrg
}

func writeStatus(w http.ResponseWriter, code int, reason metav1.StatusReason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure,
		Reason:   reason,
		Message:  message,
		Code:     int32(code),
	})
}

var errTenantServiceAccountImpersonation = errors.New("tenant service-account credentials cannot impersonate another user")
