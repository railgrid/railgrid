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

// Package auth provides server-side OIDC token verification.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	oidc "github.com/coreos/go-oidc"
	"github.com/gorilla/mux"
	"golang.org/x/oauth2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros/pkg/apiurl"
	"github.com/faroshq/faros/pkg/browsersession"
	farosclient "github.com/faroshq/faros/pkg/client"
	"github.com/faroshq/faros/pkg/hub/kcp"
)

// defaultRateLimit is the default number of requests allowed per minute per
// IP. Sized to tolerate several browsers reloading behind one NAT'd IP (each
// portal reload hits /auth/session) while still smothering brute force on
// the endpoints that keep the limiter.
const defaultRateLimit = 60

// defaultBurstDuration is the default time window for rate limiting.
const defaultBurstDuration = time.Minute

// Handler provides OAuth2/OIDC authentication endpoints.
type Handler struct {
	oidcProvider   *oidc.Provider
	oauth2Config   *oauth2.Config
	oidcConfig     *OIDCConfig
	farosClient    *farosclient.Client
	bootstrapper   *kcp.Bootstrapper
	hubExternalURL string
	devMode        bool
	logger         klog.Logger
	// rateLimiter protects auth endpoints against brute force attacks
	rateLimiter *rateLimiter
	// browserSessions is the hub-wide opaque portal session store.  It is
	// shared by legacy OIDC, static-token bootstrap, and published-app SSO.
	browserSessions *browsersession.Store
	// browserIdentity resolves an already-authenticated portal bearer without
	// exposing that bearer to any app or session cookie.
	browserIdentity func(*http.Request) (browsersession.Identity, error)
}

// NewHandler creates a new OIDC auth handler.
func NewHandler(ctx context.Context, config *OIDCConfig, farosClient *farosclient.Client, bootstrapper *kcp.Bootstrapper, hubExternalURL string, devMode bool) (*Handler, error) {
	if config.IssuerURL == "" {
		return nil, fmt.Errorf("OIDC issuer URL is required")
	}

	// In dev mode, skip TLS verification for OIDC discovery (self-signed certs).
	providerCtx := ctx
	if devMode {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode only
		}
		httpClient := &http.Client{Transport: tr}
		providerCtx = oidc.ClientContext(ctx, httpClient)
	}

	provider, err := oidc.NewProvider(providerCtx, config.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create OIDC provider: %w", err)
	}
	endpoint, err := oauth2EndpointWithBrowserAuthURL(provider.Endpoint(), config.BrowserAuthURL)
	if err != nil {
		return nil, err
	}

	// No ClientSecret: faros uses PKCE (public client). Dex must be configured
	// with public: true for this client ID.
	oauth2Config := &oauth2.Config{
		ClientID:    config.ClientID,
		RedirectURL: config.RedirectURL,
		Endpoint:    endpoint,
		Scopes:      config.Scopes,
	}

	handler := &Handler{
		oidcProvider:   provider,
		oauth2Config:   oauth2Config,
		oidcConfig:     config,
		farosClient:    farosClient,
		bootstrapper:   bootstrapper,
		hubExternalURL: hubExternalURL,
		devMode:        devMode,
		logger:         klog.Background().WithName("auth-handler"),
		// Initialize rate limiter with sane defaults for auth endpoints
		rateLimiter: newRateLimiter(defaultRateLimit, defaultBurstDuration, klog.Background().WithName("auth-rate-limit")),
	}

	return handler, nil
}

// SetBrowserSessionStore wires the hub-owned opaque browser session store.
// Keeping this explicit lets static-only hubs use the same store even when no
// OIDC Handler is constructed.
func (h *Handler) SetBrowserSessionStore(store *browsersession.Store) {
	if h != nil {
		h.browserSessions = store
	}
}

// SetBrowserIdentityResolver wires the bearer validation seam used by the
// same-origin bootstrap endpoint.  The resolver must return only a stable
// identity; it must never return a raw token.
func (h *Handler) SetBrowserIdentityResolver(resolve func(*http.Request) (browsersession.Identity, error)) {
	if h != nil {
		h.browserIdentity = resolve
	}
}

