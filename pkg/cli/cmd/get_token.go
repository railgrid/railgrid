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

package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	oidc "github.com/coreos/go-oidc"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	cliauth "github.com/railgrid/railgrid/pkg/cli/auth"
)

func newGetTokenCommand() *cobra.Command {
	var (
		issuerURL             string
		clientID              string
		account               string
		insecureSkipTLSVerify bool
	)

	cmd := &cobra.Command{
		Use:    "get-token",
		Short:  "Get an OIDC token for kubectl exec credential plugin",
		Hidden: true, // Called by kubectl, not directly by users.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetToken(cmd.Context(), cmd.OutOrStdout(), issuerURL, clientID, insecureSkipTLSVerify, account)
		},
	}

	cmd.Flags().StringVar(&issuerURL, "oidc-issuer-url", "", "OIDC issuer URL")
	cmd.Flags().StringVar(&clientID, "oidc-client-id", "", "OIDC client ID")
	cmd.Flags().StringVar(&account, "oidc-account", "", "Railgrid account ID for the token cache")
	cmd.Flags().BoolVar(&insecureSkipTLSVerify, "insecure-skip-tls-verify", false, "Skip TLS verification for OIDC provider")

	return cmd
}

// execCredential is the ExecCredential response for kubectl.
type execCredential struct {
	APIVersion string               `json:"apiVersion"`
	Kind       string               `json:"kind"`
	Status     execCredentialStatus `json:"status"`
}

type execCredentialStatus struct {
	Token               string `json:"token"`
	ExpirationTimestamp string `json:"expirationTimestamp,omitempty"`
}

// errLoginRequired marks a failure only an interactive login can fix: there is
// no usable cached login, or the identity provider rejected the refresh token.
// A transport error or an unreachable issuer is not one.
var errLoginRequired = errors.New("login required")

func runGetToken(ctx context.Context, out io.Writer, issuerURL, clientID string, insecure bool, account ...string) error {
	if issuerURL == "" || clientID == "" {
		return fmt.Errorf("--oidc-issuer-url and --oidc-client-id are required")
	}

	token, expiresAt, err := cachedOrRefreshedToken(ctx, issuerURL, clientID, insecure, account...)
	if errors.Is(err, errLoginRequired) {
		token, expiresAt, err = reloginAndGetToken(ctx, err, issuerURL, clientID, insecure, firstString(account))
	}
	if err != nil {
		return err
	}
	return outputExecCredential(out, token, expiresAt)
}

// cachedOrRefreshedToken returns the cached ID token, refreshing it first when
// it has expired.
func cachedOrRefreshedToken(ctx context.Context, issuerURL, clientID string, insecure bool, account ...string) (string, int64, error) {
	// Take an exclusive flock around the cache. OIDC refresh-token rotation is
	// single-use, so two concurrent kubectl invocations racing here would burn
	// one of the rotated tokens and brick the cache. With the lock the loser
	// waits, re-reads the cache, and uses the IDToken the winner just stored.
	unlock, err := cliauth.LockTokenCache(issuerURL, clientID, account...)
	if err != nil {
		return "", 0, fmt.Errorf("locking token cache: %w", err)
	}
	defer unlock()

	cache, loadErr := cliauth.LoadTokenCache(issuerURL, clientID, account...)
	if loadErr == nil && !cache.IsExpired() {
		return cache.IDToken, cache.ExpiresAt, nil
	}

	// Token is missing or expired -- try to refresh.
	if cache == nil || cache.RefreshToken == "" {
		return "", 0, fmt.Errorf("no valid token found; please run 'railgrid login' first%w", hidden(errLoginRequired))
	}

	// Public client refresh: no client secret needed (PKCE flow).
	newIDToken, newRefreshToken, expiry, err := refreshToken(ctx, issuerURL, clientID, "", cache.RefreshToken, insecure)
	if err != nil {
		err = fmt.Errorf("token refresh failed (run 'railgrid login' to re-authenticate): %w", err)
		if refreshTokenRejected(err) {
			err = fmt.Errorf("%w%w", err, hidden(errLoginRequired))
		}
		return "", 0, err
	}

	// Persist BEFORE returning the token: a successful refresh has already
	// rotated the refresh_token at the IdP, so silently failing the cache
	// write would leave the cached refresh token consumed and the cache
	// permanently bricked until the next interactive login.
	cache.IDToken = newIDToken
	cache.RefreshToken = newRefreshToken
	cache.ExpiresAt = expiry.Unix()
	if err := cliauth.SaveTokenCache(cache); err != nil {
		return "", 0, fmt.Errorf("saving rotated token cache (run 'railgrid login' to re-authenticate): %w", err)
	}

	return newIDToken, expiry.Unix(), nil
}

// refreshTokenRejected reports whether the identity provider answered the
// refresh grant with a client error: the refresh token is revoked, expired or
// already claimed (dex says invalid_request, most others invalid_grant), and
// retrying cannot help.
func refreshTokenRejected(err error) bool {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) || re.Response == nil {
		return false
	}
	return re.Response.StatusCode >= 400 && re.Response.StatusCode < 500
}

// hiddenError carries a sentinel for errors.Is without adding to the message.
type hiddenError struct{ err error }

func hidden(err error) error        { return hiddenError{err} }
func (hiddenError) Error() string   { return "" }
func (h hiddenError) Unwrap() error { return h.err }

func firstString(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func refreshToken(ctx context.Context, issuerURL, clientID, clientSecret, refreshToken string, insecure bool) (idToken, newRefreshToken string, expiry time.Time, err error) {
	providerCtx := ctx
	if insecure {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
		httpClient := &http.Client{Transport: tr}
		providerCtx = oidc.ClientContext(ctx, httpClient)
	}

	provider, err := oidc.NewProvider(providerCtx, issuerURL)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("creating OIDC provider: %w", err)
	}

	oauth2Config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{"openid", "profile", "email", "offline_access"},
	}

	tokenSource := oauth2Config.TokenSource(providerCtx, &oauth2.Token{
		RefreshToken: refreshToken,
	})

	token, err := tokenSource.Token()
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("refreshing token: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return "", "", time.Time{}, fmt.Errorf("no id_token in refresh response")
	}

	return rawIDToken, token.RefreshToken, token.Expiry, nil
}

func outputExecCredential(out io.Writer, token string, expiresAtUnix int64) error {
	cred := execCredential{
		APIVersion: "client.authentication.k8s.io/v1beta1",
		Kind:       "ExecCredential",
		Status: execCredentialStatus{
			Token:               token,
			ExpirationTimestamp: time.Unix(expiresAtUnix, 0).UTC().Format(time.RFC3339),
		},
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("marshaling exec credential: %w", err)
	}
	_, err = out.Write(data)
	return err
}
