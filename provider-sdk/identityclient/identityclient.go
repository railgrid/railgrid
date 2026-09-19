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

// Package identityclient asks the hub for a scoped identity, instead of
// minting one.
//
// This replaces tenantaccess.EnsureIdentity for per-object identities. The
// difference is not cosmetic:
//
//   - EnsureIdentity writes a ServiceAccount, a ClusterRole, a binding and a
//     legacy token Secret into the tenant workspace with the provider's own
//     claimed credentials. Its rules are create-if-absent (so a revoked grant
//     never shrinks), its tokens never expire, and nothing collects any of it.
//   - Ensure here asks the hub, which checks the rules against a policy,
//     records what it minted, hands back a TTL'd TokenRequest token, and
//     garbage-collects the identity when the owning object is deleted.
//
// A provider using this package needs NO serviceaccounts, clusterroles or
// clusterrolebindings permission claims at all.
package identityclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/railgrid/provider-sdk/hubclient"
)

const (
	// PathIdentities is the hub collection route.
	PathIdentities = "/api/identities"

	defaultTimeout = 20 * time.Second
	maxResponse    = 256 << 10
)

// Owner is the tenant object an identity exists for. The hub verifies it
// really exists, with this UID, before minting anything, and collects the
// identity when it stops existing — so a caller should always send the UID it
// read off the object it is reconciling.
type Owner struct {
	Provider  string `json:"provider"`
	Kind      string `json:"kind"`
	Group     string `json:"group,omitempty"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
	ClusterID string `json:"clusterID,omitempty"`
}

// Request is one create-or-refresh.
type Request struct {
	Owner     Owner
	ClusterID string
	// Rules must satisfy the hub's policy: any verb on the caller's own
	// exported group, get on NAMED resources of another provider's group, or
	// create on a declared {resource}/{verb} subresource of one, name-scoped.
	// Anything else comes back as a *Error naming the offending rule.
	Rules []rbacv1.PolicyRule
	// TTL is the requested token lifetime. Zero means the hub's default (one
	// hour); the hub caps it at 24 hours whatever is asked for.
	TTL time.Duration
}

// Token is a minted capability. It is never written to disk by this package.
type Token struct {
	Token          string    `json:"token"`
	TokenType      string    `json:"tokenType"`
	ExpiresAt      time.Time `json:"expiresAt"`
	ServiceAccount string    `json:"serviceAccount"`
	Name           string    `json:"name"`
}

// Identity is one record as the hub lists it.
type Identity struct {
	Name           string              `json:"name"`
	Owner          Owner               `json:"owner"`
	ClusterID      string              `json:"clusterID"`
	ServiceAccount string              `json:"serviceAccount"`
	Rules          []rbacv1.PolicyRule `json:"rules,omitempty"`
	TTLSeconds     int64               `json:"ttlSeconds"`
	Phase          string              `json:"phase,omitempty"`
	ExpiresAt      *time.Time          `json:"expiresAt,omitempty"`
}

// Error is a hub refusal. Code is stable and worth branching on: a policy
// refusal means the provider is asking for something it may not have and
// retrying will not help, while an attestation or availability failure is
// transient.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("hub identity service: %s (%s, HTTP %d)", e.Message, e.Code, e.Status)
}

// Permanent reports whether retrying the identical request could ever
// succeed. A policy refusal cannot; an unreachable hub can.
func (e *Error) Permanent() bool {
	return e.Status == http.StatusForbidden || e.Status == http.StatusBadRequest
}

// Client talks to the hub identity service as the provider itself.
type Client struct {
	base     string
	provider string
	token    string
	http     *http.Client
}

// Options configures a Client.
type Options struct {
	// HubURL is the hub base URL. Empty reads RAILGRID_HUB_URL.
	HubURL string
	// Provider is the provider's own name, as its CatalogEntry registers it.
	Provider string
	// Token is the provider's own service-account bearer. Empty resolves it
	// with hubclient.ResolveHubToken, the same credential the heartbeat uses —
	// which is not a coincidence: the hub authenticates this endpoint with the
	// same TokenReview the heartbeat gets.
	Token string
	// Insecure skips TLS verification. Empty reads RAILGRID_HUB_INSECURE.
	Insecure *bool
	// HTTPClient overrides the transport (tests).
	HTTPClient *http.Client
}

// New constructs a Client. It fails rather than returning a client that would
// silently do nothing: a provider whose identities cannot be minted should
// report that at startup, not on its first reconcile.
func New(opts Options) (*Client, error) {
	provider := strings.TrimSpace(opts.Provider)
	if provider == "" {
		return nil, errors.New("provider name is required")
	}
	base := strings.TrimRight(strings.TrimSpace(opts.HubURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv(hubclient.EnvHubURL)), "/")
	}
	if base == "" {
		return nil, fmt.Errorf("hub base URL is empty (set %s)", hubclient.EnvHubURL)
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("hub base URL is invalid: %w", err)
	}
	token := strings.TrimSpace(opts.Token)
	if token == "" {
		resolved, err := hubclient.ResolveHubToken()
		if err != nil {
			return nil, fmt.Errorf("resolving the provider's own token: %w", err)
		}
		token = strings.TrimSpace(resolved)
	}
	if token == "" {
		return nil, fmt.Errorf("no provider token (set %s or %s)", hubclient.EnvHubToken, hubclient.EnvProviderKubeconfig)
	}
	client := opts.HTTPClient
	if client == nil {
		insecure := os.Getenv(hubclient.EnvHubInsecure) == "true"
		if opts.Insecure != nil {
			insecure = *opts.Insecure
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if insecure {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for in-cluster hub certs
		}
		client = &http.Client{Timeout: defaultTimeout, Transport: transport}
	}
	return &Client{base: base, provider: provider, token: token, http: client}, nil
}

// Ensure creates or refreshes the identity for req.Owner and returns a fresh
// token. It is idempotent on the owner tuple: the ServiceAccount is stable
// across calls and only the token is new.
func (c *Client) Ensure(ctx context.Context, req Request) (*Token, error) {
	if c == nil {
		return nil, errors.New("identity client is nil")
	}
	owner := req.Owner
	if owner.Provider == "" {
		owner.Provider = c.provider
	}
	clusterID := req.ClusterID
	if clusterID == "" {
		clusterID = owner.ClusterID
	}
	body := map[string]any{"owner": owner, "clusterID": clusterID, "rules": req.Rules}
	if req.TTL > 0 {
		body["ttlSeconds"] = int64(req.TTL / time.Second)
	}
	var token Token
	if err := c.do(ctx, http.MethodPost, PathIdentities, nil, body, &token); err != nil {
		return nil, err
	}
	if strings.TrimSpace(token.Token) == "" || token.ExpiresAt.IsZero() {
		return nil, &Error{Status: http.StatusBadGateway, Code: "invalid_response", Message: "the hub returned an incomplete token"}
	}
	return &token, nil
}

// Release deletes every identity minted for owner. Call it from the delete
// path of the owner's reconciler: the hub's sweep would collect it anyway,
// but only after up to one token TTL, and revocation should not wait.
func (c *Client) Release(ctx context.Context, clusterID string, owner Owner) error {
	if c == nil {
		return errors.New("identity client is nil")
	}
	identities, err := c.List(ctx, clusterID, &owner)
	if err != nil {
		return err
	}
	for _, identity := range identities {
		query := url.Values{"provider": []string{c.provider}}
		path := PathIdentities + "/" + url.PathEscape(identity.Name)
		if err := c.do(ctx, http.MethodDelete, path, query, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// List returns the provider's identities, narrowed to one owner when owner is
// non-nil.
func (c *Client) List(ctx context.Context, clusterID string, owner *Owner) ([]Identity, error) {
	if c == nil {
		return nil, errors.New("identity client is nil")
	}
	query := url.Values{"provider": []string{c.provider}}
	if clusterID != "" {
		query.Set("clusterID", clusterID)
	}
	if owner != nil {
		selector := owner.Kind + "/" + owner.Name
		if owner.UID != "" {
			selector += "/" + owner.UID
		}
		query.Set("owner", selector)
		if owner.Group != "" {
			query.Set("group", owner.Group)
		}
	}
	var response struct {
		Items []Identity `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, PathIdentities, query, nil, &response); err != nil {
		return nil, err
	}
	return response.Items, nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding identity request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("building identity request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("calling the hub identity service: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(payload) > maxResponse {
		return &Error{Status: response.StatusCode, Code: "unreadable_response", Message: "the hub response could not be read"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		hubErr := &Error{Status: response.StatusCode, Code: "unknown", Message: strings.TrimSpace(string(payload))}
		var decoded Error
		if json.Unmarshal(payload, &decoded) == nil && decoded.Code != "" {
			hubErr.Code, hubErr.Message = decoded.Code, decoded.Message
		}
		return hubErr
	}
	if out == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return &Error{Status: response.StatusCode, Code: "invalid_response", Message: "the hub response could not be decoded"}
	}
	return nil
}

