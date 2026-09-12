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

package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-logr/logr"
)

// The tenant identity a provider receives is the workspace's kcp
// logical-cluster ID, in both X-Faros-Tenant and X-Faros-Cluster. The
// workspace path the hub resolves internally must never reach a provider —
// neither as the tenant header nor as a fallback when the ID is unavailable.

type headerUpstream struct {
	user, tenant, cluster string
	hasTenant, hasCluster bool
}

func newPlatformProxyRecordingHeaders(t *testing.T) (*ProviderProxy, *headerUpstream) {
	t.Helper()
	rec := &headerUpstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.user = r.Header.Get("X-Faros-User")
		rec.tenant, rec.hasTenant = r.Header.Get("X-Faros-Tenant"), r.Header.Values("X-Faros-Tenant") != nil
		rec.cluster, rec.hasCluster = r.Header.Get("X-Faros-Cluster"), r.Header.Values("X-Faros-Cluster") != nil
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	backendURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "quickstart", BackendURL: backendURL, EndpointsValid: true})
	proxy := NewBackendProxy(reg, logr.Discard())
	proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
		return "alice", "root:faros:tenants:" + testOrg + ":" + testWS, nil
	}))
	return proxy, rec
}

func TestBackendProxySendsClusterIDInBothTenantHeaders(t *testing.T) {
	proxy, rec := newPlatformProxyRecordingHeaders(t)
	proxy.SetClusterResolver(testClusterResolver)

	req := httptest.NewRequest(http.MethodGet, "/services/providers/quickstart/api/hello", nil)
	req.Header.Set("Authorization", "Bearer "+callerBearer)
	// A caller-supplied identity must be stripped, not merged.
	req.Header.Set("X-Faros-Tenant", "root:faros:tenants:someone-else")
	req.Header.Set("X-Faros-Cluster", "forged-cluster")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	want := testClusterIDFor("root:faros:tenants:" + testOrg + ":" + testWS)
	if rec.user != "alice" {
		t.Errorf("X-Faros-User = %q, want alice", rec.user)
	}
	if rec.tenant != want {
		t.Errorf("X-Faros-Tenant = %q, want the workspace cluster ID %q", rec.tenant, want)
	}
	if rec.cluster != want {
		t.Errorf("X-Faros-Cluster = %q, want the workspace cluster ID %q", rec.cluster, want)
	}
	if strings.Contains(rec.tenant, "root:faros:tenants") {
		t.Errorf("X-Faros-Tenant = %q carries a workspace path; providers must only ever see the cluster ID", rec.tenant)
	}
}

func TestBackendProxyOmitsTenantHeadersWithoutClusterID(t *testing.T) {
	cases := map[string]func(*ProviderProxy){
		"no cluster resolver wired": func(*ProviderProxy) {},
		"cluster lookup fails": func(p *ProviderProxy) {
			p.SetClusterResolver(func(context.Context, string) (string, error) { return "", errors.New("kcp unavailable") })
		},
		"cluster lookup returns empty": func(p *ProviderProxy) {
			p.SetClusterResolver(func(context.Context, string) (string, error) { return "", nil })
		},
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) {
			proxy, rec := newPlatformProxyRecordingHeaders(t)
			wire(proxy)

			req := httptest.NewRequest(http.MethodGet, "/services/providers/quickstart/api/hello", nil)
			req.Header.Set("Authorization", "Bearer "+callerBearer)
			req.Header.Set("X-Faros-Tenant", "root:faros:tenants:someone-else")
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
			}
			if rec.user != "alice" {
				t.Errorf("X-Faros-User = %q, want alice — attribution survives a missing cluster ID", rec.user)
			}
			if rec.hasTenant || rec.hasCluster {
				t.Errorf("tenant headers = (%q, %q), want neither: the workspace path is not a substitute for the cluster ID", rec.tenant, rec.cluster)
			}
		})
	}
}
