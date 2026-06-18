/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package oauthgithub serves the provider-side half of a "Connect with GitHub"
// OAuth App popup flow, so users onboard a Connection without pasting a PAT.
//
// Flow:
//   - the portal fetches /oauth/github/config to learn whether OAuth is enabled
//     and the absolute start URL to open in a popup;
//   - the popup hits /oauth/github/start, which redirects to GitHub's authorize
//     page (carrying the portal-supplied state);
//   - GitHub redirects the popup back to /oauth/github/callback, which exchanges
//     the code for an access token (server-side, using the client secret),
//     reads the authenticated login + granted scopes, and renders a tiny HTML
//     page that window.opener.postMessage()s {token, login, scopes, state} back
//     to the portal and closes itself.
//
// The portal then creates the credential Secret + Connection(type=oauth) as the
// caller — identical to the PAT path, only the token's origin differs. The
// access token never transits kcp or the hub; it goes straight to the portal in
// the user's browser.
//
// Because the callback is a top-level browser redirect from GitHub (no kedge
// auth), start/callback are served on the provider's OWN externally-reachable
// URL (GITHUB_OAUTH_REDIRECT_URL), not through the hub's authenticated
// /services proxy. In dev that's http://localhost:8083; the config endpoint,
// reached through the hub, hands the portal the matching start URL.
package oauthgithub

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	ghoauth "golang.org/x/oauth2/github"
)

// Config is resolved from the environment at startup.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string // absolute callback URL registered on the GitHub OAuth App
	StartURL     string // absolute start URL the portal opens (derived from RedirectURL)
	PortalOrigin string // postMessage target origin (the hub/portal origin)
	Scopes       []string
}

// FromEnv builds a Config from GITHUB_OAUTH_* env vars. enabled is false (with
// no error) when the required vars are unset — the provider then simply omits
// the GitHub button and the PAT form remains the only path.
func FromEnv() (cfg Config, enabled bool, err error) {
	cfg.ClientID = os.Getenv("GITHUB_OAUTH_CLIENT_ID")
	cfg.ClientSecret = os.Getenv("GITHUB_OAUTH_CLIENT_SECRET")
	cfg.RedirectURL = os.Getenv("GITHUB_OAUTH_REDIRECT_URL")
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "" {
		return Config{}, false, nil
	}
	u, err := url.Parse(cfg.RedirectURL)
	if err != nil {
		return Config{}, false, fmt.Errorf("GITHUB_OAUTH_REDIRECT_URL: %w", err)
	}
	// Derive the start URL from the callback by swapping the trailing "/callback"
	// path segment for "/start", preserving whatever host AND path prefix the
	// callback carries. This lets the callback ride the hub's existing
	// /services/providers/{name}/* proxy route (e.g.
	// https://hub/services/providers/code/oauth/github/callback) so no extra
	// per-provider ingress is needed — start derives to the matching
	// .../oauth/github/start under the same prefix. A bare provider-host callback
	// (https://code.example.com/oauth/github/callback) still derives to
	// https://code.example.com/oauth/github/start as before.
	if !strings.HasSuffix(u.Path, "/callback") {
		return Config{}, false, fmt.Errorf("GITHUB_OAUTH_REDIRECT_URL path must end in /callback, got %q", u.Path)
	}
	startU := *u
	startU.Path = strings.TrimSuffix(u.Path, "/callback") + "/start"
	startU.RawQuery = ""
	startU.Fragment = ""
	cfg.StartURL = startU.String()

	cfg.PortalOrigin = os.Getenv("GITHUB_OAUTH_PORTAL_ORIGIN")
	if cfg.PortalOrigin == "" {
		// No explicit portal origin → only safe default is to broadcast; warn
		// loudly. Production should always set this to the hub origin.
		cfg.PortalOrigin = "*"
	}
	scopes := os.Getenv("GITHUB_OAUTH_SCOPES")
	if scopes == "" {
		// read:packages lets the portal list the packages published under a repo.
		scopes = "repo,read:org,admin:public_key,read:packages"
	}
	for _, s := range strings.Split(scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			cfg.Scopes = append(cfg.Scopes, s)
		}
	}
	return cfg, true, nil
}

