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

package api

import (
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"k8s.io/client-go/rest"
)

func TestProjectProviderActionTransportReusesAnonymousPoolWithRequestCredentials(t *testing.T) {
	var mu sync.Mutex
	var headers []string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Railgrid-Workspace"))
		mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	config := &rest.Config{TLSClientConfig: rest.TLSClientConfig{CAData: testServerCertPEM(t, upstream)}}
	var reused []bool
	var first http.RoundTripper
	for _, caller := range []string{"project-a", "project-b"} {
		transport, err := projectProviderActionTransport(config, false)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = transport
			defer first.(*http.Transport).CloseIdleConnections()
		} else if transport != first {
			t.Fatal("unchanged anonymous TLS policy selected a new connection pool")
		}
		request, err := http.NewRequest(http.MethodGet, upstream.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+caller)
		request.Header.Set("X-Railgrid-Workspace", caller)
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused = append(reused, info.Reused) },
		}))
		response, err := (&http.Client{Transport: transport}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read=%v close=%v", readErr, closeErr)
		}
	}
	if !reflect.DeepEqual(reused, []bool{false, true}) {
		t.Fatalf("connection reuse = %v, want [false true]", reused)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(headers, []string{"Bearer project-a|project-a", "Bearer project-b|project-b"}) {
		t.Fatalf("request identity escaped its request: %v", headers)
	}
}

func TestProjectProviderActionTransportRotatesCAFileBeforeLookup(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "hub-ca.pem")
	certificate := testServerCertPEM(t, upstream)
	write := func(contents []byte) {
		t.Helper()
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(certificate)
	config := &rest.Config{TLSClientConfig: rest.TLSClientConfig{CAFile: path}}
	first, err := projectProviderActionTransport(config, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.(*http.Transport).CloseIdleConnections()
	// Add a valid duplicate PEM block: the effective trust is equivalent, but
	// changed file bytes still force a fresh pool rather than stale file trust.
	block, _ := pem.Decode(certificate)
	write(append(certificate, pem.EncodeToMemory(block)...))
	next, err := projectProviderActionTransport(config, false)
	if err != nil || next == first {
		t.Fatalf("rotated CA transport = %v, err=%v", next == first, err)
	}
	defer next.(*http.Transport).CloseIdleConnections()
	for _, invalid := range [][]byte{nil, []byte("invalid PEM")} {
		write(invalid)
		if _, err := projectProviderActionTransport(config, false); err == nil {
			t.Fatal("invalid current CA file reused cached trust")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := projectProviderActionTransport(config, false); err == nil {
		t.Fatal("removed current CA file reused cached trust")
	}
}

func TestProjectProviderActionTransportCacheIsBoundedAndSeparatesPolicy(t *testing.T) {
	cache := &projectProviderActionTransportCache{}
	base := http.DefaultTransport.(*http.Transport)
	creates := 0
	create := func() (*http.Transport, error) {
		creates++
		return base.Clone(), nil
	}
	first, err := cache.load(base, []byte("trust-a"), false, create)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []struct {
		base     *http.Transport
		trust    string
		insecure bool
	}{{base, "trust-a", false}, {base, "trust-a", true}, {base, "trust-b", false}, {base.Clone(), "trust-a", false}} {
		transport, err := cache.load(policy.base, []byte(policy.trust), policy.insecure, create)
		if err != nil {
			t.Fatal(err)
		}
		if policy.base == base && policy.trust == "trust-a" && !policy.insecure {
			if transport != first {
				t.Fatal("same policy missed cache")
			}
		} else if transport == first {
			t.Fatal("different TLS policy reused the first pool")
		}
	}
	for n := range projectProviderActionTransportCacheLimit + 2 {
		if _, err := cache.load(base, []byte{byte(n)}, false, create); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != projectProviderActionTransportCacheLimit || len(cache.order) != projectProviderActionTransportCacheLimit {
		t.Fatalf("cache grew beyond limit: entries=%d order=%d", len(cache.entries), len(cache.order))
	}
	if creates != 14 {
		t.Fatalf("created %d pools, want 14 distinct policies", creates)
	}
	for _, transport := range cache.entries {
		transport.CloseIdleConnections()
	}
	wantErr := errors.New("invalid trust")
	if _, err := cache.load(base, []byte("never-valid"), false, func() (*http.Transport, error) { return nil, wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("trust error = %v, want %v", err, wantErr)
	}
}