// NewBrowserSessionHandler creates the session-only portion of auth for hubs
// running in static-token mode without an OIDC provider.
func NewBrowserSessionHandler(store *browsersession.Store, resolve func(*http.Request) (browsersession.Identity, error)) *Handler {
	return &Handler{
		browserSessions: store,
		browserIdentity: resolve,
		rateLimiter:     newRateLimiter(defaultRateLimit, defaultBurstDuration, klog.Background().WithName("auth-rate-limit")),
		logger:          klog.Background().WithName("auth-handler"),
	}
}

// oauth2EndpointWithBrowserAuthURL keeps discovery-derived token exchange and
// issuer verification internal while optionally replacing only the endpoint
// emitted in browser redirects. This supports split-horizon IdPs without
// changing the issuer claim that tokens must contain.
func oauth2EndpointWithBrowserAuthURL(endpoint oauth2.Endpoint, rawURL string) (oauth2.Endpoint, error) {
	if rawURL == "" {
		return endpoint, nil
	}
	browserURL, err := url.Parse(rawURL)
	if err != nil || browserURL.Host == "" || browserURL.Scheme != "https" || browserURL.User != nil || browserURL.Fragment != "" {
		return oauth2.Endpoint{}, fmt.Errorf("invalid OIDC browser authorization URL")
	}
	endpoint.AuthURL = browserURL.String()
	return endpoint, nil
}

