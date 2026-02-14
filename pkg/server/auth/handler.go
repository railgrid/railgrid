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

	oidc "github.com/coreos/go-oidc"
	"github.com/gorilla/mux"
	"golang.org/x/oauth2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
)

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

	oauth2Config := &oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		RedirectURL:  config.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       config.Scopes,
	}

	return &Handler{
		oidcProvider:   provider,
		oauth2Config:   oauth2Config,
		oidcConfig:     config,
		kedgeClient:    kedgeClient,
		bootstrapper:   bootstrapper,
		hubExternalURL: hubExternalURL,
		devMode:        devMode,
		logger:         klog.Background().WithName("auth-handler"),
	}, nil
}

// HandleAuthorize redirects to the OIDC provider for authentication.
// GET /auth/authorize?p=<port>&s=<sessionID>
func (h *Handler) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	port := r.URL.Query().Get("p")
	sessionID := r.URL.Query().Get("s")

	if port == "" || sessionID == "" {
		http.Error(w, "missing p (port) or s (session) parameter", http.StatusBadRequest)
		return
	}

	authCode := tenancyv1alpha1.AuthCode{
		RedirectURL: fmt.Sprintf("http://127.0.0.1:%s/callback", port),
		SessionID:   sessionID,
	}

	stateJSON, err := json.Marshal(authCode)
	if err != nil {
		http.Error(w, "failed to encode state", http.StatusInternalServerError)
		return
	}
	state := base64.URLEncoding.EncodeToString(stateJSON)

	url := h.oauth2Config.AuthCodeURL(state)
	http.Redirect(w, r, url, http.StatusFound)
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

	// Exchange code for tokens.
	exchangeCtx := ctx
	if h.devMode {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode only
		}
		httpClient := &http.Client{Transport: tr}
		exchangeCtx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	}

	token, err := h.oauth2Config.Exchange(exchangeCtx, code)
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

	// Create tenant workspace for the user in KCP.
	if h.bootstrapper != nil {
		if err := h.bootstrapper.CreateTenantWorkspace(ctx, userID); err != nil {
			h.logger.Error(err, "failed to create tenant workspace", "userID", userID)
			http.Error(w, "failed to create tenant workspace", http.StatusInternalServerError)
			return
		}
	}

	// Generate kubeconfig using exec credential plugin for automatic token refresh.
	kubeconfigBytes, err := h.generateKubeconfig(userID, claims.Email)
	if err != nil {
		h.logger.Error(err, "failed to generate kubeconfig")
		http.Error(w, "failed to generate kubeconfig", http.StatusInternalServerError)
		return
	}

	// Build response with OIDC credentials so the CLI can cache and refresh tokens.
	resp := tenancyv1alpha1.LoginResponse{
		Kubeconfig:   kubeconfigBytes,
		ExpiresAt:    token.Expiry.Unix(),
		Email:        claims.Email,
		UserID:       userID,
		IDToken:      rawIDToken,
		RefreshToken: token.RefreshToken,
		IssuerURL:    h.oidcConfig.IssuerURL,
		ClientID:     h.oidcConfig.ClientID,
		ClientSecret: h.oidcConfig.ClientSecret,
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
func (h *Handler) RegisterRoutes(router *mux.Router) {
	router.HandleFunc("/auth/authorize", h.HandleAuthorize).Methods("GET")
	router.HandleFunc("/auth/callback", h.HandleCallback).Methods("GET")
	router.HandleFunc("/auth/refresh", h.HandleRefresh).Methods("POST")
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

// generateKubeconfig builds a kubeconfig pointing to the hub using an exec
// credential plugin (kedge get-token) for automatic OIDC token refresh.
func (h *Handler) generateKubeconfig(userID, email string) ([]byte, error) {
	config := clientcmdapi.NewConfig()

	config.Clusters["kedge"] = &clientcmdapi.Cluster{
		Server:                h.hubExternalURL,
		InsecureSkipTLSVerify: h.devMode,
	}

	userName := userID
	execArgs := []string{
		"get-token",
		"--oidc-issuer-url=" + h.oidcConfig.IssuerURL,
		"--oidc-client-id=" + h.oidcConfig.ClientID,
		"--oidc-client-secret=" + h.oidcConfig.ClientSecret,
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
