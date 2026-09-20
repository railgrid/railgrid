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

package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-logr/logr"
)

// A provider data-plane route names its workspace in its path:
//
//	/services/providers/{name}/{root}/clusters/{id}/…
//
// provider-sdk/dataplane.Gate refuses a request whose X-Railgrid-Cluster
// disagrees with that path (400 ErrClusterMismatch), so the hub injecting the
// caller's DEFAULT workspace there did not merely address the wrong tenant —
// it made every such call fail. The path wins, and the caller is authorized
// for it with the membership check the kcp proxy uses.

// pathCluster is the cluster a request addresses in its path. Deliberately
// not the caller's default workspace cluster, which is what the proxy used to
// send.
const pathCluster = "1dwl9p41626ptykp"

// defaultTenantPath is where the resolver lands a caller who selected
// nothing: their org workspace, whose cluster is NOT pathCluster.
const defaultTenantPath = "root:railgrid:tenants:" + testOrg

type pathClusterFixture struct {
	proxy *ProviderProxy
	rec   *headerUpstream
	// asked records the (user, cluster) tuples the authorizer was asked about.
	asked []string
}

// newPathClusterProxy builds a backend proxy in front of a header-recording
// upstream, with a resolver that lands the caller in tenantPath and an
// authorizer that admits exactly the clusters in member.
func newPathClusterProxy(t *testing.T, user, tenantPath string, member ...string) *pathClusterFixture {
	t.Helper()
	rec := &headerUpstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.user = r.Header.Get("X-Railgrid-User")
		rec.tenant, rec.hasTenant = r.Header.Get("X-Railgrid-Tenant"), r.Header.Values("X-Railgrid-Tenant") != nil
		rec.cluster, rec.hasCluster = r.Header.Get("X-Railgrid-Cluster"), r.Header.Values("X-Railgrid-Cluster") != nil
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	backendURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "quickstart", BackendURL: backendURL, EndpointsValid: true})

	f := &pathClusterFixture{proxy: NewBackendProxy(reg, logr.Discard()), rec: rec}
	f.proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
		if user == "" {
			return "", "", errors.New("anonymous caller")
		}
		return user, tenantPath, nil
	}))
	f.proxy.SetClusterResolver(testClusterResolver)
	f.proxy.SetClusterAuthorizer(ClusterAuthorizerFunc(func(_ context.Context, gotUser, gotCluster string) bool {
		f.asked = append(f.asked, gotUser+"/"+gotCluster)
		for _, allowed := range member {
			if gotCluster == allowed {
				return true
			}
		}
		return false
	}))
	return f
}

func dataplanePath(clusterID string) string {
	return "/services/providers/quickstart/dataplane/clusters/" + clusterID + "/greetings/hello/sync"
}

func TestBackendProxySendsThePathClusterToAMember(t *testing.T) {
	f := newPathClusterProxy(t, "alice", defaultTenantPath, pathCluster)

	w := serveProxy(f.proxy, dataplanePath(pathCluster))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if f.rec.tenant != pathCluster || f.rec.cluster != pathCluster {
		t.Errorf("tenant headers = (%q, %q), want both %q — the path names the workspace, and dataplane.Gate 400s on any other value",
			f.rec.tenant, f.rec.cluster, pathCluster)
	}
	if f.rec.user != "alice" {
		t.Errorf("X-Railgrid-User = %q, want alice", f.rec.user)
	}
	if len(f.asked) != 1 || f.asked[0] != "alice/"+pathCluster {
		t.Errorf("authorizer was asked %v, want one question about alice and %s", f.asked, pathCluster)
	}
}

func TestBackendProxyRefusesAPathClusterTheCallerIsNotAMemberOf(t *testing.T) {
	// A member of something else entirely: the authorizer admits only the
	// caller's own default workspace cluster.
	f := newPathClusterProxy(t, "mallory", defaultTenantPath, testClusterIDFor(defaultTenantPath))

	w := serveProxy(f.proxy, dataplanePath(pathCluster))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — addressing another tenant's workspace must be refused, not answered", w.Code)
	}
	if f.rec.hasTenant || f.rec.hasCluster || f.rec.user != "" {
		t.Errorf("the request reached the provider (user=%q tenant=%q); a refusal must stop at the hub", f.rec.user, f.rec.tenant)
	}
}

func TestBackendProxyKeepsDefaultResolutionOffTheDataPlaneGrammar(t *testing.T) {
	// MCP, OAuth callbacks, webhooks and /healthz name no cluster; they keep
	// the caller's resolved workspace exactly as before.
	for name, path := range map[string]string{
		"mcp":                                   "/services/providers/quickstart/mcp",
		"oauth callback":                        "/services/providers/quickstart/oauth/callback",
		"no cluster segment":                    "/services/providers/quickstart/dataplane/greetings/hello/sync",
		"workspace path in the cluster segment": "/services/providers/quickstart/dataplane/clusters/root:railgrid:tenants:org/greetings/hello/sync",
	} {
		t.Run(name, func(t *testing.T) {
			f := newPathClusterProxy(t, "alice", defaultTenantPath, pathCluster)

			w := serveProxy(f.proxy, path)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
			}
			want := testClusterIDFor(defaultTenantPath)
			if f.rec.tenant != want || f.rec.cluster != want {
				t.Errorf("tenant headers = (%q, %q), want both %q (the caller's resolved workspace)", f.rec.tenant, f.rec.cluster, want)
			}
			if len(f.asked) != 0 {
				t.Errorf("authorizer was consulted for a non-data-plane route: %v", f.asked)
			}
		})
	}
}

