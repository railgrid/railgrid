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
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

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
