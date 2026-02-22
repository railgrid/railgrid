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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/oauth2"
)

// OIDCLoginResult holds the result of a headless OIDC login.
type OIDCLoginResult struct {
	// Kubeconfig is the raw kubeconfig YAML returned by the hub.
	Kubeconfig []byte
	// Email is the authenticated user's email address.
	Email string
	// UserID is the authenticated user's ID in the hub.
	UserID string
	// IDToken is the raw OIDC ID token for direct API calls or caching.
	IDToken string
	// RefreshToken can be used to refresh the ID token.
	RefreshToken string
	// ExpiresAt is the Unix timestamp when the ID token expires.
	ExpiresAt int64
	// IssuerURL is the OIDC issuer URL embedded in the kubeconfig.
	IssuerURL string
	// ClientID is the OAuth2 client ID.
	ClientID string
}

// HeadlessOIDCLogin drives the full OIDC authorization-code flow headlessly.
//
// The kedge hub auth flow (see pkg/server/auth/handler.go):
//  1. GET  /auth/authorize?p=<port>&s=<session>  →  302 to Dex auth URL
//  2. GET  Dex auth URL                           →  Dex login page
//  3. POST Dex login form with credentials        →  302 to hub /auth/callback
//  4. GET  hub /auth/callback?code=…&state=…      →  302 to localhost:<port>/callback?response=<b64>
//  5. GET  localhost:<port>/callback?response=…   →  parse LoginResponse JSON
//
// The function starts a local HTTP server on a random port to receive step 5,
// then drives steps 1–4 using a cookie-aware HTTP client.
func HeadlessOIDCLogin(ctx context.Context, hubURL, email, password string) (*OIDCLoginResult, error) {
	// ── Step 0: start local callback server ─────────────────────────────────
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting callback listener: %w", err)
	}
	callbackPort := listener.Addr().(*net.TCPAddr).Port

	type serverResult struct {
		payload *loginResponse
		err     error
	}
	resultCh := make(chan serverResult, 1)

	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			encoded := r.URL.Query().Get("response")
			if encoded == "" {
				resultCh <- serverResult{err: fmt.Errorf("callback received no 'response' query param; URL: %s", r.URL.String())}
				http.Error(w, "missing response", http.StatusBadRequest)
				return
			}
			respJSON, decodeErr := base64.URLEncoding.DecodeString(encoded)
			if decodeErr != nil {
				resultCh <- serverResult{err: fmt.Errorf("base64 decode of response param failed: %w", decodeErr)}
				http.Error(w, "decode error", http.StatusBadRequest)
				return
			}
			var lr loginResponse
			if parseErr := json.Unmarshal(respJSON, &lr); parseErr != nil {
				resultCh <- serverResult{err: fmt.Errorf("parsing login response JSON: %w", parseErr)}
				http.Error(w, "parse error", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "Login successful. You may close this window.")
			resultCh <- serverResult{payload: &lr}
		}),
	}
	go func() {
		if serveErr := srv.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			resultCh <- serverResult{err: fmt.Errorf("callback server error: %w", serveErr)}
		}
	}()
	defer srv.Close() //nolint:errcheck

	// ── Step 1: build an HTTP client ─────────────────────────────────────────
	// • Keeps cookies across redirects (Dex session state).
	// • Skips TLS for hub (self-signed cert in dev mode).
	// • Does NOT auto-follow redirects (we drive them manually).
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("creating cookie jar: %w", err)
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test/dev only
	}
	client := &http.Client{
		Jar:       jar,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// ── Step 2: hit hub /auth/authorize → get Dex auth URL ───────────────────
	// PKCE (public client): generate a code_verifier so the hub can include
	// the S256 code_challenge in the upstream auth URL and verify on exchange.
	codeVerifier := oauth2.GenerateVerifier()
	authorizeURL := fmt.Sprintf("%s/auth/authorize?p=%d&s=e2etest&v=%s", hubURL, callbackPort, url.QueryEscape(codeVerifier))
	dexAuthURL, err := doRedirect(ctx, client, authorizeURL)
	if err != nil {
		return nil, fmt.Errorf("following hub /auth/authorize: %w", err)
	}

	// Rewrite Dex URL to localhost so the test runner can reach it via the
	// kind port-mapping (or kubectl port-forward).
	dexAuthURL = rewriteDexToLocalhost(dexAuthURL)

	// ── Step 3: follow any intermediate Dex redirect to the login page ───────
	loginPageURL, loginErr := doRedirect(ctx, client, dexAuthURL)
	if loginErr != nil {
		// dexAuthURL itself may be the login page (HTTP 200).
		loginPageURL = dexAuthURL
	} else {
		loginPageURL = rewriteDexToLocalhost(loginPageURL)
	}

	// ── Step 4: fetch the Dex login page and parse the <form action> ─────────
	formAction, hiddenFields, err := scrapeDexLoginForm(ctx, client, loginPageURL)
	if err != nil {
		return nil, fmt.Errorf("scraping Dex login form from %s: %w", loginPageURL, err)
	}
	formAction = rewriteDexToLocalhost(formAction)

	// ── Step 5: POST credentials ──────────────────────────────────────────────
	form := url.Values{}
	for k, v := range hiddenFields {
		form.Set(k, v)
	}
	form.Set("login", email)
	form.Set("password", password)

	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, formAction, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building Dex form POST: %w", err)
	}
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	postResp, err := client.Do(postReq)
	if err != nil {
		return nil, fmt.Errorf("posting Dex login form: %w", err)
	}
	_ = postResp.Body.Close()

	if postResp.StatusCode != http.StatusFound && postResp.StatusCode != http.StatusSeeOther {
		return nil, fmt.Errorf("dex login POST returned %d (expected 302/303); bad credentials?", postResp.StatusCode)
	}

	dexCallbackURL := postResp.Header.Get("Location")
	if dexCallbackURL == "" {
		return nil, fmt.Errorf("dex returned no Location after login POST")
	}

	// Resolve relative Dex callback URLs.
	dexCallbackURL = resolveRelative(formAction, dexCallbackURL)

	// ── Step 6: follow Dex → hub /auth/callback ───────────────────────────────
	// The Location may point to the hub's public URL (e.g. https://kedge.localhost:8443/auth/callback).
	// We follow it so the hub can exchange the auth code.
	hubCallbackReq, err := http.NewRequestWithContext(ctx, http.MethodGet, dexCallbackURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building hub /auth/callback request: %w", err)
	}
	hubCallbackResp, err := client.Do(hubCallbackReq)
	if err != nil {
		return nil, fmt.Errorf("hitting hub /auth/callback: %w", err)
	}
	_ = hubCallbackResp.Body.Close()

	// Hub should redirect to our local callback server with ?response=<b64>.
	// Our server will receive that redirect via browser/HTTP (the hub sends a 302
	// to http://127.0.0.1:{port}/callback?response=...).
	// The client's auto-redirect is disabled — we need to follow it manually.
	if hubCallbackResp.StatusCode == http.StatusFound || hubCallbackResp.StatusCode == http.StatusSeeOther {
		cliCallbackURL := hubCallbackResp.Header.Get("Location")
		if cliCallbackURL != "" {
			cliReq, err := http.NewRequestWithContext(ctx, http.MethodGet, cliCallbackURL, nil)
			if err != nil {
				return nil, fmt.Errorf("building CLI callback request: %w", err)
			}
			cliResp, err := client.Do(cliReq)
			if err != nil {
				return nil, fmt.Errorf("hitting CLI callback: %w", err)
			}
			_ = cliResp.Body.Close()
		}
	}

	// ── Step 7: wait for our callback server to receive the result ───────────
	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, res.err
		}
		return &OIDCLoginResult{
			Kubeconfig:   res.payload.Kubeconfig,
			Email:        res.payload.Email,
			UserID:       res.payload.UserID,
			IDToken:      res.payload.IDToken,
			RefreshToken: res.payload.RefreshToken,
			ExpiresAt:    res.payload.ExpiresAt,
			IssuerURL:    res.payload.IssuerURL,
			ClientID:     res.payload.ClientID,
		}, nil
	case <-time.After(45 * time.Second):
		return nil, fmt.Errorf("timed out (45s) waiting for OIDC callback on localhost:%d/callback", callbackPort)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// loginResponse mirrors tenancyv1alpha1.LoginResponse for JSON parsing.
