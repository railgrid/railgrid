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

// SvcTargetHeader mirrors the agent-side constant (pkg/agent/tunnel). The agent
// decides whether it will dial the host: loopback always, cluster DNS in
// kubernetes mode, anything else only inside its --svc-allow-cidr ranges (see
// SvcPolicyHeader for how it answers). Exported so out-of-package callers that
// build their own tunnel requests (e.g. the events WebSocket subscriber) set
// the same header — prefer Target.SetSvcHeaders.
const SvcTargetHeader = "X-Faros-Svc-Target"

// SvcTLSInsecureHeader mirrors the agent-side constant: "true" tells the agent
// to skip TLS verification for a non-loopback https target. Set from
// Service.spec.tlsInsecureSkipVerify.
const SvcTLSInsecureHeader = "X-Faros-Svc-TLS-Insecure"

// SvcPolicyHeader is the response header the agent stamps when its host policy
// acted: SvcPolicyEnforce on a 403 refusing the target (never dialed),
// SvcPolicyWarn when a target outside the allow list was dialed anyway under
// --svc-policy=warn. Its presence on a 403 distinguishes an agent refusal from
// a 403 the service itself returned.
const (
	SvcPolicyHeader  = "X-Faros-Svc-Policy"
	SvcPolicyEnforce = "enforce"
	SvcPolicyWarn    = "warn"
)

// svcTargetHeader is the internal spelling used throughout this package.
const svcTargetHeader = SvcTargetHeader

// IsHostNotAllowed reports whether resp is the agent refusing to dial the
// target (403 with X-Faros-Svc-Policy: enforce), as opposed to a 403 from
// the service. See pkg/agent/tunnel/svc.go.
func IsHostNotAllowed(resp *http.Response) bool {
	return resp != nil && resp.StatusCode == http.StatusForbidden &&
		resp.Header.Get(SvcPolicyHeader) == SvcPolicyEnforce
}

// Dialer opens a fresh connection to the edge agent over the reverse tunnel.
// *revdial.Dialer satisfies this.
type Dialer interface {
	Dial(ctx context.Context) (net.Conn, error)
}

// Target identifies the service to reach, plus its bearer token. Host is the
// agent-side address: the loopback for LinuxServer edges (the default when
// empty), cluster DNS ({name}.{namespace}.svc) for KubernetesCluster edges,
// or a spec.host the agent's --svc-allow-cidr policy permits.
type Target struct {
	Scheme string // "http" | "https"
	Host   string // defaults to 127.0.0.1
	Port   int32
	Token  string // bearer token injected as Authorization; may be empty
	// TLSInsecureSkipVerify mirrors Service.spec.tlsInsecureSkipVerify: ask the
	// agent to skip certificate verification for a non-loopback https host.
	TLSInsecureSkipVerify bool
}

// SvcTarget returns the value for the X-Faros-Svc-Target header. The agent
// validates the host against its policy (loopback always; cluster DNS in
// kubernetes mode; --svc-allow-cidr otherwise).
func (t Target) SvcTarget() string {
	host := t.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s://%s:%d", t.Scheme, host, t.Port)
}

// SetSvcHeaders stamps the agent control headers for this target on h: the
// target itself and, when TLSInsecureSkipVerify, the TLS opt-out.
func (t Target) SetSvcHeaders(h http.Header) {
	h.Set(SvcTargetHeader, t.SvcTarget())
	if t.TLSInsecureSkipVerify {
		h.Set(SvcTLSInsecureHeader, "true")
	} else {
		h.Del(SvcTLSInsecureHeader)
	}
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
	target.SetSvcHeaders(req.Header)
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
