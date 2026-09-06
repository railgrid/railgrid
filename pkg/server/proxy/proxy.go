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

// Package proxy reverse-proxies authenticated requests to kcp.
package proxy

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	oidc "github.com/coreos/go-oidc"
	"golang.org/x/oauth2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros/pkg/apiurl"
	"github.com/faroshq/faros/pkg/browsersession"
	farosclient "github.com/faroshq/faros/pkg/client"
	"github.com/faroshq/faros/pkg/hub/kcp"
)

// defaultStaticTokenRateLimit is the default number of token-login requests allowed per minute per IP.
const defaultStaticTokenRateLimit = 10

// defaultStaticTokenBurstDuration is the default time window for static token rate limiting.
const defaultStaticTokenBurstDuration = time.Minute

// KCPProxy is a reverse proxy that authenticates requests via OIDC
// and forwards them to the user's dedicated kcp tenant workspace.
type KCPProxy struct {
	kcpTarget            *url.URL
	passthroughTransport http.RoundTripper // TLS-only transport; no credentials injected
	verifier             *oidc.IDTokenVerifier
	verifyCtx            context.Context // context with HTTP client for OIDC key fetches
	farosClient          *farosclient.Client
	bootstrapper         *kcp.Bootstrapper
	staticAuthTokens     []string
	hubExternalURL       string
	devMode              bool
	logger               klog.Logger
	// authorizer gates /clusters/{id} access against the caller's
	// UserMembershipIndex (docs/hub-proxy-workspace-access.md, Option A).
	authorizer *clusterAuthorizer
	// staticTokenRateLimiter protects the token-login endpoint against brute
	// force attacks. It is keyed on ClientIP, so it only sees through a proxy
	// once SetTrustedProxies has been called with that proxy's address range.
	staticTokenRateLimiter *rateLimiter
	browserSessions        *browsersession.Store
}

// SetTrustedProxies tells the token-login rate limiter which connection peers
// are reverse proxies whose X-Forwarded-For may be believed (see ClientIP).
// Without it every request is keyed on the connection peer, so a hub behind
// a proxy throttles all of its clients as one address.
func (p *KCPProxy) SetTrustedProxies(prefixes []netip.Prefix) {
	if p != nil && p.staticTokenRateLimiter != nil {
		p.staticTokenRateLimiter.trustedProxies = prefixes
	}
}

// SetBrowserSessionStore wires the hub-wide opaque browser-session store used
// by static-token portal login.  The proxy never places the static bearer in
// that store or in the cookie; it records only the resolved User identity.
func (p *KCPProxy) SetBrowserSessionStore(store *browsersession.Store) {
	if p != nil {
		p.browserSessions = store
	}
}

// NewKCPProxy creates a reverse proxy to kcp.
// It validates bearer tokens as OIDC id_tokens before proxying.
// verifier may be nil when only static token auth is used.
func NewKCPProxy(kcpConfig *rest.Config, verifier *oidc.IDTokenVerifier, farosClient *farosclient.Client, bootstrapper *kcp.Bootstrapper, staticAuthTokens []string, hubExternalURL string, devMode bool) (*KCPProxy, error) {
	target, err := url.Parse(kcpConfig.Host)
	if err != nil {
		return nil, err
	}

	// Build transport from the kcp admin rest.Config so that all auth methods
	// (client certificates, bearer tokens, token files, exec plugins) are
	// handled automatically. In dev mode with no explicit CA, skip TLS verify.
	transportConfig := rest.CopyConfig(kcpConfig)
	if devMode {
		if len(transportConfig.CAData) == 0 && transportConfig.CAFile == "" {
			transportConfig.Insecure = true
		}
	}

	// Build a passthrough transport with the same TLS config but no credentials.
	// Used when forwarding requests with the caller's own token (OIDC tokens,
	// SA tokens, static tokens — all authenticated by kcp natively).
	passthroughConfig := &rest.Config{
		Host: kcpConfig.Host,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: transportConfig.Insecure,
			CAData:   transportConfig.CAData,
			CAFile:   transportConfig.CAFile,
		},
	}
	passthroughTransport, err := rest.TransportFor(passthroughConfig)
	if err != nil {
		return nil, fmt.Errorf("building passthrough transport: %w", err)
	}

	// Build a context with an insecure HTTP client for OIDC key fetches.
	verifyCtx := context.Background()
	if devMode {
		insecureClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode only
			},
		}
		verifyCtx = context.WithValue(verifyCtx, oauth2.HTTPClient, insecureClient)
	}

	authorizer := newClusterAuthorizer(
		func(ctx context.Context, userName string) (*tenancyv1alpha1.UserMembershipIndex, error) {
			return farosClient.UserMembershipIndices().Get(ctx, userName, metav1.GetOptions{})
		},
		bootstrapper.GetChildWorkspaceClusterName,
		// Team workspaces only. An Org workspace can also hold the `providers`
		// container for org-owned providers, and authorizing that here would
		// give every org member access to the workspace that parents provider
		// credentials.
		bootstrapper.ListChildTeamWorkspaces,
	)

	return &KCPProxy{
		kcpTarget:            target,
		passthroughTransport: passthroughTransport,
		verifier:             verifier,
		verifyCtx:            verifyCtx,
		farosClient:          farosClient,
		bootstrapper:         bootstrapper,
		staticAuthTokens:     staticAuthTokens,
		hubExternalURL:       hubExternalURL,
		devMode:              devMode,
		logger:               klog.Background().WithName("kcp-proxy"),
		authorizer:           authorizer,
		// Initialize rate limiter for token-login endpoint (10 requests per minute)
		staticTokenRateLimiter: newRateLimiter(defaultStaticTokenBurstDuration, defaultStaticTokenRateLimit),
	}, nil
}

