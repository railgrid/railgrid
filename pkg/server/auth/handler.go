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
	"net/url"
	"os"
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

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/apiurl"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
)

// defaultRateLimit is the default number of requests allowed per minute per IP.
const defaultRateLimit = 20

// defaultBurstDuration is the default time window for rate limiting.
const defaultBurstDuration = time.Minute

// Handler provides OAuth2/OIDC authentication endpoints.
type Handler struct {
	oidcProvider   *oidc.Provider
	oauth2Config   *oauth2.Config
	oidcConfig     *OIDCConfig
	kedgeClient    *kedgeclient.Client
	bootstrapper   *kcp.Bootstrapper
	hubExternalURL string
	devMode        bool
	logger         klog.Logger
	// rateLimiter protects auth endpoints against brute force attacks
	rateLimiter *rateLimiter
}

// NewHandler creates a new OIDC auth handler.
func NewHandler(ctx context.Context, config *OIDCConfig, kedgeClient *kedgeclient.Client, bootstrapper *kcp.Bootstrapper, hubExternalURL string, devMode bool) (*Handler, error) {
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

	// No ClientSecret: kedge uses PKCE (public client). Dex must be configured
	// with public: true for this client ID.
	oauth2Config := &oauth2.Config{
		ClientID:    config.ClientID,
		RedirectURL: config.RedirectURL,
		Endpoint:    provider.Endpoint(),
		Scopes:      config.Scopes,
	}

	handler := &Handler{
		oidcProvider:   provider,
		oauth2Config:   oauth2Config,
		oidcConfig:     config,
		kedgeClient:    kedgeClient,
		bootstrapper:   bootstrapper,
		hubExternalURL: hubExternalURL,
		devMode:        devMode,
		logger:         klog.Background().WithName("auth-handler"),
		// Initialize rate limiter with sane defaults for auth endpoints
		rateLimiter: newRateLimiter(defaultRateLimit, defaultBurstDuration, klog.Background().WithName("auth-rate-limit")),
	}

	return handler, nil
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
	authURL := h.oauth2Config.AuthCodeURL(state, oauth2.S256ChallengeOption(codeVerifier))
	http.Redirect(w, r, authURL, http.StatusFound)
}

// validateRedirectURI checks that the redirect URI shares the same origin as
// the hub external URL or is a localhost address (for development).
// In production mode (devMode=false), localhost redirects are rejected unless
// explicitly allowed via KEDGE_ALLOW_LOCALHOST_REDIRECTS environment variable.
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
		// In production mode, require explicit opt-in for localhost redirects
		if !h.devMode && os.Getenv("KEDGE_ALLOW_LOCALHOST_REDIRECTS") != "true" {
			h.logger.Info("blocked localhost redirect_uri in production mode",
				"redirectURI", redirectURI,
				"hint", "set KEDGE_ALLOW_LOCALHOST_REDIRECTS=true to allow (not recommended for production)")
			return fmt.Errorf("localhost redirects are not allowed in production mode")
		}
		h.logger.V(4).Info("allowing localhost redirect_uri", "host", host, "devMode", h.devMode)
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

	// Create or update User CRD.
	userID, err := h.seedUser(ctx, claims.Email, claims.Name, claims.Sub, h.oidcConfig.IssuerURL)
	if err != nil {
		h.logger.Error(err, "failed to seed user")
		http.Error(w, "failed to create user", http.StatusInternalServerError)
		return
	}

	// Create tenant workspace for the user in kcp and get the logical cluster name.
	var clusterName string
	if h.bootstrapper != nil {
		var err error
		clusterName, err = h.bootstrapper.CreateTenantWorkspace(ctx, userID, fmt.Sprintf("kedge:%s", claims.Sub))
		if err != nil {
			h.logger.Error(err, "failed to create tenant workspace", "userID", userID)
			http.Error(w, "failed to create tenant workspace", http.StatusInternalServerError)
			return
		}
		h.setDefaultCluster(ctx, userID, clusterName)
	}

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
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// HandleRefresh handles token refresh requests.
func (h *Handler) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// Verifier returns the OIDC token verifier for use by other components (e.g., API proxy).
func (h *Handler) Verifier() *oidc.IDTokenVerifier {
	return h.oidcProvider.Verifier(&oidc.Config{ClientID: h.oidcConfig.ClientID})
}

