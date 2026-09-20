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
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/railgrid/provider-sdk/dataplane"
)

// newAuthTestServer builds a Server with a kcp config present (so the gates
// are active), an injected gate func, and a static-token set populated the way
// a legacy RAILGRID_STATIC_TOKENS config would have — to prove that set no
// longer short-circuits authorization anywhere.
func newAuthTestServer(t *testing.T, gateErr error, calls *[]string) *Server {
	t.Helper()
	kcp := &rest.Config{Host: "https://kcp.invalid"}
	s := testServer("/services/providers/edges/" + DataPlaneRoot)
	s.kcpConfig = kcp
	s.tenantConfig = func(context.Context, string) (*rest.Config, error) { return kcp, nil }
	s.edgeConnManager = NewConnManager()
	s.tickets = newTicketStore()
	s.logger = klog.Background()
	s.staticTokens = map[string]struct{}{"static-secret": {}}
	s.gateFn = func(_ context.Context, _ *Server, token string, req dataplane.Request) (*unstructured.Unstructured, error) {
		*calls = append(*calls, token+" "+dataplane.SSARVerb+" edges.railgrid.ai/"+req.Resource+"/"+req.Verb+"/"+req.Name+"@"+req.ClusterID)
		if gateErr != nil {
			return nil, gateErr
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "edges.railgrid.ai/v1alpha1",
			"metadata":   map[string]any{"name": req.Name},
			"spec": map[string]any{
				"port":    float64(8080),
				"edgeRef": map[string]any{"kind": "LinuxServer", "name": "edge-1"},
			},
		}}, nil
	}
	return s
}

const (
	authTestEdgePath    = "/" + DataPlaneRoot + "/clusters/ws-1/linuxservers/edge-1/ssh"
	authTestServicePath = "/" + DataPlaneRoot + "/clusters/ws-1/services/svc-1/proxy/"
)

// MacOSServer is Service-only: it declares no verb of its own, so a path
// naming one is a 404 BEFORE any gate runs. That ordering matters — an
// un-served verb must not be usable to probe whether an object exists.
func TestMacOSServerHasNoSSHOrKubernetesDataPlane(t *testing.T) {
	for _, verb := range []string{"ssh", "k8s", "mcp", "proxy"} {
		t.Run(verb, func(t *testing.T) {
			var calls []string
			s := newAuthTestServer(t, nil, &calls)
			req := httptest.NewRequest(http.MethodGet,
				"/"+DataPlaneRoot+"/clusters/ws-1/macosservers/mac-1/"+verb, nil)
			req.Header.Set("Authorization", "Bearer caller-token")
			rr := httptest.NewRecorder()

			s.buildEdgesProxyHandler().ServeHTTP(rr, req)

			if rr.Code != http.StatusNotFound {
				t.Fatalf("MacOSServer %s status = %d, want 404 (body %q)", verb, rr.Code, rr.Body.String())
			}
			if len(calls) != 0 {
				t.Fatalf("gate ran for an un-served verb: %v", calls)
			}
		})
	}
}

func TestEdgesProxyHandlerAuthorizesEveryToken(t *testing.T) {
	cases := []struct {
		name         string
		token        string
		path         string
		authorizeErr error
		wantCall     string
	}{
		{
			name:     "static token is authorized like any other token",
			token:    "static-secret",
			path:     authTestEdgePath,
			wantCall: "static-secret create edges.railgrid.ai/linuxservers/ssh/edge-1@ws-1",
		},
		{
			name:         "static token denied by kcp is forbidden",
			token:        "static-secret",
			path:         authTestEdgePath,
			authorizeErr: fmt.Errorf("%w: access denied", dataplane.ErrDenied),
			wantCall:     "static-secret create edges.railgrid.ai/linuxservers/ssh/edge-1@ws-1",
		},
		{
			name:     "user token is authorized",
			token:    "user-token",
			path:     authTestEdgePath,
			wantCall: "user-token create edges.railgrid.ai/linuxservers/ssh/edge-1@ws-1",
		},
		{
			name:         "static token on the service proxy is authorized too",
			token:        "static-secret",
			path:         authTestServicePath,
			authorizeErr: fmt.Errorf("%w: access denied", dataplane.ErrDenied),
			wantCall:     "static-secret create edges.railgrid.ai/services/proxy/svc-1@ws-1",
		},
		{
			name:     "user token on the service proxy is authorized",
			token:    "user-token",
			path:     authTestServicePath,
			wantCall: "user-token create edges.railgrid.ai/services/proxy/svc-1@ws-1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			s := newAuthTestServer(t, tc.authorizeErr, &calls)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			rr := httptest.NewRecorder()
			s.buildEdgesProxyHandler().ServeHTTP(rr, req)

			// The authorization side-effect is the behaviour under test: exactly
			// one delegated check, with the caller's own token and the edge's
			// coordinates. Downstream status codes belong to whatever the handler
			// does after the check and are deliberately not asserted here.
			if len(calls) != 1 {
				t.Fatalf("gate called %d times, want exactly once: %v", len(calls), calls)
			}
			if calls[0] != tc.wantCall {
				t.Fatalf("gate call = %q, want %q", calls[0], tc.wantCall)
			}

			if tc.authorizeErr != nil {
				// A denial is a 404: the response must not disclose whether
				// the object exists.
				if rr.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want %d (body %q)", rr.Code, http.StatusNotFound, rr.Body.String())
				}
				return
			}
			// Allowed: only assert the request was not rejected by the auth
			// layer, whatever the downstream outcome is.
			switch rr.Code {
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable:
				t.Fatalf("authorized request rejected with %d (body %q)", rr.Code, rr.Body.String())
			}
		})
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

