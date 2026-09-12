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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	cliauth "github.com/faroshq/faros/pkg/cli/auth"
)

// whoamiView is what 'faros whoami' resolves. It is also the -o json shape.
type whoamiView struct {
	Hub       string `json:"hub"`
	Context   string `json:"context"`
	User      string `json:"user,omitempty"`
	Auth      string `json:"auth"` // oidc | static-token | other
	TokenInfo string `json:"tokenInfo,omitempty"`
	// TokenExpiresAt is the cached OIDC token expiry, when known.
	TokenExpiresAt *time.Time `json:"tokenExpiresAt,omitempty"`
	CanRefresh     bool       `json:"canRefresh"`

	Org           *orgView       `json:"org,omitempty"`
	Workspace     *workspaceView `json:"workspace,omitempty"`
	Cluster       string         `json:"cluster,omitempty"`
	TenantMessage string         `json:"tenantMessage,omitempty"`

	// KubectlTarget describes what the kubeconfig's current context points
	// at: the hub workspace, an edge, or something else.
	KubectlContext string `json:"kubectlContext"`
	KubectlTarget  string `json:"kubectlTarget"`

	Orgs []orgView `json:"orgs,omitempty"`
}

func newWhoamiCommand() *cobra.Command {
	output := newOutputFlags(outputJSON, outputYAML)
	cmd := &cobra.Command{
		Use:     "whoami",
		Aliases: []string{"status"},
		Short:   "Show who you are logged in as, and where kubectl points",
		Long: `Print the hub, your identity and token state, the active organization
and workspace with your role in each, and which cluster the current kubectl
context targets (the hub workspace or a connected edge).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := resolveWhoami(cmdContext(cmd))
			if err != nil {
				return err
			}
			if output.structured() {
				return output.printStructured(cmd.OutOrStdout(), v)
			}
			printWhoami(cmd.OutOrStdout(), v)
			return nil
		},
	}
	output.addFlag(cmd)
	return cmd
}

func resolveWhoami(ctx context.Context) (*whoamiView, error) {
	raw, _, err := loadRawKubeconfig()
	if err != nil {
		return nil, err
	}
	s, err := openHubSession()
	if err != nil {
		return nil, err
	}
	v := &whoamiView{Hub: s.Hub, Context: s.Context, Cluster: s.Cluster}
	v.KubectlContext, v.KubectlTarget = describeKubectlTarget(raw)

	// Identity and token state come from the kubeconfig credentials.
	if kctx := raw.Contexts[s.Context]; kctx != nil {
		describeAuth(v, raw.AuthInfos[kctx.AuthInfo])
	}
	if token, err := s.bearerToken(ctx); err == nil {
		if claims := jwtClaims(token); claims != nil {
			if v.User == "" {
				v.User = firstNonEmpty(claims.Email, claims.PreferredUsername, claims.Name, claims.Subject)
			}
			if claims.Expiry > 0 && v.TokenExpiresAt == nil {
				t := time.Unix(claims.Expiry, 0)
				v.TokenExpiresAt = &t
			}
		}
	}

	// Memberships: every org with the caller's role, and the active tenant.
	orgs, err := s.fetchOrgs(ctx)
	if err != nil {
		return nil, err
	}
	v.Orgs = orgs
	if v.User == "" {
		for _, o := range orgs {
			if o.Personal {
				v.User = o.DisplayName
				break
			}
		}
	}
	if err := s.resolveTenant(ctx, hubTarget{}); err != nil {
		v.TenantMessage = err.Error()
	} else {
		org, ws := s.Org, s.WS
		v.Org, v.Workspace = &org, &ws
	}
	return v, nil
}

// describeKubectlTarget explains the kubeconfig's current context.
func describeKubectlTarget(raw *clientcmdapi.Config) (string, string) {
	cur := raw.CurrentContext
	switch {
	case cur == "":
		return "", "none (no current context)"
	case cur == farosContextName:
		return cur, "hub workspace"
	case isEdgeContext(cur):
		return cur, "edge " + strings.TrimPrefix(cur, edgeContextPrefix)
	}
	if kctx := raw.Contexts[cur]; kctx != nil {
		if cl := raw.Clusters[kctx.Cluster]; cl != nil {
			return cur, "other (" + cl.Server + ")"
		}
	}
	return cur, "other"
}

// describeAuth fills the auth mode and, for OIDC, the token cache state.
func describeAuth(v *whoamiView, auth *clientcmdapi.AuthInfo) {
	switch {
	case auth == nil:
		v.Auth = "none"
	case auth.Exec != nil:
		v.Auth = "oidc"
		issuer, clientID := execOIDCArgs(auth.Exec)
		if issuer == "" || clientID == "" {
			v.TokenInfo = "exec plugin without OIDC arguments"
			return
		}
		cache, err := cliauth.LoadTokenCache(issuer, clientID)
		if err != nil {
			v.TokenInfo = "no cached token (run 'faros login')"
			return
		}
		t := time.Unix(cache.ExpiresAt, 0)
		v.TokenExpiresAt = &t
		v.CanRefresh = cache.RefreshToken != ""
		if !v.CanRefresh {
			v.TokenInfo = "no refresh token cached; log in again when the token expires"
		}
	case auth.Token != "" || auth.TokenFile != "":
		v.Auth = "static-token"
	default:
		v.Auth = "other"
	}
}

// execOIDCArgs extracts the issuer and client id from the exec plugin args
// 'faros login' wrote (--oidc-issuer-url=… --oidc-client-id=…).
func execOIDCArgs(exec *clientcmdapi.ExecConfig) (issuer, clientID string) {
	for i := 0; i < len(exec.Args); i++ {
		a := exec.Args[i]
		val := func(flag string) (string, bool) {
			if v, ok := strings.CutPrefix(a, flag+"="); ok {
				return v, true
			}
			if a == flag && i+1 < len(exec.Args) {
				i++
				return exec.Args[i], true
			}
			return "", false
		}
		if v, ok := val("--oidc-issuer-url"); ok {
			issuer = v
		} else if v, ok := val("--oidc-client-id"); ok {
			clientID = v
		}
	}
	return issuer, clientID
}

// jwtPayload is the subset of ID token claims whoami shows.
type jwtPayload struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	Expiry            int64  `json:"exp"`
}

// jwtClaims decodes the payload of a JWT without verifying it (the hub
// verifies; here it only labels the session). Non-JWT bearers return nil.
func jwtClaims(token string) *jwtPayload {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims jwtPayload
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return &claims
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func printWhoami(w io.Writer, v *whoamiView) {
	p := func(label, value string) { _, _ = fmt.Fprintf(w, "%-12s%s\n", label+":", formatStringOrDash(value)) }
	p("Hub", v.Hub)
	p("User", v.User)
	auth := v.Auth
	if v.TokenExpiresAt != nil {
		switch {
		case time.Now().After(*v.TokenExpiresAt):
			auth += ", token expired " + formatAge(*v.TokenExpiresAt) + " ago"
		default:
			auth += ", token valid for " + formatDuration(time.Until(*v.TokenExpiresAt))
		}
		if v.Auth == "oidc" {
			if v.CanRefresh {
				auth += " (auto-refresh)"
			} else {
				auth += " (no refresh token)"
			}
		}
	}
	if v.TokenInfo != "" {
		auth += "; " + v.TokenInfo
	}
	p("Auth", auth)
	if v.Org != nil {
		p("Org", fmt.Sprintf("%s (%s) — role: %s", displayLabel(v.Org.DisplayName, v.Org.UUID), v.Org.UUID, formatStringOrDash(v.Org.Role)))
	} else {
		p("Org", "")
	}
	if v.Workspace != nil {
		p("Workspace", fmt.Sprintf("%s (%s) — role: %s", displayLabel(v.Workspace.DisplayName, v.Workspace.UUID), v.Workspace.UUID, formatStringOrDash(v.Workspace.Role)))
	} else {
		p("Workspace", "")
	}
	p("Cluster", v.Cluster)
	if v.TenantMessage != "" {
		_, _ = fmt.Fprintf(w, "%-12s%s\n", "Note:", v.TenantMessage)
	}
	if v.KubectlContext != "" {
		p("kubectl", fmt.Sprintf("context %q → %s", v.KubectlContext, v.KubectlTarget))
	} else {
		p("kubectl", v.KubectlTarget)
	}
	if len(v.Orgs) > 1 {
		_, _ = fmt.Fprintln(w, "Organizations:")
		for _, o := range v.Orgs {
			mark := " "
			if v.Org != nil && o.UUID == v.Org.UUID {
				mark = "*"
			}
			_, _ = fmt.Fprintf(w, "  %s %s (%s)\n", mark, displayLabel(o.DisplayName, o.UUID), formatStringOrDash(o.Role))
		}
	}
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
