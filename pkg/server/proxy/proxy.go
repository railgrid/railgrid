package proxy

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
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

// ServeHTTP validates the OIDC token, resolves the user's tenant workspace,
// and proxies the request to KCP.
func (p *KCPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract bearer token.
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"Unauthorized","reason":"Unauthorized","code":401}`)
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	// Verify the OIDC id_token.
	idToken, err := p.verifier.Verify(p.verifyCtx, token)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"Unauthorized","reason":"Unauthorized","code":401}`)
		return
	}

	// Extract sub claim to look up the user's workspace.
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := idToken.Claims(&claims); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"failed to parse token claims","reason":"InternalError","code":500}`)
		return
	}

	// Look up the User CRD to get the userID (workspace name).
	userID, err := p.resolveUserID(r.Context(), idToken.Issuer, claims.Sub)
	if err != nil {
		p.logger.Error(err, "failed to resolve user workspace", "sub", claims.Sub)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"user workspace not found","reason":"Forbidden","code":403}`)
		return
	}

	// Build the target path for the user's tenant workspace.
	tenantClusterPath := "root:kedge:tenants:" + userID

	// Create a per-request reverse proxy targeting the user's workspace.
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
