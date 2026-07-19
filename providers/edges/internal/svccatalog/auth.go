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

package svccatalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/faroshq/provider-edges/internal/haclient"
)

// Apply configures authentication for one outgoing request to a service of the
// given definition, mutating header and query in place. It first sets the
// definition's always-sent ExtraHeaders, then applies the credential per
// def.Auth. token is the raw Secret "token" value (a single string; multi-field
// credentials such as qBittorrent's are packed as "username:password" — see
// CredentialModel.Packing). An empty token is a no-op for optional/none auth.
//
// For the session-login kinds (qBittorrent, Pi-hole) Apply performs the login
// round-trip through the dialer, so a returned error for those kinds usually
// means the credential was rejected rather than a transport failure.
func Apply(ctx context.Context, dialer haclient.Dialer, target haclient.Target, def Definition, token string, header http.Header, query url.Values) error {
	for k, v := range def.ExtraHeaders {
		header.Set(k, v)
	}
	if token == "" {
		return nil
	}
	switch def.Auth {
	case AuthBearer:
		header.Set("Authorization", "Bearer "+token)
	case AuthAPIKeyHeader:
		header.Set(def.AuthParam, token)
	case AuthAPIKeyQuery:
		query.Set(def.AuthParam, token)
	case AuthBasic:
		header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(token)))
	case AuthProxmox:
		header.Set("Authorization", "PVEAPIToken="+token)
	case AuthQBittorrent:
		sid, err := qbitLogin(ctx, dialer, target, token)
		if err != nil {
			return fmt.Errorf("qBittorrent login failed: %w", err)
		}
		header.Set("Cookie", "SID="+sid)
		header.Set("Referer", target.SvcTarget())
	case AuthPihole:
		sid, err := piholeLogin(ctx, dialer, target, token)
		if err != nil {
			return fmt.Errorf("Pi-hole login failed: %w", err)
		}
		header.Set("X-FTL-SID", sid)
	case AuthNone:
		// no credential is applied
	}
	return nil
}

// qbitLogin performs qBittorrent's cookie login and returns the SID. cred is
// "username:password" (the WebUI credentials). Returns an error if the app
// rejects the login (wrong creds, or CSRF/host-header protection — whitelist the
// edge or disable those checks in qBittorrent).
func qbitLogin(ctx context.Context, dialer haclient.Dialer, target haclient.Target, cred string) (string, error) {
	user, pass, ok := strings.Cut(cred, ":")
	if !ok {
		return "", fmt.Errorf("qBittorrent credential must be \"username:password\"")
	}
	form := url.Values{"username": {user}, "password": {pass}}
	header := http.Header{
		"Content-Type": {"application/x-www-form-urlencoded"},
		"Referer":      {target.SvcTarget()},
	}
	resp, err := haclient.DoWith(ctx, dialer, target, http.MethodPost, "/api/v2/auth/login", header, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), "Fails") {
		return "", fmt.Errorf("login rejected (%d): %s", resp.StatusCode, snippet(body))
	}
	for _, c := range resp.Cookies() {
		if c.Name == "SID" {
			return c.Value, nil
		}
	}
	return "", fmt.Errorf("no SID cookie in login response")
}

// piholeLogin performs Pi-hole v6's session login and returns the SID. cred is
// the web-interface password. Returns an error if the app rejects it (wrong
// password, or the API session limit is hit).
func piholeLogin(ctx context.Context, dialer haclient.Dialer, target haclient.Target, password string) (string, error) {
	header := http.Header{"Content-Type": {"application/json"}}
	reqBody, err := json.Marshal(map[string]string{"password": password})
	if err != nil {
		return "", err
	}
	resp, err := haclient.DoWith(ctx, dialer, target, http.MethodPost, "/api/auth", header, strings.NewReader(string(reqBody)))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login rejected (%d): %s", resp.StatusCode, snippet(body))
	}
	var out struct {
		Session struct {
			Valid bool   `json:"valid"`
			SID   string `json:"sid"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode login response: %w", err)
	}
	if !out.Session.Valid || out.Session.SID == "" {
		return "", fmt.Errorf("login not valid: %s", snippet(body))
	}
	return out.Session.SID, nil
}

// snippet trims a byte slice for inclusion in an error message.
func snippet(b []byte) string {
	const max = 240
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
