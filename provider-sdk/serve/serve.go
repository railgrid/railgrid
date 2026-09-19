/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package serve builds the whole HTTP surface a railgrid provider is allowed
// to expose, from the closed list of route classes in
// docs/provider-connectivity-contract.md §"Pillar 2 route classes".
//
// It exists for the same reason provider-sdk/dataplane does: every provider
// hand-built its own mux, and each copy drifted — one forgot /readyz, one
// registered the data plane on an http.ServeMux (which rewrites the very
// paths the grammar must refuse), one grew an /api/* facade for its portal.
// A provider that calls New cannot make any of those mistakes, because New
// takes a handler per class and refuses anything that is not one.
//
// The layout is fixed:
//
//	/healthz                 (c) liveness, always 200, never Readiness
//	/readyz                  (c) readiness, the Readiness handler
//	/mcp, /mcp/sse           (b) MCP projection
//	/dataplane/…             (a) data-plane verbs, raw path
//	/actions/…               (a′) actions, raw path
//	/workload-identities/…   (e) hub-only, refused to callers by the hub proxy
//	/oauth/…                 (d) browser OAuth
//	/agent/…                 (f) agent tunnel, via Options.Extra, raw path
//	/webhooks/…              (g) signed inbound webhook, via Options.Extra, raw path
//	/…                       portal assets with an index.html fallback
//
// There is no /api/*: New refuses to register one. If a UI needs to list,
// create or edit a thing, the thing is a bound CR and the UI reads it over
// /clusters/{id} (Pillar 1), not over a route here.
package serve

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"

	"github.com/railgrid/provider-sdk/dataplane"
)

// Route prefixes. The two grammar roots come from provider-sdk/dataplane so
// there is one spelling of each in the tree.
const (
	// DataPlanePrefix is class (a): a verb on a bound resource.
	DataPlanePrefix = "/" + dataplane.DataplaneRoot + "/"
	// ActionsPrefix is class (a′): an action, whose verb segment carries its
	// contract version.
	ActionsPrefix = "/" + dataplane.ActionsRoot + "/"
	// AgentPrefix is class (f): the agent tunnel.
	AgentPrefix = "/agent/"
	// WebhooksPrefix is class (g): a signed inbound webhook.
	WebhooksPrefix = "/webhooks/"
	// OAuthPrefix is class (d): the browser OAuth popup flow.
	OAuthPrefix = "/oauth/"

	// HealthzPath is class (c) liveness: "the process is up".
	HealthzPath = "/healthz"
	// ReadyzPath is class (c) readiness: "this provider can do its job".
	ReadyzPath = "/readyz"
	// MCPPath and MCPSSEPath are class (b). The hub's MCP aggregate federates
	// exactly these two paths (pkg/hub/mcpaggregate/enumerator.go).
	MCPPath    = "/mcp"
	MCPSSEPath = "/mcp/sse"
)

// HubOnlyPrefixes are the paths the hub's backend proxy refuses to callers
// (hubOnlyProviderPrefixes in pkg/hub/providers/proxy.go). A HubOnly handler
// must live under one of them: a "hub-only" route the proxy does not know
// about is not hub-only at all, it is a tenant-reachable route that merely
// looks private.
var HubOnlyPrefixes = []string{"/workload-identities"}

// Class names a Pillar 2 route class that Options cannot express as a field of
// its own, because its shape is provider-specific.
type Class string

const (
	// ClassAgentTunnel is class (f): /agent/clusters/{id}/{resource}/{name}/proxy,
	// authorized by a join token or an edge ServiceAccount plus a SAR.
	ClassAgentTunnel Class = "agent-tunnel"
	// ClassWebhook is class (g): /webhooks/{kind}/{clusterID}/{name}/{token},
	// authorized by an HMAC token and acting as the provider ServiceAccount.
	ClassWebhook Class = "webhook"
)

// prefixForClass pins each Class to the one prefix it may be mounted on, so a
// webhook cannot be smuggled in under /agent/ or the other way round.
var prefixForClass = map[Class]string{
	ClassAgentTunnel: AgentPrefix,
	ClassWebhook:     WebhooksPrefix,
}

