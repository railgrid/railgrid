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

package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"k8s.io/klog/v2"
)

// seenRequest is what the fake kcp behind the forwarder observed.
type seenRequest struct {
	method, path, query, authorization, protocol, host string
	body                                               string
}

func newForwarderTest(t *testing.T) (*KCPProxy, *seenRequest) {
	t.Helper()
	seen := &seenRequest{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*seen = seenRequest{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			authorization: r.Header.Get("Authorization"),
			protocol:      r.Header.Get("Sec-WebSocket-Protocol"),
			host:          r.Host, body: string(body),
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &KCPProxy{forward: newForwarder(target, http.DefaultTransport, klog.Background())}, seen
}

// TestForwardLeavesTheRequestToKCP is the whole contract of the front door:
// what the caller sent is what kcp receives, and what kcp answers is what the
// caller gets. Each case is a path the hub used to inspect — a workspace
// path in the cluster segment, an APIExport virtual workspace, a bare
// discovery root — and none of them is the hub's to decide any more.
func TestForwardLeavesTheRequestToKCP(t *testing.T) {
	for name, path := range map[string]string{
		"cluster by id":               "/clusters/1dwl9p41626ptykp/apis/apis.kcp.io/v1alpha2/apibindings",
		"cluster by workspace path":   "/clusters/root:railgrid:tenants:org:ws/api/v1/secrets",
		"organization workspace":      "/clusters/root:railgrid:tenants:org/apis/apis.kcp.io/v1alpha2/apibindings",
		"apiexport virtual workspace": "/services/apiexport/abc/edges.providers.railgrid.ai/clusters/*/apis/edges.railgrid.ai/v1alpha1/linuxservers",
		"bare discovery root":         "/apis",
	} {
		t.Run(name, func(t *testing.T) {
			p, seen := newForwarderTest(t)
			req := httptest.NewRequest(http.MethodPost, path+"?watch=true", io.NopCloser(stringsReader("{}")))
			req.Header.Set("Authorization", "Bearer caller-token")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, req)

			if w.Code != http.StatusTeapot {
				t.Fatalf("status = %d, want kcp's %d passed through", w.Code, http.StatusTeapot)
			}
			if seen.path != path {
				t.Errorf("kcp saw path %q, want %q unchanged", seen.path, path)
			}
			if seen.query != "watch=true" {
				t.Errorf("kcp saw query %q, want watch=true", seen.query)
			}
			if seen.method != http.MethodPost || seen.body != "{}" {
				t.Errorf("kcp saw %s %q, want POST {}", seen.method, seen.body)
			}
			if seen.authorization != "Bearer caller-token" {
				t.Errorf("kcp saw Authorization %q; the caller's bearer must arrive as sent", seen.authorization)
			}
		})
	}
}

// A request with no credential is still kcp's to answer. The hub refusing it
// first would only hide which layer decides.
func TestForwardDoesNotRequireABearer(t *testing.T) {
	p, seen := newForwarderTest(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/clusters/x/api", nil))
	if w.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the upstream answer", w.Code)
	}
	if seen.authorization != "" {
		t.Errorf("kcp saw Authorization %q on a request that carried none", seen.authorization)
	}
}

// A browser cannot set Authorization on a WebSocket upgrade and carries the
// bearer as the Kubernetes subprotocol instead. kcp's authenticator reads it;
// the hub must forward it untouched rather than translate it.
func TestForwardKeepsTheWebSocketBearerSubprotocol(t *testing.T) {
	p, seen := newForwarderTest(t)
	req := httptest.NewRequest(http.MethodGet, "/clusters/x/apis/edges.railgrid.ai/v1alpha1/linuxservers/box/ssh", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Protocol", "railgrid.ssh.v1, base64url.bearer.authorization.k8s.io.dGVzdA")
	p.ServeHTTP(httptest.NewRecorder(), req)
	if seen.protocol != "railgrid.ssh.v1, base64url.bearer.authorization.k8s.io.dGVzdA" {
		t.Errorf("kcp saw Sec-WebSocket-Protocol %q, want it unchanged", seen.protocol)
	}
	if seen.authorization != "" {
		t.Errorf("the hub synthesized Authorization %q from the subprotocol", seen.authorization)
	}
}

func stringsReader(s string) io.Reader { return &oneShot{s: s} }

type oneShot struct {
	s    string
	done bool
}

func (o *oneShot) Read(p []byte) (int, error) {
	if o.done {
		return 0, io.EOF
	}
	o.done = true
	return copy(p, o.s), io.EOF
}
