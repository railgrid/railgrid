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

// Package accessproxy is the small infrastructure-owned runtime that fronts
// one published template instance. Templates render it as a component of
// their own graph; the shared-Gateway HTTPRoute always points here.
//
// The proxy has two modes and a hard availability contract:
//
//   - public:  pure passthrough. No auth code runs on the request path and
//     the hub is NEVER contacted. A hub outage cannot affect a public app.
//   - private: requests need a proxy-local session. A browser without one is
//     redirected to the hub's login-time authorize endpoint exactly once;
//     the proxy exchanges the returned one-use code server-to-server and
//     then maintains its own bounded in-memory session for the granted TTL.
//     A hub outage leaves existing sessions working; only new sign-ins fail.
//
// The proxy holds no credentials of any kind: no service-account token, no
// kubeconfig, no signing keys. Access policy is kcp RBAC evaluated by the hub
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
	SessionCookieName = "__Host-faros-app-session"
	// ReturnCookieName is a short-lived opaque handle for the original clean
	// path while the browser completes the hub sign-in flow.
	ReturnCookieName = "faros-app-return"
	// CallbackPath is the reserved platform path on the app host. It must stay
	// in lockstep with the hub's appauth.CallbackPath.
	CallbackPath = "/__faros/auth/callback"

	// hubAuthorizePath / hubExchangePath mirror pkg/hub/appauth. The contract
	// is intentionally tiny (two endpoints, standard redirect + JSON), so the
	// infra module does not import hub Go packages.
	hubAuthorizePath = "/auth/apps/authorize"
	hubExchangePath  = "/auth/apps/exchange"

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
	// HubURL is the in-cluster hub origin used for the server-to-server code
	// exchange. Required in private mode.
	HubURL string
	// HubPublicURL is the browser-reachable hub origin used only for the
	// authorize redirect. Empty defaults to HubURL.
	HubPublicURL string
	HubInsecure  bool
	PublicScheme string

	// AllowNonClusterTargets disables the cluster-local target confinement.
	// Test seam only; production configuration must never set it.
	AllowNonClusterTargets bool

	// HubClient and Random are test seams.  When nil, New builds an HTTP
	// client with a bounded timeout and optional TLS verification skip.
	HubClient *http.Client
	Random    RandomSource

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

	publicScheme := strings.ToLower(strings.TrimSpace(config.PublicScheme))
	if publicScheme == "" {
		publicScheme = defaultPublicScheme
	}
	if publicScheme != "http" && publicScheme != "https" {
		return normalizedConfig{}, fmt.Errorf("public scheme must be http or https")
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
