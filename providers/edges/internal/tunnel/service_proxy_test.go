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
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"

	"github.com/railgrid/provider-sdk/dataplane"
)

// newServiceView builds a serviceView the way fetchService would decode one.
func newServiceView(kind, edge, ns, svcName string, port int32) *serviceView {
	v := &serviceView{}
	v.Spec.EdgeRef.Kind = kind
	v.Spec.EdgeRef.Name = edge
	v.Spec.Port = port
	if ns != "" {
		v.Spec.TargetRef = &struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		}{Namespace: ns, Name: svcName}
	}
	return v
}

// TestServiceViewTarget pins how each edge kind is addressed: a LinuxServer
// service on the host loopback, a KubernetesCluster service via cluster DNS.
func TestServiceViewTarget(t *testing.T) {
	cases := []struct {
		name         string
		view         *serviceView
		wantResource string
		wantTarget   string
	}{
		{
			name:         "linux server proxies to host loopback",
			view:         newServiceView("LinuxServer", "ha-box", "", "", 8123),
			wantResource: "linuxservers",
			wantTarget:   "http://127.0.0.1:8123",
		},
		{
			name:         "empty kind defaults to linux server",
			view:         newServiceView("", "ha-box", "", "", 8123),
			wantResource: "linuxservers",
			wantTarget:   "http://127.0.0.1:8123",
		},
		{
			name:         "kubernetes cluster proxies via cluster DNS",
			view:         newServiceView("KubernetesCluster", "kube-1", "home", "home-assistant", 8123),
			wantResource: "kubernetesclusters",
			wantTarget:   "http://home-assistant.home.svc:8123",
		},
		{
			name:         "macOS server proxies to host loopback",
			view:         newServiceView("MacOSServer", "mac-1", "", "", 8123),
			wantResource: "macosservers",
			wantTarget:   "http://127.0.0.1:8123",
		},
		{
			name:         "unknown edge kind has no tunnel resource",
			view:         newServiceView("UnexpectedKind", "edge-1", "", "", 8123),
			wantResource: "",
			wantTarget:   "http://127.0.0.1:8123",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.view.connResource(); got != tc.wantResource {
				t.Errorf("connResource() = %q, want %q", got, tc.wantResource)
			}
			if got := tc.view.target(); got != tc.wantTarget {
				t.Errorf("target() = %q, want %q", got, tc.wantTarget)
			}
		})
	}
}

// TestServiceViewTargetHTTPS covers the scheme passing through to the target.
func TestServiceViewTargetHTTPS(t *testing.T) {
	v := newServiceView("KubernetesCluster", "kube-1", "home", "ha", 8443)
	v.Spec.Scheme = "https"
	if got, want := v.target(), "https://ha.home.svc:8443"; got != want {
		t.Errorf("target() = %q, want %q", got, want)
	}
}

// TestServiceViewSetSvcHeaders pins the agent control headers: the target is
// always set, and the TLS opt-out only when spec.tlsInsecureSkipVerify is on
// (and is cleared when it is off, so a caller-supplied value cannot linger).
func TestServiceViewSetSvcHeaders(t *testing.T) {
	v := newServiceView("LinuxServer", "minis", "", "", 443)
	v.Spec.Host = "192.168.1.1"
	v.Spec.Scheme = "https"

	h := http.Header{svcTLSInsecureHeader: []string{"true"}}
	v.setSvcHeaders(h)
	if got, want := h.Get(svcTargetHeader), "https://192.168.1.1:443"; got != want {
		t.Errorf("%s = %q, want %q", svcTargetHeader, got, want)
	}
	if got := h.Get(svcTLSInsecureHeader); got != "" {
		t.Errorf("%s = %q, want unset when spec.tlsInsecureSkipVerify is false", svcTLSInsecureHeader, got)
	}

	v.Spec.TLSInsecureSkipVerify = true
	v.setSvcHeaders(h)
	if got := h.Get(svcTLSInsecureHeader); got != "true" {
		t.Errorf("%s = %q, want \"true\"", svcTLSInsecureHeader, got)
	}
}

