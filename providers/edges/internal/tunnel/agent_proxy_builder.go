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

package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	gossh "golang.org/x/crypto/ssh"
	"k8s.io/klog/v2"

	edgeapi "github.com/faroshq/provider-edges/internal/edgeapi"
)

// sshExec runs remoteCmd on the SSH client via a non-interactive exec channel
// and streams the combined stdout+stderr output as binary WebSocket messages.
// It closes the WebSocket when the command finishes (or on error).
func (p *Server) sshExec(ctx context.Context, wsConn *websocket.Conn, sshClient *gossh.Client, remoteCmd string, logger klog.Logger) {
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
		// remoteCmd is caller-controlled; escape and bound it before logging
		// (see sanitizeAuditValue).
		logger.V(4).Info("SSH exec command finished", "cmd", sanitizeAuditValue(remoteCmd), "err", err)
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
	// SSHHostKey is the sshd host public key in authorized_keys format (e.g.
	// "ssh-ed25519 AAAA...") the session is verified against: the operator pin
	// (spec.sshHostKey) when set, else the agent-reported status.sshHostKey.
	SSHHostKey string
	// SSHHostKeyPolicy applies when SSHHostKey is empty (see
	// edgeapi.SSHHostKeyPolicy). Empty means strict.
	SSHHostKeyPolicy edgeapi.SSHHostKeyPolicy
}

// sshHostKeyVerification describes how newSSHClient verifies the server's host
// key.
type sshHostKeyVerification struct {
	// Key is the expected host public key in authorized_keys format. Empty
	// means no key is known.
	Key string
	// Policy applies only when Key is empty: strict (the default) refuses the
	// session; tofu accepts the key the server presents and reports it as the
	// learned key so the caller can record it.
	Policy edgeapi.SSHHostKeyPolicy
	// AllowUnverified is the provider-wide legacy escape hatch
	// (--allow-unverified-ssh-host-key): an empty Key is accepted without
	// verification or recording. It never applies to an unparseable Key.
	AllowUnverified bool
}

// sshUsername returns the SSH login the session authenticates as: the resolved
// credential's username, else root.
func sshUsername(creds *SSHClientCredentials) string {
	if creds != nil && creds.Username != "" {
		return creds.Username
	}
	return "root"
}

// sshHandshakeTimeout bounds the SSH version + key exchange when the caller's
// context carries no deadline. The SSH data-plane path is a WebSocket upgrade,
// whose request context has none.
const sshHandshakeTimeout = 30 * time.Second

