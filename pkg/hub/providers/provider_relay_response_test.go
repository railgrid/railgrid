// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/railgrid/provider-sdk/dataplane"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

// Exercise the complete reverse-proxy/sanitizer path with real sockets. Shard
// authentication is injected here; its mTLS verifier has separate tests.
func responseTestRelay(t *testing.T, upstream http.Handler) string {
	t.Helper()
	backend := httptest.NewServer(upstream)
	t.Cleanup(backend.Close)
	base, _ := url.Parse(backend.URL + "/clusters/root")
	registry := NewRegistry()
	registry.Upsert(Provider{Name: "edges", BackendURL: base, EndpointsValid: true})
	registry.Upsert(Provider{Name: "planner", OrgUUID: testOrg, CatalogEntryCluster: "catalog", EndpointsValid: true, EdgeRoute: &EdgeRoute{Cluster: "hosting", ServiceName: "provider-planner"}})
	proxy := NewBackendProxy(registry, logr.Discard())
	proxy.SetKCPFrontDoor(base, http.DefaultTransport)
	relay := &ProviderRelay{Registry: registry, Proxy: proxy, Keys: serviceaccounts.StaticProofKeySource(bytes.Repeat([]byte{1}, 32)), Authorize: func(context.Context, Provider, dataplane.SubresourceRequest) error { return nil }}
	frontend := httptest.NewServer(trustedProviderRelay(relay))
	t.Cleanup(frontend.Close)
	return frontend.URL + ProviderRelayPrefix + "catalog/planner/clusters/consumer/apis/planner.example/v1/boards/demo/run"
}

func responseTestRequest(endpoint string) *http.Request {
	request, _ := http.NewRequest(http.MethodGet, endpoint, nil)
	request.Header.Set(dataplane.HeaderRemoteUser, "alice")
	return request
}

func TestProviderRelaySanitizesActualProtocolUpgrade(t *testing.T) {
	for _, protocol := range []string{"relay-test", "wrong-protocol"} {
		t.Run(protocol, func(t *testing.T) {
			endpoint := responseTestRelay(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, rw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: %s\r\nSet-Cookie: provider=secret\r\nClear-Site-Data: *\r\n\r\n", protocol)
				if err := rw.Flush(); err != nil {
					return
				}
				data := make([]byte, 4)
				if _, err := io.ReadFull(rw, data); err == nil {
					_, _ = conn.Write(data)
				}
			}))
			request := responseTestRequest(endpoint)
			request.Header.Set("Connection", "Upgrade")
			request.Header.Set("Upgrade", "relay-test")
			client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 3 * time.Second}}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if protocol != "relay-test" {
				if response.StatusCode != http.StatusBadGateway {
					t.Fatalf("invalid upgrade: %d", response.StatusCode)
				}
				return
			}
			if response.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("valid upgrade: %d", response.StatusCode)
			}
			if response.Header.Get("Set-Cookie") != "" || response.Header.Get("Clear-Site-Data") != "" {
				t.Fatal("provider state headers escaped upgrade")
			}
			stream, ok := response.Body.(io.ReadWriteCloser)
			if !ok {
				t.Fatal("upgrade lost duplex stream")
			}
			if _, err := stream.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			reply := make([]byte, 4)
			if _, err := io.ReadFull(stream, reply); err != nil || string(reply) != "ping" {
				t.Fatalf("upgrade echo %q: %v", reply, err)
			}
		})
	}
}

func TestProviderRelaySuppressesInformationalAndTrailerHeaders(t *testing.T) {
	endpoint := responseTestRelay(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "early=secret")
		w.Header().Set("Link", "</provider>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Del("Link")
		w.Header().Set("Trailer", "X-Provider-Trailer")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("content"))
		w.Header().Set("X-Provider-Trailer", "announced")
		w.Header().Set(http.TrailerPrefix+"X-Provider-Late", "unannounced")
	}))
	hints := 0
	trace := &httptrace.ClientTrace{Got1xxResponse: func(int, textproto.MIMEHeader) error { hints++; return nil }}
	request := responseTestRequest(endpoint)
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "content" {
		t.Fatalf("response %d %q: %v", response.StatusCode, body, err)
	}
	if hints != 0 {
		t.Fatalf("forwarded %d informational responses", hints)
	}
	for _, header := range []string{"Set-Cookie", "Link", "Trailer", "X-Provider-Trailer", "X-Provider-Late"} {
		if response.Header.Get(header) != "" || response.Trailer.Get(header) != "" {
			t.Errorf("provider header escaped: %s", header)
		}
	}
}
