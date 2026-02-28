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

package framework

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/clientcmd"
)

// sshWsMsg mirrors the wsMsg type in pkg/util/ssh.
type sshWsMsg struct {
	Type string `json:"type"`
	Cmd  string `json:"cmd,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// SSHWebSocketClient is a programmatic WebSocket SSH client for use in e2e tests.
// It connects directly to the hub SSH subresource endpoint, bypassing the CLI,
// so that the interactive PTY path can be exercised without a real terminal.
//
// A single background reader goroutine pumps all inbound WebSocket frames into
// a buffered channel.  CollectOutput drains that channel with a time.Timer
// instead of using SetReadDeadline.  This avoids the gorilla/websocket
// behaviour of permanently storing read errors: once a deadline fires,
// c.readErr is set and every subsequent ReadMessage returns that error
// immediately, making the connection unusable for further reads.
type SSHWebSocketClient struct {
	conn *websocket.Conn
	// msgs receives every binary/text message from the server.  The channel
	// is closed when the reader goroutine exits (connection closed / error).
	msgs chan []byte
}

// DialSSH connects to the hub SSH WebSocket endpoint for the given edge name.
// kubeconfig is used to extract the hub URL and bearer token.
func DialSSH(ctx context.Context, kubeconfig, name string) (*SSHWebSocketClient, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}

	// Build WebSocket URL.  The kubeconfig Host may contain a path suffix (e.g.
	// /clusters/<name> when kcp workspaces are in use).  We must use url.Parse
	// to replace the path rather than appending to it, exactly as
	// buildSSHWebSocketURL does in pkg/cli/cmd/ssh.go.
	u, err := url.Parse(strings.TrimRight(cfg.Host, "/"))
	if err != nil {
		return nil, fmt.Errorf("parsing hub URL %q: %w", cfg.Host, err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		u.Scheme = "wss"
	}
	u.Path = fmt.Sprintf("/services/edges-proxy/clusters/default/apis/kedge.faros.sh/v1alpha1/edges/%s/ssh", name)
	wsURL := u.String()

	headers := http.Header{}
	if cfg.BearerToken != "" {
		headers.Set("Authorization", "Bearer "+cfg.BearerToken)
	}

	dialer := &websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return nil, fmt.Errorf("dialling SSH WebSocket %s: %w", wsURL, err)
	}

	c := &SSHWebSocketClient{
		conn: conn,
		msgs: make(chan []byte, 1024),
	}
	go c.reader()
	return c, nil
}

// reader is the single goroutine that reads from the WebSocket connection and
// delivers messages to the msgs channel.  Using a single goroutine avoids the
// "concurrent readers" restriction of gorilla/websocket.  The channel is
// closed when the connection is closed or an error occurs.
func (c *SSHWebSocketClient) reader() {
	defer close(c.msgs)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		c.msgs <- data
	}
}

// SendResize sends a terminal resize message.
func (c *SSHWebSocketClient) SendResize(cols, rows int) error {
	return c.sendMsg(sshWsMsg{Type: "resize", Cols: cols, Rows: rows})
}

// SendInput sends raw bytes as a cmd message (base64-encoded).
func (c *SSHWebSocketClient) SendInput(data []byte) error {
	return c.sendMsg(sshWsMsg{
		Type: "cmd",
		Cmd:  base64.StdEncoding.EncodeToString(data),
	})
}

// CollectOutput drains WebSocket messages for up to timeout and returns all
// output concatenated as a string.  It uses a time.Timer (not SetReadDeadline)
// so that the underlying connection remains usable after the call returns.
func (c *SSHWebSocketClient) CollectOutput(ctx context.Context, timeout time.Duration) string {
	var sb strings.Builder
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case data, ok := <-c.msgs:
			if !ok {
				// reader goroutine exited (connection closed / error)
				return sb.String()
			}
			sb.Write(data)
		case <-timer.C:
			return sb.String()
		case <-ctx.Done():
			return sb.String()
		}
	}
}

// Close closes the WebSocket connection.
func (c *SSHWebSocketClient) Close() error {
	return c.conn.Close()
}

func (c *SSHWebSocketClient) sendMsg(msg sshWsMsg) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.TextMessage, b)
}