// newSSHClient creates an SSH client through a device connection. If creds is
// nil or empty, falls back to empty password authentication.
//
// Host key verification fails closed: a key that does not parse is an error,
// and an empty key is an error under the strict policy. Under tofu the key the
// server presents is accepted and returned as learnedKey (empty otherwise) so
// the caller can record it in status.sshHostKey and enforce it from then on.
//
// ctx bounds the handshake only (see sshHandshakeTimeout); the established
// session outlives it.
func newSSHClient(ctx context.Context, deviceConn net.Conn, creds *SSHClientCredentials, hk sshHostKeyVerification, logger klog.Logger) (client *gossh.Client, learnedKey string, err error) {
	sshUser := sshUsername(creds)
	var authMethods []gossh.AuthMethod

	if creds != nil {
		// Prefer private key auth if available.
		if len(creds.PrivateKey) > 0 {
			signer, err := gossh.ParsePrivateKey(creds.PrivateKey)
			if err != nil {
				return nil, "", fmt.Errorf("parsing SSH private key: %w", err)
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

	// Host key verification. A known key (operator pin or the recorded agent
	// report) is always enforced. With no key known the per-edge policy
	// decides: strict refuses, tofu learns. The provider-wide escape hatch
	// restores the legacy unverified behaviour for agents that never reported
	// a key; it is logged at V(0) on every use.
	var hostKeyCallback gossh.HostKeyCallback
	var presented gossh.PublicKey
	switch {
	case hk.Key != "":
		pk, _, _, _, err := gossh.ParseAuthorizedKey([]byte(hk.Key))
		if err != nil {
			return nil, "", fmt.Errorf("parsing SSH host key: %w", err)
		}
		hostKeyCallback = gossh.FixedHostKey(pk)
		logger.V(4).Info("Using strict SSH host key verification", "keyType", pk.Type())
	case hk.Policy == edgeapi.SSHHostKeyPolicyTOFU:
		hostKeyCallback = func(_ string, _ net.Addr, key gossh.PublicKey) error {
			presented = key
			return nil
		}
		logger.Info("no SSH host key known for edge; trusting the key presented on this first session (sshHostKeyPolicy=tofu)")
	case hk.AllowUnverified:
		hostKeyCallback = gossh.InsecureIgnoreHostKey() //nolint:gosec // explicit operator escape hatch, logged
		logger.Info("WARNING: no SSH host key known for edge and --allow-unverified-ssh-host-key is set; host identity is NOT verified (MITM risk)")
	default:
		return nil, "", fmt.Errorf("no SSH host key known for edge and sshHostKeyPolicy is strict: pin spec.sshHostKey, wait for the agent to report one, or set sshHostKeyPolicy=tofu")
	}

	sshConfig := &gossh.ClientConfig{
		User:            sshUser,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
	}

	// gossh.NewClientConn takes no context and blocks for as long as the peer
	// keeps the socket open without completing the version exchange or key
	// exchange — a stalled tunnel would pin this handler goroutine forever.
	// Bound it on the connection, the same way remoteDialer.Dial bounds its
	// relay handshake: honour the caller's deadline when it has one, else a
	// short default; clear it once the connection is up so the session itself
	// is not time-limited.
	if deadline, ok := ctx.Deadline(); ok {
		_ = deviceConn.SetDeadline(deadline)
	} else {
		_ = deviceConn.SetDeadline(time.Now().Add(sshHandshakeTimeout))
	}
	sshConn, chans, reqs, err := gossh.NewClientConn(deviceConn, "agent:22", sshConfig)
	if err != nil {
		_ = deviceConn.SetDeadline(time.Time{})
		return nil, "", fmt.Errorf("failed to create SSH client connection: %w", err)
	}
	_ = deviceConn.SetDeadline(time.Time{})

	if presented != nil {
		learnedKey = strings.TrimSpace(string(gossh.MarshalAuthorizedKey(presented)))
	}
	return gossh.NewClient(sshConn, chans, reqs), learnedKey, nil
}

// Fingerprint placeholders for keys that have none. "no key recorded" and
// "a key is recorded but it is malformed" are very different states in a
// condition message or an audit line, so they never share a rendering.
const (
	sshHostKeyFingerprintUnknown     = "unknown"
	sshHostKeyFingerprintUnparseable = "unparseable"
)

// sshHostKeyFingerprint returns the SHA256 fingerprint of an authorized_keys
// format host key. An empty (or whitespace-only) key yields "unknown"; a
// non-empty key that does not parse yields "unparseable". Used in conditions
// and audit lines so raw keys never need to be compared by eye.
func sshHostKeyFingerprint(key string) string {
	if strings.TrimSpace(key) == "" {
		return sshHostKeyFingerprintUnknown
	}
	pk, _, _, _, err := gossh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return sshHostKeyFingerprintUnparseable
	}
	return gossh.FingerprintSHA256(pk)
}

// sameSSHHostKey reports whether two authorized_keys format keys denote the
// same public key (ignoring comments and whitespace). Unparseable keys compare
// by exact string.
func sameSSHHostKey(a, b string) bool {
	pa, _, _, _, errA := gossh.ParseAuthorizedKey([]byte(a))
	pb, _, _, _, errB := gossh.ParseAuthorizedKey([]byte(b))
	if errA != nil || errB != nil {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return bytes.Equal(pa.Marshal(), pb.Marshal())
}

// isUpgradeRequest checks if the request is a protocol upgrade.
func isUpgradeRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Connection"), "Upgrade")
}
