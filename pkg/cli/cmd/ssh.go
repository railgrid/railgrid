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
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
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
		Short: "Open an SSH session to an edge via the hub",
		Long: `Open an interactive SSH session (or run a single command) on an Edge
that is connected to the hub.

Examples:
  # Interactive session
  kedge ssh my-server

  # Run a single command (non-interactive)
  kedge ssh my-server -- echo hello
`,
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: false,
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

	// Fetch the Edge resource to get the proxy URL from status.
	client, err := kedgeclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating kedge client: %w", err)
	}

	edge, err := client.Edges().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching edge %q: %w", name, err)
	}

	if edge.Status.URL == "" {
		return fmt.Errorf("edge %q has no proxy URL in status; is the agent running?", name)
	}

	wsURL, err := buildSSHWebSocketURL(config, edge.Status.URL, remoteCmd)
	if err != nil {
		return fmt.Errorf("building SSH endpoint URL: %w", err)
	}

	headers := http.Header{}
	if config.BearerToken != "" {
		headers.Set("Authorization", "Bearer "+config.BearerToken)
	}

	dialer := &websocket.Dialer{
		TLSClientConfig: tlsConfigFromRest(config),
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return fmt.Errorf("connecting to hub SSH endpoint %s: %w", wsURL, err)
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
// Edge.Status.URL for server-type edges is already the complete HTTPS URL:
//
//	https://<hub>/services/edges-proxy/clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/ssh
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
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// runSSHCommandStream reads output messages from the WebSocket until the
// connection is closed by the hub (after the remote command exits).  The
// command itself was already conveyed to the hub via the "cmd" query
// parameter in the WebSocket URL; there is nothing to write here.
func runSSHCommandStream(ctx context.Context, conn *websocket.Conn) error {
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

// runSSHInteractive bridges a raw terminal to the hub SSH WebSocket session.
func runSSHInteractive(ctx context.Context, conn *websocket.Conn) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("stdin is not a terminal; use 'kedge ssh <name> -- <command>' for non-interactive use")
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

	// Forward SIGWINCH as resize messages.
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)
	go func() {
		for range sigwinch {
			if cols, rows, err := term.GetSize(fd); err == nil {
				sendSSHResize(conn, cols, rows)
			}
		}
	}()

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

func tlsConfigFromRest(config *rest.Config) *tls.Config {
	if config.Insecure {
		return &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return nil
}
