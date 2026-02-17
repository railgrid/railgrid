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

package proxy

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	oidc "github.com/coreos/go-oidc"
	"golang.org/x/oauth2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

// KCPProxy is a reverse proxy that authenticates requests via OIDC
// and forwards them to the user's dedicated kcp tenant workspace.
type KCPProxy struct {
	kcpTarget *url.URL
	transport http.RoundTripper // built from kcp admin config; handles TLS + auth
	verifier  *oidc.IDTokenVerifier
	verifyCtx context.Context // context with HTTP client for OIDC key fetches
	kedgeClient *kedgeclient.Client
	logger      klog.Logger
}

// NewKCPProxy creates a reverse proxy to kcp.
// It validates bearer tokens as OIDC id_tokens before proxying.
func NewKCPProxy(kcpConfig *rest.Config, verifier *oidc.IDTokenVerifier, kedgeClient *kedgeclient.Client, devMode bool) (*KCPProxy, error) {
	target, err := url.Parse(kcpConfig.Host)
	if err != nil {
		return nil, err
	}

	// Build transport from the kcp admin rest.Config so that all auth methods
	// (client certificates, bearer tokens, token files, exec plugins) are
	// handled automatically. In dev mode with no explicit CA, skip TLS verify.
	transportConfig := rest.CopyConfig(kcpConfig)
	if devMode {
		if len(transportConfig.TLSClientConfig.CAData) == 0 && transportConfig.TLSClientConfig.CAFile == "" {
			transportConfig.TLSClientConfig.Insecure = true
		}
	}
	transport, err := rest.TransportFor(transportConfig)
	if err != nil {
		return nil, fmt.Errorf("building kcp transport: %w", err)
	}

	// Build a context with an insecure HTTP client for OIDC key fetches.
	verifyCtx := context.Background()
	if devMode {
		insecureClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode only
			},
		}
		verifyCtx = context.WithValue(verifyCtx, oauth2.HTTPClient, insecureClient)
	}

	return &KCPProxy{
		kcpTarget:   target,
		transport:   transport,
		verifier:    verifier,
		verifyCtx:   verifyCtx,
		kedgeClient: kedgeClient,
		logger:      klog.Background().WithName("kcp-proxy"),
	}, nil
}

// ServeHTTP validates the bearer token and proxies the request to kcp.
// Two token types are supported:
//   - OIDC id_tokens (from Dex): resolved to a tenant workspace via User CRD lookup,
//     forwarded with admin credentials.
//   - kcp ServiceAccount tokens: the clusterName claim identifies the workspace,
//     forwarded with the original SA token so kcp handles authn/authz natively.
func (p *KCPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract bearer token.
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeUnauthorized(w)
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	// Try OIDC verification first (user tokens from Dex).
	idToken, err := p.verifier.Verify(p.verifyCtx, token)
	if err == nil {
		p.serveOIDC(w, r, token, idToken)
		return
	}

	// If OIDC fails, check if this is a kcp ServiceAccount token.
	if saClaims, ok := parseServiceAccountToken(token); ok {
		p.serveServiceAccount(w, r, token, saClaims.ClusterName)
		return
	}

	writeUnauthorized(w)
}