type loginResponse struct {
	Kubeconfig   []byte `json:"kubeconfig"`
	ExpiresAt    int64  `json:"expiresAt"`
	Email        string `json:"email"`
	UserID       string `json:"userID"`
	IDToken      string `json:"idToken"`
	RefreshToken string `json:"refreshToken"`
	IssuerURL    string `json:"issuerURL"`
	ClientID     string `json:"clientID"`
}

// doRedirect performs a GET to rawURL, follows zero redirects, and returns the
// Location header of the response.  Returns an error if the response is not a
// redirect (3xx) or has no Location.
func doRedirect(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusFound, http.StatusSeeOther, http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", fmt.Errorf("redirect from %s had no Location header", rawURL)
		}
		return resolveRelative(rawURL, loc), nil
	default:
		return "", fmt.Errorf("expected redirect from %s, got %d", rawURL, resp.StatusCode)
	}
}

// scrapeDexLoginForm GETs pageURL (following up to 5 redirects manually so
// cookie-jar state is preserved), parses the HTML form, and returns the form
// action URL and any hidden input fields.
func scrapeDexLoginForm(ctx context.Context, client *http.Client, pageURL string) (string, map[string]string, error) {
	const maxHops = 5
	currentURL := pageURL
	for i := 0; i < maxHops; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, currentURL, nil)
		if err != nil {
			return "", nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", nil, err
		}
		switch resp.StatusCode {
		case http.StatusOK:
			action, hidden, parseErr := parseLoginForm(resp, currentURL)
			return action, hidden, parseErr
		case http.StatusFound, http.StatusSeeOther, http.StatusMovedPermanently, http.StatusTemporaryRedirect:
			loc := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if loc == "" {
				return "", nil, fmt.Errorf("redirect from %s had empty Location", currentURL)
			}
			currentURL = rewriteDexToLocalhost(resolveRelative(currentURL, loc))
		default:
			_ = resp.Body.Close()
			return "", nil, fmt.Errorf("fetching Dex login page %s returned %d", currentURL, resp.StatusCode)
		}
	}
	return "", nil, fmt.Errorf("too many redirects following Dex login page from %s", pageURL)
}

