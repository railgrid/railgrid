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

// Package mcpaggregate serves the hub's always-on aggregate MCP endpoint.
//
// This is a base-layer capability of the hub: the endpoint is mounted
// unconditionally at apiurl.PathPrefixMCPServer and always answers, even when
// no providers are registered (it just serves an empty tool list). It never
// depends on edges — edges are a first-class provider that federates its tools
// in exactly like every other provider (kuery, code, infrastructure, …).
//
// Per request the handler parses the tenant cluster + MCPServer name out of the
// path, verifies the caller's bearer against that tenant (see BearerVerifier),
// builds a fresh stateless mcp.Server, federates every Ready provider's own
// /mcp endpoint into it, and serves the MCP protocol over streamable HTTP.
// Nothing is federated for a bearer that fails verification.
package mcpaggregate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros/pkg/apiurl"
)

// DefaultVerifyCacheTTL is how long a successful bearer verification is
// reused for the same (bearer, cluster, MCPServer) before it is re-checked.
const DefaultVerifyCacheTTL = 60 * time.Second

// RateLimiter admits or rejects a pre-authentication verification attempt
// for one client address. The hub's token-login limiter satisfies it.
type RateLimiter interface {
	Allow(clientIP string) bool
}

// impl is the MCP Implementation advertised on `initialize`.
var impl = &mcp.Implementation{
	Name:    "faros-mcpserver",
	Title:   "Faros aggregate MCP",
	Version: "v1alpha1",
}

// Options configures the aggregate handler.
type Options struct {
	// Providers enumerates the live Ready providers to federate. Required.
	Providers ProviderEnumerator
	// ExternalURL is the hub's externally reachable base URL, used only to
	// self-describe the endpoint in the faros://about resource. Optional.
	ExternalURL string
	// Logger is used for federation diagnostics. Optional.
	Logger logr.Logger
	// Verifier checks the bearer against the tenant cluster and MCPServer
	// named by the request before anything is federated. Required: without
	// one every request is refused with 503 rather than forwarded unverified.
	Verifier BearerVerifier
	// VerifyCacheTTL bounds how long a successful verification is reused for
	// the same bearer, cluster and MCPServer. Defaults to DefaultVerifyCacheTTL.
	VerifyCacheTTL time.Duration
	// RateLimiter, when set, caps uncached verification attempts per client
	// address; cache hits are never counted so verified clients are not
	// throttled. Optional.
	RateLimiter RateLimiter
	// ClientIP derives the address the rate limiter keys on. Defaults to the
	// host part of RemoteAddr; the hub passes its proxy-header-aware helper.
	ClientIP func(*http.Request) string
}

// New returns the http.Handler mounted at apiurl.PathPrefixMCPServer. The
// handler expects the prefix to have been stripped, so it sees
// /{cluster}/apis/faros.sh/v1alpha1/mcpservers/{name}/mcp.
func New(opts Options) http.Handler {
	h := &handler{opts: opts, verified: make(map[string]time.Time)}
	if h.opts.VerifyCacheTTL <= 0 {
		h.opts.VerifyCacheTTL = DefaultVerifyCacheTTL
	}
	if h.opts.ClientIP == nil {
		h.opts.ClientIP = remoteHost
	}
	return h
}

type handler struct {
	opts Options

	mu       sync.Mutex
	verified map[string]time.Time // verification cache key -> expiry
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cluster, name, ok := parseMCPServerPath(r.URL.Path)
	if !ok {
		http.Error(w, "invalid path: expected /{cluster}/apis/faros.sh/v1alpha1/mcpservers/{name}/mcp", http.StatusBadRequest)
		return
	}
	token := extractBearer(r)
	if token == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.authorize(w, r, token, cluster, name) {
		return
	}

	// Fresh, stateless server per request so a provider that just became
	// Ready shows up on the very next tools/list.
	handler := mcp.NewStreamableHTTPHandler(
		func(req *http.Request) *mcp.Server {
			return buildServer(req.Context(), buildParams{
				cluster:     cluster,
				name:        name,
				token:       token,
				externalURL: h.opts.ExternalURL,
				enumerate:   h.opts.Providers,
				log:         h.opts.Logger,
			})
		},
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	handler.ServeHTTP(w, r)
}

// authorize verifies the bearer for (cluster, name), writing the rejection
// and returning false on failure. Successful verifications are cached by
// sha256(bearer)+cluster+name for VerifyCacheTTL; only uncached attempts are
// counted against the per-address rate limit, so the pre-auth path is what
// gets throttled, never an already-verified client.
func (h *handler) authorize(w http.ResponseWriter, r *http.Request, token, cluster, name string) bool {
	key := verifyCacheKey(token, cluster, name)
	if h.cached(key) {
		return true
	}
	if h.opts.RateLimiter != nil && !h.opts.RateLimiter.Allow(h.opts.ClientIP(r)) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "rate limit exceeded - too many requests", http.StatusTooManyRequests)
		return false
	}
	if h.opts.Verifier == nil {
		http.Error(w, "bearer verification is not configured", http.StatusServiceUnavailable)
		return false
	}
	if err := h.opts.Verifier.Verify(r, token, cluster, name); err != nil {
		status, msg := http.StatusServiceUnavailable, "bearer verification unavailable"
		switch {
		case errors.Is(err, ErrUnauthenticated):
			status, msg = http.StatusUnauthorized, "Unauthorized"
		case errors.Is(err, ErrForbidden):
			status, msg = http.StatusForbidden, "Forbidden"
		}
		h.opts.Logger.Info("mcp aggregate: bearer rejected",
			"cluster", cluster, "mcpserver", name, "client", h.opts.ClientIP(r), "status", status, "reason", err.Error())
		http.Error(w, msg, status)
		return false
	}
	h.remember(key)
	return true
}

