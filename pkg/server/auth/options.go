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

package auth

// OIDCConfig holds OIDC provider configuration.
// ClientSecret is intentionally absent: kedge uses PKCE (public client flow)
// so no client secret is required on the hub side.
type OIDCConfig struct {
	IssuerURL   string
	ClientID    string
	RedirectURL string
	Scopes      []string
}

// DefaultOIDCConfig returns default OIDC configuration.
func DefaultOIDCConfig() *OIDCConfig {
	return &OIDCConfig{
		Scopes: []string{"openid", "profile", "email", "offline_access"},
	}
}
