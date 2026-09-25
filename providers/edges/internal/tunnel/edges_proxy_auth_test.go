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

package tunnel

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
)

// The identities and coordinates the data-plane tests share. The cluster is a
// kcp logical-cluster ID: the grammar refuses a workspace path.
const (
	testCluster = "11tcw27t4rdtnacy"
	testUser    = "alice@railgrid.test"
	testGroup   = "edges.railgrid.ai"
	testVersion = "v1alpha1"
)

// verbPath renders the kube path the shard forwards for a verb on an edge in
// the test cluster.
func verbPath(resource, name, verb, tail string) string {
	p := "/clusters/" + testCluster + "/apis/" + testGroup + "/" + testVersion + "/" + resource + "/" + name + "/" + verb
	if tail != "" {
		p += "/" + tail
	}
	return p
}

// edgeObject is what the provider reads in the tenant workspace.
func edgeObject(resource, kind, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": testGroup + "/" + testVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "uid": "uid-" + resource + "-" + name},
		"spec":       map[string]any{},
	}}
}

// serviceObject is a Service published from edge-1.
func serviceObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": testGroup + "/" + testVersion,
		"kind":       "Service",
		"metadata":   map[string]any{"name": name, "uid": "uid-svc-" + name},
		"spec": map[string]any{
			"port":    int64(8080),
			"edgeRef": map[string]any{"kind": "LinuxServer", "name": "edge-1"},
		},
	}}
}

// countingCallers wraps a ProviderCallerFactory and counts how often the gate
// asked for a provider client, so a test can prove the gate did NOT run.
type countingCallers struct {
	dataplane.ProviderCallerFactory
	asProvider atomic.Int32
}

func (c *countingCallers) AsProvider(clusterID string) (dynamic.Interface, error) {
	c.asProvider.Add(1)
	return c.ProviderCallerFactory.AsProvider(clusterID)
}

// newDataPlaneCallers builds the fake the handler under test gates with: one
// tenant cluster, one granted user who may see every object in it, and the
// objects the provider can read there.
func newDataPlaneCallers(objects ...*unstructured.Unstructured) *conformance.FakeCallers {
	return &conformance.FakeCallers{
		Cluster: testCluster,
		User:    testUser,
		Objects: objects,
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Group == testGroup && a.User == testUser
		},
	}
}

// newDataPlaneTestServer is a Server whose consumer data plane is fully wired
// for a unit test: a caller factory to gate with, a tunnel registry, and a
// tenant config pointing at nothing (the verbs under test never reach kcp).
func newDataPlaneTestServer(callers dataplane.ProviderCallerFactory) *Server {
	s := testServer()
	s.kcpConfig = &rest.Config{Host: "https://kcp.invalid"}
	s.tenantConfig = func(context.Context, string) (*rest.Config, error) { return s.kcpConfig, nil }
	s.edgeConnManager = NewConnManager()
	s.logger = klog.Background()
	s.callers = callers
	return s
}

// verbRequest is a request the way serve's subresource adapter hands one to
// the data-plane handler: the URL untouched, the caller kcp stamped in the
// headers AND in the context, and the parsed route beside it. An empty user
// is a request the adapter would have refused before dispatch; it is built
// here without an identity to prove the handler refuses it too.
func verbRequest(t *testing.T, method, path, user string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	ctx := r.Context()
	if user != "" {
		r.Header.Set(dataplane.HeaderRemoteUser, user)
		r.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
		ctx = dataplane.WithProxiedIdentity(ctx, dataplane.ProxiedIdentity{User: user, Groups: []string{"system:authenticated"}})
	}
	route, err := dataplane.ParseSubresourceRequest(r)
	if err != nil {
		t.Fatalf("test path %q does not parse: %v", path, err)
	}
	ctx = dataplane.WithRoute(ctx, route)
	r = r.WithContext(ctx)
	r.Header.Set(dataplane.HeaderCluster, route.ClusterID)
	return r
}

// okDialer is a tunnel Dialer whose far end answers every request with 200
// "ok" and records the last request it saw, standing in for an edge agent.
type okDialer struct {
	dialed atomic.Int32
	last   atomic.Pointer[http.Request]
}