// serveOIDC handles OIDC-authenticated requests by resolving the user's tenant
// workspace and proxying with admin credentials.
//
// Two path formats are supported:
//   - /clusters/{logicalClusterName}/... — kcp-syntax (new kubeconfigs). The
//     cluster ID is verified against user.spec.defaultCluster.
//   - /api/... or /apis/... — bare path (legacy kubeconfigs). The workspace
//     path is constructed from the userID.
func (p *KCPProxy) serveOIDC(w http.ResponseWriter, r *http.Request, token string, idToken *oidc.IDToken) {
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := idToken.Claims(&claims); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"failed to parse token claims","reason":"InternalError","code":500}`)
		return
	}

	user, err := p.resolveUser(r.Context(), idToken.Issuer, claims.Sub)
	if err != nil {
		p.logger.Error(err, "failed to resolve user workspace", "sub", claims.Sub)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"user workspace not found","reason":"Forbidden","code":403}`)
		return
	}

	// Determine kcp path based on incoming request format.
	var kcpPath string
	if strings.HasPrefix(r.URL.Path, "/clusters/") {
		// kcp-syntax: /clusters/{logicalClusterName}/api/...
		rest := strings.TrimPrefix(r.URL.Path, "/clusters/")
		slashIdx := strings.Index(rest, "/")
		clusterID := rest
		if slashIdx >= 0 {
			clusterID = rest[:slashIdx]
		}
		// Allow exact match or mount access ({clusterName}:{mountName}).
		if clusterID != user.Spec.DefaultCluster && !strings.HasPrefix(clusterID, user.Spec.DefaultCluster+":") {
			p.logger.Info("cluster access denied", "user", user.Name, "requested", clusterID, "allowed", user.Spec.DefaultCluster)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"cluster access denied","reason":"Forbidden","code":403}`)
			return
		}
		kcpPath = r.URL.Path // already in /clusters/{id}/... format
	}

	target := *p.kcpTarget
	logger := p.logger

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = kcpPath
			req.Host = target.Host

			// Remove user auth — the transport adds kcp admin credentials
			// automatically (client certs, bearer token, etc.).
			req.Header.Del("Authorization")
		},
		Transport: p.transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error(err, "proxy upstream error", "method", r.Method, "path", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"proxy error: %s","reason":"ServiceUnavailable","code":502}`, err.Error())
		},
	}

	proxy.ServeHTTP(w, r)
}

// serveServiceAccount handles kcp ServiceAccount tokens by forwarding the
// request to the workspace identified by the clusterName claim, keeping the
// original SA token so kcp performs native authn/authz.
func (p *KCPProxy) serveServiceAccount(w http.ResponseWriter, r *http.Request, token, clusterName string) {
	target := *p.kcpTarget
	logger := p.logger

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = "/clusters/" + clusterName + req.URL.Path
			req.Host = target.Host

			// Keep the SA token — kcp authenticates it natively.
			req.Header.Set("Authorization", "Bearer "+token)
		},
		Transport: p.transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error(err, "proxy upstream error (SA)", "method", r.Method, "path", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"proxy error: %s","reason":"ServiceUnavailable","code":502}`, err.Error())
		},
	}

	proxy.ServeHTTP(w, r)
}

// saTokenClaims holds the claims we extract from a kcp ServiceAccount JWT.
type saTokenClaims struct {
	Issuer      string `json:"iss"`
	ClusterName string `json:"kubernetes.io/serviceaccount/clusterName"`
}

// parseServiceAccountToken decodes a JWT without signature verification and
// checks whether it is a kcp ServiceAccount token. kcp will verify the
// signature when the request is forwarded.
func parseServiceAccountToken(token string) (saTokenClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return saTokenClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return saTokenClaims{}, false
	}
	var claims saTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return saTokenClaims{}, false
	}
	if claims.Issuer != "kubernetes/serviceaccount" || claims.ClusterName == "" {
		return saTokenClaims{}, false
	}
	return claims, true
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"Unauthorized","reason":"Unauthorized","code":401}`)
}

// resolveUser looks up the User CRD by OIDC issuer+sub hash and returns the full User object.
func (p *KCPProxy) resolveUser(ctx context.Context, issuer, sub string) (*tenancyv1alpha1.User, error) {
	hash := sha256.Sum256([]byte(issuer + "/" + sub))
	subHash := hex.EncodeToString(hash[:])[:63]

	labelSelector := fmt.Sprintf("kedge.faros.sh/sub=%s", subHash)
	users, err := p.kedgeClient.Users().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	if len(users.Items) == 0 {
		return nil, fmt.Errorf("no user found for sub hash %s", subHash)
	}
	return &users.Items[0], nil
}