// Route is one class-(f) or class-(g) mount. Nothing else may be expressed
// this way: every other class has a field on Options.
type Route struct {
	// Prefix must start with AgentPrefix or WebhooksPrefix.
	Prefix string
	// Class must be the one that matches Prefix.
	Class Class
	// Handler receives the request with its path unmodified.
	Handler http.Handler
}

// Options are the per-class handlers. Every nil field is a class this provider
// does not serve, and its routes then do not exist — New never substitutes a
// stub that answers "ok" for something the provider cannot actually do.
type Options struct {
	// Name is the provider name, used in logs only.
	Name string
	// Readiness serves /readyz and is REQUIRED — normally
	// vwhealth.Handler(readiness). A provider with no honest readiness answer
	// is the failure mode vwhealth exists to prevent, so there is no default:
	// New refuses instead of serving a /readyz that always says ok.
	Readiness http.Handler
	// Portal is the embedded Vite build output (the dist directory as an
	// fs.FS). Nil serves no portal and 404s every unmatched path.
	Portal fs.FS
	// MCP answers both /mcp and /mcp/sse; the streamable-HTTP handler
	// dispatches on method internally. Nil omits both.
	MCP http.Handler
	// DataPlane receives everything under /dataplane/ with the path EXACTLY as
	// the caller sent it, so dataplane.ParseRequest — not an http.ServeMux —
	// decides what ".." and "//" mean.
	DataPlane http.Handler
	// Actions receives everything under /actions/, likewise unmodified.
	Actions http.Handler
	// HubOnly maps an exact path under HubOnlyPrefixes to its handler, e.g.
	// "/workload-identities/review". The route is served here and refused to
	// callers by the hub proxy; nothing in this package enforces that, which
	// is why the keys are checked against HubOnlyPrefixes.
	HubOnly map[string]http.Handler
	// OAuth receives everything under /oauth/.
	OAuth http.Handler
	// Extra holds the class-(f) and class-(g) routes. See Route.
	Extra []Route
	// Logger receives one line per request. A zero Logger falls back to klog,
	// matching provider-sdk/hubclient.
	Logger logr.Logger
}

