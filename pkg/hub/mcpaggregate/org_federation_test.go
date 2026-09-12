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

package mcpaggregate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/faroshq/faros/pkg/hub/providers"
	"github.com/faroshq/faros/pkg/hub/serviceaccounts"
)

// These tests drive the real handler, the real provider Registry, the real
// RegistryEnumerator, and the real backend proxy's OrgProviderRoute — only the
// verifier, the delegated-token issuer, and the provider backends are fakes.

const (
	orgA = "org-a"
	wsA  = "ws-a"
	orgB = "org-b"
	wsB  = "ws-b"
)

// seenRequest is what one fake backend observed about one inbound request.
type seenRequest struct {
	path, method, authorization, user, tenant, cluster string
}

// backendLog records requests a fake backend received.
type backendLog struct {
	mu   sync.Mutex
	reqs []seenRequest
}

func (l *backendLog) add(r *http.Request, method string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, seenRequest{
		path: r.URL.Path, method: method,
		authorization: r.Header.Get("Authorization"),
		user:          r.Header.Get("X-Faros-User"),
		tenant:        r.Header.Get("X-Faros-Tenant"),
		cluster:       r.Header.Get("X-Faros-Cluster"),
	})
}

func (l *backendLog) all() []seenRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]seenRequest(nil), l.reqs...)
}

// serveFakeMCP answers one JSON-RPC request as an MCP server exposing tools.
func serveFakeMCP(w http.ResponseWriter, r *http.Request, label string, tools []string, log *backendLog) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	_ = json.Unmarshal(body, &req)
	log.add(r, req.Method)
	w.Header().Set("Content-Type", "application/json")
	switch req.Method {
	case "tools/list":
		list := make([]map[string]any, 0, len(tools))
		for _, name := range tools {
			list = append(list, map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}})
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": list}})
		_, _ = w.Write(out)
	case "tools/call":
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": "called " + req.Params.Name + " on " + label}},
		}})
		_, _ = w.Write(out)
	default:
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}
}

// fakeIssuer mints a recognisable delegated token per tuple, or fails.
type fakeIssuer struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeIssuer) IssueDelegatedUserToken(_ context.Context, orgUUID, wsUUID string, user serviceaccounts.Identity, provider serviceaccounts.DelegatedProvider) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tuple := strings.Join([]string{orgUUID, wsUUID, user.User, provider.Name}, "/")
	f.calls = append(f.calls, tuple)
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	return "delegated:" + tuple, time.Now().Add(10 * time.Minute), nil
}

func (f *fakeIssuer) tuples() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// edgePath is where the platform edges provider serves a tunnelled request to
// an org-owned provider's Service (providers.EdgeRoute.EdgeProxyPath).
func edgePath(cluster, service string) string {
	return "/edgeproxy/clusters/" + cluster + "/apis/edges.faros.sh/v1alpha1/services/" + service + "/proxy"
}

type orgFixture struct {
	handler http.Handler
	issuer  *fakeIssuer
	// platformInfra is the PLATFORM infrastructure backend, which org A
	// shadows with its own copy.
	platformInfra *backendLog
	platformCode  *backendLog
	// edges records everything the platform edges provider received: its own
	// /mcp (it is a platform provider too) and every tunnelled org request.
	edges *backendLog
	// orgDirect would record a direct dial of an org provider's self-declared
	// BackendURL — which must never happen.
	orgDirect *backendLog
}

// callers maps a test bearer to the Caller the verifier would resolve.
var callers = map[string]Caller{
	"alice-a":     {OrgUUID: orgA, WorkspaceUUID: wsA, User: "alice"},
	"alice-a-org": {OrgUUID: orgA, User: "alice"},
	"bob-b":       {OrgUUID: orgB, WorkspaceUUID: wsB, User: "bob"},
	"sa-a":        {OrgUUID: orgA, WorkspaceUUID: wsA, ServiceAccount: ServiceAccountUsername("default")},
	"platform":    {ServiceAccount: ServiceAccountUsername("default")},
}

