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

package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/agent/tunnel"
	"github.com/railgrid/railgrid/pkg/apiurl"
)

// useIssuedHubCredentials keeps long-lived reporters and reconcilers on the
// rotating issued token. The native client never uses the store's bootstrap
// fallback, and its hub, cluster address and TLS configuration remain fixed.
func useIssuedHubCredentials(cfg *rest.Config, store *tunnel.CredentialStore) error {
	if store == nil {
		return fmt.Errorf("issued hub credential store is unavailable")
	}
	endpoint, err := url.Parse(cfg.Host)
	if err != nil || endpoint.Host == "" {
		return fmt.Errorf("issued hub address is invalid")
	}
	source := &issuedHubTokenSource{store: store, endpoint: endpoint}
	// client-go's static bearer wrapper must not retain the first token.
	cfg.BearerToken, cfg.BearerTokenFile = "", ""
	previous := cfg.WrapTransport
	cfg.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		if previous != nil {
			base = previous(base)
		}
		return &issuedHubRoundTripper{base: base, source: source}
	}
	return nil
}

type issuedHubTokenSource struct {
	store    *tunnel.CredentialStore
	endpoint *url.URL
}

func (s *issuedHubTokenSource) token(ctx context.Context) (string, error) {
	credential, ok := s.store.Current()
	if !ok || apiurl.HubServerURL(credential.HubURL, credential.ClusterID) != s.endpoint.String() {
		return "", fmt.Errorf("issued hub credential address changed; restart the agent")
	}
	// An active tunnel may not reconnect for hours. Native requests still need
	// fresh credentials, so their lifetime cannot depend on reconnect cadence.
	if err := s.store.EnsureFresh(ctx); err != nil {
		// A refresh failure need not break calls while the current token remains
		// valid; the API server remains the authority and rejects expired tokens.
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	credential, ok = s.store.Current()
	if !ok || strings.TrimSpace(credential.Token) == "" || apiurl.HubServerURL(credential.HubURL, credential.ClusterID) != s.endpoint.String() {
		return "", fmt.Errorf("issued hub credential is unavailable or changed address")
	}
	return credential.Token, nil
}

type issuedHubRoundTripper struct {
	base   http.RoundTripper
	source *issuedHubTokenSource
}

func (t *issuedHubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	endpoint := t.source.endpoint
	if req.URL.Scheme != endpoint.Scheme || req.URL.Host != endpoint.Host || (req.URL.Path != endpoint.Path && !strings.HasPrefix(req.URL.Path, strings.TrimRight(endpoint.Path, "/")+"/")) {
		return nil, fmt.Errorf("refusing to send an issued hub credential outside its cluster")
	}
	token, err := t.source.token(req.Context())
	if err != nil {
		return nil, err
	}
	out := req.Clone(req.Context())
	out.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(out)
}