// ServeHTTP validates the bearer token and proxies the request to kcp.
// Two token types are supported:
//   - OIDC id_tokens (from Dex): resolved to a tenant workspace via User CRD lookup,
//     forwarded with admin credentials.
//   - kcp ServiceAccount tokens: the clusterName claim identifies the workspace,
//     forwarded with the original SA token so kcp handles authn/authz natively.
func (p *KCPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract bearer token.
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeUnauthorized(w)
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	// Static token: create user/workspace if needed and proxy to user's workspace.
	// Use constant-time comparison to prevent timing side-channel attacks.
	for _, staticToken := range p.staticAuthTokens {
		if staticToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(staticToken)) == 1 {
			p.logger.V(4).Info("proxy auth: static token matched", "path", r.URL.Path)
			p.serveStaticToken(w, r, token)
			return
		}
	}

	// APIExport virtual workspaces are provider-only and authorized structurally
	// (the export must live in the caller's own workspace), so they get their
	// own branch ahead of the general SA path — which would otherwise rewrite
	// the URL by prepending /clusters/{name} and destroy it.
	if vw, ok := parseVirtualWorkspacePath(r.URL.Path); ok {
		saClaims, isSA := parseServiceAccountToken(token)
		if !isSA {
			p.logger.Info("VW: non-ServiceAccount credential rejected", "path", r.URL.Path)
			writeForbidden(w, vwRequiresProviderIdentityBody)
			return
		}
		p.serveVirtualWorkspace(w, r, token, saClaims.ClusterName(), vw)
		return
	}

	// Check for kcp ServiceAccount tokens BEFORE OIDC verification.
	// SA tokens have iss="kubernetes/serviceaccount"; the OIDC verifier would
	// correctly reject them, but running the check first saves a JWKS fetch and
	// makes the auth branch unambiguous in logs.
	if saClaims, ok := parseServiceAccountToken(token); ok {
		p.logger.Info("proxy auth: SA token", "path", r.URL.Path, "clusterName", saClaims.ClusterName())
		p.serveServiceAccount(w, r, token, saClaims.ClusterName())
		return
	}

	// Try OIDC verification (user tokens from Dex).
	if p.verifier != nil {
		idToken, err := p.verifier.Verify(p.verifyCtx, token)
		if err == nil {
			p.logger.Info("proxy auth: OIDC verified", "path", r.URL.Path)
			p.serveOIDC(w, r, token, idToken)
			return
		}
		p.logger.Info("proxy auth: OIDC verify failed", "path", r.URL.Path, "err", err.Error())
	}

	// Log only SHA-256 hash prefix to prevent token information disclosure
	// while still allowing correlation for debugging
	tokenHash := sha256.Sum256([]byte(token))
	p.logger.Info("proxy auth: no match — returning 401", "path", r.URL.Path, "tokenHash", hex.EncodeToString(tokenHash[:])[:16])
	writeUnauthorized(w)
}