func (d *okDialer) Dial(context.Context) (net.Conn, error) {
	d.dialed.Add(1)
	local, remote := net.Pipe()
	go func() {
		defer remote.Close() //nolint:errcheck
		req, err := http.ReadRequest(bufio.NewReader(remote))
		if err != nil {
			return
		}
		// Drain the body: net.Pipe is unbuffered, so a proxy still writing a
		// request body would otherwise deadlock against this response.
		_, _ = io.Copy(io.Discard, req.Body)
		d.last.Store(req)
		_, _ = io.WriteString(remote, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	}()
	return local, nil
}

// The data plane is conformant with the contract when driven as a bare
// handler: the granted verb succeeds through the tunnel, a request with no
// stamped caller is 401, a caller who cannot see the edge and a foreign
// cluster are both 404, an undeclared verb is not served, and a malformed
// path is refused.
func TestEdgesDataPlaneIsConformant(t *testing.T) {
	callers := newDataPlaneCallers(edgeObject(linuxServerResource, "LinuxServer", "edge-1"))
	s := newDataPlaneTestServer(callers)
	s.edgeConnManager.storeLocalForTest(edgeConnKey(linuxServerResource, testCluster, "edge-1"), &okDialer{})

	conformance.Test(t, s.buildEdgesProxyHandler(), conformance.Fixtures{
		Callers:        callers,
		GrantedPath:    verbPath(linuxServerResource, "edge-1", VerbK8s, "api"),
		DeniedPath:     verbPath(linuxServerResource, "edge-1", "teleport", ""),
		Method:         http.MethodGet,
		SkipStrictBody: true,
		MalformedPaths: []string{
			"/clusters/" + testCluster + "/apis/" + testGroup + "/" + testVersion + "/linuxservers/../k8s",
			"/clusters/root:railgrid:tenants:a/apis/" + testGroup + "/" + testVersion + "/linuxservers/edge-1/k8s",
			// The retired hub-proxied grammar is not a route at all.
			"/dataplane/clusters/" + testCluster + "/linuxservers/edge-1/k8s",
		},
	})
}

// The k8s verb forwards the route's tail (and the query) to the agent under
// /k8s/, with nothing about the caller attached: the identity kcp stamped
// means nothing to the edge's API server.
func TestK8sVerbForwardsTheTailToTheAgent(t *testing.T) {
	callers := newDataPlaneCallers(edgeObject(kubernetesClusterResource, "KubernetesCluster", "prod"))
	s := newDataPlaneTestServer(callers)
	dialer := &okDialer{}
	s.edgeConnManager.storeLocalForTest(edgeConnKey(kubernetesClusterResource, testCluster, "prod"), dialer)

	req := verbRequest(t, http.MethodGet, verbPath(kubernetesClusterResource, "prod", VerbK8s, "api/v1/namespaces/k8s/pods")+"?limit=5", testUser)
	req.Header.Set(dataplane.HeaderRemoteExtraPrefix+"warrant", "secret")
	rr := httptest.NewRecorder()
	s.buildEdgesProxyHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("k8s verb = %d %q, want 200 ok", rr.Code, rr.Body.String())
	}
	seen := dialer.last.Load()
	if seen == nil {
		t.Fatal("the agent saw no request")
	}
	if got, want := seen.URL.Path, "/k8s/api/v1/namespaces/k8s/pods"; got != want {
		t.Errorf("agent path = %q, want %q", got, want)
	}
	if got, want := seen.URL.RawQuery, "limit=5"; got != want {
		t.Errorf("agent query = %q, want %q", got, want)
	}
	for _, header := range []string{dataplane.HeaderRemoteUser, dataplane.HeaderRemoteGroup, dataplane.HeaderRemoteExtraPrefix + "warrant", "Authorization"} {
		if v := seen.Header.Get(header); v != "" {
			t.Errorf("%s = %q reached the agent; nothing about the caller may cross the tunnel", header, v)
		}
	}
}

