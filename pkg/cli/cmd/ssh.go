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
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"

	farosclient "github.com/faroshq/faros/pkg/client"
)

// wsSshMsg mirrors the wsMsg type used by pkg/util/ssh.
type wsSSHMsg struct {
	Type string `json:"type"`
	Cmd  string `json:"cmd,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func newSSHCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh <name> [-- command [args...]]",
		Short: "Open an SSH session to a Linux server edge via the hub",
		Long: `Open an interactive SSH session (or run a single command) on an Edge
that is connected to the hub.

Examples:
  # Interactive session
  faros ssh my-server

  # Run a single command (non-interactive)
  faros ssh my-server -- echo hello
`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeServerEdgeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSSH(cmd, args)
		},
	}

	return cmd
}

func runSSH(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Everything after "--" is the remote command.
	var remoteCmd string
	if dashIdx := cmd.ArgsLenAtDash(); dashIdx >= 0 {
		remoteCmd = strings.Join(args[dashIdx:], " ")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	config, err := loadRestConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}

	// Fetch the Edge resource to get the proxy URL from status. The Edge type
	// now lives in the edges-connectivity provider (edges.faros.sh), so we
	// read it via the dynamic client and pull status.URL out of the unstructured.
	client, err := farosclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating faros client: %w", err)
	}

	edge, gvr, err := getEdgeByName(ctx, client.Dynamic(), name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("edge %q not found in this workspace (faros edge list)", name)
		}
		return fmt.Errorf("fetching edge %q: %w", name, err)
	}
	switch gvr {
	case farosclient.LinuxServerGVR:
	case farosclient.KubernetesClusterGVR:
		return fmt.Errorf("edge %q is a Kubernetes cluster, not a Linux server; use: faros connect %s", name, name)
	default:
		return fmt.Errorf("edge %q is a %s edge; SSH is only available for Linux server edges", name, farosclient.EdgeTypeForGVR(gvr))
	}

	edgeURL, _, _ := unstructured.NestedString(edge.Object, "status", "URL")
	if edgeURL == "" {
		connected, _, _ := unstructured.NestedBool(edge.Object, "status", "connected")
		if !connected {
			return fmt.Errorf("edge %q is not connected; start the agent on it ('faros edge join-command %s' prints how)", name, name)
		}
		return fmt.Errorf("edge %q has no proxy URL in status yet; retry shortly", name)
	}

	// Externalize the edge URL: status.URL may use an internal host (for kcp
	// mount resolution). Replace the host with the hub's external address from
	// the kubeconfig.
	externalURL, err := externalizeEdgeURLFromConfig(edgeURL, config)
	if err != nil {
		return fmt.Errorf("constructing external edge URL: %w", err)
	}

	wsURL, err := buildSSHWebSocketURL(config, externalURL, remoteCmd)
	if err != nil {
		return fmt.Errorf("building SSH endpoint URL: %w", err)
	}

	headers, tlsConfig, err := wsAuthFromRest(ctx, config, externalURL)
	if err != nil {
		return fmt.Errorf("resolving kubeconfig credentials: %w", err)
	}

	dialer := &websocket.Dialer{
		TLSClientConfig: tlsConfig,
		Proxy:           http.ProxyFromEnvironment,
	}

	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return fmt.Errorf("connecting to hub SSH endpoint %s: %w", wsURL, describeDialError(err, resp))
	}
	defer conn.Close() //nolint:errcheck

	if remoteCmd != "" {
		return runSSHCommandStream(ctx, conn)
	}
	return runSSHInteractive(ctx, conn)
}

// buildSSHWebSocketURL constructs the WebSocket URL for the hub SSH subresource
// using the edge's full proxy URL from status.URL.
//
// Edge.Status.URL for server-type edges is the edges-provider proxy path
// (externalized against the current kubeconfig host by the caller):
//
//	https://<hub>/services/providers/edges/edgeproxy/clusters/{cluster}/apis/edges.faros.sh/v1alpha1/linuxservers/{name}/ssh
//
// This function simply converts the scheme to WebSocket (https→wss, http→ws)
// and optionally appends the "cmd" query parameter for non-interactive SSH exec.
func buildSSHWebSocketURL(_ *rest.Config, edgeURL, remoteCmd string) (string, error) {
	u, err := url.Parse(edgeURL)
	if err != nil {
		return "", fmt.Errorf("parsing edge URL %q: %w", edgeURL, err)
	}

	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		u.Scheme = "wss"
	}

	if remoteCmd != "" {
		q := url.Values{}
		q.Set("cmd", remoteCmd)
		// The CLI forwards its stdin (or an immediate EOF) for every
		// non-interactive command; tell the hub so it wires the command's
		// stdin to the WebSocket instead of leaving it empty.
		q.Set("stdin", "1")
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// runSSHCommandStream reads output messages from the WebSocket until the
// connection is closed by the hub (after the remote command exits). The
// command itself was already conveyed to the hub via the "cmd" query
// parameter in the WebSocket URL. What is written here is the command's
// stdin: when the local stdin is a pipe or file its bytes are forwarded as
// "cmd" messages followed by "eof" (so `cat f | faros ssh x -- "cat > f"`
// copies the file); when it is a terminal an "eof" goes out at once so
// commands that read stdin do not hang waiting for the keyboard.
func runSSHCommandStream(ctx context.Context, conn *websocket.Conn) error {
	go forwardSSHStdin(conn, os.Stdin, term.IsTerminal(int(os.Stdin.Fd())))
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			// Normal EOF — remote command finished.
			return nil //nolint:nilerr
		}
		if _, err := os.Stdout.Write(data); err != nil {
			return err
		}
	}
}

// forwardSSHStdin streams r to the hub as base64 "cmd" messages and ends
// with an "eof" message. A terminal stdin is not read at all (only the EOF
// is sent), because a non-interactive command has no business waiting for
// keystrokes. Write errors end the copy silently: the read loop reports the
// connection state.
func forwardSSHStdin(conn *websocket.Conn, r io.Reader, isTerminal bool) {
	send := func(msg wsSSHMsg) bool {
		b, _ := json.Marshal(msg)
		return conn.WriteMessage(websocket.TextMessage, b) == nil
	}
	if !isTerminal {
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 && !send(wsSSHMsg{Type: "cmd", Cmd: base64.StdEncoding.EncodeToString(buf[:n])}) {
				return
			}
			if err != nil {
				break
			}
		}
	}
	send(wsSSHMsg{Type: "eof"})
}

// runSSHInteractive bridges a raw terminal to the hub SSH WebSocket session.
func runSSHInteractive(ctx context.Context, conn *websocket.Conn) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("stdin is not a terminal; use 'faros ssh <name> -- <command>' for non-interactive use")
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("setting raw terminal: %w", err)
	}
	defer term.Restore(fd, oldState) //nolint:errcheck

	// Send initial terminal size.
	if cols, rows, err := term.GetSize(fd); err == nil {
		sendSSHResize(conn, cols, rows)
	}

	// Forward terminal resize signals as SSH resize messages (Unix only).
	watchResizeSignals(fd, conn)

	// Stdin → WebSocket
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				return
			}
			msg, _ := json.Marshal(wsSSHMsg{
				Type: "cmd",
				Cmd:  base64.StdEncoding.EncodeToString(buf[:n]),
			})
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()

	// WebSocket → Stdout
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-stdinDone:
			return nil
		default:
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return nil //nolint:nilerr
		}
		if _, err := os.Stdout.Write(data); err != nil {
			return err
		}
	}
}

func sendSSHResize(conn *websocket.Conn, cols, rows int) {
	b, _ := json.Marshal(wsSSHMsg{Type: "resize", Cols: cols, Rows: rows})
	_ = conn.WriteMessage(websocket.TextMessage, b)
}

// wsAuthFromRest resolves the request headers and TLS settings a raw WebSocket
// dial needs in order to authenticate the same way client-go would.
//
// The hub SSH endpoint is a plain WebSocket, not a Kubernetes streaming
// subresource, so it is dialled with gorilla directly rather than through a
// rest client — which means the kubeconfig's credentials have to be applied by
// hand. Reading config.BearerToken is not enough: that field is empty for every
// kubeconfig that authenticates through an exec credential plugin (the common
// case for `faros login`), a token file, basic auth, or a client certificate,
// and the dial then goes out unauthenticated and the hub answers 401 — which
// gorilla surfaces only as "websocket: bad handshake".
//
// Instead the standard client-go transport stack is assembled around a
// round tripper that records the request rather than sending it, so the exec
// plugin (and every other credential wrapper) decorates a probe request exactly
// as it would a real one; the headers it produced are then handed to the dialer.
// Client-certificate credentials, custom CAs and --insecure-skip-tls-verify come
// through the tls.Config that TLSConfigFor builds from the same config.
func wsAuthFromRest(ctx context.Context, config *rest.Config, targetURL string) (http.Header, *tls.Config, error) {
	transportConfig, err := config.TransportConfig()
	if err != nil {
		return nil, nil, err
	}
	tlsConfig, err := transport.TLSConfigFor(transportConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("building TLS config: %w", err)
	}

	capture := &captureRoundTripper{}
	rt, err := transport.HTTPWrappersForConfig(transportConfig, capture)
	if err != nil {
		return nil, nil, err
	}

	// The probe is never sent; it carries the real URL only so credential
	// plugins that key off it behave as they would for the actual dial.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, nil, err
	}
	if _, err := rt.RoundTrip(req); err != nil {
		return nil, nil, err
	}
	if capture.header == nil {
		return http.Header{}, tlsConfig, nil
	}
	return capture.header, tlsConfig, nil
}

// captureRoundTripper records the headers of the request it is given and
// answers with an empty 200 instead of sending anything over the network.
type captureRoundTripper struct {
	header http.Header
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.header = req.Header.Clone()
	return &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1",
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

// describeDialError enriches a WebSocket dial failure with the server's
// response. gorilla reports every non-101 answer as the opaque
// "websocket: bad handshake", which hides whether the hub said 401
// (credentials), 403 (RBAC) or 502 (no agent tunnel).
func describeDialError(err error, resp *http.Response) error {
	if resp == nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		return fmt.Errorf("%w (%s)", err, resp.Status)
	}
	return fmt.Errorf("%w (%s: %s)", err, resp.Status, detail)
}
