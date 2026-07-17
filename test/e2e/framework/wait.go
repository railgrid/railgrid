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

package framework

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	cliauth "github.com/faroshq/faros-kedge/pkg/cli/auth"
)

// ConditionFunc is a function that returns (done bool, err error).
// A nil error with done=false means retry; a non-nil error stops polling.
type ConditionFunc func(ctx context.Context) (bool, error)

// Poll calls condition repeatedly with the given interval until it returns
// (true, nil) or the context is done. Returns an error if the context expires
// or condition returns a non-nil error.
func Poll(ctx context.Context, interval, timeout time.Duration, condition ConditionFunc) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		done, err := condition(ctx)
		if err != nil {
			return fmt.Errorf("poll condition error: %w", err)
		}
		if done {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for condition", timeout)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// insecureHTTPClient skips TLS verification — used only in tests against
// self-signed dev certificates.
var insecureHTTPClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test only
	},
}

// WaitForTenantAPI logs in with a static token and polls the hub's
// token-login endpoint until the tenant/users APIBinding has finished
// bootstrapping. Until then the hub can 500 ("failed to create user") on the
// first tenant operations, so suites gate startup on this.
//
// This replaces the pre-decouple WaitForEdgeAPI gate: edges are now an
// optional out-of-process provider (group edges.kedge.faros.sh) that these
// suites do not bootstrap, so "edge list works" is no longer a valid
// readiness signal. Edge connectivity has its own dedicated suite.
func WaitForTenantAPI(ctx context.Context, client *KedgeClient, hubURL, token string) error {
	// Login (retryable) so the kedge context is written to the default
	// kubeconfig — some non-edge tests drive kubectl via that context.
	loginCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_ = Poll(loginCtx, 3*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
		return client.Login(ctx, token) == nil, nil
	})

	attempt := 0
	return Poll(ctx, 5*time.Second, 5*time.Minute, func(ctx context.Context) (bool, error) {
		attempt++
		code, body := postTokenLogin(ctx, hubURL, token)
		if code == http.StatusOK {
			return true, nil
		}
		fmt.Printf("[WaitForTenantAPI] attempt %d: token-login status %d body=%s\n", attempt, code, body)
		return false, nil
	})
}

// postTokenLogin POSTs the hub's static-token login endpoint with the given
// bearer token and returns the status code and (truncated) body.
func postTokenLogin(ctx context.Context, hubURL, token string) (int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hubURL+"/auth/token-login", nil)
	if err != nil {
		return 0, err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := insecureHTTPClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close() //nolint:errcheck
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return resp.StatusCode, string(b)
}

// WaitForTenantAPIWithOIDC is like WaitForTenantAPI but proves readiness via a
// headless OIDC login (users APIBinding must be bound for the login handler to
// mint a user). Used when the hub runs in OIDC-only mode (--with-dex).
func WaitForTenantAPIWithOIDC(ctx context.Context, workDir, hubURL string) error {
	var (
		result *OIDCLoginResult
		err    error
	)
	if pollErr := Poll(ctx, 5*time.Second, 5*time.Minute, func(ctx context.Context) (bool, error) {
		loginCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		result, err = HeadlessOIDCLogin(loginCtx, hubURL, DexTestUserEmail, DexTestUserPassword)
		if err != nil {
			fmt.Printf("[WaitForTenantAPIWithOIDC] OIDC login not ready: %v\n", err)
			return false, nil
		}
		return true, nil
	}); pollErr != nil {
		return fmt.Errorf("OIDC tenant API never became ready: %w (last err: %v)", pollErr, err)
	}

	// Persist the credential + kubeconfig from the successful login so tests
	// that reuse the cached OIDC session pick it up.
	if result.IDToken != "" {
		tokenCache := &cliauth.TokenCache{
			IDToken:      result.IDToken,
			RefreshToken: result.RefreshToken,
			ExpiresAt:    result.ExpiresAt,
			IssuerURL:    result.IssuerURL,
			ClientID:     result.ClientID,
		}
		if err := cliauth.SaveTokenCache(tokenCache); err != nil {
			return fmt.Errorf("caching OIDC token for tenant API wait: %w", err)
		}
	}
	oidcKubeconfig := filepath.Join(workDir, "oidc-wait.kubeconfig")
	if err := os.WriteFile(oidcKubeconfig, result.Kubeconfig, 0o600); err != nil {
		return fmt.Errorf("writing OIDC kubeconfig for tenant API wait: %w", err)
	}
	return nil
}

// HTTPGet performs a GET to url using an insecure client (self-signed certs in
// dev) and returns the HTTP status code.
func HTTPGet(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := insecureHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() //nolint:errcheck
	return resp.StatusCode, nil
}

// HTTPGetBody performs a GET to url and returns the status code and response
// body. TLS verification is skipped — used for in-cluster services exposed via
// kind port mapping with self-signed certs (Dex, hub).
func HTTPGetBody(ctx context.Context, url string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := insecureHTTPClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("reading response body: %w", err)
	}
	return resp.StatusCode, string(body), nil
}
