/*
Copyright 2026 The Railgrid Authors.

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

// Package proxy is the hub's transparent front door to kcp, plus the
// identity helpers and static-token login that share its auth dispatch.
package proxy

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
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

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/browsersession"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
	"github.com/railgrid/railgrid/pkg/util/identity"
)

// defaultStaticTokenRateLimit is the default number of token-login requests allowed per minute per IP.
const defaultStaticTokenRateLimit = 10

// defaultStaticTokenBurstDuration is the default time window for static token rate limiting.
const defaultStaticTokenBurstDuration = time.Minute

// KCPProxy is the hub's front door to kcp. It forwards every kube path
// (/clusters/{id}/…, /services/apiexport/…, bare /api and /apis) to kcp
// byte-for-byte: path, query, Authorization and the WebSocket bearer
// subprotocol all arrive as the caller sent them, and the hub adds no
// credential of its own. kcp authenticates the bearer (static token, OIDC,
// ServiceAccount) and authorizes the request with its own RBAC — the
// workspace `access` gate, the bound tenant-admin role and the organization
// boundary guard — so the hub makes no access decision on this path. The
// type also hosts the identity helpers (BrowserIdentity, IdentifyUser) and
// the static-token login the rest of the hub shares with that front door.
type KCPProxy struct {
	forward          *httputil.ReverseProxy // TLS-only transport; no credentials injected
	verifier         *oidc.IDTokenVerifier
	verifyCtx        context.Context // context with HTTP client for OIDC key fetches
	railgridClient   *railgridclient.Client
	staticAuthTokens []string
	hubExternalURL   string
	devMode          bool
	logger           klog.Logger
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

// NewKCPProxy creates the transparent front door to kcp. verifier is used
// only by BrowserIdentity and may be nil when only static token auth is used.
func NewKCPProxy(kcpConfig *rest.Config, verifier *oidc.IDTokenVerifier, railgridClient *railgridclient.Client, staticAuthTokens []string, hubExternalURL string, devMode bool) (*KCPProxy, error) {
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

	logger := klog.Background().WithName("kcp-proxy")
	return &KCPProxy{
		forward:          newForwarder(target, passthroughTransport, logger),
		verifier:         verifier,
		verifyCtx:        verifyCtx,
		railgridClient:   railgridClient,
		staticAuthTokens: staticAuthTokens,
		hubExternalURL:   hubExternalURL,
		devMode:          devMode,
		logger:           logger,
		// Initialize rate limiter for token-login endpoint (10 requests per minute)
		staticTokenRateLimiter: newRateLimiter(defaultStaticTokenBurstDuration, defaultStaticTokenRateLimit),
	}, nil
}

// newForwarder builds the one reverse proxy every kube path goes through.
// The Director rewrites only the target host: the path (including a
// workspace path in the cluster segment or a virtual-workspace prefix), the
// query and every header are kcp's to interpret. FlushInterval -1 disables
// response buffering so watches and streamed verbs deliver as events arrive.
func newForwarder(target *url.URL, transport http.RoundTripper, logger klog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
		},
		FlushInterval: -1,
		Transport:     transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error(err, "proxy upstream error", "method", r.Method, "path", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"upstream error","reason":"ServiceUnavailable","code":502}`)
		},
	}
}

// ServeHTTP forwards the request to kcp unchanged. There is no bearer check,
// cluster check or path rewrite here: a request without a credential gets
// kcp's 401, a workspace the caller holds no RBAC in gets kcp's 403, and a
// path kcp does not serve gets kcp's 404.
func (p *KCPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.forward.ServeHTTP(w, r)
}

// ensureStaticTokenUser creates or retrieves a User for a static token.
// It uses retry logic to handle conflicts from concurrent updates.
func (p *KCPProxy) ensureStaticTokenUser(ctx context.Context, id identity.StaticToken) (*tenancyv1alpha1.User, error) {
	const maxRetries = 5
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		user, err := p.ensureStaticTokenUserOnce(ctx, id)
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

// scrubStaticTokenUserSpec makes a static-token User show only its RBAC
// identity: no email, display name = RBAC identity. Fellow org and workspace
// members can read both fields from the member list, and hubs before this
// change wrote the token text into them ("static-<token>@railgrid.local" /
// "Static Token User (<token>)"), which handed every co-member a working
// credential. Returns true when the spec changed and needs writing.
func scrubStaticTokenUserSpec(user *tenancyv1alpha1.User, id identity.StaticToken) bool {
	if user.Spec.Email == "" && user.Spec.Name == id.RBACIdentity {
		return false
	}
	user.Spec.Email = ""
	user.Spec.Name = id.RBACIdentity
	return true
}

// legacyStaticTokenUserNamePrefix starts the display name hubs before the
// scrub gave static-token users, "Static Token User (<token>)".
const legacyStaticTokenUserNamePrefix = "Static Token User ("

// scrubStaticTokenUser removes the token text from a static-token User and
// from the default name of its personal Organization, which the organization
// controller derived from the User's display name ("<name>'s personal", see
// personalOrgDisplayName) and which that Org's other members see. The Org is
// renamed first so the User update, which triggers the organization
// controller, re-syncs the owner's index rows from the new name.
func (p *KCPProxy) scrubStaticTokenUser(ctx context.Context, user *tenancyv1alpha1.User, id identity.StaticToken) (*tenancyv1alpha1.User, error) {
	if err := p.scrubStaticTokenPersonalOrg(ctx, user, id); err != nil {
		return nil, err
	}
	if !scrubStaticTokenUserSpec(user, id) {
		return user, nil
	}
	updated, err := p.railgridClient.Users().Update(ctx, user, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("removing token text from user %s: %w", user.Name, err)
	}
	p.logger.Info("removed token text from static-token user", "user", user.Name, "rbacIdentity", id.RBACIdentity)
	return updated, nil
}

// scrubStaticTokenPersonalOrg renames a static-token User's personal
// Organization when it still has the token-derived default name, and
// rewrites the copies of that name in every member's UserMembershipIndex
// (index rows mirror Organization.spec.displayName at write time and are not
// refreshed on rename). A name the owner chose is left alone.
func (p *KCPProxy) scrubStaticTokenPersonalOrg(ctx context.Context, user *tenancyv1alpha1.User, id identity.StaticToken) error {
	if user.Status.PersonalOrg == "" {
		return nil
	}
	org, err := p.railgridClient.Organizations().Get(ctx, user.Status.PersonalOrg, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting personal organization of %s: %w", user.Name, err)
	}
	if !org.Spec.Personal || !strings.HasPrefix(org.Spec.DisplayName, legacyStaticTokenUserNamePrefix) {
		return nil
	}
	oldName := org.Spec.DisplayName
	newName := id.RBACIdentity + "'s personal"

	// Index rows first: if a write fails, the Org keeps the old name and the
	// next pass (login or hub start) finds and retries it.
	indices, err := p.railgridClient.UserMembershipIndices().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing membership indices: %w", err)
	}
	for i := range indices.Items {
		idx := &indices.Items[i]
		changed := false
		for j := range idx.Spec.Entries {
			if e := &idx.Spec.Entries[j]; e.OrgUUID == org.Name && e.OrgDisplayName == oldName {
				e.OrgDisplayName = newName
				changed = true
			}
		}
		if !changed {
			continue
		}
		if _, err := p.railgridClient.UserMembershipIndices().Update(ctx, idx, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("renaming organization %s in membership index %s: %w", org.Name, idx.Name, err)
		}
	}

	org.Spec.DisplayName = newName
	if _, err := p.railgridClient.Organizations().Update(ctx, org, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("renaming personal organization %s: %w", org.Name, err)
	}
	p.logger.Info("removed token text from static-token user's personal organization", "user", user.Name, "org", org.Name)
	return nil
}

// ScrubStaticTokenUsers removes the token text from the Users (and their
// personal Organizations) of every configured static token; see
// scrubStaticTokenUser. Login does the same, but a token nobody uses after an
// upgrade would otherwise stay readable in member lists indefinitely. It only
// updates Users that exist; it never creates one.
func (p *KCPProxy) ScrubStaticTokenUsers(ctx context.Context) error {
	for _, token := range p.staticAuthTokens {
		if token == "" {
			continue
		}
		id := identity.NewStaticToken(token)
		users, err := p.railgridClient.Users().List(ctx, metav1.ListOptions{LabelSelector: identity.LabelSubject + "=" + id.Sub})
		if err != nil {
			return fmt.Errorf("listing static-token users: %w", err)
		}
		for i := range users.Items {
			if _, err := p.scrubStaticTokenUser(ctx, &users.Items[i], id); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureStaticTokenUserOnce is the single-attempt logic for ensureStaticTokenUser.
func (p *KCPProxy) ensureStaticTokenUserOnce(ctx context.Context, id identity.StaticToken) (*tenancyv1alpha1.User, error) {
	labelSelector := identity.LabelSubject + "=" + id.Sub
	users, err := p.railgridClient.Users().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}

	now := metav1.Now()

	if len(users.Items) > 0 {
		user := &users.Items[0]

		// Users created before the email/name stopped being token-derived
		// still carry the token; rewrite them on their next login. Checked
		// on the in-memory copy so an already-clean User costs no extra
		// request. A conflict here is retried by ensureStaticTokenUser.
		if user.Spec.Email != "" || user.Spec.Name != id.RBACIdentity {
			if user, err = p.scrubStaticTokenUser(ctx, user, id); err != nil {
				return nil, err
			}
		}

		// Update status with last login (best-effort, ignore conflicts here).
		user.Status.Active = true
		user.Status.LastLogin = &now
		_, _ = p.railgridClient.Users().UpdateStatus(ctx, user, metav1.UpdateOptions{})

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
	// Nothing human-facing is derived from the token text: the email and
	// display name are readable by every fellow org member, so they carry only
	// the RBAC identity (a hash), which --admin-users can also match on.
	user := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{
			Name: id.UserName,
			Labels: map[string]string{
				identity.LabelSubject:           id.Sub,
				"tenants.railgrid.ai/auth-type": "static-token",
			},
		},
		Spec: tenancyv1alpha1.UserSpec{
			Name:         id.RBACIdentity,
			RBACIdentity: id.RBACIdentity,
		},
	}
	user.APIVersion = "tenants.railgrid.ai/v1alpha1"
	user.Kind = "User"

	created, err := p.railgridClient.Users().Create(ctx, user, metav1.CreateOptions{})
	if err != nil {
		// Concurrent login won the race — reuse the existing user by name.
		if apierrors.IsAlreadyExists(err) {
			existing, getErr := p.railgridClient.Users().Get(ctx, id.UserName, metav1.GetOptions{})
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
	_, _ = p.railgridClient.Users().UpdateStatus(ctx, created, metav1.UpdateOptions{})

	// Workspace creation + User.spec.DefaultCluster patching is owned
	// by the organization bootstrap controller, not the auth path.
	// See ensureStaticTokenUserOnce for the matching comment in the
	// already-exists branch.
	return created, nil
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"Unauthorized","reason":"Unauthorized","code":401}`)
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

// IdentifyUser extracts the caller's User CR name from r's bearer token
// (static token or OIDC). Used by hub REST endpoints behind the tenant
// middleware so they can identify the caller with one auth dispatch.
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

	// Static token branch first — constant-time compare.
	for _, staticToken := range p.staticAuthTokens {
		if staticToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(staticToken)) == 1 {
			user, err := p.ensureStaticTokenUser(r.Context(), identity.NewStaticToken(token))
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

// resolveUser looks up the User CRD bound to the OIDC issuer+sub, as its
// primary identity or a linked one, and returns the full User object.
func (p *KCPProxy) resolveUser(ctx context.Context, issuer, sub string) (*tenancyv1alpha1.User, error) {
	subHash := identity.OIDCSubjectHash(issuer, sub)
	user, err := identity.FindUserByOIDCSubject(ctx, p.railgridClient, subHash)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, fmt.Errorf("no user found for sub hash %s", subHash)
	}
	return user, nil
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
		fresh, err := p.railgridClient.Users().Get(ctx, user.Name, metav1.GetOptions{})
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

	// Ensure user and workspace exist.
	user, err := p.ensureStaticTokenUser(ctx, identity.NewStaticToken(token))
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

	config.Clusters["railgrid"] = &clientcmdapi.Cluster{
		Server:                serverURL,
		InsecureSkipTLSVerify: p.devMode,
	}

	config.AuthInfos["railgrid"] = &clientcmdapi.AuthInfo{
		Token: token,
	}

	config.Contexts["railgrid"] = &clientcmdapi.Context{
		Cluster:  "railgrid",
		AuthInfo: "railgrid",
	}

	config.CurrentContext = "railgrid"

	return clientcmd.Write(*config)
}
