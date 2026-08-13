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

// Package appauth is the hub's login-time authorization endpoint for
// published applications.
//
// The design deliberately keeps the hub OFF the per-request data path:
//
//   - Public apps never contact the hub at all.
//   - Private apps redirect an unauthenticated browser here exactly once.
//     The hub reuses the shared portal browser session (or bounces through
//     the normal /login flow), evaluates ONE SubjectAccessReview against the
//     tenant workspace, and hands the app's access proxy a one-use code.
//     The proxy exchanges the code server-to-server and then maintains its
//     own bounded local session; the hub is not consulted again until that
//     session expires.
//
// Access policy is plain kcp RBAC in the tenant workspace: visiting a private
// app requires `get` on the instance resource with subresource `access`
// (e.g. `applications/my-shop` + subresource `access`). Granting a member
// access is creating a ClusterRole/ClusterRoleBinding pair; revoking is
// deleting it. The hub carries no knowledge of any provider CRD schema —
// only resource coordinates supplied by the (controller-authored) proxy
// config, validated syntactically here.
package appauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"

	"github.com/faroshq/faros/pkg/browsersession"
)

const (
	// AuthorizePath is the browser-facing entry point the access proxy
	// redirects to when a private app has no session.
	AuthorizePath = "/auth/apps/authorize"
	// ExchangePath is the server-to-server endpoint the access proxy calls to
	// turn a one-use code into a session grant.
	ExchangePath = "/auth/apps/exchange"
	// CallbackPath is the reserved path on the app host that authorize
	// redirects back to. It must stay in lockstep with the access proxy's
	// callback route (providers/infrastructure/accessproxy).
	CallbackPath = "/__faros/auth/callback"

	// AccessSubresource is the RBAC convention gating private apps: a visitor
	// needs `get` on `<resource>/<name>` with this subresource in the tenant
	// workspace. The subresource is never served; it exists only as an
	// authorization tuple.
	AccessSubresource = "access"
	// AccessVerb is the verb checked against the access subresource.
	AccessVerb = "get"

	// codeTTL bounds how long a minted authorization code stays valid. The
	// redirect hop it survives is a single browser 302.
	codeTTL = 2 * time.Minute
	// sessionTTL is the proxy-local session lifetime granted by a successful
	// exchange. It bounds revocation lag: after a RoleBinding is deleted the
	// user keeps access for at most this long before the next silent
	// re-authorize (which re-runs the SAR) denies them.
	sessionTTL = 15 * time.Minute
	// maxCodes bounds the in-memory code map. Codes are only minted for
	// browsers that already hold an authenticated hub session and passed the
	// SAR, so this cap is generous.
	maxCodes = 10000
)

// segmentRE validates instance coordinates (API group labels, resource
// plurals, object names, logical cluster IDs). Deliberately strict: lowercase
// DNS-label characters and dots only.
var segmentRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// stateRE bounds the proxy-chosen opaque state echoed back on the redirect.
var stateRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// InstanceRef are the resource coordinates of a published template instance.
// They are authorization inputs only; the hub never reads the object.
type InstanceRef struct {
	Cluster  string // logical cluster ID of the tenant workspace
	Group    string // API group of the instance CRD
	Resource string // REST plural of the instance CRD
	Name     string // instance name
}

func (ref InstanceRef) validate() error {
	for _, seg := range []struct{ label, v string }{
		{"cluster", ref.Cluster},
		{"group", ref.Group},
		{"resource", ref.Resource},
		{"name", ref.Name},
	} {
		if !segmentRE.MatchString(seg.v) {
			return fmt.Errorf("invalid %s", seg.label)
		}
	}
	return nil
}

func (ref InstanceRef) key() string {
	return ref.Cluster + "/" + ref.Group + "/" + ref.Resource + "/" + ref.Name
}

// SARFactory returns a SubjectAccessReview client scoped to one logical
// cluster. The hub wires this from its kcp admin config; tests use fakes.
type SARFactory func(clusterID string) (authorizationv1client.SubjectAccessReviewInterface, error)

