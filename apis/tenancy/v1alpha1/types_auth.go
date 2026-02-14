package v1alpha1

// LoginRequest represents a login request.
type LoginRequest struct {
	RedirectURL string `json:"redirectUrl,omitempty"`
}

// LoginResponse contains login callback data.
type LoginResponse struct {
	Token      string `json:"token,omitempty"`
	Kubeconfig []byte `json:"kubeconfig,omitempty"`
	ExpiresAt  int64  `json:"expiresAt,omitempty"`
}

// AuthCode represents an OAuth2 authorization code.
type AuthCode struct {
	Code        string `json:"code"`
	State       string `json:"state"`
	RedirectURL string `json:"redirectUrl"`
}
