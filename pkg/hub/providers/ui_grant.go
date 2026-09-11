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

package providers

// UI bundles of org-owned providers.
//
// A platform provider's micro-frontend is served by the UI proxy from a URL
// the hub dials directly, and the request that fetches it carries no identity:
// the portal loads /ui/providers/{name}/main.js with a plain <script src>, and
// a script tag sends no Authorization header. That is fine for a platform
// bundle, which is the same for everyone.
//
// An org-owned provider's bundle lives in the tenant's own cluster, behind the
// edge tunnel. Two things follow. The hub cannot tell from an anonymous asset
// GET WHICH org's copy is meant, and the platform edges provider refuses every
// tunnel request that carries no bearer (providers/edges
// edges_proxy_builder.go step 1), so the hub has to attach a delegated token
// exactly as the backend proxy does for /services/providers/{name} — and a
// delegated token needs a verified (user, org, workspace) to be minted for.
//
// The grant closes that gap in two steps:
//
//  1. The portal, authenticated as the user with its X-Faros-Org /
//     X-Faros-Workspace selection, POSTs /api/providers/{name}/ui-grant. The
//     hub resolves the caller the way the backend proxy does (membership is
//     verified by the TenantResolver), checks that the caller's org owns a
//     copy of {name} with a UI, and answers with a bundle URL that carries a
//     short-lived sealed grant naming that (user, org, workspace, provider).
//  2. The portal injects <script src="/ui/providers/{name}/main.js?grant=…">.
//     The UI proxy opens the grant, resolves the ORG's copy of the provider,
//     mints the delegated token for the tuple the grant names, and forwards
//     the asset request over the edge hop — the same hop and the same token
//     swap as serveOverEdge, so the tenant's cluster never sees anything but
//     a delegated identity.
//
// The grant is sealed (AES-256-GCM under a subkey HKDF-derived from the hub's
// cross-replica secret) rather than signed: it rides in a URL that lands in
// browser history and hub access logs, and the user name inside it is not
// something those should carry in the clear. Any replica opens what any
// replica minted. It is bound to one provider and expires in minutes; it is
// not a credential at the far end — the delegated token is, and that is minted
// afresh on redemption, never embedded.
//
// The bundle is also hashed for Subresource Integrity at grant time, fetched
// through the same route on the caller's behalf, so an org-owned bundle is
// pinned in the portal document exactly as a platform one is.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/faroshq/faros/pkg/apiurl"
	"github.com/faroshq/faros/pkg/hub/serviceaccounts"
)

const (
	// PathProviderUIGrant is the gorilla/mux pattern of the grant endpoint. It
	// must be registered BEFORE the heartbeat PathPrefix route on the same
	// /api/providers prefix, or every POST there is claimed by the heartbeat
	// handler first.
	PathProviderUIGrant = PathListProviders + "/{name}/ui-grant"

	// UIGrantQueryParam is the query parameter the UI proxy reads the grant
	// from. It sits in the query rather than the path so the bundle URL keeps
	// the /ui/providers/{name}/ shape the provider's own portalkit derives its
	// service base from, and so the portal's provider-fetch allow list is
	// unchanged.
	UIGrantQueryParam = "grant"

	// uiGrantPrefix marks a grant so a stray app token or bearer pasted into
	// the query is refused before any cryptography runs.
	uiGrantPrefix = "fpui_"

	// uiGrantTTL bounds a grant's lifetime. The portal requests one immediately
	// before injecting the script and the loader gives up after 15s, so the
	// grant only needs to outlive a slow first byte over the tunnel.
	uiGrantTTL = 5 * time.Minute

	// uiGrantKeyInfo domain-separates the grant key from every other subkey
	// derived from the hub secret (app access tokens, delegated-identity
	// proofs). uiGrantAAD binds the ciphertext to this construction.
	uiGrantKeyInfo = "faros.sh/provider-ui-grant/aes-256-gcm/v1"
	uiGrantAAD     = "faros.sh/provider-ui-grant/v1"
	uiGrantVersion = 1
	uiGrantIDBytes = 16
	uiGrantMinKey  = 32
)

