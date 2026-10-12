/*
Copyright 2026 The Railgrid Authors.

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

// Package accessproxy is the small infrastructure-owned runtime that fronts
// one published template instance. Templates render it as a component of
// their own graph; the shared-Gateway HTTPRoute always points here.
//
// The proxy has two reachability modes and a hard availability contract:
//
//   - public: ordinary app requests are passthrough. The hub is not contacted
//     unless the separate Railgrid Auth option is enabled and one of its
//     reserved identity endpoints is used. A hub outage cannot affect
//     anonymous public requests or valid cached identity sessions.
//   - private: requests need a proxy-local session. A browser without one is
//     redirected to the hub's login-time authorize endpoint exactly once;
//     the proxy exchanges the returned one-use code server-to-server and
//     then maintains its own bounded in-memory session for the granted TTL.
//     A hub outage leaves existing sessions working; only new sign-ins fail.
//     A non-browser request carrying `Authorization: Bearer <app access
//     token>` (minted at the hub for this one instance) is never redirected:
//     the proxy asks the hub to verify it, caches the verdict under the
//     token's SHA-256 for at most 15 minutes, and answers 401/403 itself (see
//     bearer.go). Raw hub tokens are refused locally and never relayed; the
//     app never sees any Authorization header.
//
// Both sign-in flows carry their state in short-lived browser cookies, so they
// complete against any pod. Legacy private-mode sessions are process-local;
// Railgrid Auth sessions are hub-signed assertions, so they survive gate
// restarts and replica changes until their bounded expiry.
//
// The proxy holds no credentials of its own: no service-account token, no
// kubeconfig, no signing keys (a caller's app access token is relayed to the
// hub once and otherwise kept only as a hash). Access policy is kcp RBAC evaluated by the hub
// at sign-in time (SubjectAccessReview on the instance's `access`
// subresource); revocation takes effect within the granted session TTL.
package accessproxy

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	// SessionCookieName is intentionally a __Host cookie: Secure, Path=/, and
	// no Domain attribute are mandatory.  The app proxy never forwards it.
	SessionCookieName = "__Host-railgrid-app-session"
	// IdentitySessionCookieName carries the hub-signed, short-lived identity
	// assertion for Railgrid Auth. The browser never exposes it to JavaScript,
	// and the gate never forwards it to an upstream.
	IdentitySessionCookieName = "__Host-railgrid-app-auth"
	// ReturnCookieName is the prefix for the short-lived cookies that carry the
	// whole sign-in state — nonce, clean return path and expiry — while the
	// browser completes the hub flow. Each authorization attempt gets a suffix
	// derived from its nonce so concurrent attempts cannot overwrite each other,
	// and no server-side handle has to survive between the authorize redirect and
	// the callback (see consumeReturnState).
	//
	// Deliberately NOT a __Host- cookie. The prefix would stop a sibling app
	// under the same published-apps zone from planting one, but browsers
	// declined to store it on the development gateway (self-signed certs on
	// *.apps.127.0.0.1.sslip.io), which fails every local sign-in with "no
	// cookie" at the callback. The prefix is not what makes this state safe:
	// the nonce is compared against the state query parameter, the entry is
	// single-use, and the return path is re-sanitised on the way out, so a
	// planted cookie can only pin a state the planter already controls.
	ReturnCookieName = "railgrid-app-return"
	// CallbackPath is the reserved platform path on the app host. It must stay
	// in lockstep with the hub's appauth.CallbackPath.
	CallbackPath = "/__railgrid/auth/callback"
	// Identity endpoints are reserved only when Railgrid Auth is enabled for
	// this instance. Disabled apps keep their existing public/private routing.
	IdentityLoginPath    = "/__railgrid/auth/login"
	IdentityLogoutPath   = "/__railgrid/auth/logout"
	IdentitySessionPath  = "/__railgrid/auth/session"
	IdentityCallbackPath = "/__railgrid/auth/identity/callback"

	// hubAuthorizePath / hubExchangePath mirror pkg/hub/appauth. The contract
	// is intentionally tiny (two endpoints, standard redirect + JSON), so the
	// infra module does not import hub Go packages.
	hubAuthorizePath = "/auth/apps/authorize"
	hubExchangePath  = "/auth/apps/exchange"
	// hubVerifyPath authorizes a bearer-carrying (non-browser) request; see
	// bearer.go and pkg/hub/appauth/verify.go.
	hubVerifyPath         = "/auth/apps/verify"
	identityIssuerPath    = "/auth/apps/identity"
	identityAuthorizePath = identityIssuerPath + "/authorize"
	identityExchangePath  = identityIssuerPath + "/exchange"
	identityJWKSPath      = identityIssuerPath + "/jwks"

	// clusterLocalSuffix confines upstream targets to in-cluster Services as
	// defense in depth; the template contract already renders only
	// namespace-local Service DNS names.
	clusterLocalSuffix = ".svc.cluster.local"

	defaultListenAddress = ":8080"
	defaultPublicScheme  = "https"
	defaultHubTimeout    = 10 * time.Second

	// maxSessionTTL caps whatever TTL the hub grants on exchange.
	maxSessionTTL = 24 * time.Hour
	// defaultSessionTTL applies when the hub omits a TTL.
	defaultSessionTTL = 15 * time.Minute
)

// Mode selects the enforcement behaviour of the proxy.
type Mode string

const (
	// ModePublic forwards every request; the hub is never contacted.
	ModePublic Mode = "public"
	// ModePrivate requires a proxy-local session established through the hub
	// login-time authorize/exchange flow.
	ModePrivate Mode = "private"
)

// InstanceRef are the resource coordinates of the published template instance
// this proxy fronts. The hub uses them for its SubjectAccessReview; the proxy
// itself never reads the object.
type InstanceRef struct {
	Cluster  string
	Group    string
	Resource string
	Name     string
}

func (ref InstanceRef) validate() error {
	for _, seg := range []struct{ label, v string }{
		{"cluster", ref.Cluster},
		{"group", ref.Group},
		{"resource", ref.Resource},
		{"name", ref.Name},
	} {
		v := strings.TrimSpace(seg.v)
		if v == "" || strings.ContainsAny(v, " \r\n/\\@?#&") {
			return fmt.Errorf("instance %s is malformed", seg.label)
		}
	}
	return nil
}

// Route maps one longest-prefix path to an in-cluster HTTP Service URL.  The
// target may include a base path; the matched request suffix is appended to it.
type Route struct {
	Prefix string
	Target string
}

// Config is the complete runtime contract for one access-proxy process. The
// template graph renders these fields; no app request can alter them at
// runtime.
type Config struct {
	ListenAddress string
	Mode          Mode
	Host          string
	Routes        []Route

	// Instance identifies the published instance for hub authorization.
	// Required in private mode; unused in public mode.
	Instance InstanceRef
	// HubURL is the in-cluster hub origin used for server-to-server identity
	// exchanges and, in private mode, access authorization. Required whenever
	// either feature is enabled.
	HubURL string
	// HubPublicURL is the browser-reachable hub origin used for authorize
	// redirects and as the identity issuer's origin. Empty defaults to HubURL
	// for legacy private mode; Railgrid Auth requires it explicitly.
	HubPublicURL string
	HubInsecure  bool
	PublicScheme string

	// RailgridAuth enables the signed app-identity session endpoints. It is
	// independent of ModePrivate: the existing private access gate still
	// controls who may open the app, while this session identifies users to
	// the app API.
	RailgridAuth    bool
	AuthIssuer      string
	AuthAudience    string
	AuthEnvironment string
	AuthInstanceUID string
	AuthOrigin      string

	// AllowNonClusterTargets disables the cluster-local target confinement.
	// Test seam only; production configuration must never set it.
	AllowNonClusterTargets bool

	// HubClient and Random are test seams.  When nil, New builds an HTTP
	// client with a bounded timeout and optional TLS verification skip.
	HubClient *http.Client
	Random    RandomSource

	// Logf receives the proxy's operational messages — today only the reason a
	// sign-in callback was rejected, which is otherwise invisible because every
	// rejection is the same opaque 400 to the browser. Nil logs through the
	// standard logger with the binary's prefix.
	Logf func(format string, args ...any)

	HubTimeout time.Duration
}

// RandomSource is the minimal source used for state handles and session IDs.
type RandomSource interface {
	Read([]byte) (int, error)
}

type normalizedConfig struct {
	Config
	host         string
	hubURL       string
	hubPublicURL string
	publicScheme string
	authIssuer   string
	authOrigin   string
	routes       []normalizedRoute
	hubClient    *http.Client
	hubTimeout   time.Duration
}

type normalizedRoute struct {
	prefix string
	target *url.URL
}

func normalizeConfig(config Config) (normalizedConfig, error) {
	host, err := canonicalHost(config.Host)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("invalid app host: %w", err)
	}
	mode := config.Mode
	if mode == "" {
		mode = ModePublic
	}
	if mode != ModePublic && mode != ModePrivate {
		return normalizedConfig{}, fmt.Errorf("invalid mode %q: want public or private", mode)
	}

	publicScheme := strings.ToLower(strings.TrimSpace(config.PublicScheme))
	if publicScheme == "" {
		publicScheme = defaultPublicScheme
	}
	if publicScheme != "http" && publicScheme != "https" {
		return normalizedConfig{}, fmt.Errorf("public scheme must be http or https")
	}

	hubURLString := ""
	hubPublicURLString := ""
	if mode == ModePrivate {
		if err := config.Instance.validate(); err != nil {
			return normalizedConfig{}, fmt.Errorf("private mode: %w", err)
		}
		if strings.TrimSpace(config.HubURL) == "" {
			return normalizedConfig{}, fmt.Errorf("private mode: hub URL is required")
		}
		hubURL, err := parseHTTPOrigin(config.HubURL)
		if err != nil {
			return normalizedConfig{}, fmt.Errorf("invalid hub URL: %w", err)
		}
		hubURLString = hubURL
		hubPublicRaw := strings.TrimSpace(config.HubPublicURL)
		if hubPublicRaw == "" {
			hubPublicRaw = hubURL
		}
		hubPublicURL, err := parseHTTPOrigin(hubPublicRaw)
		if err != nil {
			return normalizedConfig{}, fmt.Errorf("invalid public hub URL: %w", err)
		}
		hubPublicURLString = hubPublicURL
	}

	authIssuer := ""
	authOrigin := ""
	if config.RailgridAuth {
		if err := config.Instance.validate(); err != nil {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: %w", err)
		}
		if !isOriginOnly(config.HubURL) {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: hub URL must be an origin without a path, query, or fragment")
		}
		if !isOriginOnly(config.HubPublicURL) {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: public hub URL must be an origin without a path, query, or fragment")
		}
		if strings.TrimSpace(config.HubURL) == "" {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: hub URL is required")
		}
		if strings.TrimSpace(config.HubPublicURL) == "" {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: public hub URL is required")
		}
		if hubURLString == "" {
			hubURL, err := parseHTTPOrigin(config.HubURL)
			if err != nil {
				return normalizedConfig{}, fmt.Errorf("invalid hub URL: %w", err)
			}
			hubURLString = hubURL
		}
		if hubPublicURLString == "" {
			hubPublicURL, err := parseHTTPOrigin(config.HubPublicURL)
			if err != nil {
				return normalizedConfig{}, fmt.Errorf("invalid public hub URL: %w", err)
			}
			hubPublicURLString = hubPublicURL
		}
		audienceParts := strings.Split(config.AuthAudience, "/")
		if strings.TrimSpace(config.AuthAudience) != config.AuthAudience || len(audienceParts) != 2 || audienceParts[0] == "" || audienceParts[1] == "" || strings.ContainsAny(config.AuthAudience, " \t\r\n") || strings.Contains(config.AuthAudience, "\\") {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: audience must be <ProjectUID>/<environment>")
		}
		if strings.TrimSpace(config.AuthEnvironment) == "" || strings.ContainsAny(config.AuthEnvironment, "\r\n") || strings.ContainsAny(config.AuthEnvironment, "/\\") || audienceParts[1] != config.AuthEnvironment {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: environment is required and must be a single path segment")
		}
		if strings.TrimSpace(config.AuthInstanceUID) == "" || strings.TrimSpace(config.AuthInstanceUID) != config.AuthInstanceUID || strings.ContainsAny(config.AuthInstanceUID, "\r\n") {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: tenant instance UID is required")
		}
		if publicScheme != "https" {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: exact app origin must use https")
		}
		issuer := config.AuthIssuer
		if strings.TrimSpace(issuer) != issuer || issuer == "" {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: issuer is required")
		}
		expectedIssuer := hubPublicURLString + identityIssuerPath
		parsedIssuer, err := url.Parse(issuer)
		if err != nil || !strings.EqualFold(parsedIssuer.Scheme, "https") || parsedIssuer.User != nil || parsedIssuer.RawQuery != "" || parsedIssuer.Fragment != "" || issuer != expectedIssuer {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: issuer must exactly match configured public hub issuer %q", expectedIssuer)
		}
		origin := config.AuthOrigin
		if strings.TrimSpace(origin) != origin || origin == "" {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: exact app origin is required")
		}
		parsedOrigin, err := url.Parse(origin)
		if err != nil || parsedOrigin.Scheme == "" || parsedOrigin.Host == "" || parsedOrigin.User != nil || parsedOrigin.Path != "" || parsedOrigin.RawQuery != "" || parsedOrigin.Fragment != "" {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: origin must be an exact origin without path, query, or fragment")
		}
		if parsedOrigin.Scheme != "https" || publicScheme != "https" || !strings.EqualFold(parsedOrigin.Host, host) || origin != "https://"+host {
			return normalizedConfig{}, fmt.Errorf("railgrid auth: origin must match configured app scheme and host")
		}
		authOrigin = origin
		authIssuer = issuer
	}

	if len(config.Routes) == 0 {
		return normalizedConfig{}, fmt.Errorf("at least one proxy route is required")
	}
	routes := make([]normalizedRoute, 0, len(config.Routes))
	seen := make(map[string]struct{}, len(config.Routes))
	for _, route := range config.Routes {
		prefix, err := normalizePrefix(route.Prefix)
		if err != nil {
			return normalizedConfig{}, err
		}
		if _, exists := seen[prefix]; exists {
			return normalizedConfig{}, fmt.Errorf("duplicate proxy route prefix %q", prefix)
		}
		seen[prefix] = struct{}{}
		target, err := url.Parse(strings.TrimSpace(route.Target))
		if err != nil || target.Scheme == "" || target.Host == "" || target.User != nil {
			return normalizedConfig{}, fmt.Errorf("invalid target URL for route %q", prefix)
		}
		if !strings.EqualFold(target.Scheme, "http") && !strings.EqualFold(target.Scheme, "https") {
			return normalizedConfig{}, fmt.Errorf("target URL for route %q must use http or https", prefix)
		}
		if !config.AllowNonClusterTargets && !strings.HasSuffix(strings.ToLower(target.Hostname()), clusterLocalSuffix) {
			// The template contract only ever renders namespace-local Service
			// DNS names. Re-asserting that here keeps a mis-rendered or
			// hand-edited config from turning the proxy into an open relay.
			return normalizedConfig{}, fmt.Errorf("target URL for route %q must be a cluster-local Service (*.svc.cluster.local)", prefix)
		}
		routes = append(routes, normalizedRoute{prefix: prefix, target: target})
	}
	sort.SliceStable(routes, func(i, j int) bool {
		return len(routes[i].prefix) > len(routes[j].prefix)
	})
	hubTimeout := config.HubTimeout
	if hubTimeout <= 0 {
		hubTimeout = defaultHubTimeout
	}
	hubClient := config.HubClient
	if hubClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if config.HubInsecure {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit dev-only option
		}
		hubClient = &http.Client{Transport: transport, Timeout: hubTimeout}
	}
	listenAddress := strings.TrimSpace(config.ListenAddress)
	if listenAddress == "" {
		listenAddress = defaultListenAddress
	}
	config.ListenAddress = listenAddress
	config.Mode = mode
	config.PublicScheme = publicScheme
	return normalizedConfig{
		Config:       config,
		host:         host,
		hubURL:       hubURLString,
		hubPublicURL: hubPublicURLString,
		publicScheme: publicScheme,
		authIssuer:   authIssuer,
		authOrigin:   authOrigin,
		routes:       routes,
		hubClient:    hubClient,
		hubTimeout:   hubTimeout,
	}, nil
}

func parseHTTPOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("malformed origin")
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("origin must use http or https")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// isOriginOnly keeps configured hub URLs from accidentally changing the
// issuer and fixed identity endpoint paths through a base path or query.
// A trailing slash is accepted because parseHTTPOrigin canonicalizes it away.
func isOriginOnly(raw string) bool {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	return err == nil && u.Scheme != "" && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func normalizePrefix(raw string) (string, error) {
	prefix := strings.TrimSpace(raw)
	if prefix == "" {
		prefix = "/"
	}
	if !strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "\r\n") {
		return "", fmt.Errorf("route prefix %q must be an absolute path", raw)
	}
	if prefix != "/" {
		prefix = strings.TrimRight(prefix, "/")
		if prefix == "" {
			prefix = "/"
		}
	}
	return prefix, nil
}

func canonicalHost(raw string) (string, error) {
	host := strings.TrimSpace(raw)
	if host == "" || strings.ContainsAny(host, "\r\n/\\@") || strings.Contains(host, "://") {
		return "", fmt.Errorf("host is malformed")
	}
	return strings.ToLower(host), nil
}

func pathMatches(prefix, path string) bool {
	if prefix == "/" {
		return strings.HasPrefix(path, "/")
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func routeSuffix(prefix, path string) string {
	if prefix == "/" {
		return path
	}
	suffix := strings.TrimPrefix(path, prefix)
	if suffix == "" {
		return "/"
	}
	if !strings.HasPrefix(suffix, "/") {
		return "/" + suffix
	}
	return suffix
}