// RegisterRoutes registers auth routes on the given gorilla/mux router.
// Auth endpoints are protected by per-IP rate limiting to prevent brute force attacks.
func (h *Handler) RegisterRoutes(router *mux.Router) {
	router.HandleFunc(apiurl.PathAuthAuthorize, h.rateLimiter.middleware(h.HandleAuthorize)).Methods("GET")
	router.HandleFunc(apiurl.PathAuthCallback, h.rateLimiter.middleware(h.HandleCallback)).Methods("GET")
	router.HandleFunc(apiurl.PathAuthRefresh, h.rateLimiter.middleware(h.HandleRefresh)).Methods("POST")
}

// seedUser creates or updates a User CRD based on OIDC claims.
func (h *Handler) seedUser(ctx context.Context, email, name, sub, issuer string) (string, error) {
	// Hash issuer+sub for a label-safe lookup key.
	hash := sha256.Sum256([]byte(issuer + "/" + sub))
	subHash := hex.EncodeToString(hash[:])[:63]

	labelSelector := fmt.Sprintf("kedge.faros.sh/sub=%s", subHash)
	users, err := h.kedgeClient.Users().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("listing users: %w", err)
	}

	now := metav1.Now()

	if len(users.Items) > 0 {
		user := &users.Items[0]
		// Update status with last login.
		user.Status.Active = true
		user.Status.LastLogin = &now
		updated, err := h.kedgeClient.Users().UpdateStatus(ctx, user, metav1.UpdateOptions{})
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
				"kedge.faros.sh/sub": subHash,
			},
		},
		Spec: tenancyv1alpha1.UserSpec{
			Email:        email,
			Name:         name,
			RBACIdentity: fmt.Sprintf("kedge:%s", sub),
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
	user.APIVersion = "kedge.faros.sh/v1alpha1"
	user.Kind = "User"

	created, err := h.kedgeClient.Users().Create(ctx, user, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating user: %w", err)
	}

	// Update status.
	created.Status.Active = true
	created.Status.LastLogin = &now
	if _, err := h.kedgeClient.Users().UpdateStatus(ctx, created, metav1.UpdateOptions{}); err != nil {
		h.logger.Error(err, "failed to update new user status", "user", created.Name)
	}

	return created.Name, nil
}

// setDefaultCluster updates the user's spec.defaultCluster if it differs.
func (h *Handler) setDefaultCluster(ctx context.Context, userID, clusterName string) {
	user, err := h.kedgeClient.Users().Get(ctx, userID, metav1.GetOptions{})
	if err != nil {
		h.logger.Error(err, "failed to get user for default cluster update", "userID", userID)
		return
	}
	if user.Spec.DefaultCluster == clusterName {
		return
	}
	user.Spec.DefaultCluster = clusterName
	user.APIVersion = "kedge.faros.sh/v1alpha1"
	user.Kind = "User"
	if _, err := h.kedgeClient.Users().Update(ctx, user, metav1.UpdateOptions{}); err != nil {
		h.logger.Error(err, "failed to update user default cluster", "userID", userID, "cluster", clusterName)
	}
}

// generateKubeconfig builds a kubeconfig pointing to the hub using an exec
// credential plugin (kedge get-token) for automatic OIDC token refresh.
// When clusterName is set, the server URL includes /clusters/{clusterName}
// for kcp-syntax compatibility.
func (h *Handler) generateKubeconfig(userID, clusterName, email string) ([]byte, error) {
	config := clientcmdapi.NewConfig()

	serverURL := h.hubExternalURL
	if clusterName != "" {
		serverURL = apiurl.HubServerURL(h.hubExternalURL, clusterName)
	}

	config.Clusters["kedge"] = &clientcmdapi.Cluster{
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
			Command:    "kedge",
			Args:       execArgs,
		},
	}

	config.Contexts["kedge"] = &clientcmdapi.Context{
		Cluster:  "kedge",
		AuthInfo: userName,
	}

	config.CurrentContext = "kedge"

	return clientcmd.Write(*config)
}
