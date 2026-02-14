package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	oidc "github.com/coreos/go-oidc"
	"golang.org/x/oauth2"
	"k8s.io/klog/v2"
)

// Handler provides OAuth2/OIDC authentication endpoints.
type Handler struct {
	oidcProvider *oidc.Provider
	oauth2Config *oauth2.Config
	oidcConfig   *OIDCConfig
	logger       klog.Logger
}

// NewHandler creates a new OIDC auth handler.
func NewHandler(ctx context.Context, config *OIDCConfig) (*Handler, error) {
	if config.IssuerURL == "" {
		return nil, fmt.Errorf("OIDC issuer URL is required")
	}

	provider, err := oidc.NewProvider(ctx, config.IssuerURL)
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
		oidcProvider: provider,
		oauth2Config: oauth2Config,
		oidcConfig:   config,
		logger:       klog.Background().WithName("auth-handler"),
	}, nil
}

// HandleAuthorize redirects to the OIDC provider for authentication.
func (h *Handler) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	state := generateStaticSessionID(r.RemoteAddr)
	url := h.oauth2Config.AuthCodeURL(state)
	http.Redirect(w, r, url, http.StatusFound)
}

// HandleCallback handles the OIDC callback after authentication.
func (h *Handler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code parameter", http.StatusBadRequest)
		return
	}

	token, err := h.oauth2Config.Exchange(ctx, code)
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

	h.logger.Info("User authenticated", "email", claims.Email, "name", claims.Name)

	// TODO: Create/update User CRD
	// TODO: Create/update workspace for user
	// TODO: Generate kubeconfig

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"email": claims.Email,
		"name":  claims.Name,
		"token": token.AccessToken,
	})
}

// HandleRefresh handles token refresh requests.
func (h *Handler) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement token refresh
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// RegisterRoutes registers auth routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/authorize", h.HandleAuthorize)
	mux.HandleFunc("/callback", h.HandleCallback)
	mux.HandleFunc("/refresh", h.HandleRefresh)
}

// generateStaticSessionID creates a deterministic session ID from a user identifier.
func generateStaticSessionID(userID string) string {
	hash := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(hash[:16])
}