// HandleAuthorize redirects to the OIDC provider for authentication.
//
// CLI mode:    GET /auth/authorize?p=<port>&s=<sessionID>&v=<codeVerifier>
// Portal mode: GET /auth/authorize?redirect_uri=<url>&s=<sessionID>&v=<codeVerifier>
//
// The CLI generates a PKCE code_verifier and passes it as "v". The hub stores
// it in the OAuth2 state and sends the corresponding S256 code_challenge to
// the OIDC provider. The verifier is recovered from state in HandleCallback
// and used to exchange the auth code — no client secret needed.
//
// When redirect_uri is provided (portal flow), it is used as the callback URL
// instead of the CLI localhost callback. The redirect_uri must share the same
// origin as the hub external URL.
func (h *Handler) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("s")
	codeVerifier := r.URL.Query().Get("v")
	redirectURI := r.URL.Query().Get("redirect_uri")
	port := r.URL.Query().Get("p")

	if sessionID == "" {
		http.Error(w, "missing s (session) parameter", http.StatusBadRequest)
		return
	}
	if codeVerifier == "" {
		http.Error(w, "missing v (PKCE code_verifier) parameter", http.StatusBadRequest)
		return
	}

	var callbackURL string
	if redirectURI != "" {
		// Portal flow: validate redirect_uri against the hub's external URL.
		if err := h.validateRedirectURI(redirectURI); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		callbackURL = redirectURI
	} else if port != "" {
		// CLI flow: build localhost callback URL from port.
		portNum, err := strconv.Atoi(port)
		if err != nil || portNum < 1 || portNum > 65535 {
			http.Error(w, "invalid port parameter: must be a number between 1 and 65535", http.StatusBadRequest)
			return
		}
		callbackURL = fmt.Sprintf("http://127.0.0.1:%d/callback", portNum)
	} else {
		http.Error(w, "missing p (port) or redirect_uri parameter", http.StatusBadRequest)
		return
	}

	authCode := tenancyv1alpha1.AuthCode{
		RedirectURL:  callbackURL,
		SessionID:    sessionID,
		CodeVerifier: codeVerifier,
	}

	stateJSON, err := json.Marshal(authCode)
	if err != nil {
		http.Error(w, "failed to encode state", http.StatusInternalServerError)
		return
	}
	state := base64.URLEncoding.EncodeToString(stateJSON)

	// Include S256 code_challenge derived from the verifier in the auth URL.
	options := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(codeVerifier)}
	// Account switching is an explicit user action.  Force the IdP to show its
	// login/account chooser instead of silently reusing an existing IdP cookie.
	if force := r.URL.Query().Get("force"); force == "1" || strings.EqualFold(force, "true") || r.URL.Query().Get("switch") == "1" {
		options = append(options, oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
	}
	authURL := h.oauth2Config.AuthCodeURL(state, options...)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// validateRedirectURI checks that the redirect URI shares the same origin as
// the hub external URL or is a localhost address (for development).
func (h *Handler) validateRedirectURI(redirectURI string) error {
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return fmt.Errorf("invalid redirect_uri: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("redirect_uri must be an absolute URL")
	}

	// Allow localhost for development.
	host := strings.Split(parsed.Host, ":")[0]
	if host == "localhost" || host == "127.0.0.1" {
		return nil
	}

	// Validate against hub external URL origin.
	hubParsed, err := url.Parse(h.hubExternalURL)
	if err != nil {
		return fmt.Errorf("invalid hub external URL configuration")
	}

	hubHost := strings.Split(hubParsed.Host, ":")[0]
	if host != hubHost {
		return fmt.Errorf("redirect_uri origin must match hub external URL")
	}

	return nil
}

// HandleCallback handles the OIDC callback after authentication.
// GET /auth/callback?code=<code>&state=<state>
func (h *Handler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	code := r.URL.Query().Get("code")
	stateParam := r.URL.Query().Get("state")
	if code == "" || stateParam == "" {
		http.Error(w, "missing code or state parameter", http.StatusBadRequest)
		return
	}

	// Decode the state to get the CLI callback URL.
	stateJSON, err := base64.URLEncoding.DecodeString(stateParam)
	if err != nil {
		http.Error(w, "invalid state parameter", http.StatusBadRequest)
		return
	}
	var authCode tenancyv1alpha1.AuthCode
	if err := json.Unmarshal(stateJSON, &authCode); err != nil {
		http.Error(w, "invalid state payload", http.StatusBadRequest)
		return
	}

	// Exchange code for tokens using the PKCE code_verifier (no client secret).
	exchangeCtx := ctx
	if h.devMode {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode only
		}
		httpClient := &http.Client{Transport: tr}
		exchangeCtx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	}

	token, err := h.oauth2Config.Exchange(exchangeCtx, code, oauth2.VerifierOption(authCode.CodeVerifier))
	if err != nil {
		h.logger.Error(err, "failed to exchange code for token")
		http.Error(w, "token exchange failed", http.StatusInternalServerError)
		return
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "missing id_token", http.StatusInternalServerError)
		return
	}

	verifier := h.oidcProvider.Verifier(&oidc.Config{ClientID: h.oidcConfig.ClientID})
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		h.logger.Error(err, "failed to verify ID token")
		http.Error(w, "token verification failed", http.StatusInternalServerError)
		return
	}

	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		Sub   string `json:"sub"`
	}
	if err := idToken.Claims(&claims); err != nil {
		h.logger.Error(err, "failed to parse ID token claims")
		http.Error(w, "failed to parse claims", http.StatusInternalServerError)
		return
	}

	// Create or update User CRD. The legacy CreateTenantWorkspace call
	// (which materialized root:faros:tenants:{userID} and patched
	// User.spec.DefaultCluster) was removed when the new multi-org
	// tenancy model took over: the organization bootstrap controller
	// now creates the personal Org + its default child Workspace and
	// patches User.spec.DefaultCluster itself once the Workspace is
	// Ready. The auth handler just needs to write the User CR; the
	// controller does the rest asynchronously.
	userID, err := h.seedUser(ctx, claims.Email, claims.Name, claims.Sub, h.oidcConfig.IssuerURL)
	if err != nil {
		h.logger.Error(err, "failed to seed user")
		http.Error(w, "failed to create user", http.StatusInternalServerError)
		return
	}

	// Read DefaultCluster after seeding — the org bootstrap controller may
	// have set it on a previous login; on first login it will still be
	// empty, in which case the issued kubeconfig points at the bare hub
	// and the user re-logs after the controller catches up.
	clusterName := h.lookupDefaultCluster(ctx, userID)

	// Generate kubeconfig using exec credential plugin for automatic token refresh.
	kubeconfigBytes, err := h.generateKubeconfig(userID, clusterName, claims.Email)
	if err != nil {
		h.logger.Error(err, "failed to generate kubeconfig")
		http.Error(w, "failed to generate kubeconfig", http.StatusInternalServerError)
		return
	}

	// Build response with OIDC credentials so the CLI can cache and refresh tokens.
	// ClientSecret is intentionally absent — PKCE public client flow needs none.
	resp := tenancyv1alpha1.LoginResponse{
		Kubeconfig:   kubeconfigBytes,
		ExpiresAt:    token.Expiry.Unix(),
		Email:        claims.Email,
		UserID:       userID,
		IDToken:      rawIDToken,
		RefreshToken: token.RefreshToken,
		IssuerURL:    h.oidcConfig.IssuerURL,
		ClientID:     h.oidcConfig.ClientID,
	}
	respJSON, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
	encoded := base64.URLEncoding.EncodeToString(respJSON)
	redirectURL := authCode.RedirectURL + "?response=" + encoded
	if h.browserSessions != nil {
		if _, sessionErr := h.browserSessions.IssueHTTP(ctx, w, browsersession.Identity{
			UserID: userID, Email: claims.Email, Name: claims.Name,
			// Matches what seedUser reconciles onto the User CR; workspace
			// RBAC and app-access authorization key off this string.
			RBACIdentity: fmt.Sprintf("faros:%s", claims.Email),
			Issuer:       idToken.Issuer, Subject: claims.Sub, AuthType: "oidc",
		}); sessionErr != nil {
			h.logger.Error(sessionErr, "failed to issue shared browser session")
			http.Error(w, "failed to create browser session", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// HandleRefresh handles token refresh requests.
func (h *Handler) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleSessionBootstrap establishes or refreshes the hub-wide browser
// session from an already-authenticated portal bearer.  The bearer is used
// only by the resolver and is never returned, persisted, or forwarded to an
// application.  A valid shared cookie can also bootstrap a refreshed UI
// session without requiring the portal to expose its bearer again.
func (h *Handler) HandleSessionBootstrap(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.browserSessions == nil {
		http.Error(w, "browser session unavailable", http.StatusServiceUnavailable)
		return
	}
	var identity browsersession.Identity
	var err error
	if h.browserIdentity != nil {
		identity, err = h.browserIdentity(r)
	}
	if err != nil || strings.TrimSpace(identity.UserID) == "" {
		// A live shared cookie is sufficient for an idempotent same-origin
		// bootstrap.  This path is useful after a page reload when localStorage
		// still contains only non-sensitive user metadata.
		if session, cookieErr := h.browserSessions.ResolveRequest(r); cookieErr == nil {
			writeBrowserSessionJSON(w, session)
			return
		}
		browsersession.ClearCookie(w)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	session, issueErr := h.browserSessions.IssueHTTP(r.Context(), w, identity)
	if issueErr != nil {
		http.Error(w, "failed to create browser session", http.StatusInternalServerError)
		return
	}
	writeBrowserSessionJSON(w, session)
}

const browserSessionHandoffTTL = time.Minute

// HandleSessionHandoff moves an already-authenticated API caller into a fresh
// browser without putting its bearer credential in Chromium. POST mints a
// short-lived opaque handle; GET atomically consumes it and issues the normal
// host-only browser-session cookie. The handle is deliberately one-use and
// expires quickly if the browser never arrives.
func (h *Handler) HandleSessionHandoff(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.browserSessions == nil {
		http.Error(w, "browser session unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method == http.MethodPost {
		if h.browserIdentity == nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		identity, err := h.browserIdentity(r)
		if err != nil || strings.TrimSpace(identity.UserID) == "" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		code, _, err := h.browserSessions.IssueTransient(r.Context(), identity, browserSessionHandoffTTL)
		if err != nil {
			http.Error(w, "browser session handoff unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"path": "/auth/session/handoff?code=" + url.QueryEscape(code)})
		return
	}

	code := strings.TrimSpace(r.URL.Query().Get("code"))
	session, err := h.browserSessions.Resolve(r.Context(), code)
	if err != nil {
		http.Error(w, "browser session handoff expired", http.StatusGone)
		return
	}
	// Consume before issuing the durable cookie. Even if the second write
	// fails, replaying a handoff URL must never mint another browser session.
	_ = h.browserSessions.Revoke(r.Context(), code)
	if _, err := h.browserSessions.IssueHTTP(r.Context(), w, session.Identity); err != nil {
		http.Error(w, "browser session handoff unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprint(w, "browser session ready")
}

// HandleLogout revokes the shared browser session and expires its cookie.
// It accepts GET for a browser navigation fallback and POST for the portal's
// normal same-origin action.
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if h != nil && h.browserSessions != nil {
		h.browserSessions.RevokeRequest(w, r)
	} else {
		browsersession.ClearCookie(w)
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		// The portal SPA is mounted under /ui/ — a root-level /login 404s.
		http.Redirect(w, r, "/ui/login", http.StatusFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeBrowserSessionJSON(w http.ResponseWriter, session browsersession.Session) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"authenticated": true,
		"userId":        session.Identity.UserID,
		"email":         session.Identity.Email,
		"name":          session.Identity.Name,
		"expiresAt":     session.ExpiresAt.Unix(),
	})
}

// Verifier returns the OIDC token verifier for use by other components (e.g., API proxy).
func (h *Handler) Verifier() *oidc.IDTokenVerifier {
	return h.oidcProvider.Verifier(&oidc.Config{ClientID: h.oidcConfig.ClientID})
}

// RegisterRoutes registers auth routes on the given gorilla/mux router.
// Auth endpoints are protected by per-IP rate limiting to prevent brute force attacks.
func (h *Handler) RegisterRoutes(router *mux.Router) {
	// authorize/callback are deliberately NOT rate-limited (matching the
	// pre-session-store wiring): they carry no guessable secret — authorize
	// only mints a redirect to the IdP and callback consumes a one-use
	// PKCE-bound state — while a per-IP budget breaks legitimate bursts
	// (CI suites, NAT'd offices, several first-time users behind one IP).
	// Brute-forceable endpoints (token login) keep their limiter.
	router.HandleFunc(apiurl.PathAuthAuthorize, h.HandleAuthorize).Methods("GET")
	router.HandleFunc(apiurl.PathAuthCallback, h.HandleCallback).Methods("GET")
	router.HandleFunc(apiurl.PathAuthRefresh, h.rateLimiter.middleware(h.HandleRefresh)).Methods("POST")
	h.RegisterBrowserSessionRoutes(router)
}

// SetTrustedProxies tells the auth rate limiter which connection peers are
// reverse proxies whose X-Forwarded-For may be believed (see proxy.ClientIP).
// Without it every request is keyed on the connection peer, so a hub behind
// a proxy throttles all of its clients as one address.
func (h *Handler) SetTrustedProxies(prefixes []netip.Prefix) {
	if h == nil {
		return
	}
	if h.rateLimiter == nil {
		h.rateLimiter = newRateLimiter(defaultRateLimit, defaultBurstDuration, klog.Background().WithName("auth-rate-limit"))
	}
	h.rateLimiter.trustedProxies = prefixes
}

// RateLimitMiddleware exposes the auth rate limiter so auth-adjacent routes
// mounted outside this package (published-app authorize/exchange) share the
// same per-IP budget as the interactive auth endpoints.
func (h *Handler) RateLimitMiddleware() func(http.HandlerFunc) http.HandlerFunc {
	if h == nil || h.rateLimiter == nil {
		return nil
	}
	return h.rateLimiter.middleware
}

// RegisterBrowserSessionRoutes mounts only the shared session endpoints. It is
// used by static-token-only hubs that do not construct a full OIDC Handler.
func (h *Handler) RegisterBrowserSessionRoutes(router *mux.Router) {
	if h == nil || router == nil {
		return
	}
	if h.rateLimiter == nil {
		h.rateLimiter = newRateLimiter(defaultRateLimit, defaultBurstDuration, klog.Background().WithName("auth-rate-limit"))
	}
	router.HandleFunc("/auth/session/bootstrap", h.rateLimiter.middleware(h.HandleSessionBootstrap)).Methods("GET", "POST")
	router.HandleFunc("/auth/session", h.rateLimiter.middleware(h.HandleSessionBootstrap)).Methods("GET", "POST")
	router.HandleFunc("/auth/session/handoff", h.rateLimiter.middleware(h.HandleSessionHandoff)).Methods("GET", "POST")
	router.HandleFunc("/auth/logout", h.rateLimiter.middleware(h.HandleLogout)).Methods("GET", "POST")
}

// seedUser creates or updates a User CRD based on OIDC claims.
// adoptInvitedUser finds a pending invited User (matching email,
// case-insensitive, and crucially NO issuer/sub label yet) and binds it to
// the just-verified OIDC subject: it stamps the sub label, records the OIDC
// provider, and drops the invited marker. Returns nil when no adoptable user
// exists. Bound accounts are never matched by email — the sub label is the
// only credential-grade identity link.
func (h *Handler) adoptInvitedUser(ctx context.Context, email, subHash, sub string) (*tenancyv1alpha1.User, error) {
	list, err := h.farosClient.Users().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing users for invite adoption: %w", err)
	}
	want := strings.ToLower(strings.TrimSpace(email))
	if want == "" {
		return nil, nil
	}
	for i := range list.Items {
		user := &list.Items[i]
		if strings.ToLower(user.Spec.Email) != want {
			continue
		}
		if user.Labels["tenants.faros.sh/sub"] != "" {
			// Already bound to an IdP subject; email similarity grants
			// nothing.
			continue
		}
		if user.Labels == nil {
			user.Labels = map[string]string{}
		}
		user.Labels["tenants.faros.sh/sub"] = subHash
		delete(user.Labels, "tenants.faros.sh/invited")
		user.Spec.OIDCProviders = append(user.Spec.OIDCProviders, tenancyv1alpha1.OIDCProvider{
			Name:       "dex",
			ProviderID: sub,
			Email:      email,
		})
		updated, err := h.farosClient.Users().Update(ctx, user, metav1.UpdateOptions{})
		if err != nil {
			return nil, fmt.Errorf("adopting invited user %s: %w", user.Name, err)
		}
		return updated, nil
	}
	return nil, nil
}

func (h *Handler) seedUser(ctx context.Context, email, name, sub, issuer string) (string, error) {
	// Hash issuer+sub for a label-safe lookup key.
	hash := sha256.Sum256([]byte(issuer + "/" + sub))
	subHash := hex.EncodeToString(hash[:])[:63]

	labelSelector := fmt.Sprintf("tenants.faros.sh/sub=%s", subHash)
	users, err := h.farosClient.Users().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("listing users: %w", err)
	}

	now := metav1.Now()

	if len(users.Items) == 0 {
		// No account bound to this issuer/subject yet. Before creating one,
		// adopt a pending invited User with the same (IdP-verified) email:
		// invitations pre-provision the account so memberships and app-access
		// grants written before first sign-in apply immediately. Only users
		// WITHOUT a sub label are adoptable — an email match must never
		// re-bind an account that already belongs to another IdP subject.
		if adopted, err := h.adoptInvitedUser(ctx, email, subHash, sub); err != nil {
			return "", err
		} else if adopted != nil {
			users.Items = append(users.Items, *adopted)
		}
	}

	if len(users.Items) > 0 {
		user := &users.Items[0]

		// Reconcile spec fields that may have drifted on legacy users created
		// before the sub→email RBAC switch. Without this, an old User CRD keeps
		// faros:<sub> in RBACIdentity forever, which no longer matches the
		// kcp-extracted username (now email-based) and locks the user out.
		wantRBAC := fmt.Sprintf("faros:%s", email)
		if user.Spec.RBACIdentity != wantRBAC || user.Spec.Email != email || user.Spec.Name != name {
			user.Spec.RBACIdentity = wantRBAC
			user.Spec.Email = email
			user.Spec.Name = name
			updatedSpec, err := h.farosClient.Users().Update(ctx, user, metav1.UpdateOptions{})
			if err != nil {
				return "", fmt.Errorf("updating user spec: %w", err)
			}
			user = updatedSpec
		}

		// Update status with last login.
		user.Status.Active = true
		user.Status.LastLogin = &now
		updated, err := h.farosClient.Users().UpdateStatus(ctx, user, metav1.UpdateOptions{})
		if err != nil {
			return "", fmt.Errorf("updating user status: %w", err)
		}
		return updated.Name, nil
	}

	// Create new user.
	user := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "user-",
			Labels: map[string]string{
				"tenants.faros.sh/sub": subHash,
			},
		},
		Spec: tenancyv1alpha1.UserSpec{
			Email:        email,
			Name:         name,
			RBACIdentity: fmt.Sprintf("faros:%s", email),
			OIDCProviders: []tenancyv1alpha1.OIDCProvider{
				{
					Name:       "dex",
					ProviderID: sub,
					Email:      email,
				},
			},
		},
	}
	// Set apiVersion and kind for dynamic client.
	user.APIVersion = "tenants.faros.sh/v1alpha1"
	user.Kind = "User"

	created, err := h.farosClient.Users().Create(ctx, user, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating user: %w", err)
	}

	// Update status.
	created.Status.Active = true
	created.Status.LastLogin = &now
	if _, err := h.farosClient.Users().UpdateStatus(ctx, created, metav1.UpdateOptions{}); err != nil {
		h.logger.Error(err, "failed to update new user status", "user", created.Name)
	}

	return created.Name, nil
}

// lookupDefaultCluster returns the User's spec.defaultCluster, polling
// briefly to give the organization bootstrap controller time to
// materialize the personal Org's default child Workspace and patch the
// field. Without the poll, a fresh user's first login would get a
// kubeconfig pointing at the bare hub (no /clusters/{name}) and every
// kubectl request would 404 until they logged in a second time.
//
// The legacy setDefaultCluster method was removed when the auth handler
// stopped calling CreateTenantWorkspace; the organization bootstrap
// controller is now the sole writer of User.spec.DefaultCluster.
func (h *Handler) lookupDefaultCluster(ctx context.Context, userID string) string {
	// The bootstrap controller's chain (org workspace + child workspace
	// + faros APIBinding bind + ClusterRoleBinding + default MCPServer
	// + cluster-hash lookup) takes ~10-25s on a cold start; the poll
	// budget needs to cover that with margin. On subsequent logins the
	// field is already set and the first iteration returns immediately.
	const (
		pollInterval = 500 * time.Millisecond
		pollTimeout  = 90 * time.Second
	)
	start := time.Now()
	deadline := start.Add(pollTimeout)
	for {
		user, err := h.farosClient.Users().Get(ctx, userID, metav1.GetOptions{})
		if err == nil && user.Spec.DefaultCluster != "" {
			if elapsed := time.Since(start); elapsed > pollInterval {
				h.logger.Info("Waited for bootstrap controller to populate User.spec.defaultCluster", "userID", userID, "waited", elapsed.String())
			}
			return user.Spec.DefaultCluster
		}
		if time.Now().After(deadline) {
			h.logger.Info("User.spec.defaultCluster still empty after poll; issuing kubeconfig without cluster name", "userID", userID, "waited", pollTimeout.String())
			return ""
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(pollInterval):
		}
	}
}

// generateKubeconfig builds a kubeconfig pointing to the hub using an exec
// credential plugin (faros get-token) for automatic OIDC token refresh.
// When clusterName is set, the server URL includes /clusters/{clusterName}
// for kcp-syntax compatibility.
func (h *Handler) generateKubeconfig(userID, clusterName, email string) ([]byte, error) {
	config := clientcmdapi.NewConfig()

	serverURL := h.hubExternalURL
	if clusterName != "" {
		serverURL = apiurl.HubServerURL(h.hubExternalURL, clusterName)
	}

	config.Clusters["faros"] = &clientcmdapi.Cluster{
		Server:                serverURL,
		InsecureSkipTLSVerify: h.devMode,
	}

	userName := userID
	// No --oidc-client-secret: PKCE public client refresh requires only the
	// issuer URL and client ID. The refresh token is stored in the token cache.
	execArgs := []string{
		"get-token",
		"--oidc-issuer-url=" + h.oidcConfig.IssuerURL,
		"--oidc-client-id=" + h.oidcConfig.ClientID,
	}
	if h.devMode {
		execArgs = append(execArgs, "--insecure-skip-tls-verify")
	}

	config.AuthInfos[userName] = &clientcmdapi.AuthInfo{
		Exec: &clientcmdapi.ExecConfig{
			APIVersion: "client.authentication.k8s.io/v1beta1",
			Command:    "faros",
			Args:       execArgs,
		},
	}

	config.Contexts["faros"] = &clientcmdapi.Context{
		Cluster:  "faros",
		AuthInfo: userName,
	}

	config.CurrentContext = "faros"

	return clientcmd.Write(*config)
}