// The Service routes are the shared grammar now, parsed by
// provider-sdk/dataplane rather than by a hand-rolled splitter. What this
// pins is the provider's half: which {resource}/{verb} pairs it serves, and
// that the old dialect no longer parses at all.
func TestServiceRouteGrammar(t *testing.T) {
	cases := []struct {
		name             string
		path             string
		wantOK           bool
		cluster, obj     string
		verb, tail       string
		wantVerbUnserved bool
	}{
		{
			name:    "proxy verb, no trailing path",
			path:    "/" + DataPlaneRoot + "/clusters/abc/services/ha-box-home-assistant/proxy",
			wantOK:  true,
			cluster: "abc", obj: "ha-box-home-assistant", verb: "proxy",
		},
		{
			name:    "proxy verb with a trailing service path",
			path:    "/" + DataPlaneRoot + "/clusters/abc/services/ha/proxy/api/services/cover/open_cover",
			wantOK:  true,
			cluster: "abc", obj: "ha", verb: "proxy", tail: "api/services/cover/open_cover",
		},
		{
			name:    "mcp verb",
			path:    "/" + DataPlaneRoot + "/clusters/xyz/services/ha/mcp",
			wantOK:  true,
			cluster: "xyz", obj: "ha", verb: "mcp",
		},
		{
			name:    "ticket verb",
			path:    "/" + DataPlaneRoot + "/clusters/xyz/services/ha/ticket",
			wantOK:  true,
			cluster: "xyz", obj: "ha", verb: "ticket",
		},
		{
			name:    "a connectable kind parses, and serves its own verbs",
			path:    "/" + DataPlaneRoot + "/clusters/abc/linuxservers/srv/ssh",
			wantOK:  true,
			cluster: "abc", obj: "srv", verb: "ssh",
		},
		{
			name:             "a verb this provider does not serve is refused",
			path:             "/" + DataPlaneRoot + "/clusters/abc/services/ha/k8s",
			wantOK:           true,
			cluster:          "abc",
			obj:              "ha",
			verb:             "k8s",
			wantVerbUnserved: true,
		},
		{
			name:   "the old apis/{group}/{version} dialect no longer parses",
			path:   "/" + DataPlaneRoot + "/clusters/abc/apis/edges.railgrid.ai/v1alpha1/services/ha/proxy",
			wantOK: false,
		},
		{
			name:   "too short",
			path:   "/" + DataPlaneRoot + "/clusters/abc/services/ha",
			wantOK: false,
		},
		{
			name:   "a traversal segment is refused, not cleaned",
			path:   "/" + DataPlaneRoot + "/clusters/abc/services/../ha/proxy",
			wantOK: false,
		},
		{
			name:   "a workspace path is not a cluster ID",
			path:   "/" + DataPlaneRoot + "/clusters/root:railgrid:tenants:a/services/ha/proxy",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, ok := dataplane.ParsePath(DataPlaneRoot, tc.path)
			if ok != tc.wantOK {
				t.Fatalf("ParsePath(%q) ok=%v, want %v", tc.path, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if req.ClusterID != tc.cluster || req.Name != tc.obj || req.Verb != tc.verb || req.Tail != tc.tail {
				t.Fatalf("ParsePath(%q) = %+v, want cluster=%q name=%q verb=%q tail=%q",
					tc.path, req, tc.cluster, tc.obj, tc.verb, tc.tail)
			}
			if served := verbServed(req.Resource, req.Verb); served == tc.wantVerbUnserved {
				t.Fatalf("verbServed(%q, %q) = %v", req.Resource, req.Verb, served)
			}
		})
	}
}

// The default must stay "secret": every Service written before spec.auth
// existed has an empty value, and reading that as passthrough would forward
// tenant hub tokens to appliances that never expected one.
func TestServiceViewAuthModeDefaults(t *testing.T) {
	for _, tc := range []struct{ spec, want string }{
		{"", serviceAuthSecret},
		{"secret", serviceAuthSecret},
		{"passthrough", serviceAuthPassthrough},
		{"none", serviceAuthNone},
		{"nonsense", serviceAuthSecret},
	} {
		v := &serviceView{}
		v.Spec.Auth = tc.spec
		if got := v.authMode(); got != tc.want {
			t.Errorf("authMode(%q) = %q, want %q", tc.spec, got, tc.want)
		}
	}
}

// applyServiceAuth decides what the upstream behind the tunnel sees. The
// passthrough case is the one that matters for a self-hosted provider backend:
// its whole authorization model is the caller's own bearer, so substituting a
// shared token would collapse per-user RBAC.
func TestApplyServiceAuth(t *testing.T) {
	const caller = "Bearer caller-token"
	for _, tc := range []struct {
		name  string
		mode  string
		token string
		want  string
	}{
		{"passthrough keeps the caller's credential", serviceAuthPassthrough, "", caller},
		{"passthrough ignores any service token", serviceAuthPassthrough, "svc-token", caller},
		{"secret substitutes the service token", serviceAuthSecret, "svc-token", "Bearer svc-token"},
		{"secret with no token strips rather than leaking the caller's", serviceAuthSecret, "", ""},
		{"none strips", serviceAuthNone, "svc-token", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			h.Set("Authorization", caller)
			applyServiceAuth(h, tc.mode, tc.token)
			if got := h.Get("Authorization"); got != tc.want {
				t.Fatalf("Authorization = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReadServiceTokenReadsTheSecretAsTheProvider pins WHO reads a Service's
// auth Secret: the provider, through its own APIExport virtual workspace
// (tenantConfigFor), after the caller has passed both gates — never the caller.
//
// It used to be read with the caller's bearer, which no hub-minted identity
// can ever satisfy: the hub's identity policy does not mint core-group `get`
// on secrets, so a workload identity (a factory runner dispatch, kuery) 403'd
// on every Service that had a credential attached, while a human's token
// worked. The Secret is edges-owned — the portal writes it labelled
// railgrid.ai/owner: edges — so it sits inside this provider's label-scoped
// `secrets` claim and its own virtual workspace serves it.
//
// The confused-deputy protection is unchanged and is asserted here too: the
// Secret's namespace/name come from the gated Service's spec (svc, decoded
// from the object gate 1 read as the caller), never from the request, so the
// provider can only ever unwrap the credential attached to a Service the
// caller was just authorized to use.
func TestReadServiceTokenReadsTheSecretAsTheProvider(t *testing.T) {
	var gotAuth, gotPath string
	reads := 0
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		// data.token is base64("svc-token").
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Secret",` +
			`"metadata":{"name":"railgrid-edges-svc-ha","namespace":"railgrid-system",` +
			`"labels":{"railgrid.ai/owner":"edges"}},` +
			`"data":{"token":"c3ZjLXRva2Vu"}}`))
	}))
	defer kcp.Close()

	// kcpConfig is what the caller-scoped client would have been built from
	// (userClusterConfig re-roots it and swaps in the caller's bearer). It
	// points at an unroutable host on purpose: a read that still went as the
	// caller would fail here rather than quietly pass.
	s := &Server{kcpConfig: &rest.Config{Host: "https://caller-path.invalid", BearerToken: "provider-sa"}}
	s.SetTenantConfigGetter(func(_ context.Context, cluster string) (*rest.Config, error) {
		return &rest.Config{Host: kcp.URL + "/clusters/" + cluster, BearerToken: "provider-vw"}, nil
	})

	svc := newServiceView(linuxServerKind, "ha-box", "", "", 8123)
	svc.Spec.AuthSecretRef = &corev1.SecretReference{Namespace: "railgrid-system", Name: "railgrid-edges-svc-ha"}

	token, err := s.readServiceToken(context.Background(), "tenant-a", svc)
	if err != nil {
		t.Fatalf("readServiceToken() error = %v, want nil", err)
	}
	if token != "svc-token" {
		t.Errorf("token = %q, want %q", token, "svc-token")
	}
	if reads != 1 {
		t.Fatalf("secret reads = %d, want exactly 1 (on the provider virtual workspace)", reads)
	}
	if got, want := gotAuth, "Bearer provider-vw"; got != want {
		t.Errorf("Authorization on the secret read = %q, want %q — the Secret must be read as the provider, not the caller", got, want)
	}
	// The path proves both the tenant the read was scoped to and that the
	// coordinates came from the gated Service's spec.
	if got, want := gotPath, "/clusters/tenant-a/api/v1/namespaces/railgrid-system/secrets/railgrid-edges-svc-ha"; got != want {
		t.Errorf("secret read path = %q, want %q", got, want)
	}
}

// A Service with no spec.authSecretRef is proxy-only: there is nothing to
// unwrap, and the provider must not touch kcp at all for it.
func TestReadServiceTokenSkipsKCPWithoutASecretRef(t *testing.T) {
	calls := 0
	s := &Server{}
	s.SetTenantConfigGetter(func(_ context.Context, _ string) (*rest.Config, error) {
		calls++
		return &rest.Config{Host: "https://unused.invalid"}, nil
	})

	token, err := s.readServiceToken(context.Background(), "tenant-a",
		newServiceView(linuxServerKind, "ha-box", "", "", 8123))
	if err != nil || token != "" {
		t.Fatalf("readServiceToken() = %q, %v; want \"\", nil", token, err)
	}
	if calls != 0 {
		t.Errorf("tenant config resolved %d times, want 0 for a secret-less service", calls)
	}
}