// parseLoginForm reads the HTML body from resp and extracts the form action and
// hidden inputs.  currentURL is used to resolve relative URLs.  It closes resp.Body.
func parseLoginForm(resp *http.Response, currentURL string) (string, map[string]string, error) {
	defer resp.Body.Close() //nolint:errcheck

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("parsing Dex login page HTML: %w", err)
	}

	base, _ := url.Parse(currentURL)
	hidden := map[string]string{}
	var formAction string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "form":
				for _, a := range n.Attr {
					if a.Key == "action" {
						if base != nil {
							if ref, e := url.Parse(a.Val); e == nil {
								formAction = base.ResolveReference(ref).String()
							} else {
								formAction = a.Val
							}
						} else {
							formAction = a.Val
						}
					}
				}
			case "input":
				var name, val, typ string
				for _, a := range n.Attr {
					switch a.Key {
					case "name":
						name = a.Val
					case "value":
						val = a.Val
					case "type":
						typ = a.Val
					}
				}
				if typ == "hidden" && name != "" {
					hidden[name] = val
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if formAction == "" {
		return "", nil, fmt.Errorf("no <form action> found on Dex login page %s", currentURL)
	}
	return formAction, hidden, nil
}

// rewriteDexToLocalhost rewrites a URL whose host is the Dex cluster-DNS name
// or any Dex-ish hostname so the test runner can reach it via the kind port
// mapping on localhost:DexServicePort.
func rewriteDexToLocalhost(rawURL string) string {
	if rawURL == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := u.Hostname()
	if host == DexExternalHost || strings.HasPrefix(host, "dex.") {
		u.Scheme = "http"
		u.Host = fmt.Sprintf("localhost:%d", DexServicePort)
		return u.String()
	}
	return rawURL
}

// resolveRelative resolves ref against base, returning an absolute URL.
func resolveRelative(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}
