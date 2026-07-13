// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// OAuth connections: instead of pasting a PAT, the user brings an OAuth app
// (client id/secret), clicks Connect, authorizes at the provider, and the
// callback stores access + refresh tokens in the connection Secret — under
// the same "token" key the tool families already read, so GitHub/MCP tools
// work unchanged. The background loop refreshes tokens before expiry.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/llm"
)

// oauthPreset holds a provider's endpoints and quirks.
type oauthPreset struct {
	AuthorizeURL string
	TokenURL     string
	// ScopeSep joins requested scopes (Slack uses commas).
	ScopeSep string
	// ExtraAuthParams are appended to the authorize URL (e.g. Google's
	// offline access).
	ExtraAuthParams url.Values
}

var oauthPresets = map[string]oauthPreset{
	"github": {
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		ScopeSep:     " ",
	},
	"google": {
		AuthorizeURL:    "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:        "https://oauth2.googleapis.com/token",
		ScopeSep:        " ",
		ExtraAuthParams: url.Values{"access_type": {"offline"}, "prompt": {"consent"}, "response_type": {"code"}},
	},
	"slack": {
		AuthorizeURL: "https://slack.com/oauth/v2/authorize",
		TokenURL:     "https://slack.com/api/oauth.v2.access",
		ScopeSep:     ",",
	},
}

// resolvePreset returns the preset for a connection, honoring spec overrides
// (self-hosted GitHub Enterprise etc.).
func resolvePreset(conn *agentsv1alpha1.Connection) (oauthPreset, error) {
	if conn.Spec.OAuth == nil {
		return oauthPreset{}, fmt.Errorf("connection has no oauth configuration")
	}
	p, ok := oauthPresets[conn.Spec.OAuth.Provider]
	if !ok {
		return oauthPreset{}, fmt.Errorf("unsupported oauth provider %q", conn.Spec.OAuth.Provider)
	}
	if v := strings.TrimSpace(conn.Spec.OAuth.AuthorizeURL); v != "" {
		p.AuthorizeURL = v
	}
	if v := strings.TrimSpace(conn.Spec.OAuth.TokenURL); v != "" {
		p.TokenURL = v
	}
	return p, nil
}

// oauthState is the signed round-trip payload for the authorize→callback hop.
type oauthState struct {
	Cluster    string `json:"c"`
	Connection string `json:"n"`
	Redirect   string `json:"r"`
	Expiry     int64  `json:"e"`
}

func (s *Server) encodeOAuthState(st oauthState) (string, error) {
	key := s.webhookKeyBytes()
	if len(key) == 0 {
		return "", fmt.Errorf("state signing unavailable — set KEDGE_PROVIDER_KUBECONFIG or AGENTS_WEBHOOK_KEY")
	}
	raw, _ := json.Marshal(st)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))[:32], nil
}

func (s *Server) decodeOAuthState(state string) (oauthState, error) {
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		return oauthState{}, fmt.Errorf("malformed state")
	}
	key := s.webhookKeyBytes()
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0]))
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))[:32]), []byte(parts[1])) {
		return oauthState{}, fmt.Errorf("invalid state signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return oauthState{}, err
	}
	var st oauthState
	if err := json.Unmarshal(raw, &st); err != nil {
		return oauthState{}, err
	}
	if time.Now().Unix() > st.Expiry {
		return oauthState{}, fmt.Errorf("state expired — restart the connect flow")
	}
	return st, nil
}

