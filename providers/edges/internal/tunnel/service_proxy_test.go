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

// The Service routes are kube paths — kcp custom subresources on this
// provider's export — parsed by provider-sdk/dataplane rather than by a
// hand-rolled splitter. What this pins is the provider's half: which
// {resource}/{verb} pairs it serves, and that the retired hub-proxied grammar
// is not a route at all.
func TestServiceRouteGrammar(t *testing.T) {
	const base = "/clusters/11tcw27t4rdtnacy/apis/edges.railgrid.ai/v1alpha1"
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
			path:    base + "/services/ha-box-home-assistant/proxy",
			wantOK:  true,
			cluster: "11tcw27t4rdtnacy", obj: "ha-box-home-assistant", verb: "proxy",
		},
		{
			name:    "proxy verb with a trailing service path",
			path:    base + "/services/ha/proxy/api/services/cover/open_cover",
			wantOK:  true,
			cluster: "11tcw27t4rdtnacy", obj: "ha", verb: "proxy", tail: "api/services/cover/open_cover",
		},
		{
			name:    "mcp verb",
			path:    base + "/services/ha/mcp",
			wantOK:  true,
			cluster: "11tcw27t4rdtnacy", obj: "ha", verb: "mcp",
		},
		{
			name:    "a connectable kind parses, and serves its own verbs",
			path:    base + "/linuxservers/srv/ssh",
			wantOK:  true,
			cluster: "11tcw27t4rdtnacy", obj: "srv", verb: "ssh",
		},
		{
			name:             "a verb this provider does not serve is refused",
			path:             base + "/services/ha/k8s",
			wantOK:           true,
			cluster:          "11tcw27t4rdtnacy",
			obj:              "ha",
			verb:             "k8s",
			wantVerbUnserved: true,
		},
		{
			name:             "the ticket verb is gone: a browser presents the kube bearer subprotocol instead",
			path:             base + "/services/ha/ticket",
			wantOK:           true,
			cluster:          "11tcw27t4rdtnacy",
			obj:              "ha",
			verb:             "ticket",
			wantVerbUnserved: true,
		},
		{
			name:   "the retired hub-proxied grammar is not a route",
			path:   "/dataplane/clusters/11tcw27t4rdtnacy/services/ha/proxy",
			wantOK: false,
		},
		{
			name:   "too short",
			path:   base + "/services/ha",
			wantOK: false,
		},
		{
			name:   "a traversal segment is refused, not cleaned",
			path:   base + "/services/../ha/proxy",
			wantOK: false,
		},
		{
			name:   "a workspace path is not a cluster ID",
			path:   "/clusters/root:railgrid:tenants:a/apis/edges.railgrid.ai/v1alpha1/services/ha/proxy",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := dataplane.ParseSubresourcePath(tc.path)
			if ok := err == nil; ok != tc.wantOK {
				t.Fatalf("ParseSubresourcePath(%q) ok=%v (err %v), want %v", tc.path, ok, err, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if req.ClusterID != tc.cluster || req.Name != tc.obj || req.Verb != tc.verb || req.Tail != tc.tail {
				t.Fatalf("ParseSubresourcePath(%q) = %+v, want cluster=%q name=%q verb=%q tail=%q",
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

// applyServiceAuth decides what the upstream behind the tunnel sees. On the
// kube path the request's own Authorization was the caller's kcp credential
// and never reaches this proxy (the adapter drops it); a credential meant for
// the far end travels in X-Railgrid-Upstream-Authorization instead, which
// passthrough forwards as the upstream Authorization. That is what lets a
// self-hosted provider backend keep its own per-user authorization model —
// the hub's edge hop hands it the delegated token this way — while no mode
// ever leaks the carrier header itself.
func TestApplyServiceAuth(t *testing.T) {
	const upstream = "Bearer delegated-token"
	for _, tc := range []struct {
		name         string
		mode         string
		token        string
		withUpstream bool
		want         string
	}{
		{"passthrough forwards the upstream credential", serviceAuthPassthrough, "", true, upstream},
		{"passthrough ignores any service token", serviceAuthPassthrough, "svc-token", true, upstream},
		{"passthrough with nothing to forward sends nothing", serviceAuthPassthrough, "", false, ""},
		{"secret substitutes the service token", serviceAuthSecret, "svc-token", true, "Bearer svc-token"},
		{"secret with no token strips rather than leaking the caller's", serviceAuthSecret, "", true, ""},
		{"none strips", serviceAuthNone, "svc-token", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			// What a request would carry if the adapter had not already
			// dropped it: it must never be what the upstream sees.
			h.Set("Authorization", "Bearer callers-kcp-credential")
			if tc.withUpstream {
				h.Set(dataplane.HeaderUpstreamAuthorization, upstream)
			}
			applyServiceAuth(h, tc.mode, tc.token)
			if got := h.Get("Authorization"); got != tc.want {
				t.Fatalf("Authorization = %q, want %q", got, tc.want)
			}
			if got := h.Get(dataplane.HeaderUpstreamAuthorization); got != "" {
				t.Fatalf("%s = %q leaked to the upstream", dataplane.HeaderUpstreamAuthorization, got)
			}
		})
	}
}

// What a service behind the tunnel learns about the caller: the railgrid
// identity headers, set from the identity kcp stamped and never from what the
// caller spelled; the requestheader identity itself never crosses.
func TestStampCallerForUpstream(t *testing.T) {
	h := http.Header{}
	h.Set(dataplane.HeaderRemoteUser, "alice@railgrid.test")
	h.Set(dataplane.HeaderRemoteGroup, "system:authenticated")
	h.Set(dataplane.HeaderRemoteExtraPrefix+"warrant", "secret")
	h.Set(dataplane.HeaderHops, "3")
	h.Set(dataplane.HeaderUser, "forged@railgrid.test")
	ctx := dataplane.WithProxiedIdentity(context.Background(), dataplane.ProxiedIdentity{User: "alice@railgrid.test"})

	stampCallerForUpstream(h, ctx, "11tcw27t4rdtnacy")

	if got := h.Get(dataplane.HeaderUser); got != "alice@railgrid.test" {
		t.Errorf("%s = %q, want the stamped identity", dataplane.HeaderUser, got)
	}
	if got := h.Get(dataplane.HeaderTenant); got != "11tcw27t4rdtnacy" {
		t.Errorf("%s = %q", dataplane.HeaderTenant, got)
	}
	if got := h.Get(dataplane.HeaderCluster); got != "11tcw27t4rdtnacy" {
		t.Errorf("%s = %q", dataplane.HeaderCluster, got)
	}
	for _, name := range []string{dataplane.HeaderRemoteUser, dataplane.HeaderRemoteGroup, dataplane.HeaderRemoteExtraPrefix + "warrant", dataplane.HeaderHops} {
		if got := h.Get(name); got != "" {
			t.Errorf("%s = %q crossed the tunnel", name, got)
		}
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
// worked — and on the kube path there is no caller bearer at all. The Secret
// is edges-owned — the portal writes it labelled railgrid.ai/owner: edges — so
// it sits inside this provider's label-scoped `secrets` claim and its own
// virtual workspace serves it.
//
// The confused-deputy protection is unchanged and is asserted here too: the
// Secret's namespace/name come from the gated Service's spec (svc, decoded
// from the object the gate read after reviewing the caller's access), never
// from the request, so the provider can only ever unwrap the credential
// attached to a Service the caller was just authorized to use.
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

	// kcpConfig points at an unroutable host on purpose: a read that went
	// anywhere but through the tenant config getter (the provider's export
	// virtual workspace) would fail here rather than quietly pass.
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
