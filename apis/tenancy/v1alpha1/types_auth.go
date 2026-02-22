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
	// ClientSecret is intentionally omitted: kedge uses PKCE (public client)
	// so no client secret is ever issued or sent to CLI users.
	IssuerURL string `json:"issuerUrl,omitempty"`
	ClientID  string `json:"clientId,omitempty"`
}

// AuthCode carries the CLI callback port, session, and PKCE code verifier
// through the OAuth2 state parameter.
type AuthCode struct {
	RedirectURL  string `json:"redirectURL"` // CLI localhost callback URL
	SessionID    string `json:"sid"`
	CodeVerifier string `json:"cv"` // PKCE code verifier (RFC 7636)
}
