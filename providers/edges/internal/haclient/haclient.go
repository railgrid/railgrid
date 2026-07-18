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

// Package haclient issues HTTP requests to a service on an edge host through the
// agent's /svc reverse proxy, over the revdial tunnel. It is used both by the
// Service MCP tools and by the validation reconciler. Routing every call
// through the agent's /svc handler keeps loopback enforcement in one place.
package haclient

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
)

// svcTargetHeader mirrors the agent-side constant (pkg/agent/tunnel). The agent
// enforces that the target host is loopback.
const svcTargetHeader = "X-Kedge-Svc-Target"

// Dialer opens a fresh connection to the edge agent over the reverse tunnel.
// *revdial.Dialer satisfies this.
type Dialer interface {
	Dial(ctx context.Context) (net.Conn, error)
}

// Target identifies the service to reach, plus its bearer token. Host is the
// agent-side address: the loopback for LinuxServer edges (the default when
// empty), or cluster DNS ({name}.{namespace}.svc) for KubernetesCluster edges.
type Target struct {
	Scheme string // "http" | "https"
	Host   string // defaults to 127.0.0.1
	Port   int32
	Token  string // bearer token injected as Authorization; may be empty
}

// SvcTarget returns the value for the X-Kedge-Svc-Target header. The agent
// validates the host against what its mode permits (loopback always; cluster
// DNS in kubernetes mode).
func (t Target) SvcTarget() string {
	host := t.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s://%s:%d", t.Scheme, host, t.Port)
}

// Do issues one request to the service behind (dialer, target), injecting the
// target's token as "Authorization: Bearer" (the Home Assistant / Grafana auth
// style). path is the service-local path (e.g. "/api/states"); it is sent to
// the agent as "/svc<path>". A fresh tunnel connection is used per call. The
// caller owns closing the returned response body.
func Do(ctx context.Context, dialer Dialer, target Target, method, path string, body io.Reader) (*http.Response, error) {
	var h http.Header
	if target.Token != "" {
		h = http.Header{"Authorization": []string{"Bearer " + target.Token}}
	}
	return DoWith(ctx, dialer, target, method, path, h, body)
}

// DoWith is Do without the implicit Bearer header: the caller supplies whatever
// auth headers the service needs (e.g. "X-Api-Key" for the *arr apps, a "Cookie"
// for qBittorrent), and encodes any query into path. target.Token is used only
// for the svc-target host:port. The caller owns closing the response body.
func DoWith(ctx context.Context, dialer Dialer, target Target, method, path string, header http.Header, body io.Reader) (*http.Response, error) {
	conn, err := dialer.Dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("dialing edge agent: %w", err)
	}

	// The agent mux serves the service proxy under /svc/.
	req, err := http.NewRequestWithContext(ctx, method, "http://edge-agent/svc"+path, body)
	if err != nil {
		conn.Close() //nolint:errcheck
		return nil, err
	}
	req.Header.Set(svcTargetHeader, target.SvcTarget())
	for k, vals := range header {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}

	if err := req.Write(conn); err != nil {
		conn.Close() //nolint:errcheck
		return nil, fmt.Errorf("writing request to tunnel: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		conn.Close() //nolint:errcheck
		return nil, fmt.Errorf("reading response from tunnel: %w", err)
	}
	// Tie the connection's lifetime to the body so streaming responses work and
	// the socket is released when the caller closes the body.
	resp.Body = &connBody{ReadCloser: resp.Body, conn: conn}
	return resp, nil
}

// connBody closes the underlying tunnel connection when the response body is
// closed.
type connBody struct {
	io.ReadCloser
	conn net.Conn
}

func (b *connBody) Close() error {
	err := b.ReadCloser.Close()
	if cerr := b.conn.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}