// refreshFraction is when a TokenSource re-mints: at 80% of the token's
// lifetime, so a refresh has a fifth of the TTL to retry through a hub blip
// before the credential it holds stops working.
const refreshFraction = 0.8

// TokenSource keeps one owner's token fresh. It is what a long-running agent
// or reconciler holds instead of a standing credential: the token in hand is
// always valid, always TTL'd, and always re-derived from a hub-checked record.
//
// It refreshes lazily, on Token(), rather than from a goroutine: a caller that
// stops asking stops refreshing, which is exactly when the identity should be
// allowed to lapse and be collected.
type TokenSource struct {
	client  *Client
	request Request
	now     func() time.Time

	mu      sync.Mutex
	token   *Token
	refresh time.Time
}

// NewTokenSource builds a refreshing source for one identity request.
func NewTokenSource(client *Client, request Request) *TokenSource {
	return &TokenSource{client: client, request: request, now: time.Now}
}

// Token returns a valid token, minting or refreshing as needed.
func (s *TokenSource) Token(ctx context.Context) (*Token, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("token source is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != nil && s.now().Before(s.refresh) {
		return s.token, nil
	}
	token, err := s.client.Ensure(ctx, s.request)
	if err != nil {
		// A live token that has not actually expired is better than failing a
		// reconcile on a transient hub error: keep serving it until its own
		// expiry, and try again on the next call.
		if s.token != nil && s.now().Before(s.token.ExpiresAt) {
			var hubErr *Error
			if !errors.As(err, &hubErr) || !hubErr.Permanent() {
				return s.token, nil
			}
		}
		s.token, s.refresh = nil, time.Time{}
		return nil, err
	}
	s.token = token
	lifetime := token.ExpiresAt.Sub(s.now())
	if lifetime <= 0 {
		s.refresh = s.now()
	} else {
		s.refresh = s.now().Add(time.Duration(float64(lifetime) * refreshFraction))
	}
	return token, nil
}

// Invalidate drops the cached token so the next Token() re-mints. Call it when
// the hub rejects the token in hand.
func (s *TokenSource) Invalidate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token, s.refresh = nil, time.Time{}
}
