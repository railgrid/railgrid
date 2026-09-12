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

package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const sshTestURL = "https://hub.example.com/services/providers/edges/edgeproxy/clusters/c1/apis/edges.faros.sh/v1alpha1/linuxservers/edge/ssh"

// TestWSAuthFromRestCredentials covers the credential shapes a kubeconfig can
// carry. The exec case is the one that regressed: reading config.BearerToken
// directly leaves an exec-authenticated dial with no Authorization header, and
// the hub answers 401 — surfacing only as "websocket: bad handshake".
func TestWSAuthFromRestCredentials(t *testing.T) {
	tests := []struct {
		name       string
		config     *rest.Config
		wantAuth   string
		wantNoAuth bool
	}{
		{
			name:     "bearer token",
			config:   &rest.Config{Host: "https://hub.example.com", BearerToken: "static-token"},
			wantAuth: "Bearer static-token",
		},
		{
			name: "exec credential plugin",
			config: &rest.Config{
				Host: "https://hub.example.com",
				ExecProvider: &clientcmdapi.ExecConfig{
					APIVersion:      "client.authentication.k8s.io/v1beta1",
					Command:         "sh",
					Args:            []string{"-c", `printf '{"apiVersion":"client.authentication.k8s.io/v1beta1","kind":"ExecCredential","status":{"token":"exec-token"}}'`},
					InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
				},
			},
			wantAuth: "Bearer exec-token",
		},
		{
			name:     "basic auth",
			config:   &rest.Config{Host: "https://hub.example.com", Username: "u", Password: "p"},
			wantAuth: "Basic dTpw",
		},
		{
			name:       "no credentials",
			config:     &rest.Config{Host: "https://hub.example.com"},
			wantNoAuth: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			headers, _, err := wsAuthFromRest(context.Background(), tc.config, sshTestURL)
			if err != nil {
				t.Fatalf("wsAuthFromRest() error: %v", err)
			}
			got := headers.Get("Authorization")
			if tc.wantNoAuth {
				if got != "" {
					t.Fatalf("Authorization = %q, want none", got)
				}
				return
			}
			if got != tc.wantAuth {
				t.Fatalf("Authorization = %q, want %q", got, tc.wantAuth)
			}
		})
	}
}

// TestWSAuthFromRestTLS checks that transport-level settings survive: an
// insecure config must yield a tls.Config the dialer can use, and a plain
// config must not silently turn verification off.
func TestWSAuthFromRestTLS(t *testing.T) {
	insecure := &rest.Config{Host: "https://hub.example.com"}
	insecure.Insecure = true
	_, tlsConfig, err := wsAuthFromRest(context.Background(), insecure, sshTestURL)
	if err != nil {
		t.Fatalf("wsAuthFromRest() error: %v", err)
	}
	if tlsConfig == nil || !tlsConfig.InsecureSkipVerify {
		t.Fatalf("InsecureSkipVerify not propagated: %+v", tlsConfig)
	}

	_, tlsConfig, err = wsAuthFromRest(context.Background(), &rest.Config{Host: "https://hub.example.com"}, sshTestURL)
	if err != nil {
		t.Fatalf("wsAuthFromRest() error: %v", err)
	}
	if tlsConfig != nil && tlsConfig.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify set for a secure config")
	}
}

// TestDescribeDialError checks that the hub's status and body are surfaced,
// since gorilla collapses every non-101 answer into "bad handshake".
func TestDescribeDialError(t *testing.T) {
	base := errors.New("websocket: bad handshake")

	if got := describeDialError(base, nil); got != base {
		t.Fatalf("describeDialError(nil resp) = %v, want the original error", got)
	}

	resp := &http.Response{
		Status:     "401 Unauthorized",
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader("Unauthorized\n")),
	}
	got := describeDialError(base, resp).Error()
	for _, want := range []string{"401 Unauthorized", "Unauthorized", "bad handshake"} {
		if !strings.Contains(got, want) {
			t.Fatalf("describeDialError() = %q, want it to contain %q", got, want)
		}
	}
	if !errors.Is(describeDialError(base, &http.Response{Status: "403 Forbidden", Body: io.NopCloser(strings.NewReader(""))}), base) {
		t.Fatal("describeDialError() lost the wrapped error")
	}
}

func TestBuildSSHWebSocketURLAsksForStdin(t *testing.T) {
	u, err := buildSSHWebSocketURL(nil, "https://hub.example.com/services/providers/edges/edgeproxy/clusters/c/apis/edges.faros.sh/v1alpha1/linuxservers/box/ssh", "uptime")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "wss" || parsed.Query().Get("cmd") != "uptime" || parsed.Query().Get("stdin") != "1" {
		t.Fatalf("unexpected exec URL %s", u)
	}
	u, err = buildSSHWebSocketURL(nil, "https://hub.example.com/x/ssh", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(u, "stdin") {
		t.Fatalf("interactive URL must not ask for stdin forwarding: %s", u)
	}
}

// TestForwardSSHStdin runs the stdin forwarder against a WebSocket server
// that records what it receives: piped bytes arrive as base64 "cmd"
// messages followed by "eof"; a terminal stdin sends only the "eof".
func TestForwardSSHStdin(t *testing.T) {
	type got struct {
		msgs []wsSSHMsg
	}
	run := func(t *testing.T, input string, isTerminal bool) []wsSSHMsg {
		t.Helper()
		done := make(chan got, 1)
		upgrader := websocket.Upgrader{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer c.Close() //nolint:errcheck
			var g got
			for {
				_, data, err := c.ReadMessage()
				if err != nil {
					break
				}
				var m wsSSHMsg
				if err := json.Unmarshal(data, &m); err != nil {
					t.Errorf("bad message %s: %v", data, err)
					break
				}
				g.msgs = append(g.msgs, m)
				if m.Type == "eof" {
					break
				}
			}
			done <- g
		}))
		defer srv.Close()
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close() //nolint:errcheck
		forwardSSHStdin(conn, strings.NewReader(input), isTerminal)
		select {
		case g := <-done:
			return g.msgs
		case <-time.After(5 * time.Second):
			t.Fatal("server never saw eof")
		}
		return nil
	}

	msgs := run(t, "hello-stdin\n", false)
	var data []byte
	for _, m := range msgs[:len(msgs)-1] {
		if m.Type != "cmd" {
			t.Fatalf("unexpected message %+v", m)
		}
		b, err := base64.StdEncoding.DecodeString(m.Cmd)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, b...)
	}
	if string(data) != "hello-stdin\n" || msgs[len(msgs)-1].Type != "eof" {
		t.Fatalf("piped stdin arrived as %+v", msgs)
	}

	msgs = run(t, "keyboard input must not be read", true)
	if len(msgs) != 1 || msgs[0].Type != "eof" {
		t.Fatalf("terminal stdin should send only eof, got %+v", msgs)
	}
}
