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

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalhostAddrRejectsNonLoopbackListeners(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{name: "localhost", addr: "localhost:17873", want: "localhost:17873"},
		{name: "IPv4 loopback", addr: "127.0.0.1:17873", want: "127.0.0.1:17873"},
		{name: "IPv6 loopback", addr: "[::1]:17873", want: "[::1]:17873"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := localhostAddr(tt.addr)
			if err != nil {
				t.Fatalf("localhostAddr(%q) returned error: %v", tt.addr, err)
			}
			if got != tt.want {
				t.Fatalf("localhostAddr(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}

	for _, addr := range []string{"0.0.0.0:17873", "[::]:17873", "192.0.2.10:17873", ":17873"} {
		t.Run("reject "+addr, func(t *testing.T) {
			if _, err := localhostAddr(addr); err == nil {
				t.Fatalf("localhostAddr(%q) accepted a non-loopback listener", addr)
			}
		})
	}
}

func TestHealthReportsDisabledExecution(t *testing.T) {
	recorder := httptest.NewRecorder()
	health(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var body healthResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body.Status != "ok" || body.ExecutionEnabled {
		t.Fatalf("health response = %+v, want status=ok executionEnabled=false", body)
	}
}
