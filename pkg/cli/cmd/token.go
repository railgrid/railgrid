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
	"fmt"
	"time"

	"github.com/spf13/cobra"

	cliauth "github.com/faroshq/faros/pkg/cli/auth"
)

func newTokenCommand() *cobra.Command {
	var forceRefresh bool
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print a bearer token for the hub (refreshing it when needed)",
		Long: `Print the bearer token the kubeconfig credentials produce, for curl and
other tools that cannot run the kubectl exec plugin:

  curl -H "Authorization: Bearer $(faros token)" $HUB/api/orgs

With OIDC logins the cached ID token is returned and refreshed when expired.
--refresh forces a refresh now, which is also the quickest way to check that
the refresh-token flow works against your identity provider.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			raw, _, err := loadRawKubeconfig()
			if err != nil {
				return err
			}
			_, _, _, auth, err := farosClusterAndAuth(raw)
			if err != nil {
				return err
			}
			if auth.Exec != nil && forceRefresh {
				issuer, clientID := execOIDCArgs(auth.Exec)
				if issuer == "" || clientID == "" {
					return fmt.Errorf("the kubeconfig's exec plugin is not the faros OIDC plugin; cannot refresh")
				}
				expiry, err := forceTokenRefresh(ctx, issuer, clientID, globalInsecureTLS || hasInsecureArg(auth.Exec.Args))
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Refreshed; new token valid for %s.\n", formatDuration(time.Until(expiry)))
			} else if forceRefresh {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Static token credentials do not refresh; printing the configured token.")
			}
			s, err := openHubSession()
			if err != nil {
				return err
			}
			token, err := s.bearerToken(ctx)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), token)
			return err
		},
	}
	cmd.Flags().BoolVar(&forceRefresh, "refresh", false, "Refresh the OIDC token now even if it has not expired")
	return cmd
}

func hasInsecureArg(args []string) bool {
	for _, a := range args {
		if a == "--insecure-skip-tls-verify" || a == "--insecure-skip-tls-verify=true" {
			return true
		}
	}
	return false
}

// forceTokenRefresh performs the refresh-token grant regardless of the cached
// token's expiry and persists the rotated tokens. It returns the new expiry.
func forceTokenRefresh(ctx context.Context, issuerURL, clientID string, insecure bool) (time.Time, error) {
	unlock, err := cliauth.LockTokenCache(issuerURL, clientID)
	if err != nil {
		return time.Time{}, fmt.Errorf("locking token cache: %w", err)
	}
	defer unlock()

	cache, err := cliauth.LoadTokenCache(issuerURL, clientID)
	if err != nil {
		return time.Time{}, fmt.Errorf("no cached token for this hub; run 'faros login' first (%w)", err)
	}
	if cache.RefreshToken == "" {
		return time.Time{}, fmt.Errorf("the cached login has no refresh token; the identity provider did not issue one (run 'faros login' again)")
	}
	newIDToken, newRefreshToken, expiry, err := refreshToken(ctx, issuerURL, clientID, "", cache.RefreshToken, insecure)
	if err != nil {
		return time.Time{}, fmt.Errorf("token refresh failed (run 'faros login' to re-authenticate): %w", err)
	}
	cache.IDToken = newIDToken
	cache.RefreshToken = newRefreshToken
	cache.ExpiresAt = expiry.Unix()
	if err := cliauth.SaveTokenCache(cache); err != nil {
		return time.Time{}, fmt.Errorf("saving rotated token cache (run 'faros login' to re-authenticate): %w", err)
	}
	return expiry, nil
}