// oauthAuthorize builds the provider authorize URL for a connection. The
// portal opens it; the provider redirects back to our public callback.
func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	conn, err := c.Connections().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	if conn.Spec.Auth != "oauth" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "connection is not auth: oauth")
		return
	}
	preset, err := resolvePreset(conn)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	var req enableInboundRequest // reuse: carries publicBaseURL
	_ = json.NewDecoder(r.Body).Decode(&req)
	base := strings.TrimRight(strings.TrimSpace(req.PublicBaseURL), "/")
	if base == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "publicBaseURL is required (the portal origin)")
		return
	}
	clientID := s.connectionSecretValue(r, c, name, "client_id")
	if clientID == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "connection secret has no client_id — recreate the connection with your OAuth app credentials")
		return
	}
	redirect := base + "/services/providers/agents/oauth/callback"
	state, err := s.encodeOAuthState(oauthState{
		Cluster: id.clusterID, Connection: name, Redirect: redirect,
		Expiry: time.Now().Add(15 * time.Minute).Unix(),
	})
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", err.Error())
		return
	}
	q := url.Values{
		"client_id":    {clientID},
		"redirect_uri": {redirect},
		"state":        {state},
	}
	if sc := conn.Spec.OAuth.Scopes; len(sc) > 0 {
		q.Set("scope", strings.Join(sc, preset.ScopeSep))
	}
	for k, vs := range preset.ExtraAuthParams {
		for _, v := range vs {
			q.Set(k, v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"authorizeURL": preset.AuthorizeURL + "?" + q.Encode()})
}

