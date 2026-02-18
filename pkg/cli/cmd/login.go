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
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	cliauth "github.com/faroshq/faros-kedge/pkg/cli/auth"
)

func newLoginCommand() *cobra.Command {
	var (
		hubURL                string
		insecureSkipTLSVerify bool
		token                 string
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with the kedge hub via OIDC or static token",
		RunE: func(cmd *cobra.Command, args []string) error {
			if hubURL == "" {
				return fmt.Errorf("--hub-url is required")
			}
			if token != "" {
				return runStaticTokenLogin(hubURL, token, insecureSkipTLSVerify)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()
			return runLogin(ctx, hubURL, insecureSkipTLSVerify)
		},
	}

	cmd.Flags().StringVar(&hubURL, "hub-url", "", "Hub server URL (required)")
	cmd.Flags().BoolVar(&insecureSkipTLSVerify, "insecure-skip-tls-verify", false, "Skip TLS certificate verification")
	cmd.Flags().StringVar(&token, "token", "", "Static bearer token (skips OIDC browser flow)")

	return cmd
}

func runStaticTokenLogin(hubURL, token string, insecure bool) error {
	// Build a kubeconfig with the static token embedded directly.
	newConfig := clientcmdapi.NewConfig()
	newConfig.Clusters["kedge"] = &clientcmdapi.Cluster{
		Server:                hubURL,
		InsecureSkipTLSVerify: insecure,
	}
	newConfig.AuthInfos["kedge"] = &clientcmdapi.AuthInfo{
		Token: token,
	}
	newConfig.Contexts["kedge"] = &clientcmdapi.Context{
		Cluster:  "kedge",
		AuthInfo: "kedge",
	}
	newConfig.CurrentContext = "kedge"

	kubeconfigBytes, err := clientcmd.Write(*newConfig)
	if err != nil {
		return fmt.Errorf("serializing kubeconfig: %w", err)
	}

	if err := mergeKubeconfig(kubeconfigBytes); err != nil {
		return fmt.Errorf("merging kubeconfig: %w", err)
	}

	fmt.Printf("Login successful! Using static token authentication.\n")
	fmt.Printf("Kubeconfig context \"kedge\" has been set.\n")
	fmt.Printf("Run: kubectl --context=kedge get namespaces\n")
	return nil
}

func runLogin(ctx context.Context, hubURL string, insecure bool) error {
	// 1. Start local callback server on a random port.
	authenticator := cliauth.NewLocalhostCallbackAuthenticator()
	if err := authenticator.Start(); err != nil {
		return fmt.Errorf("starting callback server: %w", err)
	}

	// 2. Generate a random session ID.
	sessionBytes := make([]byte, 3)
	if _, err := rand.Read(sessionBytes); err != nil {
		return fmt.Errorf("generating session ID: %w", err)
	}
	sessionID := hex.EncodeToString(sessionBytes)

	// 3. Build the authorize URL.
	authorizeURL := fmt.Sprintf("%s/auth/authorize?p=%d&s=%s", hubURL, authenticator.Port(), sessionID)

	// 4. Open browser.
	fmt.Printf("Opening browser for login...\n")
	if err := openBrowser(authorizeURL); err != nil {
		fmt.Printf("Could not open browser automatically.\nPlease open the following URL in your browser:\n\n  %s\n\n", authorizeURL)
	}

	// 5. Optionally verify hub is reachable (best-effort).
	if insecure {
		http.DefaultTransport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
	}

	fmt.Println("Waiting for login to complete...")

	// 6. Wait for the callback response.
	resp, err := authenticator.WaitForResponse(ctx)
	if err != nil {
		return fmt.Errorf("waiting for login response: %w", err)
	}

	// 7. Save OIDC token cache so the exec credential plugin can use it.
	if resp.IDToken != "" && resp.IssuerURL != "" {
		cache := &cliauth.TokenCache{
			IDToken:      resp.IDToken,
			RefreshToken: resp.RefreshToken,
			ExpiresAt:    resp.ExpiresAt,
			IssuerURL:    resp.IssuerURL,
			ClientID:     resp.ClientID,
			ClientSecret: resp.ClientSecret,
		}
		if err := cliauth.SaveTokenCache(cache); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save token cache: %v\n", err)
		}
	}

	// 8. Merge the received kubeconfig into ~/.kube/config.
	if err := mergeKubeconfig(resp.Kubeconfig); err != nil {
		return fmt.Errorf("merging kubeconfig: %w", err)
	}

	fmt.Printf("Login successful! Logged in as %s (user: %s)\n", resp.Email, resp.UserID)
	fmt.Printf("Kubeconfig context \"kedge\" has been set.\n")
	fmt.Printf("Run: kubectl --context=kedge get users\n")
	return nil
}

// mergeKubeconfig merges the received kubeconfig bytes into the default kubeconfig file.
func mergeKubeconfig(kubeconfigBytes []byte) error {
	// Parse the new kubeconfig.
	newConfig, err := clientcmd.Load(kubeconfigBytes)
	if err != nil {
		return fmt.Errorf("parsing received kubeconfig: %w", err)
	}

	// Load the existing kubeconfig.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	existingConfig, err := loadingRules.GetStartingConfig()
	if err != nil {
		// If no existing config, just use the new one.
		existingConfig = clientcmdapi.NewConfig()
	}

	// Merge: overwrite clusters, contexts, and auth infos from the new config.
	for k, v := range newConfig.Clusters {
		existingConfig.Clusters[k] = v
	}
	for k, v := range newConfig.AuthInfos {
		existingConfig.AuthInfos[k] = v
	}
	for k, v := range newConfig.Contexts {
		existingConfig.Contexts[k] = v
	}
	existingConfig.CurrentContext = newConfig.CurrentContext

	// Write back.
	configPath := loadingRules.GetDefaultFilename()
	if err := clientcmd.WriteToFile(*existingConfig, configPath); err != nil {
		return fmt.Errorf("writing kubeconfig to %s: %w", configPath, err)
	}

	return nil
}

// openBrowser opens the given URL in the default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}
