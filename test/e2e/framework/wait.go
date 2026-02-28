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

// WaitForEdgeAPI polls `kedge edge list` (after a one-time login) until the
// edge API is available (i.e. KCP APIBindings have finished bootstrapping).
// Without this wait, tests that create edges right after hub setup can get
// "server could not find the requested resource".
func WaitForEdgeAPI(ctx context.Context, client *KedgeClient, token string) error {
	// Login once so the kedge context is written to the default kubeconfig.
	if err := client.Login(ctx, token); err != nil {
		return fmt.Errorf("login before edge API wait: %w", err)
	}
	return Poll(ctx, 5*time.Second, 3*time.Minute, func(ctx context.Context) (bool, error) {
		_, err := client.EdgeList(ctx)
		if err == nil {
			return true, nil
		}
		// "server could not find" = APIBinding not ready yet — keep polling.
		// Any other error also warrants a retry (hub may be mid-restart).
		return false, nil
	})
}

// WaitForEdgeAPIWithOIDC is like WaitForEdgeAPI but authenticates via headless
// OIDC login instead of a static token. Used when the hub runs in OIDC-only
// mode (no staticAuthTokens configured), e.g. when --with-dex is active.
func WaitForEdgeAPIWithOIDC(ctx context.Context, workDir, hubURL string) error {
	result, err := HeadlessOIDCLogin(ctx, hubURL, DexTestUserEmail, DexTestUserPassword)
	if err != nil {
		return fmt.Errorf("OIDC headless login for edge API wait: %w", err)
	}
	if result.IDToken != "" {
		tokenCache := &cliauth.TokenCache{
			IDToken:      result.IDToken,
			RefreshToken: result.RefreshToken,
			ExpiresAt:    result.ExpiresAt,
			IssuerURL:    result.IssuerURL,
			ClientID:     result.ClientID,
		}
		if err := cliauth.SaveTokenCache(tokenCache); err != nil {
			return fmt.Errorf("caching OIDC token for edge API wait: %w", err)
		}
	}
	oidcKubeconfig := filepath.Join(workDir, "oidc-wait.kubeconfig")
	if err := os.WriteFile(oidcKubeconfig, result.Kubeconfig, 0o600); err != nil {
		return fmt.Errorf("writing OIDC kubeconfig for edge API wait: %w", err)
	}
	client := NewKedgeClient(workDir, oidcKubeconfig, hubURL)
	return Poll(ctx, 5*time.Second, 3*time.Minute, func(ctx context.Context) (bool, error) {
		_, err := client.EdgeList(ctx)
		return err == nil, nil
	})
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

// HTTPGetBody performs a GET to url using a plain (non-insecure) HTTP client
// and returns the status code and response body as a string.
// Use this for plain HTTP endpoints (e.g. in-cluster Dex via kind port mapping).
func HTTPGetBody(ctx context.Context, url string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := http.DefaultClient.Do(req)
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