// oauthCallback completes the flow: verifies state, exchanges the code, and
// stores tokens in the connection Secret via the virtual workspace (this
// route is anonymous — the signed state is the auth).
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	if s.bg == nil || s.bg.vwURL == "" {
		http.Error(w, "background executor is not running on this provider", http.StatusServiceUnavailable)
		return
	}
	st, err := s.decodeOAuthState(r.URL.Query().Get("state"))
	if err != nil {
		http.Error(w, "invalid state: "+err.Error(), http.StatusForbidden)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	dyn, err := s.bg.scoped(st.Cluster)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cu, err := dyn.Resource(agentsclient.ConnectionGVR).Get(r.Context(), st.Connection, metav1.GetOptions{})
	if err != nil {
		http.Error(w, "connection not found", http.StatusNotFound)
		return
	}
	conn, err := fromU[agentsv1alpha1.Connection](cu)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	preset, err := resolvePreset(conn)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sec, err := (vwSecrets{dyn}).GetSecret(r.Context(), llm.SecretNamespace, connectionSecretName(st.Connection))
	if err != nil {
		http.Error(w, "connection secret unavailable", http.StatusInternalServerError)
		return
	}
	clientID, clientSecret := string(sec.Data["client_id"]), string(sec.Data["client_secret"])

	tok, err := exchangeOAuthCode(r.Context(), preset.TokenURL, clientID, clientSecret, code, st.Redirect)
	if err != nil {
		http.Error(w, "token exchange failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Merge tokens into the Secret under the keys the tool families read.
	updates := map[string]string{"token": tok.AccessToken}
	if tok.RefreshToken != "" {
		updates["refresh_token"] = tok.RefreshToken
	}
	if tok.ExpiresIn > 0 {
		updates["expiry"] = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	if err := updateSecretKeys(r.Context(), dyn, connectionSecretName(st.Connection), updates); err != nil {
		http.Error(w, "storing tokens: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Best-effort status update.
	status := map[string]any{"oauthConnected": true}
	if v, ok := updates["expiry"]; ok {
		status["tokenExpiresAt"] = v
	}
	obj := cu.DeepCopy()
	stMap, _, _ := unstructured.NestedMap(obj.Object, "status")
	if stMap == nil {
		stMap = map[string]any{}
	}
	for k, v := range status {
		stMap[k] = v
	}
	_ = unstructured.SetNestedMap(obj.Object, stMap, "status")
	_, _ = dyn.Resource(agentsclient.ConnectionGVR).UpdateStatus(r.Context(), obj, metav1.UpdateOptions{})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<html><body style="font-family:sans-serif;text-align:center;margin-top:20vh">
		<h2>✅ %s connected</h2><p>You can close this tab and return to kedge.</p></body></html>`,
		conn.Spec.OAuth.Provider)
}

type oauthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
	// Slack nests errors differently.
	OK *bool `json:"ok,omitempty"`
}

func exchangeOAuthCode(ctx context.Context, tokenURL, clientID, clientSecret, code, redirect string) (oauthToken, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"redirect_uri":  {redirect},
	}
	return postOAuthForm(ctx, tokenURL, form)
}

func refreshOAuthToken(ctx context.Context, tokenURL, clientID, clientSecret, refreshToken string) (oauthToken, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshToken},
	}
	return postOAuthForm(ctx, tokenURL, form)
}

func postOAuthForm(ctx context.Context, tokenURL string, form url.Values) (oauthToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return oauthToken{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	var tok oauthToken
	if err := json.Unmarshal(raw, &tok); err != nil {
		return oauthToken{}, fmt.Errorf("parsing token response (HTTP %d)", resp.StatusCode)
	}
	if tok.Error != "" {
		return oauthToken{}, fmt.Errorf("%s: %s", tok.Error, tok.ErrorDesc)
	}
	if tok.OK != nil && !*tok.OK {
		return oauthToken{}, fmt.Errorf("provider rejected the exchange")
	}
	if tok.AccessToken == "" {
		return oauthToken{}, fmt.Errorf("no access_token in response (HTTP %d)", resp.StatusCode)
	}
	return tok, nil
}

// updateSecretKeys merges keys into an existing tenant Secret via the VW.
func updateSecretKeys(ctx context.Context, dyn dynamic.Interface, name string, updates map[string]string) error {
	u, err := dyn.Resource(agentsclient.SecretGVR).Namespace(llm.SecretNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	data, _, _ := unstructured.NestedMap(u.Object, "data")
	if data == nil {
		data = map[string]any{}
	}
	for k, v := range updates {
		data[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	if err := unstructured.SetNestedMap(u.Object, data, "data"); err != nil {
		return err
	}
	_, err = dyn.Resource(agentsclient.SecretGVR).Namespace(llm.SecretNamespace).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

// refreshOAuthTokens renews expiring OAuth connection tokens (called from the
// background tick). Only connections with a refresh_token and an expiry within
// the horizon are touched.
func (b *background) refreshOAuthTokens(ctx context.Context) {
	list, err := b.wildcard.Resource(agentsclient.ConnectionGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	for i := range list.Items {
		item := &list.Items[i]
		conn, err := fromU[agentsv1alpha1.Connection](item)
		if err != nil || conn.Spec.Auth != "oauth" || conn.Spec.OAuth == nil {
			continue
		}
		cluster := item.GetAnnotations()["kcp.io/cluster"]
		dyn, err := b.scoped(cluster)
		if err != nil {
			continue
		}
		sec, err := (vwSecrets{dyn}).GetSecret(ctx, llm.SecretNamespace, connectionSecretName(conn.Name))
		if err != nil {
			continue
		}
		refresh := string(sec.Data["refresh_token"])
		expiryRaw := string(sec.Data["expiry"])
		if refresh == "" || expiryRaw == "" {
			continue // nothing to refresh (e.g. Slack bot tokens don't expire)
		}
		expiry, err := time.Parse(time.RFC3339, expiryRaw)
		if err != nil || time.Until(expiry) > 15*time.Minute {
			continue
		}
		preset, err := resolvePreset(conn)
		if err != nil {
			continue
		}
		tok, err := refreshOAuthToken(ctx, preset.TokenURL, string(sec.Data["client_id"]), string(sec.Data["client_secret"]), refresh)
		if err != nil {
			log.Printf("background: oauth refresh for %s/%s failed: %v", cluster, conn.Name, err)
			continue
		}
		updates := map[string]string{"token": tok.AccessToken}
		if tok.RefreshToken != "" {
			updates["refresh_token"] = tok.RefreshToken
		}
		if tok.ExpiresIn > 0 {
			updates["expiry"] = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
		}
		if err := updateSecretKeys(ctx, dyn, connectionSecretName(conn.Name), updates); err != nil {
			log.Printf("background: storing refreshed token for %s/%s: %v", cluster, conn.Name, err)
			continue
		}
		log.Printf("background: refreshed oauth token for %s/%s", cluster, conn.Name)
	}
}

// connectionSecretValue reads one key from a connection's Secret as the caller.
func (s *Server) connectionSecretValue(r *http.Request, c *agentsclient.Client, name, key string) string {
	sec, err := c.GetSecret(r.Context(), llm.SecretNamespace, connectionSecretName(name))
	if err != nil {
		return ""
	}
	if v, ok := sec.Data[key]; ok {
		return string(v)
	}
	return ""
}
