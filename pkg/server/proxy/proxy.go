package proxy

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	oidc "github.com/coreos/go-oidc"
	"golang.org/x/oauth2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

// KCPProxy is a reverse proxy that authenticates requests via OIDC
// and forwards them to the user's dedicated KCP tenant workspace.
type KCPProxy struct {
	kcpTarget   *url.URL
	transport   http.RoundTripper
	bearerToken string
	verifier    *oidc.IDTokenVerifier
	verifyCtx   context.Context // context with HTTP client for OIDC key fetches
	kedgeClient *kedgeclient.Client
	logger      klog.Logger
}

// NewKCPProxy creates a reverse proxy to KCP.
// It validates bearer tokens as OIDC id_tokens before proxying.
func NewKCPProxy(kcpConfig *rest.Config, verifier *oidc.IDTokenVerifier, kedgeClient *kedgeclient.Client, devMode bool) (*KCPProxy, error) {
	target, err := url.Parse(kcpConfig.Host)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Load CA data from KCP config for TLS verification.
	// Handle both CAData (inline) and CAFile (file path).
	caData := kcpConfig.TLSClientConfig.CAData
	if len(caData) == 0 && kcpConfig.TLSClientConfig.CAFile != "" {
		var err error
		caData, err = os.ReadFile(kcpConfig.TLSClientConfig.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading KCP CA file %s: %w", kcpConfig.TLSClientConfig.CAFile, err)
		}
	}

	if len(caData) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return nil, fmt.Errorf("failed to parse KCP CA certificate")
		}
		tlsConfig.RootCAs = pool
	} else if kcpConfig.TLSClientConfig.Insecure || devMode {
		tlsConfig.InsecureSkipVerify = true //nolint:gosec // explicitly configured
	}

	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
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
		bearerToken: kcpConfig.BearerToken,
		verifier:    verifier,
		verifyCtx:   verifyCtx,
		kedgeClient: kedgeClient,
		logger:      klog.Background().WithName("kcp-proxy"),
	}, nil
}

// ServeHTTP validates the bearer token and proxies the request to KCP.
// Two token types are supported:
//   - OIDC id_tokens (from Dex): resolved to a tenant workspace via User CRD lookup,
//     forwarded with admin credentials.
//   - KCP ServiceAccount tokens: the clusterName claim identifies the workspace,
//     forwarded with the original SA token so KCP handles authn/authz natively.
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

	// If OIDC fails, check if this is a KCP ServiceAccount token.
	if saClaims, ok := parseServiceAccountToken(token); ok {
		p.serveServiceAccount(w, r, token, saClaims.ClusterName)
		return
	}

	writeUnauthorized(w)
}

// serveOIDC handles OIDC-authenticated requests by resolving the user's tenant
// workspace and proxying with admin credentials.
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

	userID, err := p.resolveUserID(r.Context(), idToken.Issuer, claims.Sub)
	if err != nil {
		p.logger.Error(err, "failed to resolve user workspace", "sub", claims.Sub)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"user workspace not found","reason":"Forbidden","code":403}`)
		return
	}

	tenantClusterPath := "root:kedge:tenants:" + userID

	target := *p.kcpTarget
	bearerToken := p.bearerToken
	logger := p.logger

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = "/clusters/" + tenantClusterPath + req.URL.Path
			req.Host = target.Host

			// Replace user auth with KCP admin credentials.
			req.Header.Del("Authorization")
			if bearerToken != "" {
				req.Header.Set("Authorization", "Bearer "+bearerToken)
			}
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

// serveServiceAccount handles KCP ServiceAccount tokens by forwarding the
// request to the workspace identified by the clusterName claim, keeping the
// original SA token so KCP performs native authn/authz.
func (p *KCPProxy) serveServiceAccount(w http.ResponseWriter, r *http.Request, token, clusterName string) {
	target := *p.kcpTarget
	logger := p.logger

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = "/clusters/" + clusterName + req.URL.Path
			req.Host = target.Host

			// Keep the SA token — KCP authenticates it natively.
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

// saTokenClaims holds the claims we extract from a KCP ServiceAccount JWT.
type saTokenClaims struct {
	Issuer      string `json:"iss"`
	ClusterName string `json:"kubernetes.io/serviceaccount/clusterName"`
}

// parseServiceAccountToken decodes a JWT without signature verification and
// checks whether it is a KCP ServiceAccount token. KCP will verify the
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

// resolveUserID looks up the User CRD by OIDC issuer+sub hash and returns the user's name
// (which is also the KCP tenant workspace name).
func (p *KCPProxy) resolveUserID(ctx context.Context, issuer, sub string) (string, error) {
	hash := sha256.Sum256([]byte(issuer + "/" + sub))
	subHash := hex.EncodeToString(hash[:])[:63]

	labelSelector := fmt.Sprintf("kedge.faros.sh/sub=%s", subHash)
	users, err := p.kedgeClient.Users().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("listing users: %w", err)
	}
	if len(users.Items) == 0 {
		return "", fmt.Errorf("no user found for sub hash %s", subHash)
	}
	return users.Items[0].Name, nil
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
