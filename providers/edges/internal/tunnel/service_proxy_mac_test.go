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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// serviceProxyDialer exposes one in-memory edge-agent connection and records
// the request the hub sends through it. It lets this test cover the actual
// serveService -> ConnManager -> serviceHTTPProxy path without a live agent.
type serviceProxyDialer struct {
	dialed  bool
	request chan *http.Request
	errors  chan error
}

func (d *serviceProxyDialer) Dial(context.Context) (net.Conn, error) {
	d.dialed = true
	local, remote := net.Pipe()
	go func() {
		defer remote.Close() //nolint:errcheck
		req, err := http.ReadRequest(bufio.NewReader(remote))
		if err != nil {
			d.errors <- err
			return
		}
		d.request <- req
		_, _ = io.WriteString(remote, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	}()
	return local, nil
}

func newServiceProxyTestServer(t *testing.T, edgeKind string, edgeName string) (*Server, *serviceProxyDialer) {
	t.Helper()
	const cluster = "tenant-a"
	const serviceName = "mac-service"

	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/services/"+serviceName) {
			http.Error(w, "unexpected kcp request", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiVersion": "edges.faros.sh/v1alpha1",
			"kind":       "Service",
			"metadata":   map[string]any{"name": serviceName},
			"spec": map[string]any{
				"edgeRef": map[string]any{"kind": edgeKind, "name": edgeName},
				"port":    int32(8123),
			},
		})
	}))
	t.Cleanup(kcp.Close)

	s := testServer("/services/providers/edges/edgeproxy")
	s.kcpConfig = &rest.Config{Host: kcp.URL}
	s.tenantConfig = func(_ context.Context, gotCluster string) (*rest.Config, error) {
		if gotCluster != cluster {
			return nil, errors.New("wrong tenant cluster")
		}
		return s.kcpConfig, nil
	}
	s.authorizeFn = func(_ context.Context, _ *rest.Config, _ *rest.Config, token, gotCluster, verb, group, resource, name string) error {
		if token != "caller-token" || gotCluster != cluster || verb != "proxy" ||
			group != "edges.faros.sh" || resource != serviceResource || name != serviceName {
			return errors.New("unexpected delegated authorization request")
		}
		return nil
	}
	s.edgeConnManager = NewConnManager()
	s.logger = klog.Background()
	d := &serviceProxyDialer{request: make(chan *http.Request, 1), errors: make(chan error, 1)}
	if edgeKind == macOSServerKind {
		s.edgeConnManager.storeLocalForTest(edgeConnKey(macOSServerResource, cluster, edgeName), d)
	}
	return s, d
}

func TestMacOSServiceProxyUsesTheMacTunnelAndHostLoopback(t *testing.T) {
	s, dialer := newServiceProxyTestServer(t, macOSServerKind, "mac-1")
	req := httptest.NewRequest(http.MethodGet,
		"/clusters/tenant-a/apis/edges.faros.sh/v1alpha1/services/mac-service/proxy/api/ping", nil)
	rr := httptest.NewRecorder()

	s.serveService(rr, req, "caller-token", "tenant-a", "mac-service", "proxy", "/api/ping")

	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("service proxy response = %d %q, want 200 %q", rr.Code, rr.Body.String(), "ok")
	}
	select {
	case err := <-dialer.errors:
		t.Fatalf("edge-agent request: %v", err)
	case agentReq := <-dialer.request:
		if got, want := agentReq.URL.Path, "/svc/api/ping"; got != want {
			t.Errorf("agent path = %q, want %q", got, want)
		}
		if got, want := agentReq.Header.Get(svcTargetHeader), "http://127.0.0.1:8123"; got != want {
			t.Errorf("%s = %q, want %q", svcTargetHeader, got, want)
		}
		if got := agentReq.Header.Get("Authorization"); got != "" {
			t.Errorf("agent Authorization = %q, want empty for a secret-less default service", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the Mac edge-agent request")
	}
	if !dialer.dialed {
		t.Fatal("Mac service did not use the Mac tunnel")
	}
}

func TestServiceProxyRejectsUnknownEdgeKindBeforeDialing(t *testing.T) {
	s, dialer := newServiceProxyTestServer(t, "UnexpectedKind", "mac-1")
	req := httptest.NewRequest(http.MethodGet,
		"/clusters/tenant-a/apis/edges.faros.sh/v1alpha1/services/mac-service/proxy", nil)
	rr := httptest.NewRecorder()

	s.serveService(rr, req, "caller-token", "tenant-a", "mac-service", "proxy", "")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown edge kind status = %d, want 400 (body %q)", rr.Code, rr.Body.String())
	}
	if dialer.dialed {
		t.Fatal("unknown edge kind reached a tunnel dialer")
	}
}