// New returns the provider's complete http.Handler.
//
// It fails rather than silently serving something the contract does not allow:
// a missing Readiness, an Extra route outside /agent/ or /webhooks/ (or whose
// Class disagrees with its prefix), a HubOnly path the hub proxy would not
// deny to callers, a duplicate mount, and any attempt to register /api/*.
func New(o Options) (http.Handler, error) {
	if o.Readiness == nil {
		return nil, errors.New("serve: Readiness is required (pass vwhealth.Handler(readiness); a provider that cannot answer /readyz honestly must not serve)")
	}

	logger := o.Logger
	if logger.GetSink() == nil {
		logger = klog.Background()
	}
	if o.Name != "" {
		logger = logger.WithName(o.Name)
	}

	s := &server{name: o.Name, log: logger, mux: http.NewServeMux(), portal: o.Portal}
	if o.Portal != nil {
		s.files = http.FileServer(http.FS(o.Portal))
	}

	// (c) Liveness is unconditional and is NOT Readiness: a provider whose
	// watches are dead is alive and must not be restarted, it must stop
	// claiming to be ready.
	s.mux.HandleFunc(HealthzPath, healthz)
	s.mux.Handle(ReadyzPath, o.Readiness)

	// (b) MCP.
	if o.MCP != nil {
		s.mux.Handle(MCPPath, o.MCP)
		s.mux.Handle(MCPSSEPath, o.MCP)
	}

	// (e) Hub-only. Sorted so the error a bad map produces is deterministic.
	for _, p := range sortedKeys(o.HubOnly) {
		handler := o.HubOnly[p]
		if handler == nil {
			return nil, fmt.Errorf("serve: HubOnly[%q] is nil", p)
		}
		if err := rejectAPIRoute(p); err != nil {
			return nil, err
		}
		if !isHubOnlyPath(p) {
			return nil, fmt.Errorf("serve: HubOnly path %q is not under %s; the hub proxy only refuses callers on those prefixes, so anything else would be tenant-reachable (pkg/hub/providers/proxy.go)", p, strings.Join(HubOnlyPrefixes, ", "))
		}
		s.mux.Handle(p, handler)
	}

	// (d) Browser OAuth.
	if o.OAuth != nil {
		s.mux.Handle(OAuthPrefix, o.OAuth)
	}

	// (a) and (a′) are dispatched off the raw path, never off the mux — see
	// server.ServeHTTP.
	if o.DataPlane != nil {
		s.raw = append(s.raw, rawRoute{prefix: DataPlanePrefix, handler: o.DataPlane})
	}
	if o.Actions != nil {
		s.raw = append(s.raw, rawRoute{prefix: ActionsPrefix, handler: o.Actions})
	}

	// (f) and (g). Same raw dispatch: both carry a cluster ID and a name in
	// the path, and both must see what the caller actually sent.
	seen := map[string]bool{DataPlanePrefix: true, ActionsPrefix: true}
	for _, route := range o.Extra {
		if err := rejectAPIRoute(route.Prefix); err != nil {
			return nil, err
		}
		want, ok := prefixForClass[route.Class]
		if !ok {
			return nil, fmt.Errorf("serve: Extra route %q has class %q; only %q and %q may be mounted this way, every other class has an Options field", route.Prefix, route.Class, ClassAgentTunnel, ClassWebhook)
		}
		if !strings.HasPrefix(route.Prefix, want) {
			return nil, fmt.Errorf("serve: Extra route %q does not start with %q, which class %q requires", route.Prefix, want, route.Class)
		}
		if route.Handler == nil {
			return nil, fmt.Errorf("serve: Extra route %q has a nil handler", route.Prefix)
		}
		if seen[route.Prefix] {
			return nil, fmt.Errorf("serve: Extra route %q is mounted twice", route.Prefix)
		}
		seen[route.Prefix] = true
		s.raw = append(s.raw, rawRoute{prefix: route.Prefix, handler: route.Handler})
	}

	// Longest prefix wins, so /webhooks/github/ can sit beside a broader
	// /webhooks/ without either shadowing the other by declaration order.
	sort.SliceStable(s.raw, func(i, j int) bool { return len(s.raw[i].prefix) > len(s.raw[j].prefix) })

	// The portal is last and is the only catch-all.
	s.mux.HandleFunc("/", s.handlePortal)

	return s, nil
}

// rawRoute is a prefix dispatched before the mux sees the request.
type rawRoute struct {
	prefix  string
	handler http.Handler
}

type server struct {
	name   string
	log    logr.Logger
	mux    *http.ServeMux
	raw    []rawRoute
	portal fs.FS
	files  http.Handler
}

