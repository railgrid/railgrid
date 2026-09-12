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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros/pkg/apiurl"
	cliauth "github.com/faroshq/faros/pkg/cli/auth"
)

// hubURLEnv names the environment variable consulted when --hub-url is not
// given. There is no hosted faros hub, so there is no built-in default.
const hubURLEnv = "FAROS_HUB_URL"

func newLoginCommand() *cobra.Command {
	var (
		hubURL      string
		token       string
		interactive bool
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to a faros hub (browser OIDC flow, or a static token)",
		Long: `Authenticate against a hub and write a kubeconfig context named "faros"
whose credentials refresh automatically (OIDC) or carry the static token.

  faros login --hub-url https://hub.example.com        # opens the browser
  faros login --hub-url https://hub.example.com -i     # …then pick org/workspace
  faros login --hub-url https://hub.example.com --token <token>
  export FAROS_HUB_URL=https://hub.example.com          # instead of --hub-url

On a self-signed hub add --insecure-skip-tls-verify. After login, 'faros use'
switches organization and workspace and 'faros whoami' shows the session.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			insecureSkipTLSVerify := globalInsecureTLS
			out := cmd.OutOrStdout()
			if hubURL == "" {
				hubURL = strings.TrimSpace(os.Getenv(hubURLEnv))
			}
			if hubURL == "" {
				return fmt.Errorf("no hub configured: pass --hub-url https://<your-hub> or set %s", hubURLEnv)
			}
			hubURL = normalizeHubURL(hubURL)
			if token != "" {
				if err := runStaticTokenLogin(out, hubURL, token, insecureSkipTLSVerify); err != nil {
					return err
				}
			} else {
				// Check if hub has OIDC configured before opening browser.
				oidcEnabled, err := checkHubAuthMode(hubURL, insecureSkipTLSVerify)
				if err != nil {
					return err
				}
				if !oidcEnabled {
					return fmt.Errorf("hub at %s does not have OIDC configured — use: faros login --hub-url %s --token <token>", hubURL, hubURL)
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
				defer cancel()
				if err := runLogin(ctx, out, hubURL); err != nil {
					return err
				}
			}
			if interactive {
				_, _ = fmt.Fprintln(out)
				return runUse(cmd.Context(), "", "")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&hubURL, "hub-url", "", "Hub server URL (or set "+hubURLEnv+")")
	cmd.Flags().StringVar(&token, "token", "", "Static bearer token (skips OIDC browser flow)")
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "After login, interactively pick the organization and workspace")

	return cmd
}

// checkHubAuthMode queries the hub's /healthz endpoint to determine if OIDC
// is configured. Returns true if OIDC is enabled, false otherwise.
// On error (e.g. old server returning plain text), it assumes OIDC is enabled
// for backwards compatibility.
func checkHubAuthMode(hubURL string, insecure bool) (bool, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	if insecure {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
	}
	resp, err := client.Get(hubURL + apiurl.PathHealthz)
	if err != nil {
		return false, fmt.Errorf("checking hub auth mode: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(resp.Body)
	var result struct {
		OIDC bool `json:"oidc"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		// Old server without JSON healthz — assume OIDC (backwards compat).
		return true, nil
	}
	return result.OIDC, nil
}

func runStaticTokenLogin(out io.Writer, hubURL, token string, insecure bool) error {
	// Call the server's token-login endpoint to provision user/workspace
	// and get a kubeconfig with the correct cluster URL.
	client := &http.Client{}
	if insecure {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
	}

	req, err := http.NewRequest(http.MethodPost, hubURL+apiurl.PathAuthTokenLogin, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("calling token-login endpoint: %w", err)
	}
	defer resp.Body.Close() // nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token-login failed (status %d): %s", resp.StatusCode, string(body))
	}

	var loginResp tenancyv1alpha1.LoginResponse
	if err := json.Unmarshal(body, &loginResp); err != nil {
		return fmt.Errorf("parsing login response: %w", err)
	}

	contextName, err := mergeKubeconfig(loginResp.Kubeconfig)
	if err != nil {
		return fmt.Errorf("merging kubeconfig: %w", err)
	}

	printLoginSuccess(out, loginResp.Email, loginResp.UserID, contextName, nil)
	return nil
}

// printLoginSuccess prints the outcome and what to do next. expiresAt is the
// OIDC token expiry (nil for static tokens).
func printLoginSuccess(out io.Writer, email, userID, contextName string, expiresAt *time.Time) {
	who := email
	if who == "" {
		who = userID
	}
	_, _ = fmt.Fprintf(out, "Logged in as %s.\n", who)
	_, _ = fmt.Fprintf(out, "Kubeconfig context %q is current; kubectl now targets your default workspace.\n", contextName)
	if expiresAt != nil {
		_, _ = fmt.Fprintf(out, "Token valid for %s; it refreshes automatically.\n", formatDuration(time.Until(*expiresAt)))
	}
	_, _ = fmt.Fprintf(out, "\nNext:\n  faros use          pick an organization and workspace\n  faros edge list    see connected clusters and servers\n  faros whoami       show this session\n")
}

