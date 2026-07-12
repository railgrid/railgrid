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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Connection types. Each turns a tool family and/or a messaging channel on for
// the agents that reference it.
const (
	ConnectionTypeGitHub    = "github"
	ConnectionTypeMCP       = "mcp"
	ConnectionTypeWebSearch = "websearch"
	ConnectionTypeHTTP      = "http"
	ConnectionTypeTelegram  = "telegram"
	ConnectionTypeSlack     = "slack"
	ConnectionTypeSMTP      = "smtp"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=connections,singular=connection,scope=Cluster,shortName=conn
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Connection is a named credential to an external system. The secret material
// lives in a tenant-workspace Secret (kedge-agents-conn-<name>); this resource
// carries only the type and non-secret configuration.
type Connection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConnectionSpec   `json:"spec,omitempty"`
	Status ConnectionStatus `json:"status,omitempty"`
}

// ConnectionSpec is the user-authored connection configuration.
type ConnectionSpec struct {
	// Type selects the integration: github, mcp, websearch, http, telegram,
	// slack, or smtp.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=github;mcp;websearch;http;telegram;slack;smtp
	Type string `json:"type"`

	// DisplayName is a human-readable label for the connection.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName,omitempty"`

	// Auth selects how credentials are obtained: "secret" (default) reads a
	// static token from the referenced Secret; "oauth" runs the provider's
	// authorize/callback flow and refreshes the token automatically.
	// +optional
	// +kubebuilder:validation:Enum=secret;oauth
	// +kubebuilder:default=secret
	Auth string `json:"auth,omitempty"`

	// OAuth configures the flow when Auth is "oauth". Ignored otherwise.
	// +optional
	OAuth *ConnectionOAuth `json:"oauth,omitempty"`

	// SecretRef names the tenant-workspace Secret holding this connection's
	// credentials. Defaults to kedge-agents-conn-<connection-name> when empty.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	SecretRef string `json:"secretRef,omitempty"`

	// BaseURL is the endpoint for http, mcp, and self-hosted github/slack
	// connections. Ignored by types that have a fixed endpoint.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	BaseURL string `json:"baseURL,omitempty"`

	// AllowedHosts restricts which hosts the web/http tools may reach when this
	// connection is in scope. Empty means the provider default policy applies.
	// +optional
	AllowedHosts []string `json:"allowedHosts,omitempty"`

	// Channel identifies the destination for messaging connections: a Telegram
	// chat ID, a Slack channel ID, or an email address for smtp.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Channel string `json:"channel,omitempty"`

	// Config carries additional non-secret, type-specific settings.
	// +optional
	Config map[string]string `json:"config,omitempty"`
}

// ConnectionOAuth configures the OAuth authorize/callback flow for a
// connection whose Auth is "oauth". The client ID/secret live in the
// connection Secret; only non-secret parameters appear here.
type ConnectionOAuth struct {
	// Provider names the OAuth provider preset: "github", "google", "slack".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=github;google;slack
	Provider string `json:"provider"`

	// Scopes requested during authorization.
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// AuthorizeURL and TokenURL override the provider preset endpoints (for
	// self-hosted GitHub Enterprise or Slack, or a custom provider).
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	AuthorizeURL string `json:"authorizeURL,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	TokenURL string `json:"tokenURL,omitempty"`
}

// ConnectionStatus is the observed connection state.
type ConnectionStatus struct {
	// Phase is Ready when the referenced Secret exists and validates, or Error.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Message explains a non-Ready phase.
	// +optional
	Message string `json:"message,omitempty"`

	// WebhookPath is the hub-relative inbound webhook path for messaging
	// connections that receive events (telegram, slack). Empty for outbound-only
	// or non-messaging types.
	// +optional
	WebhookPath string `json:"webhookPath,omitempty"`

	// OAuthConnected reports whether an oauth-auth connection has a valid,
	// refreshable token. Always false for secret-auth connections.
	// +optional
	OAuthConnected bool `json:"oauthConnected,omitempty"`

	// TokenExpiresAt is when the current OAuth access token expires.
	// +optional
	TokenExpiresAt *metav1.Time `json:"tokenExpiresAt,omitempty"`

	// UpdatedAt reflects the latest status observation.
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ConnectionList contains a list of Connections.
type ConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Connection `json:"items"`
}
