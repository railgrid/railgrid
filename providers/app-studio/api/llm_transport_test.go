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
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"testing"
)

func TestProjectMCPTransportReusesConnectionWithoutRetainingCallerHeaders(t *testing.T) {
	var mu sync.Mutex
	var observedHeaders []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		observedHeaders = append(observedHeaders, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Railgrid-Workspace"))
		mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	base := http.DefaultTransport
	http.DefaultTransport = base.(*http.Transport).Clone()
	defer func() {
		http.DefaultTransport.(*http.Transport).CloseIdleConnections()
		http.DefaultTransport = base
	}()
	var reused []bool
	for _, caller := range []string{"caller-a", "caller-b"} {
		request, err := http.NewRequest(http.MethodPost, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+caller)
		request.Header.Set("X-Railgrid-Workspace", caller)
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused = append(reused, info.Reused) },
		}))
		transport := projectMCPTransport(true)
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
	if len(reused) != 2 || reused[0] || !reused[1] {
		t.Fatalf("connection reuse = %v, want [false true]", reused)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observedHeaders) != 2 || observedHeaders[0] != "Bearer caller-a|caller-a" || observedHeaders[1] != "Bearer caller-b|caller-b" {
		t.Fatalf("request scope changed across pooled calls: %v", observedHeaders)
	}
}

func TestProjectMCPTransportPreservesBaseTLSOptionsAndTracksDefaultTransport(t *testing.T) {
	original := http.DefaultTransport
	first := original.(*http.Transport).Clone()
	first.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "configured-host"}
	http.DefaultTransport = first
	defer func() {
		first.CloseIdleConnections()
		http.DefaultTransport = original
	}()
	secure := projectMCPTransport(false)
	if secure != first {
		t.Fatal("secure requests did not use the configured default transport")
	}
	pooled := projectMCPTransport(true).(*http.Transport)
	defer pooled.CloseIdleConnections()
	if pooled == first || pooled.TLSClientConfig == first.TLSClientConfig {
		t.Fatal("development TLS transport modified the default transport")
	}
	if !pooled.TLSClientConfig.InsecureSkipVerify || pooled.TLSClientConfig.MinVersion != tls.VersionTLS12 || pooled.TLSClientConfig.ServerName != "configured-host" {
		t.Fatal("development TLS transport discarded existing TLS options")
	}
	if first.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("development TLS setting escaped into secure requests")
	}
	if projectMCPTransport(true) != pooled {
		t.Fatal("development requests did not share the connection pool")
	}
	second := original.(*http.Transport).Clone()
	defer second.CloseIdleConnections()
	http.DefaultTransport = second
	if replacement := projectMCPTransport(true); replacement == pooled || replacement == second {
		t.Fatal("replacement default transport reused the old pool or was modified directly")
	}
}