func (h *handler) cached(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	exp, ok := h.verified[key]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(h.verified, key)
		return false
	}
	return true
}

// maxVerifiedEntries bounds the cache; expired entries are swept once it is
// reached, and if that frees nothing the entry nearest expiry is evicted so
// a flood of distinct bearers can neither grow it without limit nor lock a
// newly verified client out of the cache (which would push every request of
// that client through online verification and the per-IP budget).
const maxVerifiedEntries = 4096

func (h *handler) remember(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if _, ok := h.verified[key]; !ok && len(h.verified) >= maxVerifiedEntries {
		for k, exp := range h.verified {
			if now.After(exp) {
				delete(h.verified, k)
			}
		}
	}
	if _, ok := h.verified[key]; !ok && len(h.verified) >= maxVerifiedEntries {
		var victim string
		var soonest time.Time
		for k, exp := range h.verified {
			if victim == "" || exp.Before(soonest) {
				victim, soonest = k, exp
			}
		}
		delete(h.verified, victim)
	}
	h.verified[key] = now.Add(h.opts.VerifyCacheTTL)
}

// verifyCacheKey never stores the bearer itself: only its digest, bound to
// the cluster and MCPServer it was verified for.
func verifyCacheKey(token, cluster, name string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:]) + "|" + cluster + "|" + name
}

// remoteHost is the default ClientIP: the connection's peer address with no
// proxy headers consulted.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type buildParams struct {
	cluster     string
	name        string
	token       string
	externalURL string
	enumerate   ProviderEnumerator
	log         logr.Logger
}

// buildServer constructs the aggregate mcp.Server for one request: generic
// per-tenant metadata, the faros://about resource, and every Ready provider's
// federated tools. It never fails — with no providers it serves an empty but
// valid MCP server.
func buildServer(ctx context.Context, p buildParams) *mcp.Server {
	title := fmt.Sprintf("Faros — %s (tenant %s)", p.name, p.cluster)
	instructions := fmt.Sprintf(
		"You are connected to the faros aggregate MCP endpoint %q in tenant workspace %q.\n\n"+
			"This single endpoint federates the tools of every enabled faros provider in this tenant "+
			"(for example infrastructure, code, and edge access). Provider tools are namespaced as "+
			"\"<provider>__<tool>\". Call tools/list to enumerate what is currently reachable — the set "+
			"reflects which providers are enabled and healthy right now.",
		p.name, p.cluster,
	)

	var targets []ProviderTarget
	if p.enumerate != nil {
		targets = p.enumerate(ctx)
	}

	// Merge each provider's own instructions (e.g. a Home Assistant Service's
	// operator-authored entity/room guidance) into the aggregate's instructions,
	// so that context reaches the model here — not only on the provider's direct
	// endpoint. Fetched before the server is built (instructions are fixed at
	// construction).
	if extra := FederatedInstructions(ctx, targets, p.token, p.cluster); extra != "" {
		instructions += "\n\n--- Provider guidance ---\n\n" + extra
	}

	srv := mcp.NewServer(impl, &mcp.ServerOptions{Instructions: instructions})

	registerAboutResource(srv, aboutDoc{
		Role:        "aggregate",
		Tenant:      p.cluster,
		MCPServer:   p.name,
		Title:       title,
		EndpointURL: p.externalURL + apiurl.MCPServerPath(p.cluster, p.name),
	})

	registerProviderTools(ctx, srv, p.log, targets, p.token, p.cluster)
	return srv
}

// aboutDoc is the structured self-description served at faros://about.
type aboutDoc struct {
	Role        string `json:"role"`
	Tenant      string `json:"tenant"`
	MCPServer   string `json:"mcpServer"`
	Title       string `json:"title"`
	EndpointURL string `json:"endpointURL,omitempty"`
}

const aboutResourceURI = "faros://about"

func registerAboutResource(srv *mcp.Server, about aboutDoc) {
	srv.AddResource(&mcp.Resource{
		URI:         aboutResourceURI,
		Name:        "faros-about",
		Title:       "About this faros MCP endpoint",
		MIMEType:    "application/json",
		Description: "Structured JSON describing this endpoint's role, tenant context, and URL. Read once on connect.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		payload, err := json.MarshalIndent(about, "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      aboutResourceURI,
				MIMEType: "application/json",
				Text:     string(payload),
			}},
		}, nil
	})
}

// extractBearer pulls the token from an Authorization: Bearer header.
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// parseMCPServerPath extracts cluster + MCPServer name from the path seen after
// the apiurl.PathPrefixMCPServer prefix is stripped.
//
// Expected format:
//
//	/{cluster}/apis/faros.sh/v1alpha1/mcpservers/{name}/mcp
func parseMCPServerPath(path string) (cluster, name string, ok bool) {
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 8)
	if len(parts) < 7 {
		return "", "", false
	}
	if parts[1] != "apis" || parts[2] != "faros.sh" || parts[3] != "v1alpha1" ||
		parts[4] != "mcpservers" || parts[6] != "mcp" {
		return "", "", false
	}
	return parts[0], parts[5], true
}

// PathPrefix is the router prefix this handler mounts under. Re-exported for
// the hub server wiring so the prefix and the handler live together.
var PathPrefix = apiurl.PathPrefixMCPServer
