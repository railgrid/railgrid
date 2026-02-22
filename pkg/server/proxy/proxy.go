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
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	oidc "github.com/coreos/go-oidc"
	"golang.org/x/oauth2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
)

// KCPProxy is a reverse proxy that authenticates requests via OIDC
// and forwards them to the user's dedicated kcp tenant workspace.
type KCPProxy struct {
	kcpTarget        *url.URL
	transport        http.RoundTripper // built from kcp admin config; handles TLS + auth
	verifier         *oidc.IDTokenVerifier
	verifyCtx        context.Context // context with HTTP client for OIDC key fetches
	kedgeClient      *kedgeclient.Client
	bootstrapper     *kcp.Bootstrapper
	staticAuthTokens []string
	hubExternalURL   string
	devMode          bool
	logger           klog.Logger
}

// NewKCPProxy creates a reverse proxy to kcp.
// It validates bearer tokens as OIDC id_tokens before proxying.
// verifier may be nil when only static token auth is used.
func NewKCPProxy(kcpConfig *rest.Config, verifier *oidc.IDTokenVerifier, kedgeClient *kedgeclient.Client, bootstrapper *kcp.Bootstrapper, staticAuthTokens []string, hubExternalURL string, devMode bool) (*KCPProxy, error) {
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
	transport, err := rest.TransportFor(transportConfig)
	if err != nil {
		return nil, fmt.Errorf("building kcp transport: %w", err)
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

	return &KCPProxy{
		kcpTarget:        target,
		transport:        transport,
		verifier:         verifier,
		verifyCtx:        verifyCtx,
		kedgeClient:      kedgeClient,
		bootstrapper:     bootstrapper,
		staticAuthTokens: staticAuthTokens,
		hubExternalURL:   hubExternalURL,
		devMode:          devMode,
		logger:           klog.Background().WithName("kcp-proxy"),
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
			p.serveStaticToken(w, r, token)
			return
		}
	}

	// Try OIDC verification (user tokens from Dex).
	if p.verifier != nil {
		idToken, err := p.verifier.Verify(p.verifyCtx, token)
		if err == nil {
			p.serveOIDC(w, r, token, idToken)
			return
		}
	}

	// If OIDC fails or is not configured, check if this is a kcp ServiceAccount token.
	if saClaims, ok := parseServiceAccountToken(token); ok {
		p.serveServiceAccount(w, r, token, saClaims.ClusterName)
		return
	}

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

	// Determine kcp path based on incoming request format.
	var kcpPath string
	if strings.HasPrefix(r.URL.Path, "/clusters/") {
		// kcp-syntax: /clusters/{logicalClusterName}/api/...
		rest := strings.TrimPrefix(r.URL.Path, "/clusters/")
		slashIdx := strings.Index(rest, "/")
		clusterID := rest
		if slashIdx >= 0 {
			clusterID = rest[:slashIdx]
		}
		// Allow exact match or mount access ({clusterName}:{mountName}).
		if clusterID != user.Spec.DefaultCluster && !strings.HasPrefix(clusterID, user.Spec.DefaultCluster+":") {
			p.logger.Info("cluster access denied", "user", user.Name, "requested", clusterID, "allowed", user.Spec.DefaultCluster)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"cluster access denied","reason":"Forbidden","code":403}`)
			return
		}
		kcpPath = r.URL.Path // already in /clusters/{id}/... format
	}

	target := *p.kcpTarget
	logger := p.logger

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = kcpPath
			req.Host = target.Host

			// Remove user auth — the transport adds kcp admin credentials
			// automatically (client certs, bearer token, etc.).
			req.Header.Del("Authorization")
		},
		Transport: p.transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error(err, "proxy upstream error", "method", r.Method, "path", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"proxy error: %s","reason":"ServiceUnavailable","code":502}`, err.Error())
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

	// Determine kcp path based on incoming request format.
	var kcpPath string
	if strings.HasPrefix(r.URL.Path, "/clusters/") {
		// kcp-syntax: /clusters/{logicalClusterName}/api/...
		rest := strings.TrimPrefix(r.URL.Path, "/clusters/")
		slashIdx := strings.Index(rest, "/")
		clusterID := rest
		if slashIdx >= 0 {
			clusterID = rest[:slashIdx]
		}
		// Allow exact match or mount access ({clusterName}:{mountName}).
		if clusterID != user.Spec.DefaultCluster && !strings.HasPrefix(clusterID, user.Spec.DefaultCluster+":") {
			p.logger.Info("cluster access denied", "user", user.Name, "requested", clusterID, "allowed", user.Spec.DefaultCluster)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"cluster access denied","reason":"Forbidden","code":403}`)
			return
		}
		kcpPath = r.URL.Path // already in /clusters/{id}/... format
	} else {
		// Bare path: construct workspace path from user's default cluster.
		if user.Spec.DefaultCluster != "" {
			kcpPath = "/clusters/" + user.Spec.DefaultCluster + r.URL.Path
		} else {
			p.logger.Error(nil, "user has no default cluster", "user", user.Name)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"user workspace not configured","reason":"Forbidden","code":403}`)
			return
		}
	}

	target := *p.kcpTarget
	logger := p.logger

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = kcpPath
			req.Host = target.Host

			// Remove user auth — the transport adds kcp admin credentials.
			req.Header.Del("Authorization")
		},
		Transport: p.transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error(err, "proxy upstream error (static token)", "method", r.Method, "path", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"proxy error: %s","reason":"ServiceUnavailable","code":502}`, err.Error())
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

// ensureStaticTokenUserOnce is the single-attempt logic for ensureStaticTokenUser.
func (p *KCPProxy) ensureStaticTokenUserOnce(ctx context.Context, token, subHash string) (*tenancyv1alpha1.User, error) {
	labelSelector := fmt.Sprintf("kedge.faros.sh/sub=%s", subHash)
	users, err := p.kedgeClient.Users().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}

	now := metav1.Now()

	if len(users.Items) > 0 {
		user := &users.Items[0]

		// Update status with last login (best-effort, ignore conflicts here).
		user.Status.Active = true
		user.Status.LastLogin = &now
		_, _ = p.kedgeClient.Users().UpdateStatus(ctx, user, metav1.UpdateOptions{})

		// Ensure workspace exists if bootstrapper is available.
		if p.bootstrapper != nil && user.Spec.DefaultCluster == "" {
			clusterName, err := p.bootstrapper.CreateTenantWorkspace(ctx, user.Name)
			if err != nil {
				return nil, fmt.Errorf("creating tenant workspace: %w", err)
			}

			// Re-fetch user to get latest version before update.
			user, err = p.kedgeClient.Users().Get(ctx, user.Name, metav1.GetOptions{})
			if err != nil {
				return nil, fmt.Errorf("re-fetching user: %w", err)
			}

			user.Spec.DefaultCluster = clusterName
			user.APIVersion = "kedge.faros.sh/v1alpha1"
			user.Kind = "User"
			user, err = p.kedgeClient.Users().Update(ctx, user, metav1.UpdateOptions{})
			if err != nil {
				return nil, fmt.Errorf("updating user default cluster: %w", err)
			}
		}
		return user, nil
	}

	// Create new user with a short token prefix for identification.
	tokenPrefix := token
	if len(tokenPrefix) > 8 {
		tokenPrefix = tokenPrefix[:8]
	}

	user := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "static-user-",
			Labels: map[string]string{
				"kedge.faros.sh/sub":       subHash,
				"kedge.faros.sh/auth-type": "static-token",
			},
		},
		Spec: tenancyv1alpha1.UserSpec{
			Email:        fmt.Sprintf("static-%s@kedge.local", tokenPrefix),
			Name:         fmt.Sprintf("Static Token User (%s...)", tokenPrefix),
			RBACIdentity: fmt.Sprintf("kedge:static:%s", subHash[:16]),
		},
	}
	user.APIVersion = "kedge.faros.sh/v1alpha1"
	user.Kind = "User"

	created, err := p.kedgeClient.Users().Create(ctx, user, metav1.CreateOptions{})
	if err != nil {
		// If user already exists (race condition), re-fetch and return.
		if apierrors.IsAlreadyExists(err) {
			users, listErr := p.kedgeClient.Users().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
			if listErr != nil {
				return nil, fmt.Errorf("listing users after conflict: %w", listErr)
			}
			if len(users.Items) > 0 {
				return &users.Items[0], nil
			}
		}
		return nil, fmt.Errorf("creating user: %w", err)
	}

	// Update status (best-effort).
	created.Status.Active = true
	created.Status.LastLogin = &now
	_, _ = p.kedgeClient.Users().UpdateStatus(ctx, created, metav1.UpdateOptions{})

	// Create tenant workspace if bootstrapper is available.
	if p.bootstrapper != nil {
		clusterName, err := p.bootstrapper.CreateTenantWorkspace(ctx, created.Name)
		if err != nil {
			return nil, fmt.Errorf("creating tenant workspace: %w", err)
		}

		// Re-fetch user to get latest version before update.
		created, err = p.kedgeClient.Users().Get(ctx, created.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("re-fetching user: %w", err)
		}

		created.Spec.DefaultCluster = clusterName
		created.APIVersion = "kedge.faros.sh/v1alpha1"
		created.Kind = "User"
		created, err = p.kedgeClient.Users().Update(ctx, created, metav1.UpdateOptions{})
		if err != nil {
			return nil, fmt.Errorf("updating user default cluster: %w", err)
		}
	}

	return created, nil
}

// serveServiceAccount handles kcp ServiceAccount tokens by forwarding the
// request to the workspace identified by the clusterName claim, keeping the
// original SA token so kcp performs native authn/authz.
func (p *KCPProxy) serveServiceAccount(w http.ResponseWriter, r *http.Request, token, clusterName string) {
	target := *p.kcpTarget
	logger := p.logger

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = "/clusters/" + clusterName + req.URL.Path
			req.Host = target.Host

			// Keep the SA token — kcp authenticates it natively.
			req.Header.Set("Authorization", "Bearer "+token)
		},
		Transport: p.transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error(err, "proxy upstream error (SA)", "method", r.Method, "path", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"proxy error: %s","reason":"ServiceUnavailable","code":502}`, err.Error())
		},
	}

	proxy.ServeHTTP(w, r)
}

