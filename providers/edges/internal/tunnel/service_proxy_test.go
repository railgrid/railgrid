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

package tunnel

import (
	"net/http"
	"testing"
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

func TestParseServicePath(t *testing.T) {
	s := testServer("/services/providers/edges/edgeproxy")

	cases := []struct {
		name                             string
		path                             string
		wantOK                           bool
		cluster, obj, subresource, wantR string
	}{
		{
			name:        "proxy subresource, no trailing path",
			path:        "/clusters/abc/apis/edges.faros.sh/v1alpha1/services/ha-box-home-assistant/proxy",
			wantOK:      true,
			cluster:     "abc",
			obj:         "ha-box-home-assistant",
			subresource: "proxy",
			wantR:       "",
		},
		{
			name:        "proxy subresource with trailing service path",
			path:        "/clusters/abc/apis/edges.faros.sh/v1alpha1/services/ha/proxy/api/services/cover/open_cover",
			wantOK:      true,
			cluster:     "abc",
			obj:         "ha",
			subresource: "proxy",
			wantR:       "/api/services/cover/open_cover",
		},
		{
			name:        "mcp subresource",
			path:        "/clusters/xyz/apis/edges.faros.sh/v1alpha1/services/ha/mcp",
			wantOK:      true,
			cluster:     "xyz",
			obj:         "ha",
			subresource: "mcp",
			wantR:       "",
		},
		{
			name:   "connectable kind is not an edgeservice path",
			path:   "/clusters/abc/apis/edges.faros.sh/v1alpha1/linuxservers/srv/ssh",
			wantOK: false,
		},
		{
			name:   "wrong group",
			path:   "/clusters/abc/apis/other.group/v1alpha1/services/ha/proxy",
			wantOK: false,
		},
		{
			name:   "too short",
			path:   "/clusters/abc/apis/edges.faros.sh/v1alpha1/services/ha",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster, name, sub, rest, ok := s.parseServicePath(tc.path)
			if ok != tc.wantOK {
				t.Fatalf("parseServicePath(%q) ok=%v, want %v", tc.path, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if cluster != tc.cluster || name != tc.obj || sub != tc.subresource || rest != tc.wantR {
				t.Fatalf("parseServicePath(%q) = (%q,%q,%q,%q), want (%q,%q,%q,%q)",
					tc.path, cluster, name, sub, rest, tc.cluster, tc.obj, tc.subresource, tc.wantR)
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
