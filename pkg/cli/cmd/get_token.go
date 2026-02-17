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

package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	oidc "github.com/coreos/go-oidc"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	cliauth "github.com/faroshq/faros-kedge/pkg/cli/auth"
)

func newGetTokenCommand() *cobra.Command {
	var (
		issuerURL             string
		clientID              string
		clientSecret          string
		insecureSkipTLSVerify bool
	)

	cmd := &cobra.Command{
		Use:    "get-token",
		Short:  "Get an OIDC token for kubectl exec credential plugin",
		Hidden: true, // Called by kubectl, not directly by users.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetToken(cmd.Context(), issuerURL, clientID, clientSecret, insecureSkipTLSVerify)
		},
	}

	cmd.Flags().StringVar(&issuerURL, "oidc-issuer-url", "", "OIDC issuer URL")
	cmd.Flags().StringVar(&clientID, "oidc-client-id", "", "OIDC client ID")
	cmd.Flags().StringVar(&clientSecret, "oidc-client-secret", "", "OIDC client secret")
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

func runGetToken(ctx context.Context, issuerURL, clientID, clientSecret string, insecure bool) error {
	if issuerURL == "" || clientID == "" {
		return fmt.Errorf("--oidc-issuer-url and --oidc-client-id are required")
	}

	// Try to load a cached token.
	cache, err := cliauth.LoadTokenCache(issuerURL, clientID)
	if err == nil && !cache.IsExpired() {
		return outputExecCredential(cache.IDToken, cache.ExpiresAt)
	}

	// Token is missing or expired -- try to refresh.
	if cache == nil || cache.RefreshToken == "" {
		return fmt.Errorf("no valid token found; please run 'kedge login' first")
	}

	newIDToken, newRefreshToken, expiry, err := refreshToken(ctx, issuerURL, clientID, clientSecret, cache.RefreshToken, insecure)
	if err != nil {
		return fmt.Errorf("token refresh failed (run 'kedge login' to re-authenticate): %w", err)
	}

	// Update cache.
	cache.IDToken = newIDToken
	cache.RefreshToken = newRefreshToken
	cache.ExpiresAt = expiry.Unix()
	if err := cliauth.SaveTokenCache(cache); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save token cache: %v\n", err)
	}

	return outputExecCredential(newIDToken, expiry.Unix())
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

func outputExecCredential(token string, expiresAtUnix int64) error {
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
	_, err = os.Stdout.Write(data)
	return err
}