// saTokenClaims holds the claims we extract from a kcp ServiceAccount JWT.
type saTokenClaims struct {
	Issuer      string `json:"iss"`
	ClusterName string `json:"kubernetes.io/serviceaccount/clusterName"`
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
	if claims.Issuer != "kubernetes/serviceaccount" || claims.ClusterName == "" {
		return saTokenClaims{}, false
	}
	return claims, true
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"Unauthorized","reason":"Unauthorized","code":401}`)
}

// resolveUser looks up the User CRD by OIDC issuer+sub hash and returns the full User object.
func (p *KCPProxy) resolveUser(ctx context.Context, issuer, sub string) (*tenancyv1alpha1.User, error) {
	hash := sha256.Sum256([]byte(issuer + "/" + sub))
	subHash := hex.EncodeToString(hash[:])[:63]

	labelSelector := fmt.Sprintf("kedge.faros.sh/sub=%s", subHash)
	users, err := p.kedgeClient.Users().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	if len(users.Items) == 0 {
		return nil, fmt.Errorf("no user found for sub hash %s", subHash)
	}
	return &users.Items[0], nil
}

// HandleTokenLogin handles static token login requests.
// POST /auth/token-login with Authorization: Bearer <token>
// Returns a LoginResponse with kubeconfig pointing to the user's workspace.
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
		serverURL += "/clusters/" + user.Spec.DefaultCluster
	}

	config.Clusters["kedge"] = &clientcmdapi.Cluster{
		Server:                serverURL,
		InsecureSkipTLSVerify: p.devMode,
	}

	config.AuthInfos["kedge"] = &clientcmdapi.AuthInfo{
		Token: token,
	}

	config.Contexts["kedge"] = &clientcmdapi.Context{
		Cluster:  "kedge",
		AuthInfo: "kedge",
	}

	config.CurrentContext = "kedge"

	return clientcmd.Write(*config)
}