func TestBackendProxyPathClusterBeatsTheOrgWorkspaceHeaders(t *testing.T) {
	// The portal's sidebar selection is what the resolver turned into
	// tenantPath here. On a data-plane route it loses to the path: the
	// provider reads the path, and a header that disagrees is a 400.
	selected := "root:railgrid:tenants:" + testOrg + ":" + testWS
	f := newPathClusterProxy(t, "alice", selected, pathCluster)

	w := serveProxy(f.proxy, dataplanePath(pathCluster))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if f.rec.cluster != pathCluster {
		t.Errorf("X-Railgrid-Cluster = %q, want the path's %q — the selection headers must not override the path",
			f.rec.cluster, pathCluster)
	}
	if f.rec.cluster == testClusterIDFor(selected) {
		t.Errorf("X-Railgrid-Cluster = %q is the selected workspace's cluster; the path was ignored", f.rec.cluster)
	}
}

func TestBackendProxyNeedsNoMembershipLookupForTheCallersOwnCluster(t *testing.T) {
	// A workload ServiceAccount has no UserMembershipIndex to check: it was
	// verified against the tenant it names. When the path addresses that same
	// tenant there is nothing left to authorize, so no authorizer is needed.
	own := "root:railgrid:tenants:" + testOrg + ":" + testWS
	f := newPathClusterProxy(t, "system:serviceaccount:default:railgrid-wi-abc", own)
	f.proxy.SetClusterAuthorizer(nil)

	w := serveProxy(f.proxy, dataplanePath(testClusterIDFor(own)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if want := testClusterIDFor(own); f.rec.tenant != want || f.rec.cluster != want {
		t.Errorf("tenant headers = (%q, %q), want both %q", f.rec.tenant, f.rec.cluster, want)
	}
}

func TestBackendProxyLeavesAnonymousDataPlaneCallsAlone(t *testing.T) {
	// /healthz and other unauthenticated probes are unchanged: nothing to
	// authorize, nothing to inject, and no refusal from the hub.
	f := newPathClusterProxy(t, "", defaultTenantPath, pathCluster)

	w := httptest.NewRecorder()
	f.proxy.ServeHTTP(w, httptest.NewRequest(http.MethodGet, dataplanePath(pathCluster), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if f.rec.hasTenant || f.rec.hasCluster {
		t.Errorf("tenant headers = (%q, %q), want neither for an anonymous caller", f.rec.tenant, f.rec.cluster)
	}
	if len(f.asked) != 0 {
		t.Errorf("authorizer was consulted for an anonymous caller: %v", f.asked)
	}
}

// resolverWithAuthorizer is a TenantResolver that also answers the cluster
// question, the way the hub's kcp-backed resolver does.
type resolverWithAuthorizer struct{ asked bool }

func (r *resolverWithAuthorizer) Resolve(*http.Request) (string, string, error) {
	return "alice", defaultTenantPath, nil
}

func (r *resolverWithAuthorizer) AuthorizeCluster(context.Context, string, string) bool {
	r.asked = true
	return true
}

func TestSetTenantResolverAdoptsAResolverThatAuthorizesClusters(t *testing.T) {
	f := newPathClusterProxy(t, "alice", defaultTenantPath)
	f.proxy.SetClusterAuthorizer(nil)
	resolver := &resolverWithAuthorizer{}
	f.proxy.SetTenantResolver(resolver)

	if w := serveProxy(f.proxy, dataplanePath(pathCluster)); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if !resolver.asked {
		t.Fatal("the resolver's own AuthorizeCluster was never consulted; the hub wires it by SetTenantResolver alone")
	}
	if f.rec.cluster != pathCluster {
		t.Errorf("X-Railgrid-Cluster = %q, want %q", f.rec.cluster, pathCluster)
	}
}

func TestPathClusterIDMatchesTheDataPlaneAddressingPrefix(t *testing.T) {
	for _, tc := range []struct {
		rest string
		want string
	}{
		{rest: "/dataplane/clusters/" + pathCluster + "/greetings/hello/sync", want: pathCluster},
		{rest: "/actions/clusters/" + pathCluster + "/greetings/hello/deploy/v1", want: pathCluster},
		// The edges dialect ParsePath refuses: kubectl through the tunnel.
		// The addressing prefix is the same, so the cluster is the same.
		{rest: "/dataplane/clusters/" + pathCluster + "/apis/apps/v1/namespaces/default/deployments", want: pathCluster},
		{rest: "/dataplane/clusters/" + pathCluster, want: pathCluster},
		// Not data-plane routes.
		{rest: "/", want: ""},
		{rest: "/healthz", want: ""},
		{rest: "/mcp", want: ""},
		{rest: "/dataplane/greetings/hello/sync", want: ""},
		{rest: "/clusters/" + pathCluster + "/greetings", want: ""},
		// A workspace path is not a cluster ID; the hub proxy will not serve
		// one either.
		{rest: "/dataplane/clusters/root:railgrid:tenants:org/greetings", want: ""},
		{rest: "/dataplane/clusters/", want: ""},
		// Cleaned before it is read, so a traversal cannot make the hub
		// authorize (and announce) a cluster the provider's own mux will not
		// see once it cleans the same path.
		{rest: "/dataplane/clusters/" + pathCluster + "/../../mcp", want: ""},
		{rest: "/dataplane/clusters/NotACluster/greetings", want: ""},
	} {
		got, ok := pathClusterID(tc.rest)
		if tc.want == "" {
			if ok {
				t.Errorf("pathClusterID(%q) = %q, true; want no match", tc.rest, got)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Errorf("pathClusterID(%q) = %q, %t; want %q, true", tc.rest, got, ok, tc.want)
		}
	}
}
