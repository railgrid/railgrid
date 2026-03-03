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

package builder

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	utilhttp "github.com/faroshq/faros-kedge/pkg/util/http"
)

// newSSHUpgrader returns a websocket.Upgrader configured with the same
// CheckOrigin policy used by edgesSSHHandler.
func newSSHUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return utilhttp.CheckSameOrAllowedOrigin(r, []url.URL{})
		},
	}
}

// TestSSHWebSocketUpgrader_SameOriginAllowed verifies that a request whose
// Origin header matches the server host is accepted.
func TestSSHWebSocketUpgrader_SameOriginAllowed(t *testing.T) {
	upgrader := newSSHUpgrader()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// The upgrader already wrote the error response; nothing more to do.
			return
		}
		conn.Close() //nolint:errcheck
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	header := http.Header{"Origin": []string{srv.URL}}

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("expected same-origin connection to succeed, got error: %v (status=%d)", err, wsStatusCode(resp))
	}
	conn.Close() //nolint:errcheck
}

// TestSSHWebSocketUpgrader_CrossOriginRejected verifies that a request whose
// Origin header comes from a different host is rejected with 403 Forbidden.
func TestSSHWebSocketUpgrader_CrossOriginRejected(t *testing.T) {
	upgrader := newSSHUpgrader()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close() //nolint:errcheck
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	// Deliberately set a different origin.
	header := http.Header{"Origin": []string{"http://evil.example.com"}}

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		conn.Close() //nolint:errcheck
		t.Fatal("expected cross-origin connection to be rejected, but it succeeded")
	}
	if resp == nil {
		t.Fatalf("expected an HTTP response on rejection, got nil (err=%v)", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403 Forbidden, got %d", resp.StatusCode)
	}
}

// TestSSHWebSocketUpgrader_NoOriginAllowed verifies that requests without an
// Origin header (e.g. native SSH clients making programmatic WebSocket calls)
// are accepted — absence of Origin is treated as same-origin.
func TestSSHWebSocketUpgrader_NoOriginAllowed(t *testing.T) {
	upgrader := newSSHUpgrader()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close() //nolint:errcheck
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	// No Origin header — simulates a programmatic/non-browser client.
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("expected request without Origin header to succeed, got error: %v (status=%d)", err, wsStatusCode(resp))
	}
	conn.Close() //nolint:errcheck
}

// wsStatusCode safely extracts the HTTP status code from a (possibly nil) response.
func wsStatusCode(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}