// Config assembles a Handler.
type Config struct {
	// Sessions is the shared hub browser-session store (portal SSO).
	Sessions *browsersession.Store
	// SARClient resolves per-workspace SubjectAccessReview clients.
	SARClient SARFactory
	// AppsDomain is the DNS zone published apps live under
	// (e.g. "apps.faros.example"). Redirects are only issued to hosts
	// directly under this zone. Empty disables the endpoints.
	AppsDomain string
	// Codes persists the one-use authorization codes. It defaults to a
	// bounded process-local map, which is only correct for a single hub
	// replica: authorize and exchange are separate requests and a scaled hub
	// serves them from different pods. Supply a shared store (see
	// pkg/hub/sharedstore) whenever the hub runs more than one replica.
	Codes CodeStore
	// LoginPath is the hub-relative path of the interactive login page an
	// unauthenticated browser is sent to. Defaults to "/ui/login" (the
	// portal SPA is mounted under /ui/).
	LoginPath string
	// Now and Random are test seams.
	Now    func() time.Time
	Random io.Reader
}

// Handler serves the authorize and exchange endpoints.
type Handler struct {
	sessions   *browsersession.Store
	sarClient  SARFactory
	appsDomain string
	loginPath  string
	now        func() time.Time
	random     io.Reader
	codes      CodeStore
}

// CodeRecord is what authorize binds a code to and exchange verifies against.
// It carries identity metadata only — never a credential.
type CodeRecord struct {
	Ref          InstanceRef
	RedirectHost string
	Identity     browsersession.Identity
	ExpiresAt    time.Time
}

// CodeStore persists one-use authorization codes between the browser's
// authorize hop and the access proxy's server-to-server exchange.
//
// Take MUST be atomic across every hub replica: a code that two callers can
// redeem is a replayable app sign-in.
type CodeStore interface {
	Put(ctx context.Context, code string, record CodeRecord) error
	Take(ctx context.Context, code string) (CodeRecord, bool)
}

// New validates cfg and returns a Handler.
func New(cfg Config) (*Handler, error) {
	if cfg.Sessions == nil {
		return nil, fmt.Errorf("appauth: browser session store is required")
	}
	if cfg.SARClient == nil {
		return nil, fmt.Errorf("appauth: SAR client factory is required")
	}
	domain := strings.ToLower(strings.Trim(strings.TrimSpace(cfg.AppsDomain), "."))
	if domain == "" {
		return nil, fmt.Errorf("appauth: apps domain is required")
	}
	h := &Handler{
		sessions:   cfg.Sessions,
		sarClient:  cfg.SARClient,
		appsDomain: domain,
		loginPath:  cfg.LoginPath,
		now:        cfg.Now,
		random:     cfg.Random,
		codes:      cfg.Codes,
	}
	if h.loginPath == "" {
		h.loginPath = "/ui/login"
	}
	if h.now == nil {
		h.now = time.Now
	}
	if h.random == nil {
		h.random = rand.Reader
	}
	if h.codes == nil {
		// Read the clock through the handler rather than capturing it, so a
		// test that swaps h.now after construction also moves the store's clock.
		h.codes = newMemoryCodeStore(func() time.Time { return h.now() })
	}
	return h, nil
}

// RegisterRoutes mounts the endpoints. limit wraps each handler with the
// hub's auth rate limiter; pass nil to mount unwrapped (tests).
func (h *Handler) RegisterRoutes(router *mux.Router, limit func(http.HandlerFunc) http.HandlerFunc) {
	wrap := func(fn http.HandlerFunc) http.HandlerFunc {
		if limit == nil {
			return fn
		}
		return limit(fn)
	}
	router.HandleFunc(AuthorizePath, wrap(h.HandleAuthorize)).Methods("GET")
	router.HandleFunc(ExchangePath, wrap(h.HandleExchange)).Methods("POST")
}

