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
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	gossh "golang.org/x/crypto/ssh"

	"k8s.io/klog/v2"
)

// sshExec runs remoteCmd on the SSH client via a non-interactive exec channel
// and streams the combined stdout+stderr output as binary WebSocket messages.
// It closes the WebSocket when the command finishes (or on error).
func (p *virtualWorkspaces) sshExec(ctx context.Context, wsConn *websocket.Conn, sshClient *gossh.Client, remoteCmd string, logger klog.Logger) {
	sshSession, err := sshClient.NewSession()
	if err != nil {
		logger.Error(err, "failed to create SSH exec session")
		return
	}
	defer sshSession.Close() //nolint:errcheck

	// Pipe stdout+stderr to a goroutine that forwards chunks to the WebSocket.
	pr, pw := io.Pipe()
	sshSession.Stdout = pw
	sshSession.Stderr = pw

	// Forward pipe → WebSocket in the background.
	fwdDone := make(chan struct{})
	go func() {
		defer close(fwdDone)
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				if werr := wsConn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					logger.V(4).Info("WebSocket write error during exec", "err", werr)
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Run the remote command (blocks until it exits).
	if err := sshSession.Run(remoteCmd); err != nil {
		logger.V(4).Info("SSH exec command finished", "cmd", remoteCmd, "err", err)
	}

	// Close the write end of the pipe so the forwarder goroutine sees EOF.
	pw.Close() //nolint:errcheck
	<-fwdDone  // wait for all output to be forwarded before closing the WebSocket
}

// openAgentSSHTunnel sends an HTTP upgrade request to the agent's /ssh endpoint
// and returns a net.Conn providing raw TCP access to the agent's sshd.
//
// Protocol:
//
//  1. Hub sends:   GET /ssh HTTP/1.1\r\nUpgrade: ssh-tunnel\r\n...
//  2. Agent sends: HTTP/1.1 101 Switching Protocols\r\n...
//  3. Both sides switch to raw SSH byte stream.
//
// A bufferedConn is returned so that any bytes the bufio.Reader buffered past
// the 101 response headers (e.g. the SSH banner) are not lost.
func openAgentSSHTunnel(ctx context.Context, conn net.Conn) (net.Conn, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://agent/ssh", nil)
	if err != nil {
		return nil, fmt.Errorf("building SSH tunnel request: %w", err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "ssh-tunnel")

	if err := req.Write(conn); err != nil {
		return nil, fmt.Errorf("writing SSH tunnel request: %w", err)
	}

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, fmt.Errorf("reading SSH tunnel response: %w", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("expected 101 Switching Protocols from agent, got %d", resp.StatusCode)
	}

	// Wrap conn so that bytes already buffered by the bufio.Reader (e.g. the
	// SSH banner that may have arrived before we finished reading the headers)
	// are not lost.
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

// bufferedConn wraps a net.Conn with a bufio.Reader so that bytes pre-buffered
// during HTTP response parsing are available via Read before the underlying
// connection is used directly.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (bc *bufferedConn) Read(b []byte) (int, error) {
	return bc.reader.Read(b)
}

// SSHClientCredentials holds resolved SSH credentials for authentication.
type SSHClientCredentials struct {
	Username   string
	Password   string // non-empty if password auth is available
	PrivateKey []byte // non-empty if key auth is available
}

// newSSHClient creates an SSH client through a device connection.
// If creds is nil or empty, falls back to empty password authentication.
func newSSHClient(_ context.Context, deviceConn net.Conn, creds *SSHClientCredentials, logger klog.Logger) (*gossh.Client, error) {
	// Default to root user with empty password if no credentials provided.
	sshUser := "root"
	var authMethods []gossh.AuthMethod

	if creds != nil && creds.Username != "" {
		sshUser = creds.Username
	}

	if creds != nil {
		// Prefer private key auth if available.
		if len(creds.PrivateKey) > 0 {
			signer, err := gossh.ParsePrivateKey(creds.PrivateKey)
			if err != nil {
				return nil, fmt.Errorf("parsing SSH private key: %w", err)
			}
			authMethods = append(authMethods, gossh.PublicKeys(signer))
			logger.V(4).Info("Using SSH public key authentication", "user", sshUser)
		}

		// Add password auth if available (can be combined with key auth).
		if creds.Password != "" {
			authMethods = append(authMethods, gossh.Password(creds.Password))
			logger.V(4).Info("Using SSH password authentication", "user", sshUser)
		}
	}

	// Fallback to empty password if no auth methods configured.
	if len(authMethods) == 0 {
		authMethods = []gossh.AuthMethod{gossh.Password("")}
		logger.V(4).Info("Using empty password authentication (fallback)", "user", sshUser)
	}

	// TODO(#64): InsecureIgnoreHostKey accepts any SSH host key — MITM risk.
	// Store a known-good public key in the Edge CRD at registration time
	// and use gossh.FixedHostKey or a custom HostKeyCallback here.
	// https://github.com/faroshq/kedge/issues/64
	sshConfig := &gossh.ClientConfig{
		User:            sshUser,
		Auth:            authMethods,
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // tracked in #64
	}

	sshConn, chans, reqs, err := gossh.NewClientConn(deviceConn, "agent:22", sshConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH client connection: %w", err)
	}

	return gossh.NewClient(sshConn, chans, reqs), nil
}

// isUpgradeRequest checks if the request is a protocol upgrade.
func isUpgradeRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Connection"), "Upgrade")
}