func runLogin(ctx context.Context, out io.Writer, hubURL string) error {
	// 1. Start local callback server on a random port.
	authenticator := cliauth.NewLocalhostCallbackAuthenticator()
	if err := authenticator.Start(); err != nil {
		return fmt.Errorf("starting callback server: %w", err)
	}

	// 2. Generate a random session ID and PKCE code_verifier.
	sessionBytes := make([]byte, 3)
	if _, err := rand.Read(sessionBytes); err != nil {
		return fmt.Errorf("generating session ID: %w", err)
	}
	sessionID := hex.EncodeToString(sessionBytes)

	codeVerifier := oauth2.GenerateVerifier()

	// 3. Build the authorize URL — include the PKCE verifier so the hub can
	//    exchange the auth code without a client secret.
	authorizeURL := fmt.Sprintf("%s/auth/authorize?p=%d&s=%s&v=%s",
		hubURL, authenticator.Port(), sessionID, codeVerifier)

	// 4. Open browser.
	_, _ = fmt.Fprintf(out, "Opening browser for login...\n")
	if err := openBrowser(authorizeURL); err != nil {
		_, _ = fmt.Fprintf(out, "Could not open browser automatically.\nPlease open the following URL in your browser:\n\n  %s\n\n", authorizeURL)
	}

	_, _ = fmt.Fprintln(out, "Waiting for login to complete...")

	// 6. Wait for the callback response.
	resp, err := authenticator.WaitForResponse(ctx)
	if err != nil {
		return fmt.Errorf("waiting for login response: %w", err)
	}

	// 7. Save OIDC token cache so the exec credential plugin can use it.
	// ClientSecret is intentionally not cached — PKCE public client refresh
	// needs only the refresh token, issuer URL, and client ID.
	var expiresAt *time.Time
	if resp.IDToken != "" && resp.IssuerURL != "" {
		if resp.RefreshToken == "" {
			fmt.Fprintf(os.Stderr, "Warning: the identity provider issued no refresh token; you will have to log in again when the token expires.\n"+
				"         (dex: the %q client needs 'public: true' and the hub requests the offline_access scope.)\n", resp.ClientID)
		}
		if resp.ExpiresAt > 0 {
			t := time.Unix(resp.ExpiresAt, 0)
			expiresAt = &t
		}
		cache := &cliauth.TokenCache{
			IDToken:      resp.IDToken,
			RefreshToken: resp.RefreshToken,
			ExpiresAt:    resp.ExpiresAt,
			IssuerURL:    resp.IssuerURL,
			ClientID:     resp.ClientID,
		}
		if err := cliauth.SaveTokenCache(cache); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save token cache: %v\n", err)
		}
	}

	// 8. Merge the received kubeconfig into ~/.kube/config.
	contextName, err := mergeKubeconfig(resp.Kubeconfig)
	if err != nil {
		return fmt.Errorf("merging kubeconfig: %w", err)
	}

	printLoginSuccess(out, resp.Email, resp.UserID, contextName, expiresAt)
	return nil
}

// mergeKubeconfig merges the received kubeconfig bytes into the default
// kubeconfig file and returns the name of the context it made current. The hub
// picks that name, so callers must not assume it.
func mergeKubeconfig(kubeconfigBytes []byte) (string, error) {
	// Parse the new kubeconfig.
	newConfig, err := clientcmd.Load(kubeconfigBytes)
	if err != nil {
		return "", fmt.Errorf("parsing received kubeconfig: %w", err)
	}

	// The hub emits the exec credential plugin with Command="faros", which
	// only resolves on PATH for the curl/tar.gz install. Krew installs the
	// binary as `kubectl-faros` — there is no `faros` symlink — so kubectl
	// would fail to exec the plugin. Rewrite to the absolute path of the
	// running binary so both install modes work.
	rewriteFarosExecCommand(newConfig)

	// Load the existing kubeconfig.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	existingConfig, err := loadingRules.GetStartingConfig()
	if err != nil {
		// If no existing config, just use the new one.
		existingConfig = clientcmdapi.NewConfig()
	}

	// Merge: overwrite clusters, contexts, and auth infos from the new config.
	for k, v := range newConfig.Clusters {
		// Re-login always points the cluster at the home workspace. When the
		// user previously switched workspaces with `faros use` on the same
		// hub, keep that selection — only credentials and TLS settings are
		// refreshed. Logging into a different hub still takes the new URL.
		if prev := existingConfig.Clusters[k]; prev != nil && strings.Contains(prev.Server, "/clusters/") {
			prevBase, prevCluster := apiurl.SplitBaseAndCluster(prev.Server)
			newBase, newCluster := apiurl.SplitBaseAndCluster(v.Server)
			if prevBase == newBase && prevCluster != newCluster {
				v.Server = prev.Server
				fmt.Printf("Keeping previously selected workspace: %s\n", prev.Server)
			}
		}
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
		return "", fmt.Errorf("writing kubeconfig to %s: %w", configPath, err)
	}

	return existingConfig.CurrentContext, nil
}

// rewriteFarosExecCommand replaces the sentinel `faros` command in any exec
// credential plugin with the absolute path of the currently running binary.
// This makes the kubeconfig work regardless of how the CLI was installed —
// curl/tar.gz (binary named `faros`), krew (binary named `kubectl-faros`), or
// any custom path.
func rewriteFarosExecCommand(cfg *clientcmdapi.Config) {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		// Fall back to leaving the kubeconfig untouched — better than writing
		// an empty Command field that would silently break later.
		return
	}
	for _, ai := range cfg.AuthInfos {
		if ai == nil || ai.Exec == nil {
			continue
		}
		// `kedge` is the pre-rename sentinel — hubs deployed before the
		// rename still emit it. Rewriting it too keeps the new CLI working
		// against an old hub instead of shelling out to a stale `kedge`
		// binary (which reads a different token cache and fails to refresh).
		if ai.Exec.Command == "faros" || ai.Exec.Command == "kedge" {
			ai.Exec.Command = exe
		}
	}
}

// openBrowser opens the given URL in the default browser. $BROWSER, when
// set, names the command to run instead (the freedesktop convention); it
// also lets tests and headless environments capture the URL.
func openBrowser(url string) error {
	if browser := strings.TrimSpace(os.Getenv("BROWSER")); browser != "" {
		parts := strings.Fields(browser)
		return exec.Command(parts[0], append(parts[1:], url)...).Start()
	}
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
