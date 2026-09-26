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

package serve

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
)

// The adapter is the one parser: a declared coordinate reaches its handler
// with the URL untouched, the parsed route (an action's version restored from
// the declaration) and the caller in the context.
func TestSubresourcePathDispatchesToTheSameHandler(t *testing.T) {
	type seen struct {
		path     string
		route    dataplane.SubresourceRequest
		routed   bool
		identity dataplane.ProxiedIdentity
		ok       bool
		cluster  string
	}
	var dataPlaneSaw, actionsSaw *seen
	record := func(into **seen) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := dataplane.ProxiedIdentityFrom(r.Context())
			route, routed := dataplane.RouteFrom(r.Context())
			*into = &seen{path: r.URL.Path, route: route, routed: routed, identity: id, ok: ok, cluster: r.Header.Get(dataplane.HeaderCluster)}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	handler, err := New(Options{
		Readiness: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane: record(&dataPlaneSaw),
		Actions:   record(&actionsSaw),
		Subresources: map[string]SubresourceRoute{
			"linuxservers/addon-credentials": {},
			"repositories/mint-clone-token":  {Action: true, Version: "v1"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	stamped := func(method, path string) *http.Request {
		r, _ := http.NewRequest(method, srv.URL+path, nil)
		r.Header.Set(dataplane.HeaderRemoteUser, "alice")
		r.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
		r.Header.Set(dataplane.HeaderHops, "1")
		return r
	}

	// A data-plane verb.
	resp, err := http.DefaultClient.Do(stamped(http.MethodPost, "/clusters/1v98kgkp03uox9qw/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/addon-credentials"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent || dataPlaneSaw == nil {
		t.Fatalf("verb: status %d, handler seen=%v", resp.StatusCode, dataPlaneSaw != nil)
	}
	if dataPlaneSaw.path != "/clusters/1v98kgkp03uox9qw/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/addon-credentials" {
		t.Fatalf("path = %q; the URL must arrive unmodified", dataPlaneSaw.path)
	}
	if !dataPlaneSaw.routed || dataPlaneSaw.route.Resource != "linuxservers" || dataPlaneSaw.route.Name != "edge-1" || dataPlaneSaw.route.Verb != "addon-credentials" || dataPlaneSaw.route.Version != "" || dataPlaneSaw.route.APIVersion != "v1alpha1" {
		t.Fatalf("route = %+v (routed=%v)", dataPlaneSaw.route, dataPlaneSaw.routed)
	}
	if !dataPlaneSaw.ok || dataPlaneSaw.identity.User != "alice" || dataPlaneSaw.cluster != "1v98kgkp03uox9qw" {
		t.Fatalf("caller not carried: %+v", dataPlaneSaw)
	}

	// An action, which needs its version restored.
	resp, err = http.DefaultClient.Do(stamped(http.MethodPost, "/clusters/1v98kgkp03uox9qw/apis/code.railgrid.ai/v1alpha1/repositories/app/mint-clone-token"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent || actionsSaw == nil {
		t.Fatalf("action: status %d, handler seen=%v", resp.StatusCode, actionsSaw != nil)
	}
	if actionsSaw.path != "/clusters/1v98kgkp03uox9qw/apis/code.railgrid.ai/v1alpha1/repositories/app/mint-clone-token" {
		t.Fatalf("action path = %q; the URL must arrive unmodified", actionsSaw.path)
	}
	if !actionsSaw.routed || actionsSaw.route.Version != "v1" || actionsSaw.route.Verb != "mint-clone-token" {
		t.Fatalf("action route = %+v; the declared version must be restored", actionsSaw.route)
	}
}

// A multi-component object's component travels as ?component= and lands in
// the parsed route, with the URL (and the verb's own query) untouched.
func TestSubresourcePathCarriesTheComponentAsAQueryParameter(t *testing.T) {
	var saw string
	var route dataplane.SubresourceRequest
	handler, err := New(Options{
		Readiness: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			saw = r.URL.RequestURI()
			route, _ = dataplane.RouteFrom(r.Context())
			w.WriteHeader(http.StatusNoContent)
		}),
		Subresources: map[string]SubresourceRoute{"instances/log": {}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	r, _ := http.NewRequest(http.MethodGet, srv.URL+"/clusters/1v98kgkp03uox9qw/apis/infrastructure.railgrid.ai/v1alpha1/instances/site/log/follow?component=app&since=1h", nil)
	r.Header.Set(dataplane.HeaderRemoteUser, "alice")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if saw != "/clusters/1v98kgkp03uox9qw/apis/infrastructure.railgrid.ai/v1alpha1/instances/site/log/follow?component=app&since=1h" {
		t.Fatalf("handler saw %q; the URL must arrive unmodified", saw)
	}
	if route.Component != "app" || route.Tail != "follow" || route.Verb != "log" {
		t.Fatalf("route = %+v", route)
	}
	bad, _ := http.NewRequest(http.MethodGet, srv.URL+"/clusters/1v98kgkp03uox9qw/apis/infrastructure.railgrid.ai/v1alpha1/instances/site/log?component=a%2Fb", nil)
	bad.Header.Set(dataplane.HeaderRemoteUser, "alice")
	resp, err = http.DefaultClient.Do(bad)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("component with a separator: status = %d, want 400", resp.StatusCode)
	}
}

func TestSubresourcePathRefusesWhatItMust(t *testing.T) {
	var reached bool
	handler, err := New(Options{
		Readiness: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true; w.WriteHeader(http.StatusNoContent) }),
		Subresources: map[string]SubresourceRoute{
			"linuxservers/addon-credentials": {},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	good := "/clusters/1v98kgkp03uox9qw/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/addon-credentials"
	for name, tc := range map[string]struct {
		path   string
		header map[string]string
		want   int
	}{
		"no stamped identity is not anonymous": {path: good, header: nil, want: http.StatusUnauthorized},
		"an undeclared verb is not served":     {path: "/clusters/1v98kgkp03uox9qw/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/ssh", header: map[string]string{dataplane.HeaderRemoteUser: "alice"}, want: http.StatusNotFound},
		"a round-tripping request is refused":  {path: good, header: map[string]string{dataplane.HeaderRemoteUser: "alice", dataplane.HeaderHops: "11"}, want: http.StatusLoopDetected},
		"a workspace path is not a cluster":    {path: "/clusters/root:acme/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/addon-credentials", header: map[string]string{dataplane.HeaderRemoteUser: "alice"}, want: http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			reached = false
			r, _ := http.NewRequest(http.MethodPost, srv.URL+tc.path, nil)
			for k, v := range tc.header {
				r.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if reached {
				t.Fatal("the handler ran for a refused request")
			}
		})
	}
}

func TestSubresourceTableIsValidatedAtConstruction(t *testing.T) {
	ready := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	for name, opts := range map[string]Options{
		"no handler to dispatch to": {Readiness: ready, Subresources: map[string]SubresourceRoute{"a/b": {}}},
		"not a coordinate":          {Readiness: ready, DataPlane: ok, Subresources: map[string]SubresourceRoute{"justaverb": {}}},
		"status is reserved":        {Readiness: ready, DataPlane: ok, Subresources: map[string]SubresourceRoute{"instances/status": {}}},
		"scale is reserved":         {Readiness: ready, DataPlane: ok, Subresources: map[string]SubresourceRoute{"instances/scale": {}}},
		"action without a version":  {Readiness: ready, Actions: ok, Subresources: map[string]SubresourceRoute{"repositories/commit": {Action: true}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(opts); err == nil {
				t.Fatal("New accepted an invalid Subresources table")
			}
		})
	}
}