// HandleAuthorize is the browser entry point.
//
//	GET /auth/apps/authorize?cluster=&group=&resource=&name=&redirect_uri=&state=
func (h *Handler) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	q := r.URL.Query()
	ref := InstanceRef{
		Cluster:  q.Get("cluster"),
		Group:    q.Get("group"),
		Resource: q.Get("resource"),
		Name:     q.Get("name"),
	}
	state := q.Get("state")
	redirectURI := q.Get("redirect_uri")
	redirect, err := h.validateRedirect(redirectURI)
	if err != nil || ref.validate() != nil || !stateRE.MatchString(state) {
		h.renderError(w, http.StatusBadRequest, "Invalid app access request",
			"The application sign-in request was malformed. Return to the app and try again.")
		return
	}

	session, err := h.sessions.ResolveRequest(r)
	if err != nil {
		// No usable shared session: send the browser through the normal hub
		// login. The portal redirects back to this exact hub-relative URL
		// afterwards, so the flow resumes with a fresh session cookie.
		browsersession.ClearCookie(w)
		next := r.URL.RequestURI() // hub-relative; never an absolute URL
		http.Redirect(w, r, h.loginPath+"?next="+url.QueryEscape(next), http.StatusFound)
		return
	}

	allowed, err := h.authorize(r.Context(), session.Identity, ref)
	if err != nil {
		h.renderError(w, http.StatusServiceUnavailable, "App access is unavailable",
			"The platform could not evaluate access policy. Try again shortly.")
		return
	}
	if !allowed {
		h.renderError(w, http.StatusForbidden, "App access denied",
			"Your account does not have access to this application. Ask the app owner to share it with you.")
		return
	}

	// Bind the code to the redirect's full host INCLUDING any port — the
	// gate exchanges with its configured external host verbatim (e.g.
	// "app.example:10443" behind a port-forwarded local Gateway), and a
	// hostname-only binding would 410 every exchange and loop the browser.
	code, err := h.mintCode(r.Context(), ref, redirect.Host, session.Identity)
	if err != nil {
		h.renderError(w, http.StatusInternalServerError, "App access is unavailable",
			"The platform could not complete sign-in. Try again shortly.")
		return
	}
	target := *redirect
	tq := url.Values{}
	tq.Set("code", code)
	tq.Set("state", state)
	target.RawQuery = tq.Encode()
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target.String(), http.StatusFound)
}

// exchangeRequest is the proxy's server-to-server exchange payload.
type exchangeRequest struct {
	Code     string `json:"code"`
	Host     string `json:"host"`
	Cluster  string `json:"cluster"`
	Group    string `json:"group"`
	Resource string `json:"resource"`
	Name     string `json:"name"`
}

// ExchangeResponse is returned to the proxy on a successful exchange. It
// contains identity metadata only — never a credential of any kind.
type ExchangeResponse struct {
	Allowed           bool   `json:"allowed"`
	UserID            string `json:"userId"`
	Email             string `json:"email,omitempty"`
	Name              string `json:"name,omitempty"`
	SessionTTLSeconds int64  `json:"sessionTtlSeconds"`
}

// HandleExchange consumes a one-use code.
//
//	POST /auth/apps/exchange  {"code":..., "host":..., "cluster":..., ...}
func (h *Handler) HandleExchange(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req exchangeRequest
	body := http.MaxBytesReader(w, r.Body, 16*1024)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		http.Error(w, "malformed exchange request", http.StatusBadRequest)
		return
	}
	ref := InstanceRef{Cluster: req.Cluster, Group: req.Group, Resource: req.Resource, Name: req.Name}
	if ref.validate() != nil || strings.TrimSpace(req.Code) == "" || strings.TrimSpace(req.Host) == "" {
		http.Error(w, "malformed exchange request", http.StatusBadRequest)
		return
	}
	record, ok := h.codes.Take(r.Context(), req.Code)
	if !ok || record.Ref.key() != ref.key() || !strings.EqualFold(record.RedirectHost, req.Host) {
		// Expired, replayed, or bound to different coordinates. 410 tells the
		// proxy to restart the authorize flow rather than retry.
		http.Error(w, "sign-in expired", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ExchangeResponse{
		Allowed:           true,
		UserID:            record.Identity.UserID,
		Email:             record.Identity.Email,
		Name:              record.Identity.Name,
		SessionTTLSeconds: int64(sessionTTL / time.Second),
	})
}