// MacOSServer is Service-only: it declares no consumer verb of its own, so a
// path naming one is a 404 BEFORE any gate runs. That ordering matters — an
// un-served verb must not be usable to probe whether an object exists.
func TestMacOSServerHasNoSSHOrKubernetesDataPlane(t *testing.T) {
	for _, verb := range []string{"ssh", "k8s", "mcp", "proxy"} {
		t.Run(verb, func(t *testing.T) {
			callers := &countingCallers{ProviderCallerFactory: newDataPlaneCallers(edgeObject(macOSServerResource, "MacOSServer", "mac-1"))}
			s := newDataPlaneTestServer(callers)
			rr := httptest.NewRecorder()
			s.buildEdgesProxyHandler().ServeHTTP(rr, verbRequest(t, http.MethodGet, verbPath(macOSServerResource, "mac-1", verb, ""), testUser))

			if rr.Code != http.StatusNotFound {
				t.Fatalf("MacOSServer %s status = %d, want 404 (body %q)", verb, rr.Code, rr.Body.String())
			}
			if n := callers.asProvider.Load(); n != 0 {
				t.Fatalf("gate ran %d times for an un-served verb", n)
			}
		})
	}
}

// Every verb is gated on the identity kcp stamped, and only that: a caller who
// cannot see the object is refused with 404 (never 403, which would disclose
// existence), a request the adapter would never have dispatched — no identity
// — is 401, and a granted caller gets past the gate to the tunnel lookup.
func TestEdgesProxyHandlerGatesOnTheStampedIdentity(t *testing.T) {
	objects := []*unstructured.Unstructured{
		edgeObject(linuxServerResource, "LinuxServer", "edge-1"),
		serviceObject("svc-1"),
	}
	cases := []struct {
		name string
		path string
		user string
		want int
	}{
		{name: "granted caller reaches the tunnel lookup on ssh", path: verbPath(linuxServerResource, "edge-1", VerbSSH, ""), user: testUser, want: http.StatusBadGateway},
		{name: "granted caller reaches the tunnel lookup on the service proxy", path: verbPath(serviceResource, "svc-1", VerbProxy, ""), user: testUser, want: http.StatusBadGateway},
		{name: "a stranger is denied without disclosure", path: verbPath(linuxServerResource, "edge-1", VerbSSH, ""), user: conformance.StrangerUser, want: http.StatusNotFound},
		{name: "a stranger is denied on the service proxy", path: verbPath(serviceResource, "svc-1", VerbProxy, ""), user: conformance.StrangerUser, want: http.StatusNotFound},
		{name: "an object that does not exist is denied identically", path: verbPath(linuxServerResource, "ghost", VerbSSH, ""), user: testUser, want: http.StatusNotFound},
		{name: "no stamped caller is unauthorized", path: verbPath(linuxServerResource, "edge-1", VerbSSH, ""), user: "", want: http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newDataPlaneTestServer(newDataPlaneCallers(objects...))
			rr := httptest.NewRecorder()
			s.buildEdgesProxyHandler().ServeHTTP(rr, verbRequest(t, http.MethodGet, tc.path, tc.user))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %q)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// A bearer on a verb is not a credential. The adapter drops Authorization
// before dispatch, and the handler never reads it: a request that spells a
// stranger's identity in the headers but carries some token is still the
// stranger.
func TestEdgesProxyHandlerIgnoresBearers(t *testing.T) {
	s := newDataPlaneTestServer(newDataPlaneCallers(edgeObject(linuxServerResource, "LinuxServer", "edge-1")))
	req := verbRequest(t, http.MethodGet, verbPath(linuxServerResource, "edge-1", VerbSSH, ""), conformance.StrangerUser)
	req.Header.Set("Authorization", "Bearer would-have-been-the-caller")
	rr := httptest.NewRecorder()
	s.buildEdgesProxyHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: a bearer must not stand in for the stamped identity (body %q)", rr.Code, rr.Body.String())
	}
}

// A request that did not come through serve's adapter carries no route, and
// nothing else is entitled to say what it addresses.
func TestEdgesProxyHandlerRefusesARequestWithoutARoute(t *testing.T) {
	s := newDataPlaneTestServer(newDataPlaneCallers(edgeObject(linuxServerResource, "LinuxServer", "edge-1")))
	req := httptest.NewRequest(http.MethodGet, verbPath(linuxServerResource, "edge-1", VerbSSH, ""), nil)
	req = req.WithContext(dataplane.WithProxiedIdentity(req.Context(), dataplane.ProxiedIdentity{User: testUser}))
	rr := httptest.NewRecorder()
	s.buildEdgesProxyHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", rr.Code, rr.Body.String())
	}
}

// recordingDialer is a tunnel Dialer that records whether the data plane ever
// reached it. Registering one lets a test prove a request was refused BEFORE
// the tunnel lookup rather than merely failing downstream.
type recordingDialer struct{ dialed bool }