func newOrgFixture(t *testing.T) *orgFixture {
	t.Helper()
	f := &orgFixture{
		issuer:        &fakeIssuer{},
		platformInfra: &backendLog{},
		platformCode:  &backendLog{},
		edges:         &backendLog{},
		orgDirect:     &backendLog{},
	}
	mustURL := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return u
	}
	newBackend := func(h http.HandlerFunc) *url.URL {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return mustURL(srv.URL)
	}

	platformInfraURL := newBackend(func(w http.ResponseWriter, r *http.Request) {
		serveFakeMCP(w, r, "platform-infrastructure", []string{"platform_provision"}, f.platformInfra)
	})
	platformCodeURL := newBackend(func(w http.ResponseWriter, r *http.Request) {
		serveFakeMCP(w, r, "platform-code", []string{"commit_files"}, f.platformCode)
	})
	orgDirectURL := newBackend(func(w http.ResponseWriter, r *http.Request) {
		serveFakeMCP(w, r, "org-direct", []string{"leaked"}, f.orgDirect)
	})
	edgesURL := newBackend(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			serveFakeMCP(w, r, "edges", []string{"ssh_exec"}, f.edges)
		case edgePath("lc-ws-a", "provider-infrastructure") + "/mcp":
			serveFakeMCP(w, r, "org-a-infrastructure", []string{"provision"}, f.edges)
		case edgePath("lc-ws-a", "provider-vault") + "/mcp":
			serveFakeMCP(w, r, "org-a-vault", []string{"read_secret"}, f.edges)
		case edgePath("lc-ws-b", "provider-billing") + "/mcp":
			serveFakeMCP(w, r, "org-b-billing", []string{"invoice"}, f.edges)
		default:
			f.edges.add(r, "unexpected")
			http.NotFound(w, r)
		}
	})

	reg := providers.NewRegistry()
	reg.Upsert(providers.Provider{Name: providers.EdgesProviderName, BackendURL: edgesURL, EndpointsValid: true})
	reg.Upsert(providers.Provider{Name: "infrastructure", DisplayName: "Infrastructure", BackendURL: platformInfraURL, EndpointsValid: true})
	reg.Upsert(providers.Provider{Name: "code", BackendURL: platformCodeURL, EndpointsValid: true})
	// Org A self-hosts infrastructure (shadowing the platform one) and runs
	// a provider of its own. Their self-declared BackendURL is a real,
	// dialable server here precisely so that a direct dial would be caught.
	reg.Upsert(providers.Provider{
		Name: "infrastructure", OrgUUID: orgA, BackendURL: orgDirectURL, EndpointsValid: true,
		EdgeRoute: &providers.EdgeRoute{WorkspaceUUID: wsA, Cluster: "lc-ws-a", EdgeName: "prod", ServiceName: "provider-infrastructure"},
	})
	reg.Upsert(providers.Provider{
		Name: "vault", OrgUUID: orgA, BackendURL: orgDirectURL, EndpointsValid: true,
		EdgeRoute: &providers.EdgeRoute{WorkspaceUUID: wsA, Cluster: "lc-ws-a", EdgeName: "prod", ServiceName: "provider-vault"},
	})
	// Org B runs its own provider.
	reg.Upsert(providers.Provider{
		Name: "billing", OrgUUID: orgB, BackendURL: orgDirectURL, EndpointsValid: true,
		EdgeRoute: &providers.EdgeRoute{WorkspaceUUID: wsB, Cluster: "lc-ws-b", EdgeName: "b-edge", ServiceName: "provider-billing"},
	})

	backendProxy := providers.NewBackendProxy(reg, logr.Discard())
	backendProxy.SetDelegatedTokenIssuer(f.issuer)

	f.handler = New(Options{
		Providers: RegistryEnumerator(reg, backendProxy, logr.Discard()),
		Verifier: BearerVerifierFunc(func(_ *http.Request, token, _, _ string) (Caller, error) {
			c, ok := callers[token]
			if !ok {
				return Caller{}, ErrUnauthenticated
			}
			return c, nil
		}),
	})
	return f
}