// authorize runs the single SubjectAccessReview backing a private-app login.
//
// The SAR subject is the account's kcp RBAC identity ("faros:<email>") —
// the username every tenant-workspace binding is written against: the
// workspace-admin ClusterRoleBinding (which is why workspace members can
// always open their own apps with no explicit grant) and the per-app
// faros-app-access grants alike. The User CR name is a platform-internal
// key that appears in NO kcp binding; a SAR against it would deny everyone.
func (h *Handler) authorize(ctx context.Context, identity browsersession.Identity, ref InstanceRef) (bool, error) {
	user := strings.TrimSpace(identity.RBACIdentity)
	if user == "" {
		return false, nil
	}
	client, err := h.sarClient(ref.Cluster)
	if err != nil {
		return false, err
	}
	review, err := client.Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   user,
			Groups: []string{"system:authenticated"},
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Group:       ref.Group,
				Resource:    ref.Resource,
				Subresource: AccessSubresource,
				Name:        ref.Name,
				Verb:        AccessVerb,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return review.Status.Allowed, nil
}

// validateRedirect pins the redirect to the reserved callback path on a host
// directly under the apps domain.
func (h *Handler) validateRedirect(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return nil, fmt.Errorf("invalid redirect")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || !strings.HasSuffix(host, "."+h.appsDomain) {
		return nil, fmt.Errorf("redirect host outside apps domain")
	}
	// Exactly one label under the apps zone: no nested subdomains.
	if strings.Contains(strings.TrimSuffix(host, "."+h.appsDomain), ".") {
		return nil, fmt.Errorf("redirect host outside apps domain")
	}
	if u.EscapedPath() != CallbackPath {
		return nil, fmt.Errorf("redirect path is not the app callback")
	}
	return u, nil
}

func (h *Handler) mintCode(ctx context.Context, ref InstanceRef, redirectHost string, identity browsersession.Identity) (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(h.random, buf); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(buf)
	record := CodeRecord{
		Ref:          ref,
		RedirectHost: strings.ToLower(redirectHost),
		Identity:     identity,
		ExpiresAt:    h.now().Add(codeTTL),
	}
	if err := h.codes.Put(ctx, code, record); err != nil {
		return "", err
	}
	return code, nil
}

// memoryCodeStore is the default, process-local CodeStore. Correct for a
// single hub replica only — see Config.Codes.
type memoryCodeStore struct {
	now func() time.Time

	mu    sync.Mutex
	codes map[string]CodeRecord
}

func newMemoryCodeStore(now func() time.Time) *memoryCodeStore {
	return &memoryCodeStore{now: now, codes: map[string]CodeRecord{}}
}

func (m *memoryCodeStore) Put(_ context.Context, code string, record CodeRecord) error {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	for len(m.codes) >= maxCodes {
		m.evictOldestLocked()
	}
	m.codes[code] = record
	return nil
}

func (m *memoryCodeStore) Take(_ context.Context, code string) (CodeRecord, bool) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.codes[code]
	if !ok {
		return CodeRecord{}, false
	}
	delete(m.codes, code)
	if now.After(record.ExpiresAt) {
		return CodeRecord{}, false
	}
	return record, true
}

func (m *memoryCodeStore) cleanupLocked(now time.Time) {
	for k, v := range m.codes {
		if now.After(v.ExpiresAt) {
			delete(m.codes, k)
		}
	}
}

func (m *memoryCodeStore) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for k, v := range m.codes {
		if oldestKey == "" || v.ExpiresAt.Before(oldest) {
			oldestKey, oldest = k, v.ExpiresAt
		}
	}
	if oldestKey != "" {
		delete(m.codes, oldestKey)
	}
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// renderError writes a small self-contained branded error page. It exposes no
// identity-provider or policy detail.
func (h *Handler) renderError(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>%s</title><style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0c0a14;color:#e8e6f0;font:16px/1.5 system-ui,sans-serif}
main{max-width:26rem;padding:2rem;text-align:center}
h1{font-size:1.15rem;margin:0 0 .6rem}
p{margin:0;color:#a9a4bd;font-size:.92rem}
</style></head><body><main><h1>%s</h1><p>%s</p></main></body></html>`,
		htmlEscape(title), htmlEscape(title), htmlEscape(detail))
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}