// serveOIDC handles OIDC-authenticated requests by resolving the user's tenant
// workspace and proxying with admin credentials.
//
// Two path formats are supported:
//   - /clusters/{logicalClusterName}/... — kcp-syntax (new kubeconfigs). The
//     cluster ID is verified against user.spec.defaultCluster.
//   - /api/... or /apis/... — bare path (legacy kubeconfigs). The workspace
//     path is constructed from the userID.
func (p *KCPProxy) serveOIDC(w http.ResponseWriter, r *http.Request, token string, idToken *oidc.IDToken) {
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := idToken.Claims(&claims); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"failed to parse token claims","reason":"InternalError","code":500}`)
		return
	}

	user, err := p.resolveUser(r.Context(), idToken.Issuer, claims.Sub)
	if err != nil {
		p.logger.Error(err, "failed to resolve user workspace", "sub", claims.Sub)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"user workspace not found","reason":"Forbidden","code":403}`)
		return
	}
	// Wait for the bootstrap controller to finish provisioning the user's
	// personal org/workspace (and its membership index) on the very first
	// request after sign-up. Warm-path requests short-circuit immediately.
	user = p.waitForDefaultCluster(r.Context(), user)

	// Authorize the requested cluster against the caller's membership (A-1/A-3).
	kcpPath, errStatus, errBody := p.authorizeKCPPath(r.Context(), user.Name, r.URL.Path)
	if errStatus != 0 {
		p.logger.Info("cluster access denied", "user", user.Name, "path", r.URL.Path, "status", errStatus)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(errStatus)
		_, _ = fmt.Fprint(w, errBody)
		return
	}

	target := *p.kcpTarget
	logger := p.logger

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = kcpPath
			req.Host = target.Host

			// Forward the user's bearer token unchanged — kcp authenticates it
			// directly (OIDC), so the request runs with the user's identity and
			// their RBAC is enforced by kcp natively. Do not strip Authorization
			// or add Impersonate-* headers.
			_ = user
		},
		Transport: p.passthroughTransport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error(err, "proxy upstream error", "method", r.Method, "path", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"upstream error","reason":"ServiceUnavailable","code":502}`)
		},
	}

	proxy.ServeHTTP(w, r)
}

// serveStaticToken handles static-token-authenticated requests by creating
// a user and workspace (if needed) and proxying to the user's tenant workspace.
func (p *KCPProxy) serveStaticToken(w http.ResponseWriter, r *http.Request, token string) {
	ctx := r.Context()

	// Use token hash as a stable identifier for the static token user.
	tokenHash := sha256.Sum256([]byte("static-token/" + token))
	subHash := hex.EncodeToString(tokenHash[:])[:63]

	// Look up or create the user for this static token.
	user, err := p.ensureStaticTokenUser(ctx, token, subHash)
	if err != nil {
		p.logger.Error(err, "failed to ensure static token user")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"failed to create user","reason":"InternalError","code":500}`)
		return
	}
	// Wait for the bootstrap controller to finish provisioning the user's
	// personal org/workspace (and its membership index) on first request.
	user = p.waitForDefaultCluster(ctx, user)

	// Authorize the requested cluster against the caller's membership (A-1/A-3).
	kcpPath, errStatus, errBody := p.authorizeKCPPath(ctx, user.Name, r.URL.Path)
	if errStatus != 0 {
		p.logger.Info("cluster access denied", "user", user.Name, "path", r.URL.Path, "status", errStatus)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(errStatus)
		_, _ = fmt.Fprint(w, errBody)
		return
	}

	target := *p.kcpTarget
	logger := p.logger

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = kcpPath
			req.Host = target.Host

			// Forward the user's bearer token unchanged — kcp must have a
			// matching static-token auth entry so it authenticates the request
			// as this user and enforces their RBAC natively. Do not strip
			// Authorization or add Impersonate-* headers.
			_ = user
		},
		Transport: p.passthroughTransport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error(err, "proxy upstream error (static token)", "method", r.Method, "path", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"upstream error","reason":"ServiceUnavailable","code":502}`)
		},
	}

	proxy.ServeHTTP(w, r)
}

// ensureStaticTokenUser creates or retrieves a User for a static token.
// It uses retry logic to handle conflicts from concurrent updates.
func (p *KCPProxy) ensureStaticTokenUser(ctx context.Context, token, subHash string) (*tenancyv1alpha1.User, error) {
	const maxRetries = 5
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		user, err := p.ensureStaticTokenUserOnce(ctx, token, subHash)
		if err == nil {
			return user, nil
		}
		// Check if the underlying error is a conflict (handles wrapped errors).
		if !apierrors.IsConflict(err) && !isConflictError(err) {
			return nil, err
		}
		lastErr = err
		p.logger.Info("retrying due to conflict", "attempt", i+1, "maxRetries", maxRetries)
	}

	return nil, fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// isConflictError checks if the error message indicates a conflict.
// This handles cases where apierrors.IsConflict doesn't work due to error wrapping.
func isConflictError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "the object has been modified") ||
		strings.Contains(errMsg, "please apply your changes to the latest version")
}

// sanitizeTokenSlug turns an arbitrary static token into a stable, email-safe
// slug. Unlike a fixed-length prefix, it preserves the full token, so tokens
// that share a prefix (e.g. "dev-token" / "dev-token2") map to distinct slugs
// and therefore distinct users. Characters outside [a-z0-9-] are folded to '-'.
func sanitizeTokenSlug(token string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(token) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "token"
	}
	return s
}

// ensureStaticTokenUserOnce is the single-attempt logic for ensureStaticTokenUser.
func (p *KCPProxy) ensureStaticTokenUserOnce(ctx context.Context, token, subHash string) (*tenancyv1alpha1.User, error) {
	labelSelector := fmt.Sprintf("tenants.faros.sh/sub=%s", subHash)
	users, err := p.farosClient.Users().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}

	now := metav1.Now()

	if len(users.Items) > 0 {
		user := &users.Items[0]

		// Update status with last login (best-effort, ignore conflicts here).
		user.Status.Active = true
		user.Status.LastLogin = &now
		_, _ = p.farosClient.Users().UpdateStatus(ctx, user, metav1.UpdateOptions{})

		// Workspace creation and User.spec.DefaultCluster patching are
		// owned by the organization bootstrap controller (it materializes
		// the personal Org's default child Workspace and writes its path
		// into User.spec.DefaultCluster once Ready). The legacy
		// CreateTenantWorkspace + EnsureWorkspaceAdmin + EnsureDefaultMCPServer
		// chain that used to live here was removed; the controller
		// re-runs those steps idempotently on every reconcile so the
		// per-login backfill is unnecessary.
		return user, nil
	}

	// Create new user with a DETERMINISTIC name derived from the token hash.
	// Using GenerateName here is racy: two concurrent logins both miss the
	// List above and both create a user with a different random name, so the
	// AlreadyExists guard never fires and we get duplicate users for one token.
	// A stable name makes a racing create collide → AlreadyExists → reuse.
	//
	// Derive the human-facing email/display slug from the FULL token (sanitized),
	// not a fixed-length prefix: two tokens that share a prefix (e.g. "dev-token"
	// and "dev-token2") would otherwise collapse to the same email, which — since
	// --admin-users matches on email — also leaks admin between distinct users.
	tokenSlug := sanitizeTokenSlug(token)
	userName := "static-user-" + subHash[:16]

	user := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{
			Name: userName,
			Labels: map[string]string{
				"tenants.faros.sh/sub":       subHash,
				"tenants.faros.sh/auth-type": "static-token",
			},
		},
		Spec: tenancyv1alpha1.UserSpec{
			Email:        fmt.Sprintf("static-%s@faros.local", tokenSlug),
			Name:         fmt.Sprintf("Static Token User (%s)", tokenSlug),
			RBACIdentity: fmt.Sprintf("faros:static:%s", subHash[:16]),
		},
	}
	user.APIVersion = "tenants.faros.sh/v1alpha1"
	user.Kind = "User"

	created, err := p.farosClient.Users().Create(ctx, user, metav1.CreateOptions{})
	if err != nil {
		// Concurrent login won the race — reuse the existing user by name.
		if apierrors.IsAlreadyExists(err) {
			existing, getErr := p.farosClient.Users().Get(ctx, userName, metav1.GetOptions{})
			if getErr != nil {
				return nil, fmt.Errorf("getting user after create conflict: %w", getErr)
			}
			return existing, nil
		}
		return nil, fmt.Errorf("creating user: %w", err)
	}

	// Update status (best-effort).
	created.Status.Active = true
	created.Status.LastLogin = &now
	_, _ = p.farosClient.Users().UpdateStatus(ctx, created, metav1.UpdateOptions{})

	// Workspace creation + User.spec.DefaultCluster patching is owned
	// by the organization bootstrap controller, not the auth path.
	// See ensureStaticTokenUserOnce for the matching comment in the
	// already-exists branch.
	return created, nil
}

// serveServiceAccount handles kcp ServiceAccount tokens by forwarding the
// request to the workspace identified by the clusterName claim, keeping the
// original SA token so kcp performs native authn/authz.
func (p *KCPProxy) serveServiceAccount(w http.ResponseWriter, r *http.Request, token, clusterName string) {
	// Validate clusterName against a strict regex to prevent path traversal.
	matched, _ := regexp.MatchString(`^[a-z0-9]+(?:[:-][a-z0-9]+)*$`, clusterName)
	if !matched {
		p.logger.Info("SA: clusterName regex rejected — 401", "clusterName", clusterName)
		writeUnauthorized(w)
		return
	}

	// O-10 gate: refuse direct access to Organization workspaces even when
	// authenticated as a kcp ServiceAccount. SA tokens bound at the Org
	// workspace shouldn't exist in v1, but defense-in-depth: the structural
	// check here costs nothing and stops a misconfigured token from
	// reaching the Org workspace's API server.
	if isOrgWorkspacePath(clusterName) {
		p.logger.Info("SA: org workspace access denied (O-10)", "cluster", clusterName)
		writeOrgWorkspaceForbidden(w)
		return
	}

	target := *p.kcpTarget
	logger := p.logger

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host

			// The agent kubeconfig may already include /clusters/{name} in its
			// server URL, so the incoming path can be
			//   /clusters/{name}/api/...
			// Strip the prefix to avoid doubling it when we prepend below.
			clusterPrefix := "/clusters/" + clusterName
			reqPath := req.URL.Path
			if strings.HasPrefix(reqPath, clusterPrefix+"/") || reqPath == clusterPrefix {
				reqPath = strings.TrimPrefix(reqPath, clusterPrefix)
				if reqPath == "" {
					reqPath = "/"
				}
			}
			req.URL.Path = clusterPrefix + reqPath
			req.Host = target.Host

			// Keep the SA token — kcp authenticates it natively.
			req.Header.Set("Authorization", "Bearer "+token)
			logger.Info("SA: forwarding to kcp", "targetPath", req.URL.Path, "host", req.URL.Host)
		},
		Transport: p.passthroughTransport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error(err, "proxy upstream error (SA)", "method", r.Method, "path", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"upstream error","reason":"ServiceUnavailable","code":502}`)
		},
	}

	proxy.ServeHTTP(w, r)
}