// mcpCall POSTs one JSON-RPC method with bearer and returns the result.
func mcpCall(t *testing.T, h http.Handler, bearer, method, params string) json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, testMCPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s as %q: status = %d (body %s)", method, bearer, rr.Code, rr.Body.String())
	}
	payload := rr.Body.Bytes()
	if strings.HasPrefix(rr.Header().Get("Content-Type"), "text/event-stream") {
		d, ok := firstSSEData(payload)
		if !ok {
			t.Fatalf("no SSE data line in response: %s", payload)
		}
		payload = d
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, payload)
	}
	return env.Result
}

func toolNames(t *testing.T, h http.Handler, bearer string) []string {
	t.Helper()
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(mcpCall(t, h, bearer, "tools/list", `{}`), &out); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	names := make([]string, 0, len(out.Tools))
	for _, tl := range out.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	return names
}

func assertTools(t *testing.T, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

// assertOrgRequestsDelegated checks every tunnelled request in reqs carried
// exactly the delegated token (so never a caller's bearer) and the delegated
// user's name, and returns how many there were.
func assertOrgRequestsDelegated(t *testing.T, reqs []seenRequest, wantToken, wantUser string) int {
	t.Helper()
	n := 0
	for _, r := range reqs {
		if !strings.HasPrefix(r.path, "/edgeproxy/") {
			continue
		}
		n++
		if r.authorization != "Bearer "+wantToken {
			t.Errorf("org-owned provider request %s %s carried Authorization %q, want the delegated token %q", r.method, r.path, r.authorization, wantToken)
		}
		if _, isCallerBearer := callers[strings.TrimPrefix(r.authorization, "Bearer ")]; isCallerBearer {
			t.Errorf("a caller's own bearer reached an org-owned provider (%s)", r.path)
		}
		if r.user != wantUser {
			t.Errorf("org-owned provider request X-Faros-User = %q, want %q", r.user, wantUser)
		}
	}
	return n
}

// Org A's member sees Org A's providers — its self-hosted infrastructure in
// place of the platform one, plus its own vault — reached over the edge with a
// delegated token, and never Org B's provider.
func TestOrgMemberFederatesOwnProvidersOverEdge(t *testing.T) {
	f := newOrgFixture(t)

	got := toolNames(t, f.handler, "alice-a")
	assertTools(t, got, "code__commit_files", "edges__ssh_exec", "infrastructure__provision", "vault__read_secret")

	// Shadowing: the platform infrastructure backend was never consulted for
	// an Org that replaced it.
	if n := len(f.platformInfra.all()); n != 0 {
		t.Fatalf("platform infrastructure received %d requests for an Org that shadows it, want 0", n)
	}
	// The org providers' self-declared BackendURL is never dialled.
	if n := len(f.orgDirect.all()); n != 0 {
		t.Fatalf("an org provider's BackendURL was dialled directly %d times, want 0", n)
	}
	// Every org request went over the edge, with the delegated token for
	// exactly this (org, workspace, user, provider), never the bearer.
	infraToken := "delegated:" + orgA + "/" + wsA + "/alice/infrastructure"
	var infraReqs, vaultReqs []seenRequest
	for _, r := range f.edges.all() {
		switch {
		case strings.HasPrefix(r.path, edgePath("lc-ws-a", "provider-infrastructure")):
			infraReqs = append(infraReqs, r)
		case strings.HasPrefix(r.path, edgePath("lc-ws-a", "provider-vault")):
			vaultReqs = append(vaultReqs, r)
		case strings.HasPrefix(r.path, "/edgeproxy/"):
			t.Fatalf("unexpected tunnelled request %+v", r)
		}
	}
	if assertOrgRequestsDelegated(t, infraReqs, infraToken, "alice") == 0 {
		t.Fatal("org infrastructure was never reached over the edge")
	}
	if assertOrgRequestsDelegated(t, vaultReqs, "delegated:"+orgA+"/"+wsA+"/alice/vault", "alice") == 0 {
		t.Fatal("org vault was never reached over the edge")
	}
	for _, r := range f.edges.all() {
		if r.method == "unexpected" {
			t.Fatalf("edges provider received an unexpected request %+v", r)
		}
	}

	// tools/call on the shadowing copy goes the same way.
	result := mcpCall(t, f.handler, "alice-a", "tools/call", `{"name":"infrastructure__provision","arguments":{}}`)
	if !strings.Contains(string(result), "called provision on org-a-infrastructure") {
		t.Fatalf("tools/call did not reach the org's own copy: %s", result)
	}
	calls := 0
	for _, r := range f.edges.all() {
		if r.method == "tools/call" {
			calls++
			if r.authorization != "Bearer "+infraToken {
				t.Fatalf("tools/call to the org provider carried Authorization %q", r.authorization)
			}
		}
	}
	if calls != 1 {
		t.Fatalf("tools/call reached the org provider %d times, want 1", calls)
	}
	for _, tuple := range f.issuer.tuples() {
		if !strings.HasPrefix(tuple, orgA+"/"+wsA+"/alice/") {
			t.Fatalf("delegated token minted for %q, want only (org-a, ws-a, alice)", tuple)
		}
	}
}

// Org B's member sees Org B's provider and the PLATFORM infrastructure (Org B
// shadows nothing) — and nothing of Org A's.
func TestOrgProvidersInvisibleToOtherOrgs(t *testing.T) {
	f := newOrgFixture(t)

	got := toolNames(t, f.handler, "bob-b")
	assertTools(t, got, "billing__invoice", "code__commit_files", "edges__ssh_exec", "infrastructure__platform_provision")

	for _, r := range f.edges.all() {
		if strings.HasPrefix(r.path, edgePath("lc-ws-a", "")) || strings.Contains(r.path, "lc-ws-a") {
			t.Fatalf("Org B's request reached Org A's provider: %+v", r)
		}
	}
	for _, tuple := range f.issuer.tuples() {
		if tuple != orgB+"/"+wsB+"/bob/billing" {
			t.Fatalf("delegated token minted for %q while serving Org B, want only org-b/ws-b/bob/billing", tuple)
		}
	}
	// And the reverse: Org A's member never sees billing.
	for _, name := range toolNames(t, f.handler, "alice-a") {
		if strings.HasPrefix(name, "billing__") {
			t.Fatalf("Org A sees Org B's tool %q", name)
		}
	}
}

// A ServiceAccount bearer and an org-scope cluster have no delegated identity:
// the org providers are skipped — and the platform copy an org shadows does
// not come back in their place. The bearer reaches no org provider.
func TestOrgProvidersSkippedWithoutDelegatedIdentity(t *testing.T) {
	for _, bearer := range []string{"sa-a", "alice-a-org"} {
		t.Run(bearer, func(t *testing.T) {
			f := newOrgFixture(t)
			got := toolNames(t, f.handler, bearer)
			assertTools(t, got, "code__commit_files", "edges__ssh_exec")
			if n := len(f.platformInfra.all()); n != 0 {
				t.Fatalf("platform infrastructure received %d requests for an Org that shadows it, want 0", n)
			}
			if n := len(f.orgDirect.all()); n != 0 {
				t.Fatalf("an org provider's BackendURL was dialled directly %d times, want 0", n)
			}
			for _, r := range f.edges.all() {
				if strings.HasPrefix(r.path, "/edgeproxy/") {
					t.Fatalf("org provider contacted without a delegated identity: %+v", r)
				}
			}
			if calls := f.issuer.tuples(); len(calls) != 0 {
				t.Fatalf("delegated tokens minted for a caller with no delegated identity: %v", calls)
			}
		})
	}
}

// A failed mint drops the provider for this request; it never falls back to
// the caller's bearer or to the provider's BackendURL.
func TestOrgProviderSkippedWhenMintFails(t *testing.T) {
	f := newOrgFixture(t)
	f.issuer.err = errors.New("kcp unavailable")

	got := toolNames(t, f.handler, "alice-a")
	assertTools(t, got, "code__commit_files", "edges__ssh_exec")
	for _, r := range f.edges.all() {
		if strings.HasPrefix(r.path, "/edgeproxy/") {
			t.Fatalf("org provider contacted after a failed mint: %+v", r)
		}
	}
	if n := len(f.orgDirect.all()) + len(f.platformInfra.all()); n != 0 {
		t.Fatalf("%d fallback requests after a failed mint, want 0", n)
	}
}

// Platform providers are unchanged: dialled directly with the caller's bearer
// and the verified cluster as X-Faros-Tenant / X-Faros-Cluster.
func TestPlatformProvidersStillReceiveCallerBearer(t *testing.T) {
	f := newOrgFixture(t)
	for _, bearer := range []string{"alice-a", "platform"} {
		toolNames(t, f.handler, bearer)
	}
	reqs := f.platformCode.all()
	if len(reqs) == 0 {
		t.Fatal("platform code provider was never federated")
	}
	for _, r := range reqs {
		if r.authorization != "Bearer alice-a" && r.authorization != "Bearer platform" {
			t.Fatalf("platform provider received Authorization %q, want the caller's bearer", r.authorization)
		}
		if r.tenant != "some-cluster" || r.cluster != "some-cluster" {
			t.Fatalf("platform provider saw tenant=%q cluster=%q, want some-cluster for both", r.tenant, r.cluster)
		}
		if r.user != "" {
			t.Fatalf("platform provider saw X-Faros-User %q, want none (unchanged)", r.user)
		}
	}
	// An Org-less caller gets the platform catalog only.
	assertTools(t, toolNames(t, f.handler, "platform"), "code__commit_files", "edges__ssh_exec", "infrastructure__platform_provision")
}

// Defence in depth in the federation client: an org-owned target that somehow
// arrives without a Transport is never contacted — not with the bearer, not at
// all.
func TestOrgTargetWithoutTransportIsNeverDialled(t *testing.T) {
	provider, upstream := countingProvider(t)
	h := New(Options{
		Providers: func(context.Context, Caller) []ProviderTarget {
			return []ProviderTarget{{Name: "vault", OrgUUID: orgA, MCPURL: provider.URL}}
		},
		Verifier: allowAll,
	})
	if rr := toolsList(h, "caller-bearer"); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := upstream.Load(); got != 0 {
		t.Fatalf("org target without a transport received %d requests, want 0", got)
	}
	out := DiscoverFederation(context.Background(), []ProviderTarget{{Name: "vault", OrgUUID: orgA, MCPURL: provider.URL}}, "caller-bearer", "c")
	if len(out) != 1 || out[0].Reachable || out[0].Error == "" {
		t.Fatalf("DiscoverFederation = %+v, want one unreachable entry with an error", out)
	}
	if got := upstream.Load(); got != 0 {
		t.Fatalf("org target without a transport received %d discovery requests, want 0", got)
	}
}

// A target with a Transport never has the caller's bearer attached by the
// federation client — the transport alone decides Authorization.
func TestTransportTargetsNeverSeeCallerBearer(t *testing.T) {
	var sawAuth []string
	var mu sync.Mutex
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		sawAuth = append(sawAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		return jsonResponse(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`), nil
	})
	targets := []ProviderTarget{{Name: "vault", OrgUUID: orgA, MCPURL: "http://edges.invalid/mcp", Transport: rt}}
	DiscoverFederation(context.Background(), targets, "caller-bearer", "c")
	FederatedInstructions(context.Background(), targets, "caller-bearer", "c")
	mu.Lock()
	defer mu.Unlock()
	if len(sawAuth) != 2 {
		t.Fatalf("transport saw %d requests, want 2", len(sawAuth))
	}
	for _, a := range sawAuth {
		if a != "" {
			t.Fatalf("transport target received Authorization %q from the federation client, want none", a)
		}
	}
}
