package auth

// OIDCConfig holds OIDC provider configuration.
type OIDCConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
}

// DefaultOIDCConfig returns default OIDC configuration.
func DefaultOIDCConfig() *OIDCConfig {
	return &OIDCConfig{
		Scopes: []string{"openid", "profile", "email", "offline_access"},
	}
}