// newNoKCPTestServer builds a Server with NO kcp credential and no test bypass —
// the posture of a provider whose RAILGRID_PROVIDER_KUBECONFIG is missing or
// unreadable — with a live tunnel already registered for both the edge and the
// Service's edge.
func newNoKCPTestServer(t *testing.T, calls *[]string) (*Server, *recordingDialer) {
	t.Helper()
	s := testServer("/services/providers/edges/" + DataPlaneRoot)
	s.kcpConfig = nil
	s.tenantConfig = nil
	s.edgeConnManager = NewConnManager()
	s.tickets = newTicketStore()
	s.logger = klog.Background()
	s.gateFn = func(_ context.Context, _ *Server, token string, req dataplane.Request) (*unstructured.Unstructured, error) {
		*calls = append(*calls, token+" "+dataplane.SSARVerb+" edges.railgrid.ai/"+req.Resource+"/"+req.Verb+"/"+req.Name+"@"+req.ClusterID)
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"name": req.Name},
			"spec": map[string]any{
				"port":    float64(8080),
				"edgeRef": map[string]any{"kind": "LinuxServer", "name": "edge-1"},
			},
		}}, nil
	}
	d := &recordingDialer{}
	s.edgeConnManager.dials[edgeConnKey("linuxservers", "ws-1", "edge-1")] = d
	return s, d
}

// Without a kcp credential there is nothing to TokenReview a bearer against, so
// the consumer data plane must refuse the request instead of skipping the check
// and proxying. The handlers are mounted unconditionally, so this is the only
// thing standing between a misconfigured provider and an unauthorized proxy.
func TestEdgesProxyFailsClosedWithoutKCPConfig(t *testing.T) {
	for _, path := range []string{authTestEdgePath, authTestServicePath} {
		t.Run(path, func(t *testing.T) {
			var calls []string
			s, dialer := newNoKCPTestServer(t, &calls)

			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer any-token")
			rr := httptest.NewRecorder()
			s.buildEdgesProxyHandler().ServeHTTP(rr, req)

			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d (body %q)", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
			}
			if dialer.dialed {
				t.Fatal("request reached the tunnel dialer with no kcp authorization available")
			}
			if len(calls) != 0 {
				t.Fatalf("the gate ran with no kcp config: %v", calls)
			}
		})
	}
}

// The test-only bypass (AllowStaticTokenBypass, which main.go never sets) is
// the single exception: unit tests exercise the tunnel plane with no kcp at
// all, so those requests must still be served.
func TestEdgesProxyServesUnderTestOnlyBypass(t *testing.T) {
	var calls []string
	s, dialer := newNoKCPTestServer(t, &calls)
	s.allowStaticTokenBypass = true

	req := httptest.NewRequest(http.MethodGet, "/"+DataPlaneRoot+"/clusters/ws-1/linuxservers/edge-1/k8s", nil)
	req.Header.Set("Authorization", "Bearer any-token")
	rr := httptest.NewRecorder()
	s.buildEdgesProxyHandler().ServeHTTP(rr, req)

	if rr.Code == http.StatusServiceUnavailable {
		t.Fatalf("test bypass refused the request: %d (body %q)", rr.Code, rr.Body.String())
	}
	if !dialer.dialed {
		t.Fatal("test bypass did not reach the tunnel dialer")
	}
}

// New refuses the test-only static-token set unless explicitly opted in, and
// never alongside a kcp config.
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