// Handler serves /oauth/github/{config,start,callback}.
type Handler struct {
	cfg     Config
	enabled bool
	oauth   *oauth2.Config
}

// NewHandler returns a handler. When enabled is false it still serves
// /oauth/github/config (reporting enabled:false) so the portal can probe.
func NewHandler(cfg Config, enabled bool) *Handler {
	h := &Handler{cfg: cfg, enabled: enabled}
	if enabled {
		h.oauth = &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     ghoauth.Endpoint,
			Scopes:       cfg.Scopes,
		}
	}
	return h
}

// Mount registers the routes on mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/oauth/github/config", h.handleConfig)
	mux.HandleFunc("/oauth/github/start", h.handleStart)
	mux.HandleFunc("/oauth/github/callback", h.handleCallback)
}

type configResponse struct {
	Enabled  bool   `json:"enabled"`
	StartURL string `json:"startURL,omitempty"`
	Scopes   string `json:"scopes,omitempty"`
}

func (h *Handler) handleConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	resp := configResponse{Enabled: h.enabled}
	if h.enabled {
		resp.StartURL = h.cfg.StartURL
		resp.Scopes = strings.Join(h.cfg.Scopes, ", ")
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleStart(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		http.Error(w, "github oauth not configured", http.StatusNotFound)
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		http.Error(w, "missing state", http.StatusBadRequest)
		return
	}
	// The portal owns CSRF: it generated state and validates it on the
	// postMessage. We echo it through GitHub unchanged.
	http.Redirect(w, r, h.oauth.AuthCodeURL(state), http.StatusFound)
}

func (h *Handler) handleCallback(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		http.Error(w, "github oauth not configured", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		h.renderResult(w, callbackResult{State: q.Get("state"), Error: e + ": " + q.Get("error_description")})
		return
	}
	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		h.renderResult(w, callbackResult{State: state, Error: "missing code or state"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tok, err := h.oauth.Exchange(ctx, code)
	if err != nil {
		h.renderResult(w, callbackResult{State: state, Error: "token exchange failed: " + err.Error()})
		return
	}

	login, scopes, err := fetchUser(ctx, tok.AccessToken)
	if err != nil {
		// The token is valid even if the /user probe failed; still return it.
		login = ""
	}
	h.renderResult(w, callbackResult{
		State:  state,
		Token:  tok.AccessToken,
		Login:  login,
		Scopes: scopes,
	})
}

// fetchUser reads the authenticated login and granted scopes (X-OAuth-Scopes).
func fetchUser(ctx context.Context, token string) (login, scopes string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	scopes = resp.Header.Get("X-OAuth-Scopes")
	if resp.StatusCode != http.StatusOK {
		return "", scopes, fmt.Errorf("github /user: %s", resp.Status)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", scopes, err
	}
	return u.Login, scopes, nil
}

type callbackResult struct {
	State  string
	Token  string
	Login  string
	Scopes string
	Error  string
}

// renderResult returns an HTML page that posts the result to the opener and
// closes the popup. The message is sent to the configured portal origin only.
func (h *Handler) renderResult(w http.ResponseWriter, res callbackResult) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := map[string]any{
		"Origin": h.cfg.PortalOrigin,
		"Payload": template.JS(mustJSON(map[string]string{
			"type":   "kedge-github-oauth",
			"state":  res.State,
			"token":  res.Token,
			"login":  res.Login,
			"scopes": res.Scopes,
			"error":  res.Error,
		})),
	}
	_ = callbackTmpl.Execute(w, data)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var callbackTmpl = template.Must(template.New("cb").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Connecting…</title></head>
<body style="font-family:system-ui,sans-serif;background:#0d0d14;color:#eeeef3;display:flex;align-items:center;justify-content:center;height:100vh;margin:0">
<p id="m">Completing GitHub connection…</p>
<script>
(function(){
  var payload = {{ .Payload }};
  try {
    // {{ .Origin }} is rendered by html/template as a quoted JS string literal.
    if (window.opener) { window.opener.postMessage(payload, {{ .Origin }}); }
  } catch (e) {}
  document.getElementById('m').textContent = payload.error ? ('Failed: ' + payload.error) : 'Connected — you can close this window.';
  setTimeout(function(){ window.close(); }, payload.error ? 4000 : 600);
})();
</script>
</body></html>`))
