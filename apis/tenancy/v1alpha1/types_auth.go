package v1alpha1

// LoginRequest represents a login request.
type LoginRequest struct {
	RedirectURL string `json:"redirectUrl,omitempty"`
}

// LoginResponse contains login callback data.
type LoginResponse struct {
	Kubeconfig []byte `json:"kubeconfig,omitempty"`
	ExpiresAt  int64  `json:"expiresAt,omitempty"`
	Email      string `json:"email,omitempty"`
	UserID     string `json:"userId,omitempty"`

	// OIDC credentials for the exec credential plugin to cache and refresh.
	IDToken      string `json:"idToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`

	// OIDC provider config so the exec plugin can refresh tokens.
	IssuerURL    string `json:"issuerUrl,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

// AuthCode carries the CLI callback port and session through the OAuth2 state parameter.
type AuthCode struct {
	RedirectURL string `json:"redirectURL"` // CLI localhost callback URL
	SessionID   string `json:"sid"`
}
