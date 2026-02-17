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

// Package auth handles OIDC authentication for the CLI.
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

// LocalhostCallbackAuthenticator starts a local HTTP server on a random port
// and waits for the hub to redirect back with the login response.
type LocalhostCallbackAuthenticator struct {
	listener net.Listener
	server   *http.Server
	done     chan struct{}
	response tenancyv1alpha1.LoginResponse
	err      error
	once     sync.Once
}

// NewLocalhostCallbackAuthenticator creates a new authenticator.
func NewLocalhostCallbackAuthenticator() *LocalhostCallbackAuthenticator {
	return &LocalhostCallbackAuthenticator{
		done: make(chan struct{}),
	}
}

// Start begins listening on a random available port.
func (a *LocalhostCallbackAuthenticator) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	a.listener = ln

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", a.callback)

	a.server = &http.Server{Handler: mux}
	go func() {
		if err := a.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			a.err = err
			a.once.Do(func() { close(a.done) })
		}
	}()

	return nil
}

// Port returns the port the server is listening on.
func (a *LocalhostCallbackAuthenticator) Port() int {
	return a.listener.Addr().(*net.TCPAddr).Port
}

// Endpoint returns the full callback URL.
func (a *LocalhostCallbackAuthenticator) Endpoint() string {
	return fmt.Sprintf("http://127.0.0.1:%d/callback", a.Port())
}

// WaitForResponse blocks until the callback is received or the context is cancelled.
func (a *LocalhostCallbackAuthenticator) WaitForResponse(ctx context.Context) (tenancyv1alpha1.LoginResponse, error) {
	select {
	case <-a.done:
		_ = a.server.Shutdown(context.Background())
		if a.err != nil {
			return tenancyv1alpha1.LoginResponse{}, a.err
		}
		return a.response, nil
	case <-ctx.Done():
		_ = a.server.Shutdown(context.Background())
		return tenancyv1alpha1.LoginResponse{}, ctx.Err()
	}
}

// callback handles the redirect from the hub with the encoded login response.
func (a *LocalhostCallbackAuthenticator) callback(w http.ResponseWriter, r *http.Request) {
	encoded := r.URL.Query().Get("response")
	if encoded == "" {
		http.Error(w, "missing response parameter", http.StatusBadRequest)
		return
	}

	data, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		http.Error(w, "invalid response encoding", http.StatusBadRequest)
		return
	}

	var resp tenancyv1alpha1.LoginResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		http.Error(w, "invalid response payload", http.StatusBadRequest)
		return
	}

	a.response = resp

	w.Header().Set("Content-Type", "text/html")
	_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body><h2>Login successful!</h2><p>You can close this tab and return to the terminal.</p></body></html>`)

	a.once.Do(func() { close(a.done) })
}