func (d *recordingDialer) Dial(context.Context) (net.Conn, error) {
	d.dialed = true
	return nil, errors.New("recordingDialer: no connection")
}

// Without a provider caller factory there is nothing to run the access review
// with, so the consumer data plane must refuse the request instead of skipping
// the gate and proxying. The handlers are mounted unconditionally, so this is
// the only thing standing between a misconfigured provider (no usable kcp
// kubeconfig) and an ungated proxy. There is no test-only bypass on this path.
func TestEdgesProxyFailsClosedWithoutCallers(t *testing.T) {
	for _, path := range []string{
		verbPath(linuxServerResource, "edge-1", VerbSSH, ""),
		verbPath(serviceResource, "svc-1", VerbProxy, ""),
		verbPath(linuxServerResource, "edge-1", VerbK8s, "api"),
	} {
		t.Run(path, func(t *testing.T) {
			s := newDataPlaneTestServer(nil)
			s.kcpConfig = nil
			s.tenantConfig = nil
			s.allowStaticTokenBypass = true // the agent-ingress bypass must not leak here
			dialer := &recordingDialer{}
			s.edgeConnManager.storeLocalForTest(edgeConnKey(linuxServerResource, testCluster, "edge-1"), dialer)

			rr := httptest.NewRecorder()
			s.buildEdgesProxyHandler().ServeHTTP(rr, verbRequest(t, http.MethodGet, path, testUser))

			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d (body %q)", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
			}
			if dialer.dialed {
				t.Fatal("request reached the tunnel dialer with nothing to gate it")
			}
		})
	}
}

// The SSH upgrade must select a subprotocol whenever the client offered one,
// or a browser aborts the connection. kcp strips the bearer protocol on
// success, so normally another one arrives; the bearer protocol is echoed only
// when it is all there is, and is never read as a credential.
func TestSelectSubprotocol(t *testing.T) {
	const bearer = bearerSubprotocolPrefix + "dG9rZW4"
	cases := []struct {
		name    string
		offered []string
		want    string
	}{
		{name: "none offered", want: ""},
		{name: "a plain protocol is echoed", offered: []string{"railgrid.ssh.v1"}, want: "railgrid.ssh.v1"},
		{name: "the bearer protocol is skipped when another is offered", offered: []string{bearer + ", railgrid.ssh.v1"}, want: "railgrid.ssh.v1"},
		{name: "across header lines too", offered: []string{bearer, "railgrid.ssh.v1"}, want: "railgrid.ssh.v1"},
		{name: "the bearer protocol alone is echoed back", offered: []string{bearer}, want: bearer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			for _, v := range tc.offered {
				r.Header.Add("Sec-WebSocket-Protocol", v)
			}
			if got := selectSubprotocol(r); got != tc.want {
				t.Fatalf("selectSubprotocol(%q) = %q, want %q", strings.Join(tc.offered, " | "), got, tc.want)
			}
		})
	}
}

// New refuses the test-only static-token set unless explicitly opted in, and
// never alongside a kcp config. The set only ever stands in for an AGENT
// credential on the tunnel class; the consumer data plane never consults it.
func TestNewRejectsStaticTokensOutsideTestBypass(t *testing.T) {
	kind := KindConfig{
		GVR:  schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "linuxservers"},
		Kind: "LinuxServer",
	}
	base := Config{Kinds: []KindConfig{kind}, AgentPickupPath: "/agent/proxy", Logger: klog.Background()}

	cfg := base
	cfg.StaticTokens = []string{"static-secret"}
	if _, err := New(cfg); err == nil {
		t.Fatal("New accepted StaticTokens without AllowStaticTokenBypass")
	}

	cfg = base
	cfg.StaticTokens = []string{"static-secret"}
	cfg.AllowStaticTokenBypass = true
	cfg.KCPConfig = &rest.Config{Host: "https://kcp.invalid"}
	if _, err := New(cfg); err == nil {
		t.Fatal("New accepted AllowStaticTokenBypass alongside a KCPConfig")
	}

	cfg = base
	cfg.StaticTokens = []string{"static-secret"}
	cfg.AllowStaticTokenBypass = true
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New with test bypass and no kcp config: %v", err)
	}
	if _, ok := s.staticTokens["static-secret"]; !ok {
		t.Fatal("test bypass did not register the static token")
	}
}
