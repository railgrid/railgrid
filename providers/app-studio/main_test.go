// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	h, err := newHandler(nil)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	res, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Errorf("close /healthz response body: %v", err)
		}
	}()
	if got, want := res.StatusCode, http.StatusOK; got != want {
		t.Fatalf("GET /healthz status = %d, want %d", got, want)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("GET /healthz body = %q, want status ok", string(body))
	}
}

func TestPortalAssets(t *testing.T) {
	_, distFS, err := portalHandler()
	if err != nil {
		t.Fatalf("portalHandler: %v", err)
	}
	if _, err := fs.Stat(distFS, "main.js"); errors.Is(err, fs.ErrNotExist) {
		t.Skip("portal bundle not built; run make build-app-studio-provider")
	} else if err != nil {
		t.Fatalf("stat main.js: %v", err)
	}

	h, err := newHandler(nil)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		path         string
		contentType  string
		bodyContains string
	}{
		{path: "/main.js", contentType: "javascript", bodyContains: "kedge-provider-app-studio"},
		{path: "/icon.svg", contentType: "image/svg+xml", bodyContains: "<svg"},
		{path: "/does-not-exist", contentType: "text/html", bodyContains: "App Studio provider"},
	} {
		res, err := srv.Client().Get(srv.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		func() {
			defer func() {
				if err := res.Body.Close(); err != nil {
					t.Errorf("close %s response body: %v", tc.path, err)
				}
			}()
			if got, want := res.StatusCode, http.StatusOK; got != want {
				t.Fatalf("GET %s status = %d, want %d", tc.path, got, want)
			}
			if got := res.Header.Get("Content-Type"); !strings.Contains(got, tc.contentType) {
				t.Fatalf("GET %s content-type = %q, want %q", tc.path, got, tc.contentType)
			}
			body, _ := io.ReadAll(res.Body)
			if !strings.Contains(string(body), tc.bodyContains) {
				t.Fatalf("GET %s body missing %q", tc.path, tc.bodyContains)
			}
		}()
	}
}
