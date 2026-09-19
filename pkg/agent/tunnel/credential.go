/*
Copyright 2026 The Railgrid Authors.

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

package tunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// An agent's credential is a TTL'd, hub-minted scoped identity, not a
// permanent ServiceAccount token in a kubeconfig on disk.
//
// The agent cannot mint or renew it itself: the hub's identity service
// authenticates PROVIDERS, and an agent is not one. So the edges provider
// stands in for it and exposes the refresh as an ordinary declared data-plane
// verb, agent-token, gated exactly like every other — the agent's current
// token must pass gate 1 (a real GET of its own edge) and gate 2 (an SSAR for
// create on {resource}/agent-token, name-scoped). An agent can therefore
// refresh its own credential and nothing else's, using nothing but the
// credential it already holds.

// CredentialHeader is the upgrade-response header the provider returns the
// enrolment bundle on. It mirrors tunnel.AgentCredentialHeader in the edges
// provider; the two ends are pinned to each other by
// TestAgentCredentialWireFormat.
const CredentialHeader = "X-Railgrid-Agent-Credential"

// refreshFraction is when the agent re-mints: at 80% of the token's lifetime,
// leaving a fifth of the TTL to retry through a hub blip before the credential
// in hand stops working. Same figure as provider-sdk/identityclient, and for
// the same reason.
const refreshFraction = 0.8

// Credential is the enrolment bundle: the token plus everything needed to
// refresh it. Nothing about the route is compiled into the agent — the
// provider that serves it renders RefreshPath, so an agent follows a provider
// that moved without being rebuilt.
type Credential struct {
	Token     string    `json:"token"`
	TokenType string    `json:"tokenType"`
	ExpiresAt time.Time `json:"expiresAt"`

	HubURL     string `json:"hubURL"`
	CACertData []byte `json:"caCertData,omitempty"`

	Provider  string `json:"provider"`
	ClusterID string `json:"clusterID"`
	Resource  string `json:"resource"`
	Name      string `json:"name"`

	RefreshPath        string `json:"refreshPath"`
	SSHCredentialsPath string `json:"sshCredentialsPath,omitempty"`
}

// DecodeCredential parses the base64(JSON) bundle from the upgrade response.
func DecodeCredential(encoded string) (Credential, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return Credential{}, fmt.Errorf("decoding the agent credential: %w", err)
	}
	var credential Credential
	if err := json.Unmarshal(raw, &credential); err != nil {
		return Credential{}, fmt.Errorf("parsing the agent credential: %w", err)
	}
	if strings.TrimSpace(credential.Token) == "" {
		return Credential{}, errors.New("the agent credential carries no token")
	}
	return credential, nil
}

// refreshDue reports when the agent should re-mint.
func (c Credential) refreshDue() time.Time {
	if c.ExpiresAt.IsZero() {
		return time.Time{}
	}
	lifetime := time.Until(c.ExpiresAt)
	if lifetime <= 0 {
		return time.Now()
	}
	return time.Now().Add(time.Duration(float64(lifetime) * refreshFraction))
}

// CredentialStore holds the agent's current credential and re-mints it through
// the provider before it expires.
//
// It starts empty: an enrolling agent has only its bootstrap join token, which
// lives outside this store (the fallback in Token). The first successful
// connect delivers a bundle, Adopt records it, and from then on the join token
// is neither needed nor valid.
//
// Refresh happens on the reconnect path rather than from a timer, for the same
// reason provider-sdk's TokenSource refreshes lazily: an agent that has
// stopped reconnecting has stopped needing a credential, and its identity
// should be allowed to lapse and be collected.
type CredentialStore struct {
	// Fallback is the bootstrap join token, used until a credential arrives.
	Fallback func() string
	// Persist is called with each new credential so the agent can survive a
	// restart. Nil keeps it in memory only.
	Persist func(Credential) error
	// TLSConfig is used for the refresh call. Nil uses the default.
	TLSConfig *tls.Config
	// HTTPClient overrides the transport (tests).
	HTTPClient *http.Client

	mu         sync.Mutex
	credential Credential
	refreshAt  time.Time
	haveIt     bool
}

// Adopt records a credential the provider just issued.
func (s *CredentialStore) Adopt(credential Credential) error {
	s.mu.Lock()
	s.credential, s.refreshAt, s.haveIt = credential, credential.refreshDue(), true
	persist := s.Persist
	s.mu.Unlock()
	if persist != nil {
		return persist(credential)
	}
	return nil
}

// Current returns the credential in hand, and whether there is one.
func (s *CredentialStore) Current() (Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credential, s.haveIt
}

// Token is the bearer for the next connect attempt: the join token until the
// agent has enrolled, the scoped identity afterwards.
func (s *CredentialStore) Token() string {
	s.mu.Lock()
	credential, haveIt := s.credential, s.haveIt
	s.mu.Unlock()
	if haveIt && strings.TrimSpace(credential.Token) != "" {
		return credential.Token
	}
	if s.Fallback != nil {
		return s.Fallback()
	}
	return ""
}

// EnsureFresh re-mints when the credential is past 80% of its life. It is
// called before every connect attempt, so a reconnect loop keeps the
// credential alive on its own.
//
// A failed refresh is not fatal while the current token is still valid: the
// next attempt tries again, and there is a fifth of the TTL of room. Once the
// token has actually expired there is nothing left to authenticate the refresh
// with, and the edge must be re-enrolled with a fresh join token — which is
// the honest consequence of a credential that expires, and is stated in
// docs/edges-agent-credentials.md.
func (s *CredentialStore) EnsureFresh(ctx context.Context) error {
	s.mu.Lock()
	credential, haveIt, refreshAt := s.credential, s.haveIt, s.refreshAt
	s.mu.Unlock()
	if !haveIt || refreshAt.IsZero() || time.Now().Before(refreshAt) {
		return nil
	}
	if credential.RefreshPath == "" {
		return errors.New("the credential carries no refresh route")
	}
	refreshed, err := s.refresh(ctx, credential)
	if err != nil {
		return err
	}
	return s.Adopt(refreshed)
}

// refresh POSTs to the provider's agent-token verb with the credential in
// hand and returns the new bundle.
func (s *CredentialStore) refresh(ctx context.Context, credential Credential) (Credential, error) {
	target := strings.TrimRight(credential.HubURL, "/") + credential.RefreshPath
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return Credential{}, fmt.Errorf("building the refresh request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential.Token)
	request.Header.Set("Accept", "application/json")

	response, err := s.client().Do(request)
	if err != nil {
		return Credential{}, fmt.Errorf("refreshing the agent credential: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Credential{}, fmt.Errorf("reading the refresh response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Credential{}, fmt.Errorf("refreshing the agent credential: HTTP %d: %s",
			response.StatusCode, strings.TrimSpace(string(payload)))
	}
	var refreshed Credential
	if err := json.Unmarshal(payload, &refreshed); err != nil {
		return Credential{}, fmt.Errorf("parsing the refreshed credential: %w", err)
	}
	if strings.TrimSpace(refreshed.Token) == "" {
		return Credential{}, errors.New("the refresh returned no token")
	}
	return refreshed, nil
}

// PostSSHCredentials hands the host's SSH credentials to the provider through
// the gated ssh-credentials verb.
//
// The agent used to write this Secret itself, which is why it held
// get/create/update on Secrets and get/create on Namespaces in the tenant
// workspace — the broadest grant an agent had, on a credential that never
// expired, and precisely the rule the hub's identity policy refuses to mint
// for anyone. Now the agent proves which edge it is (the two gates) and the
// provider does the write; nothing in this body names a destination.
func (s *CredentialStore) PostSSHCredentials(ctx context.Context, body SSHCredentials) error {
	credential, haveIt := s.Current()
	if !haveIt {
		return errors.New("no agent credential yet")
	}
	if credential.SSHCredentialsPath == "" {
		return errors.New("this edge kind has no ssh-credentials route")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding the SSH credentials: %w", err)
	}
	target := strings.TrimRight(credential.HubURL, "/") + credential.SSHCredentialsPath
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("building the SSH credentials request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential.Token)
	request.Header.Set("Content-Type", "application/json")

	response, err := s.client().Do(request)
	if err != nil {
		return fmt.Errorf("posting the SSH credentials: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf("posting the SSH credentials: HTTP %d: %s",
			response.StatusCode, strings.TrimSpace(string(payload)))
	}
	return nil
}

// SSHCredentials is the ssh-credentials request body. It mirrors the
// provider's sshCredentialsRequest.
type SSHCredentials struct {
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`
	PrivateKey []byte `json:"privateKey,omitempty"`
	HostKey    string `json:"hostKey,omitempty"`
}

func (s *CredentialStore) client() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if s.TLSConfig != nil {
		transport.TLSClientConfig = s.TLSConfig
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}
}
