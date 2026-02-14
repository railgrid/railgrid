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
}

// AuthCode carries the CLI callback port and session through the OAuth2 state parameter.
type AuthCode struct {
	RedirectURL string `json:"redirectURL"` // CLI localhost callback URL
	SessionID   string `json:"sid"`
}