// saTokenClaims holds the claims we extract from a kcp ServiceAccount JWT. kcp
// carries the SA's logical cluster in the token: bound tokens nest it under
// kubernetes.io.clusterName, legacy tokens use the flat
// kubernetes.io/serviceaccount/clusterName key. We read both, matching kcp's
// WithInClusterServiceAccountRequestRewrite (pkg/server/filters/serviceaccounts.go).
type saTokenClaims struct {
	Issuer            string `json:"iss"`
	ClusterNameLegacy string `json:"kubernetes.io/serviceaccount/clusterName"`
	Kubernetes        struct {
		ClusterName string `json:"clusterName"`
	} `json:"kubernetes.io"`
}

// ClusterName returns the SA's logical cluster, preferring the bound-token claim
// and falling back to the legacy flat claim.
func (c saTokenClaims) ClusterName() string {
	if c.Kubernetes.ClusterName != "" {
		return c.Kubernetes.ClusterName
	}
	return c.ClusterNameLegacy
}

// parseServiceAccountToken decodes a JWT without signature verification and
// checks whether it is a kcp ServiceAccount token. kcp will verify the
// signature when the request is forwarded.
func parseServiceAccountToken(token string) (saTokenClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return saTokenClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return saTokenClaims{}, false
	}
	var claims saTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return saTokenClaims{}, false
	}
	// It's a kcp SA token when it carries a logical-cluster claim (bound or
	// legacy). An OIDC/Dex user token has neither, so this never misclassifies a
	// user token. We no longer require iss=="kubernetes/serviceaccount" so that
	// bound SA tokens (different issuer) are also recognised.
	if claims.ClusterName() == "" {
		return saTokenClaims{}, false
	}
	return claims, true
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"Unauthorized","reason":"Unauthorized","code":401}`)
}

// orgWorkspacePathPrefix is the kcp logical-cluster path under which every
// Organization workspace lives (root:faros:orgs:{org-uuid}). The proxy
// uses this prefix together with the structural rule "an Organization
// workspace has exactly one segment after orgs:" to decide whether a
// requested target is an Org workspace.
const orgWorkspacePathPrefix = "root:faros:tenants:"

// orgWorkspaceForbiddenBody is the JSON the proxy returns when refusing a
// direct request to an Organization workspace per docs/organizations.md
// decision O-10 ("Org workspaces are hub-mediated only"). The body uses
// the standard Kubernetes Status envelope so kubectl renders the message
// nicely while also carrying a faros-specific reason + a pointer at the
// hub REST surface so CLI tooling can suggest the right endpoint.
const orgWorkspaceForbiddenBody = `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"Organization workspaces are hub-mediated and not directly accessible — use the hub REST endpoints at /api/orgs/{org-uuid}/... instead.","reason":"OrgWorkspaceNotDirectlyAccessible","code":403,"details":{"kind":"OrganizationWorkspace","group":"tenants.faros.sh"}}`

// isOrgWorkspacePath reports whether clusterPath addresses a kcp
// Organization workspace (path root:faros:orgs:{single-segment}). Child
// "team" workspaces under an Org, which look like
// root:faros:orgs:{org-uuid}:{ws-uuid}, do NOT match — those remain
// tenant-accessible per the design.
//
// The check is structural rather than annotation-based on purpose: every
// Organization workspace lives at exactly this path shape by the
// invariant established in PR #2 (workspacetype-organization.yaml +
// Bootstrapper.EnsureOrgWorkspace). Using the path keeps the proxy hot
// path free of kcp lookups and avoids a cache layer.
func isOrgWorkspacePath(clusterPath string) bool {
	if !strings.HasPrefix(clusterPath, orgWorkspacePathPrefix) {
		return false
	}
	rest := strings.TrimPrefix(clusterPath, orgWorkspacePathPrefix)
	if rest == "" {
		return false
	}
	// Exactly one segment after `orgs:` ⇒ an Org workspace. A second
	// colon ⇒ child team Workspace (root:faros:orgs:{org}:{ws}).
	return !strings.Contains(rest, ":")
}

// extractClusterPathFromKCPPath returns the logical-cluster path portion
// of a kcp-syntax URL path like `/clusters/{cluster}/api/...`. Returns
// the empty string if the input doesn't carry a /clusters/ prefix
// (i.e. it has already been resolved by resolveKCPPath, which would
// have prepended the default cluster). The proxy calls this AFTER
// resolveKCPPath so kcpPath is always /clusters/-prefixed.
func extractClusterPathFromKCPPath(kcpPath string) string {
	if !strings.HasPrefix(kcpPath, "/clusters/") {
		return ""
	}
	rest := strings.TrimPrefix(kcpPath, "/clusters/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
}

// writeOrgWorkspaceForbidden writes the O-10 403 response.
func writeOrgWorkspaceForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = fmt.Fprint(w, orgWorkspaceForbiddenBody)
}

// Response bodies for the membership-gated cluster authorization (Option A).
const (
	bareNoClusterBody       = `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"no workspace selected — address /clusters/{id} (resolve the id via the hub REST endpoints, e.g. /api/orgs/{org}/workspaces)","reason":"BadRequest","code":400}`
	addressByIDBody         = `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"address workspaces by cluster ID (/clusters/{id}), not by path — resolve the id via /api/orgs/{org}/workspaces/{ws}","reason":"Forbidden","code":403}`
	clusterAccessDeniedBody = `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"cluster access denied","reason":"Forbidden","code":403}`
)

// authorizeKCPPath authorizes userName's request URL against their membership
// and returns the kcp path to forward (unchanged for /clusters/{id}) or an
// error (status, body). Implements docs/hub-proxy-workspace-access.md:
//
//   - bare /api|/apis (no cluster segment) → rejected; there is no
//     DefaultCluster default (A-1).
//   - /clusters/{org-workspace-path} → O-10 refusal (Org workspaces are
//     hub-mediated).
//   - /clusters/{tenant-path} (path-form) → rejected; clients address by ID.
//   - /clusters/{id}[:{edge}] → allowed iff the caller is a member of the
//     workspace the id (or the id of an edge's parent) belongs to (A-3).
//
// Returns (kcpPath, 0, "") on success, or ("", status, body) on denial.
func (p *KCPProxy) authorizeKCPPath(ctx context.Context, userName, urlPath string) (string, int, string) {
	if !strings.HasPrefix(urlPath, "/clusters/") {
		return "", http.StatusBadRequest, bareNoClusterBody
	}
	seg := extractClusterPathFromKCPPath(urlPath)
	switch {
	case seg == "":
		return "", http.StatusBadRequest, bareNoClusterBody
	case isOrgWorkspacePath(seg):
		return "", http.StatusForbidden, orgWorkspaceForbiddenBody
	case strings.HasPrefix(seg, "root:"):
		return "", http.StatusForbidden, addressByIDBody
	}
	if !p.authorizer.authorize(ctx, userName, seg) {
		return "", http.StatusForbidden, clusterAccessDeniedBody
	}
	return urlPath, 0, ""
}

// ErrIdentifyNoBearer is returned by IdentifyUser when the request
// carries no Authorization: Bearer header. Callers (e.g. the tenant
// middleware) translate this into a 401.
var ErrIdentifyNoBearer = errors.New("no Authorization: Bearer token")

// ErrUserRecordUnavailable wraps failures that happen AFTER the caller's
// credential has been accepted — the static token matched, or the OIDC token
// verified — but the backing User record could not be read or created.
//
// It exists so callers can tell "this caller is not authenticated" from "this
// caller is authenticated and the hub is having trouble". Both surface as an
// error from IdentifyUser, and conflating them forces a choice between serving
// unauthenticated callers and 500-ing authenticated ones during a hiccup.
// Endpoints that only need to know the caller is legitimate (not who they are)
// can proceed on this; anything that acts as the user must still fail.
var ErrUserRecordUnavailable = errors.New("caller authenticated but user record unavailable")

// IdentifyUser extracts the caller's User CR name from r's bearer
// token, using the same auth schemes as ServeHTTP (static token,
// OIDC). Used by hub REST endpoints behind the tenant middleware so
// they can identify the caller without duplicating the proxy's auth
// dispatch.
//
// Returns ErrIdentifyNoBearer for missing/unparseable Authorization
// headers, and other errors for verification failures. kcp
// ServiceAccount tokens are intentionally not accepted here — REST
// endpoints are addressed by humans (or by their portal session) and
// not by edge-side bots.
func (p *KCPProxy) IdentifyUser(r *http.Request) (string, error) {
	identity, err := p.BrowserIdentity(r)
	if err != nil {
		return "", err
	}
	return identity.UserID, nil
}

// BrowserIdentity validates a portal bearer and resolves it to the stable
// platform user identity used by the shared browser-session store.  The
// returned value contains no bearer, kubeconfig, or OIDC token.
func (p *KCPProxy) BrowserIdentity(r *http.Request) (browsersession.Identity, error) {
	if r == nil {
		return browsersession.Identity{}, ErrIdentifyNoBearer
	}
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return browsersession.Identity{}, ErrIdentifyNoBearer
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	// Static token branch first — constant-time compare per ServeHTTP.
	for _, staticToken := range p.staticAuthTokens {
		if staticToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(staticToken)) == 1 {
			tokenHash := sha256.Sum256([]byte("static-token/" + token))
			subHash := hex.EncodeToString(tokenHash[:])[:63]
			user, err := p.ensureStaticTokenUser(r.Context(), token, subHash)
			if err != nil {
				return browsersession.Identity{}, fmt.Errorf("%w: resolving static-token user: %w", ErrUserRecordUnavailable, err)
			}
			return browsersession.Identity{UserID: user.Name, Email: user.Spec.Email, Name: user.Spec.Name, RBACIdentity: user.Spec.RBACIdentity, AuthType: "static-token"}, nil
		}
	}

	// OIDC branch.
	if p.verifier != nil {
		idToken, err := p.verifier.Verify(p.verifyCtx, token)
		if err != nil {
			return browsersession.Identity{}, fmt.Errorf("verifying OIDC token: %w", err)
		}
		var claims struct {
			Sub   string `json:"sub"`
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		if err := idToken.Claims(&claims); err != nil {
			return browsersession.Identity{}, fmt.Errorf("parsing OIDC claims: %w", err)
		}
		user, err := p.resolveUser(r.Context(), idToken.Issuer, claims.Sub)
		if err != nil {
			return browsersession.Identity{}, fmt.Errorf("%w: resolving OIDC user: %w", ErrUserRecordUnavailable, err)
		}
		return browsersession.Identity{UserID: user.Name, Email: user.Spec.Email, Name: user.Spec.Name, RBACIdentity: user.Spec.RBACIdentity, Issuer: idToken.Issuer, Subject: claims.Sub, AuthType: "oidc"}, nil
	}

	return browsersession.Identity{}, ErrIdentifyNoBearer
}

// resolveUser looks up the User CRD by OIDC issuer+sub hash and returns the full User object.
func (p *KCPProxy) resolveUser(ctx context.Context, issuer, sub string) (*tenancyv1alpha1.User, error) {
	hash := sha256.Sum256([]byte(issuer + "/" + sub))
	subHash := hex.EncodeToString(hash[:])[:63]

	labelSelector := fmt.Sprintf("tenants.faros.sh/sub=%s", subHash)
	users, err := p.farosClient.Users().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	if len(users.Items) == 0 {
		return nil, fmt.Errorf("no user found for sub hash %s", subHash)
	}
	return &users.Items[0], nil
}

// waitForDefaultCluster blocks until User.spec.defaultCluster is
// populated by the organization bootstrap controller, or the budget
// runs out. Returns the most recent User snapshot. The first iteration
// short-circuits when DefaultCluster is already set, so warm-path
// requests pay no cost — only the very first request after a fresh
// user creation pays the cold-start wait while Step E + G + H + I + J
// of the bootstrap reconciler complete.
func (p *KCPProxy) waitForDefaultCluster(ctx context.Context, user *tenancyv1alpha1.User) *tenancyv1alpha1.User {
	if user.Spec.DefaultCluster != "" {
		return user
	}
	const (
		pollInterval = 500 * time.Millisecond
		pollTimeout  = 90 * time.Second
	)
	start := time.Now()
	deadline := start.Add(pollTimeout)
	for {
		fresh, err := p.farosClient.Users().Get(ctx, user.Name, metav1.GetOptions{})
		if err == nil && fresh.Spec.DefaultCluster != "" {
			if elapsed := time.Since(start); elapsed > pollInterval {
				p.logger.Info("Waited for bootstrap controller to populate User.spec.defaultCluster", "user", user.Name, "waited", elapsed.String())
			}
			return fresh
		}
		if time.Now().After(deadline) {
			p.logger.Info("User.spec.defaultCluster still empty after poll", "user", user.Name, "waited", pollTimeout.String())
			return user
		}
		select {
		case <-ctx.Done():
			return user
		case <-time.After(pollInterval):
		}
	}
}

// HandleTokenLoginRateLimited wraps HandleTokenLogin with rate limiting.
// This should be used when registering the route to protect against brute force attacks.
// In devMode the limiter is bypassed: dev clusters share a single client IP
// (localhost) across many test/CLI invocations, which trivially exhausts the
// per-IP bucket and is not a realistic threat model for dev.
func (p *KCPProxy) HandleTokenLoginRateLimited(w http.ResponseWriter, r *http.Request) {
	if p.devMode {
		p.HandleTokenLogin(w, r)
		return
	}
	p.staticTokenRateLimiter.middleware(p.HandleTokenLogin)(w, r)
}

// HandleTokenLogin handles static token login requests.
// POST /auth/token-login with Authorization: Bearer <token>
// Returns a LoginResponse with kubeconfig pointing to the user's workspace.
// Note: This handler should be wrapped with rate limiting using HandleTokenLoginRateLimited
// when registering routes to prevent brute force attacks.
func (p *KCPProxy) HandleTokenLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"Method not allowed","reason":"MethodNotAllowed","code":405}`)
		return
	}

	// Extract bearer token.
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeUnauthorized(w)
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	// Validate token against static tokens.
	// Use constant-time comparison to prevent timing side-channel attacks.
	validToken := false
	for _, staticToken := range p.staticAuthTokens {
		if staticToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(staticToken)) == 1 {
			validToken = true
			break
		}
	}
	if !validToken {
		writeUnauthorized(w)
		return
	}

	ctx := r.Context()

	// Use token hash as a stable identifier for the static token user.
	tokenHash := sha256.Sum256([]byte("static-token/" + token))
	subHash := hex.EncodeToString(tokenHash[:])[:63]

	// Ensure user and workspace exist.
	user, err := p.ensureStaticTokenUser(ctx, token, subHash)
	if err != nil {
		p.logger.Error(err, "failed to ensure static token user")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"failed to create user","reason":"InternalError","code":500}`)
		return
	}
	// Wait for the bootstrap controller to populate DefaultCluster so
	// the kubeconfig server URL includes /clusters/{hash}. Without
	// this, e2e flows that POST /auth/token-login and then run kubectl
	// immediately get a bare-hub server URL and hit 404.
	user = p.waitForDefaultCluster(ctx, user)

	// Generate kubeconfig pointing to the user's workspace.
	kubeconfigBytes, err := p.generateStaticTokenKubeconfig(user, token)
	if err != nil {
		p.logger.Error(err, "failed to generate kubeconfig")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"failed to generate kubeconfig","reason":"InternalError","code":500}`)
		return
	}

	// Build response.
	resp := tenancyv1alpha1.LoginResponse{
		Kubeconfig: kubeconfigBytes,
		Email:      user.Spec.Email,
		UserID:     user.Name,
	}
	if p.browserSessions != nil {
		if _, sessionErr := p.browserSessions.IssueHTTP(r.Context(), w, browsersession.Identity{
			UserID: user.Name, Email: user.Spec.Email, Name: user.Spec.Name,
			RBACIdentity: user.Spec.RBACIdentity,
			AuthType:     "static-token",
		}); sessionErr != nil {
			p.logger.Error(sessionErr, "failed to issue shared browser session")
			http.Error(w, "failed to create browser session", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		p.logger.Error(err, "failed to encode login response")
	}
}

// generateStaticTokenKubeconfig builds a kubeconfig for a static token user.
func (p *KCPProxy) generateStaticTokenKubeconfig(user *tenancyv1alpha1.User, token string) ([]byte, error) {
	config := clientcmdapi.NewConfig()

	serverURL := p.hubExternalURL
	if user.Spec.DefaultCluster != "" {
		serverURL = apiurl.HubServerURL(p.hubExternalURL, user.Spec.DefaultCluster)
	}

	config.Clusters["faros"] = &clientcmdapi.Cluster{
		Server:                serverURL,
		InsecureSkipTLSVerify: p.devMode,
	}

	config.AuthInfos["faros"] = &clientcmdapi.AuthInfo{
		Token: token,
	}

	config.Contexts["faros"] = &clientcmdapi.Context{
		Cluster:  "faros",
		AuthInfo: "faros",
	}

	config.CurrentContext = "faros"

	return clientcmd.Write(*config)
}