// ServeHTTP dispatches the grammar prefixes off the RAW path and only then
// falls through to the mux.
//
// This ordering is the whole point of the type. http.ServeMux cleans the
// request path and answers a non-clean one with a 301/307 redirect. On the
// data plane that is wrong twice over: ".." and "//" are exactly what the
// grammar must refuse, and a redirect hands the caller back a path it never
// asked for — which, cleaned, may address a different object. Letting
// dataplane.ParseRequest see what the caller actually sent is the only way the
// refusal happens where the contract says it does.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	recorder := &statusRecorder{ResponseWriter: w}

	handler := s.mux.ServeHTTP
	for _, route := range s.raw {
		if strings.HasPrefix(r.URL.Path, route.prefix) {
			handler = route.handler.ServeHTTP
			break
		}
	}
	handler(recorder, r)

	s.log.Info("request",
		"method", r.Method,
		"path", r.URL.Path,
		"status", recorder.code(),
		"duration", time.Since(start),
	)
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handlePortal serves the embedded bundle: a real file for an asset path (404
// when the bundle has no such file), and index.html for anything else so a
// direct browser visit to a client-side route shows the app rather than a 404.
func (s *server) handlePortal(w http.ResponseWriter, r *http.Request) {
	// GET for full responses; HEAD for the cache/preflight checks a browser
	// may issue while loading <img> or <script>.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.portal == nil || s.files == nil {
		http.NotFound(w, r)
		return
	}
	// An asset path is one whose last segment has a dot — the same test the
	// hub's UI proxy applies (isAssetPath, pkg/hub/providers/proxy.go), so the
	// two ends of the proxy agree on what the SPA owns.
	if name := strings.TrimPrefix(r.URL.Path, "/"); name != "" && isAssetPath(name) {
		if s.serveAsset(w, name) {
			return
		}
		// A path that looks like an asset and is not in the bundle is a 404,
		// never index.html: a retired lazy chunk answered with an HTML 200
		// surfaces in the browser as a MIME error that hides the real fault.
		http.NotFound(w, r)
		return
	}
	// Index fallback. Reuse the http.FileServer so Last-Modified and the
	// conditional-request handling come out right; clone the request rather
	// than mutating the caller's.
	index := r.Clone(r.Context())
	index.URL.Path = "/"
	s.files.ServeHTTP(w, index)
}

// serveAsset writes the embedded file at name, or reports false (having
// written nothing) when it is absent. Content-Type comes from the extension
// because http.FileServer's sniffing does not apply when we copy the bytes
// ourselves.
func (s *server) serveAsset(w http.ResponseWriter, name string) bool {
	f, err := s.portal.Open(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Error(err, "portal asset", "name", name)
		}
		return false
	}
	defer func() { _ = f.Close() }()

	if info, err := f.Stat(); err == nil && info.IsDir() {
		return false
	}

	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := io.Copy(w, f); err != nil {
		s.log.Error(err, "portal asset write", "name", name)
	}
	return true
}

// isAssetPath mirrors pkg/hub/providers/proxy.go.
func isAssetPath(rest string) bool {
	last := strings.TrimPrefix(rest, "/")
	if i := strings.LastIndexByte(last, '/'); i >= 0 {
		last = last[i+1:]
	}
	return strings.Contains(last, ".")
}

func isHubOnlyPath(p string) bool {
	clean := strings.ToLower(path.Clean("/" + strings.TrimPrefix(p, "/")))
	for _, prefix := range HubOnlyPrefixes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

// rejectAPIRoute is the one rule that has to be stated as a prohibition: every
// other class is a field, but /api/* is the deviation providers keep
// reinventing, so it is named and refused.
func rejectAPIRoute(p string) error {
	clean := strings.ToLower(path.Clean("/" + strings.TrimPrefix(p, "/")))
	if clean == "/api" || strings.HasPrefix(clean, "/api/") {
		return fmt.Errorf("serve: refusing to register %q: there is no /api/* route class. A UI reads and writes bound CRs over /clusters/{id}; a backend route that mirrors a CR is a deviation even when it is authorized correctly (docs/provider-connectivity-contract.md §\"Pillar 2 route classes\", rule 1)", p)
	}
	return nil
}

func sortedKeys(m map[string]http.Handler) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// statusRecorder remembers the status code for the request log.
//
// It forwards Flush and Hijack explicitly (and exposes Unwrap for
// http.ResponseController) because the MCP SSE transport and any tunnel under
// /agent/ type-assert for them; a wrapper that hid those would turn a
// streaming response into a buffered one.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	if err != nil {
		return n, fmt.Errorf("write response: %w", err)
	}
	return n, nil
}

// code is the status actually sent: a handler that wrote nothing at all still
// produced a 200 on the wire.
func (s *statusRecorder) code() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Flush is best-effort: a response that cannot be flushed (a test recorder,
// a wrapper that does not support it) is still a correct response, and the
// alternative — not implementing Flusher — silently disables streaming.
func (s *statusRecorder) Flush() {
	_ = http.NewResponseController(s.ResponseWriter).Flush()
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(s.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, fmt.Errorf("hijack: %w", err)
	}
	return conn, rw, nil
}