// errUIGrantInvalid is every reason a presented grant is not usable. The UI
// proxy answers 401 and logs the wrapped detail.
var errUIGrantInvalid = errors.New("invalid provider UI grant")

// uiGrantClaims is the sealed content of a grant: the tuple the delegated
// token is minted for on redemption, the provider the grant is bound to, and
// its validity window.
type uiGrantClaims struct {
	Version       int    `json:"v"`
	ID            string `json:"jti"`
	OrgUUID       string `json:"o"`
	WorkspaceUUID string `json:"w"`
	User          string `json:"u"`
	Provider      string `json:"p"`
	IssuedAt      int64  `json:"iat"`
	ExpiresAt     int64  `json:"exp"`
}

func (c uiGrantClaims) caller() DelegatedCaller {
	return DelegatedCaller{User: c.User, OrgUUID: c.OrgUUID, WorkspaceUUID: c.WorkspaceUUID}
}

func uiGrantAEAD(secret []byte) (cipher.AEAD, error) {
	if len(secret) < uiGrantMinKey {
		return nil, fmt.Errorf("provider UI grant key is too short")
	}
	key, err := hkdf.Key(sha256.New, secret, nil, uiGrantKeyInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealUIGrant mints a grant for claims. random supplies the nonce and ID.
func sealUIGrant(secret []byte, random io.Reader, claims uiGrantClaims) (string, error) {
	aead, err := uiGrantAEAD(secret)
	if err != nil {
		return "", err
	}
	id := make([]byte, uiGrantIDBytes)
	if _, err := io.ReadFull(random, id); err != nil {
		return "", err
	}
	claims.Version = uiGrantVersion
	claims.ID = base64.RawURLEncoding.EncodeToString(id)
	plaintext, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, plaintext, []byte(uiGrantAAD))
	return uiGrantPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// openUIGrant authenticates and decodes grant and checks its validity window
// against now. It does NOT check the provider binding; the redeeming request
// does that against the path it arrived on.
func openUIGrant(secret []byte, grant string, now time.Time) (uiGrantClaims, error) {
	if !strings.HasPrefix(grant, uiGrantPrefix) {
		return uiGrantClaims{}, fmt.Errorf("%w: not a provider UI grant", errUIGrantInvalid)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(grant, uiGrantPrefix))
	if err != nil {
		return uiGrantClaims{}, fmt.Errorf("%w: malformed encoding", errUIGrantInvalid)
	}
	aead, err := uiGrantAEAD(secret)
	if err != nil {
		return uiGrantClaims{}, err
	}
	if len(raw) < aead.NonceSize()+aead.Overhead() {
		return uiGrantClaims{}, fmt.Errorf("%w: truncated", errUIGrantInvalid)
	}
	nonce, sealed := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, sealed, []byte(uiGrantAAD))
	if err != nil {
		return uiGrantClaims{}, fmt.Errorf("%w: not issued by this hub", errUIGrantInvalid)
	}
	var claims uiGrantClaims
	if err := json.Unmarshal(plaintext, &claims); err != nil {
		return uiGrantClaims{}, fmt.Errorf("%w: unreadable claims", errUIGrantInvalid)
	}
	if claims.Version != uiGrantVersion || claims.OrgUUID == "" || claims.WorkspaceUUID == "" ||
		claims.User == "" || claims.Provider == "" || claims.ExpiresAt == 0 {
		return uiGrantClaims{}, fmt.Errorf("%w: incomplete claims", errUIGrantInvalid)
	}
	if !now.Before(time.Unix(claims.ExpiresAt, 0)) {
		return uiGrantClaims{}, fmt.Errorf("%w: expired", errUIGrantInvalid)
	}
	return claims, nil
}

// SetUIGrantKeys installs the secret the UI proxy opens grants with. The same
// source mints them (UIGrantHandler reads it through the proxy), so every
// replica agrees. Without one the proxy refuses every grant-bearing request
// rather than serving an org bundle unscoped.
func (p *ProviderProxy) SetUIGrantKeys(keys serviceaccounts.ProofKeySource) {
	p.uiGrantKeys = keys
}

// orgUIOverEdge reports whether prov's UI can be carried by its edge route.
// The route fronts the Service the provider published as its backend, so the
// UI has to be served by that same authority: a spec.ui.url on another host
// has nothing to land on. Path prefixes may differ (they are appended per
// request); only scheme and host must match.
func orgUIOverEdge(prov Provider) bool {
	return prov.OrgUUID != "" && prov.UIURL != nil && prov.BackendURL != nil &&
		prov.UIURL.Scheme == prov.BackendURL.Scheme && prov.UIURL.Host == prov.BackendURL.Host
}

// orgUIAssetPath is the provider-relative path of one UI asset: the
// spec.ui.url path prefix, then the path after /ui/providers/{name} — the same
// join the direct UI proxy makes for a platform provider.
func orgUIAssetPath(prov Provider, rest string) string {
	return singleJoiningSlash(prov.UIURL.Path, rest)
}

// serveOrgUIAsset redeems grant for one asset of the org-owned provider name
// and forwards the request over the provider's edge hop.
func (p *ProviderProxy) serveOrgUIAsset(w http.ResponseWriter, r *http.Request, name, rest, grant string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if p.uiGrantKeys == nil {
		p.log.Info("refusing provider UI grant: no grant key source wired", "provider", name)
		http.Error(w, "provider UI grants are not available", http.StatusServiceUnavailable)
		return
	}
	secret, err := p.uiGrantKeys.DelegatedProofKey(r.Context())
	if err != nil {
		p.log.Error(err, "loading provider UI grant key", "provider", name)
		http.Error(w, "provider UI grants are not available", http.StatusServiceUnavailable)
		return
	}
	claims, err := openUIGrant(secret, grant, time.Now())
	if err != nil {
		p.log.V(1).Info("refusing provider UI grant", "provider", name, "reason", err.Error())
		http.Error(w, "invalid or expired provider UI grant", http.StatusUnauthorized)
		return
	}
	if claims.Provider != name {
		// A grant for one provider must not fetch another's bundle, even
		// within the same org.
		p.log.V(1).Info("refusing provider UI grant", "provider", name, "reason", "grant is bound to another provider", "grantProvider", claims.Provider)
		http.Error(w, "invalid or expired provider UI grant", http.StatusUnauthorized)
		return
	}
	// The grant names the org, so resolution is in THAT org — never the
	// platform record, which is what a grant-less request reaches. GetForOrg
	// falls back to the platform copy when the org has none; refuse that
	// rather than serve a platform bundle under an org grant.
	prov, found := p.reg.GetForOrg(claims.OrgUUID, name)
	if !found || prov.OrgUUID != claims.OrgUUID || !orgUIOverEdge(prov) {
		http.Error(w, "provider has no org-owned UI bundle: "+name, http.StatusNotFound)
		return
	}
	if !prov.Ready() {
		http.Error(w, "provider not ready: "+name, http.StatusServiceUnavailable)
		return
	}
	hop, err := resolveEdgeHop(p.reg, prov)
	switch {
	case errors.Is(err, errEdgeRouteUnusable):
		p.log.Info("org-owned provider has no usable edge route yet", "provider", prov.Name, "org", prov.OrgUUID)
		http.Error(w, "provider backend is not routable yet: "+name, http.StatusServiceUnavailable)
		return
	case err != nil:
		p.log.Info("edge transport unavailable: the platform edges provider has no backend", "provider", prov.Name, "org", prov.OrgUUID)
		http.Error(w, "edge transport unavailable for provider: "+name, http.StatusServiceUnavailable)
		return
	}
	// Same decision point as the backend proxy and OrgProviderRoute: the far
	// end receives a delegated token for the tuple the grant names, minted on
	// the hub's authority, and nothing else.
	token, refusal := issueDelegatedToken(r.Context(), p.delegatedIssuer, prov, claims.caller())
	if refusal != nil {
		if refusal.err != nil {
			p.log.Error(refusal.err, "issuing delegated user token for provider UI asset",
				"provider", prov.Name, "org", prov.OrgUUID, "workspace", claims.WorkspaceUUID, "user", claims.User)
		} else {
			p.log.Info("refusing provider UI asset: "+refusal.reason, "provider", prov.Name, "org", prov.OrgUUID)
		}
		http.Error(w, refusal.message, refusal.status)
		return
	}

	dst := hop.url(orgUIAssetPath(prov, rest))
	basePath := p.pathPrefix + "/" + prov.Name
	query := r.URL.Query()
	query.Del(UIGrantQueryParam)
	rawQuery := query.Encode()

	rp := &httputil.ReverseProxy{
		FlushInterval: -1,
		Director: func(req *http.Request) {
			req.URL.Scheme = dst.Scheme
			req.URL.Host = dst.Host
			req.URL.Path = dst.Path
			req.URL.RawPath = ""
			// The grant stops here: the tenant's cluster has no use for it
			// and must not be handed something it could replay at the hub.
			req.URL.RawQuery = rawQuery
			req.Host = dst.Host
			// Identity under the ORG provider's name, as the backend proxy
			// does (E-6): the headers describe who is fetching the bundle.
			req.Header.Del("X-Faros-User")
			req.Header.Del("X-Faros-Tenant")
			req.Header.Del("X-Faros-Cluster")
			req.Header.Set("X-Faros-User", claims.User)
			p.setHeaders(req, prov.Name, basePath)
			setDelegatedAuthorization(req.Header, token)
		},
		ModifyResponse: func(resp *http.Response) error {
			// The URL carries a grant, so a shared cache must never keep this
			// response: after expiry the cached copy would answer a URL the
			// hub no longer would.
			resp.Header.Set("Cache-Control", "private, no-store")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.log.Error(err, "edge upstream error serving provider UI asset",
				"provider", prov.Name, "org", prov.OrgUUID, "edge", hop.route.EdgeName, "service", hop.route.ServiceName)
			http.Error(w, "provider upstream error", http.StatusBadGateway)
		},
	}
	p.log.V(4).Info("forwarding provider UI asset over edge",
		"provider", prov.Name, "org", prov.OrgUUID, "edge", hop.route.EdgeName, "path", dst.Path)
	rp.ServeHTTP(w, r)
}

// UIGrantHandler serves POST /api/providers/{name}/ui-grant. It is built
// early (so it can be registered ahead of the heartbeat prefix route) and
// configured once the auth stack exists, like the proxies.
type UIGrantHandler struct {
	reg   *Registry
	proxy *ProviderProxy // the UI proxy: shares its grant keys and delegated issuer
	log   logr.Logger
	now   func() time.Time
	rand  io.Reader

	mu       sync.RWMutex
	resolver TenantResolver

	// uiIntegrity caches the SRI pin per org provider and version, on the
	// same terms as the catalog reconciler's cache for platform bundles.
	uiIntegrityMu sync.Mutex
	uiIntegrity   map[providerKey]uiIntegrityRecord
	// uiClient overrides the transport-bound client built per grant; tests
	// only.
	uiFetchTimeout time.Duration
}

// NewUIGrantHandler builds the grant endpoint over reg, minting with the keys
// and issuer installed on uiProxy (SetUIGrantKeys, SetDelegatedTokenIssuer).
func NewUIGrantHandler(reg *Registry, uiProxy *ProviderProxy, log logr.Logger) *UIGrantHandler {
	return &UIGrantHandler{
		reg:            reg,
		proxy:          uiProxy,
		log:            log.WithName("ui-grant"),
		now:            time.Now,
		rand:           rand.Reader,
		uiFetchTimeout: uiIntegrityFetchTimeout,
	}
}

// SetTenantResolver installs the resolver that names the caller. It is the
// same resolver the backend proxy uses, so the membership checks behind
// X-Faros-Org / X-Faros-Workspace are the same ones. Until one is installed
// every request is refused.
func (h *UIGrantHandler) SetTenantResolver(r TenantResolver) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resolver = r
}

