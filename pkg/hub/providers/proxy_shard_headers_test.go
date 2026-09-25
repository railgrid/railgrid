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
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-logr/logr"
)

// A kcp shard stamps X-Remote-User / X-Remote-Group / X-Remote-Extra-* and a
// hop counter when it forwards a custom subresource to a provider, and the
// provider trusts those as the caller's identity on its /clusters/... route.
// The hub's backend proxy reaches the same provider, so those headers must
// never survive the front door: a tenant could otherwise address
// /services/providers/<name>/clusters/... wearing any identity it likes.
func TestBackendProxyStripsShardIdentityHeaders(t *testing.T) {
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
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

	req := httptest.NewRequest(http.MethodPost, "/services/providers/quickstart/clusters/1v98kgkp03uox9qw/apis/quickstart.providers.railgrid.ai/v1alpha1/greetings/x/greet", nil)
	req.Header.Set("X-Remote-User", "system:admin")
	req.Header.Set("X-Remote-Group", "system:masters")
	req.Header.Set("X-Remote-Extra-Scopes", "cluster:*")
	req.Header.Set("X-Kcp-Internal-Proxy-Hops", "0")
	req.Header.Set("X-Unrelated", "kept")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	for _, h := range []string{"X-Remote-User", "X-Remote-Group", "X-Remote-Extra-Scopes", "X-Kcp-Internal-Proxy-Hops"} {
		if v := seen.Values(h); v != nil {
			t.Errorf("%s = %q reached the provider through the hub; a shard-only identity header must be stripped", h, v)
		}
	}
	if seen.Get("X-Unrelated") != "kept" {
		t.Errorf("an unrelated header was dropped: %v", seen)
	}
}