// uiGrantResponse is the wire shape of a minted grant.
type uiGrantResponse struct {
	// URL is the bundle URL to load, grant included. Same-origin; the portal
	// injects it as a classic <script src> exactly like a platform bundle.
	URL string `json:"url"`
	// Integrity is the SRI pin for that bundle, hashed through the same
	// route. Empty when the hash fetch failed; the portal then loads unpinned
	// and logs it, as it does for a platform bundle the hub could not hash.
	Integrity string `json:"integrity,omitempty"`
	// ExpiresAt is when the grant stops being redeemable.
	ExpiresAt time.Time `json:"expiresAt"`
}

func (h *UIGrantHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name, ok := parseUIGrantPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.mu.RLock()
	resolver := h.resolver
	h.mu.RUnlock()
	if resolver == nil || h.proxy.uiGrantKeys == nil {
		h.log.Info("refusing provider UI grant request: grants are not configured", "provider", name)
		http.Error(w, "provider UI grants are not available", http.StatusServiceUnavailable)
		return
	}
	user, tenantPath, err := resolver.Resolve(r)
	if err != nil || user == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	orgUUID, wsUUID := splitTenantPath(tenantPath)
	if orgUUID == "" {
		http.Error(w, "caller has no tenant workspace", http.StatusForbidden)
		return
	}
	if wsUUID == "" {
		// The delegated token the asset fetch is made with lives in a team
		// workspace; an org-scope selection has nowhere to mint it. Say so
		// here rather than at redemption, where the browser only sees a
		// failed script.
		http.Error(w, "a workspace selection (X-Faros-Workspace) is required to load provider: "+name, http.StatusForbidden)
		return
	}
	prov, found := h.reg.GetForOrg(orgUUID, name)
	if !found || prov.OrgUUID != orgUUID || prov.UIURL == nil {
		// Platform bundles need no grant, and a provider without a UI has
		// nothing to grant; both are "not found" for this endpoint.
		http.Error(w, "provider has no org-owned UI bundle: "+name, http.StatusNotFound)
		return
	}
	if !orgUIOverEdge(prov) {
		h.log.Info("org-owned provider UI is not served by its backend authority; the edge route cannot carry it",
			"provider", name, "org", orgUUID, "ui", urlString(prov.UIURL), "backend", urlString(prov.BackendURL))
		http.Error(w, "provider UI is not reachable over its edge route: "+name, http.StatusNotFound)
		return
	}
	if !prov.Ready() {
		http.Error(w, "provider not ready: "+name, http.StatusServiceUnavailable)
		return
	}

	secret, err := h.proxy.uiGrantKeys.DelegatedProofKey(r.Context())
	if err != nil {
		h.log.Error(err, "loading provider UI grant key", "provider", name)
		http.Error(w, "provider UI grants are not available", http.StatusServiceUnavailable)
		return
	}
	now := h.now()
	claims := uiGrantClaims{
		OrgUUID:       orgUUID,
		WorkspaceUUID: wsUUID,
		User:          user,
		Provider:      name,
		IssuedAt:      now.Unix(),
		ExpiresAt:     now.Add(uiGrantTTL).Unix(),
	}
	grant, err := sealUIGrant(secret, h.rand, claims)
	if err != nil {
		h.log.Error(err, "sealing provider UI grant", "provider", name)
		http.Error(w, "provider UI grants are not available", http.StatusServiceUnavailable)
		return
	}

	integrity := h.pinOrgUIIntegrity(r.Context(), prov, claims.caller())

	bundle := url.URL{
		Path:     apiurl.PathPrefixProvidersUI + "/" + prov.Name + "/main.js",
		RawQuery: url.Values{"v": {prov.Version}, UIGrantQueryParam: {grant}}.Encode(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(uiGrantResponse{
		URL:       bundle.String(),
		Integrity: integrity,
		ExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC(),
	})
}

// parseUIGrantPath extracts the provider name from
// /api/providers/{name}/ui-grant. Returns ("", false) on mismatch.
func parseUIGrantPath(p string) (string, bool) {
	const prefix = PathListProviders + "/"
	const suffix = "/ui-grant"
	rest := strings.TrimPrefix(p, prefix)
	if rest == p || !strings.HasSuffix(rest, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(rest, suffix)
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

// pinOrgUIIntegrity returns the SRI pin for prov's bundle, fetching it through
// the caller's own delegated route when no fresh pin is cached for the running
// version. A fetch failure keeps a cached pin for the same version and
// otherwise yields "" (unpinned), logged — the same policy as
// CatalogReconciler.pinUIIntegrity for platform bundles.
func (h *UIGrantHandler) pinOrgUIIntegrity(ctx context.Context, prov Provider, caller DelegatedCaller) string {
	version := uiIntegrityVersion(prov.Version, prov.ReportedVersion)
	key := providerKey{Org: prov.OrgUUID, Name: prov.Name}
	now := h.now()

	h.uiIntegrityMu.Lock()
	cached, ok := h.uiIntegrity[key]
	h.uiIntegrityMu.Unlock()
	if ok && cached.version == version && now.Sub(cached.hashedAt) < UIIntegrityResync {
		return cached.integrity
	}

	integrity, err := h.hashOrgProviderMainJS(ctx, prov, caller)
	if err != nil {
		h.log.Info("WARNING could not pin org-owned provider UI bundle; portal loads it unpinned",
			"provider", prov.Name, "org", prov.OrgUUID, "version", version, "err", err.Error())
		if ok && cached.version == version {
			return cached.integrity
		}
		return ""
	}
	h.uiIntegrityMu.Lock()
	if h.uiIntegrity == nil {
		h.uiIntegrity = map[providerKey]uiIntegrityRecord{}
	}
	h.uiIntegrity[key] = uiIntegrityRecord{version: version, integrity: integrity, hashedAt: now}
	h.uiIntegrityMu.Unlock()
	if !ok || cached.integrity != integrity {
		h.log.Info("Pinned org-owned provider UI bundle", "provider", prov.Name, "org", prov.OrgUUID, "version", version, "integrity", integrity)
	}
	return integrity
}

// hashOrgProviderMainJS GETs the bundle over the provider's edge hop as
// caller — OrgProviderRoute, the one hub-originated path to an org-owned
// provider — and returns its SRI metadata. It reads exactly the bytes the UI
// proxy will later forward for the same path.
func (h *UIGrantHandler) hashOrgProviderMainJS(ctx context.Context, prov Provider, caller DelegatedCaller) (string, error) {
	route, err := h.proxy.OrgProviderRoute(ctx, prov, caller)
	if err != nil {
		return "", err
	}
	base, err := url.Parse(route.BaseURL)
	if err != nil {
		return "", fmt.Errorf("parse org provider route: %w", err)
	}
	ui := *base
	ui.Path = singleJoiningSlash(base.Path, strings.TrimSuffix(prov.UIURL.Path, "/"))
	client := &http.Client{
		Transport: route.Transport,
		Timeout:   h.uiFetchTimeout,
		// As for platform bundles: a provider-controlled redirect cannot move
		// the fetch elsewhere. The transport would refuse the destination
		// anyway; this keeps the failure legible.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	data, err := fetchProviderMainJS(ctx, client, &ui)
	if err != nil {
		return "", err
	}
	return sriSHA384(data), nil
}
