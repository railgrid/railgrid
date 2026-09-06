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
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	edgeapi "github.com/faroshq/provider-edges/internal/edgeapi"
)

// newTestHostKey generates an sshd host key and returns its signer and its
// authorized_keys form.
func newTestHostKey(t *testing.T) (gossh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating host key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("creating signer: %v", err)
	}
	return signer, strings.TrimSpace(string(gossh.MarshalAuthorizedKey(signer.PublicKey())))
}

// startTestSSHServer runs a minimal SSH server (no client auth) on a loopback
// listener and returns a connection to it. A real socket rather than net.Pipe:
// the SSH version exchange writes before it reads on both sides, which
// deadlocks on an unbuffered pipe.
func startTestSSHServer(t *testing.T, signer gossh.Signer) net.Conn {
	t.Helper()
	cfg := &gossh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		sc, chans, reqs, err := gossh.NewServerConn(conn, cfg)
		if err != nil {
			conn.Close() //nolint:errcheck
			return
		}
		defer sc.Close() //nolint:errcheck
		go gossh.DiscardRequests(reqs)
		for ch := range chans {
			ch.Reject(gossh.Prohibited, "test server") //nolint:errcheck
		}
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialling test SSH server: %v", err)
	}
	t.Cleanup(func() { clientConn.Close() }) //nolint:errcheck
	return clientConn
}

func TestNewSSHClientHostKeyVerification(t *testing.T) {
	serverSigner, serverKey := newTestHostKey(t)
	_, otherKey := newTestHostKey(t)

	cases := []struct {
		name        string
		hk          sshHostKeyVerification
		wantErr     string
		wantLearned string
	}{
		{
			name:        "empty key with tofu succeeds and learns the presented key",
			hk:          sshHostKeyVerification{Policy: edgeapi.SSHHostKeyPolicyTOFU},
			wantLearned: serverKey,
		},
		{
			name: "matching key succeeds and learns nothing",
			hk:   sshHostKeyVerification{Key: serverKey, Policy: edgeapi.SSHHostKeyPolicyStrict},
		},
		{
			name:    "mismatching key fails the handshake even under tofu and the escape hatch",
			hk:      sshHostKeyVerification{Key: otherKey, Policy: edgeapi.SSHHostKeyPolicyTOFU, AllowUnverified: true},
			wantErr: "failed to create SSH client connection",
		},
		{
			name: "empty key with the escape hatch succeeds without learning",
			hk:   sshHostKeyVerification{Policy: edgeapi.SSHHostKeyPolicyStrict, AllowUnverified: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := startTestSSHServer(t, serverSigner)
			client, learned, err := newSSHClient(context.Background(), conn, nil, tc.hk, klog.Background())
			if tc.wantErr != "" {
				if err == nil {
					client.Close() //nolint:errcheck
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("newSSHClient: %v", err)
			}
			defer client.Close() //nolint:errcheck
			if learned != tc.wantLearned {
				t.Fatalf("learned key = %q, want %q", learned, tc.wantLearned)
			}
		})
	}
}

// refusedConn is a net.Conn that records any attempt to use it and refuses.
//
// Cases that must be refused BEFORE a handshake are asserted against it: a real
// socket cannot prove that nothing was sent, and net.Pipe is not an option
// either — the SSH version exchange writes before it reads on both sides, which
// deadlocks on an unbuffered pipe (see startTestSSHServer). Read/Write also
// return an error rather than blocking, so a regression fails the assertion
// instead of hanging the test.
//
// SetDeadline and friends deliberately do NOT count as use: newSSHClient arms
// the handshake deadline on its way into gossh.NewClientConn, which a refused
// session never reaches.
type refusedConn struct {
	mu   sync.Mutex
	used []string
}

func (c *refusedConn) mark(op string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.used = append(c.used, op)
	return fmt.Errorf("refusedConn: unexpected %s", op)
}

func (c *refusedConn) uses() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.used...)
}

func (c *refusedConn) Read([]byte) (int, error)         { return 0, c.mark("read") }
func (c *refusedConn) Write([]byte) (int, error)        { return 0, c.mark("write") }
func (c *refusedConn) Close() error                     { return nil }
func (c *refusedConn) LocalAddr() net.Addr              { return refusedAddr{} }
func (c *refusedConn) RemoteAddr() net.Addr             { return refusedAddr{} }
func (c *refusedConn) SetDeadline(time.Time) error      { return nil }
func (c *refusedConn) SetReadDeadline(time.Time) error  { return nil }
func (c *refusedConn) SetWriteDeadline(time.Time) error { return nil }

type refusedAddr struct{}

func (refusedAddr) Network() string { return "refused" }
func (refusedAddr) String() string  { return "refused" }

// TestNewSSHClientRefusesBeforeTouchingTheConnection asserts that a host key
// that cannot be honoured is rejected without a single byte reaching the edge.
func TestNewSSHClientRefusesBeforeTouchingTheConnection(t *testing.T) {
	cases := []struct {
		name    string
		hk      sshHostKeyVerification
		wantErr string
	}{
		{
			name:    "garbage key, even with tofu and the escape hatch set",
			hk:      sshHostKeyVerification{Key: "not a key", Policy: edgeapi.SSHHostKeyPolicyTOFU, AllowUnverified: true},
			wantErr: "parsing SSH host key",
		},
		{
			name:    "empty key with strict policy",
			hk:      sshHostKeyVerification{Policy: edgeapi.SSHHostKeyPolicyStrict},
			wantErr: "no SSH host key known",
		},
		{
			name:    "empty key with no policy defaults to strict",
			hk:      sshHostKeyVerification{},
			wantErr: "no SSH host key known",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := &refusedConn{}
			client, learned, err := newSSHClient(context.Background(), conn, nil, tc.hk, klog.Background())
			if err == nil {
				client.Close() //nolint:errcheck
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.wantErr)
			}
			if learned != "" {
				t.Fatalf("refused session learned a key: %q", learned)
			}
			if used := conn.uses(); len(used) != 0 {
				t.Fatalf("the connection was used before the refusal: %v", used)
			}
		})
	}
}

// TestNewSSHClientBoundsTheHandshake covers a peer that accepts the connection
// and then says nothing: gossh.NewClientConn takes no context, so without a
// deadline on the connection the handler goroutine would block forever.
func TestNewSSHClientBoundsTheHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck

	// Accept and hold the connection open without ever sending a version string.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		<-time.After(time.Minute)
		conn.Close() //nolint:errcheck
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialling the stalled peer: %v", err)
	}
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck

	// A short caller deadline stands in for the production default
	// (sshHandshakeTimeout), which is too long for a test.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		client, _, err := newSSHClient(ctx, conn, nil, sshHostKeyVerification{AllowUnverified: true}, klog.Background())
		if client != nil {
			client.Close() //nolint:errcheck
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the stalled handshake to fail")
		}
		if !strings.Contains(err.Error(), "failed to create SSH client connection") {
			t.Fatalf("error = %q, want the client-connection failure", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("handshake took %s; the deadline was not honoured", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("newSSHClient did not return: the SSH handshake is unbounded")
	}
}

func TestSSHHostKeyFingerprintDistinguishesUnknownFromUnparseable(t *testing.T) {
	// "no key recorded" and "a key is recorded but malformed" must not share a
	// rendering: they call for different operator action.
	for _, empty := range []string{"", "   ", "\n\t "} {
		if got := sshHostKeyFingerprint(empty); got != sshHostKeyFingerprintUnknown {
			t.Fatalf("sshHostKeyFingerprint(%q) = %q, want %q", empty, got, sshHostKeyFingerprintUnknown)
		}
	}
	for _, bad := range []string{"not a key", "ssh-ed25519 !!!!"} {
		if got := sshHostKeyFingerprint(bad); got != sshHostKeyFingerprintUnparseable {
			t.Fatalf("sshHostKeyFingerprint(%q) = %q, want %q", bad, got, sshHostKeyFingerprintUnparseable)
		}
	}
	_, key := newTestHostKey(t)
	if got := sshHostKeyFingerprint(key); !strings.HasPrefix(got, "SHA256:") {
		t.Fatalf("sshHostKeyFingerprint(valid key) = %q, want a SHA256 fingerprint", got)
	}
}

func TestApplyReportedSSHHostKeyIsWriteOnce(t *testing.T) {
	_, first := newTestHostKey(t)
	_, second := newTestHostKey(t)
	now := metav1.Now()

	status := map[string]interface{}{}
	applyReportedSSHHostKey(status, first, now)
	if status["sshHostKey"] != first {
		t.Fatalf("first report not recorded: %v", status["sshHostKey"])
	}
	if c := findStatusCondition(status, edgeapi.ConnectionConditionSSHHostKeyChanged); c != nil {
		t.Fatalf("unexpected condition after first report: %v", c)
	}

	// A different key is NOT recorded; the mismatch is surfaced as a condition
	// that names both fingerprints.
	applyReportedSSHHostKey(status, second, now)
	if status["sshHostKey"] != first {
		t.Fatalf("recorded key was replaced by a later report: %v", status["sshHostKey"])
	}
	c := findStatusCondition(status, edgeapi.ConnectionConditionSSHHostKeyChanged)
	if c == nil {
		t.Fatal("SSHHostKeyChanged condition not set on mismatch")
	}
	if c["status"] != string(metav1.ConditionTrue) || c["reason"] != "FingerprintMismatch" {
		t.Fatalf("condition = %v, want True/FingerprintMismatch", c)
	}
	msg, _ := c["message"].(string)
	for _, fp := range []string{sshHostKeyFingerprint(first), sshHostKeyFingerprint(second)} {
		if !strings.Contains(msg, fp) {
			t.Fatalf("condition message %q lacks fingerprint %s", msg, fp)
		}
	}

	// The same key again (with a comment appended, as sshd tooling emits)
	// keeps the record and clears the condition.
	applyReportedSSHHostKey(status, first+" host@example", now)
	if status["sshHostKey"] != first {
		t.Fatalf("recorded key changed on a matching report: %v", status["sshHostKey"])
	}
	c = findStatusCondition(status, edgeapi.ConnectionConditionSSHHostKeyChanged)
	if c == nil || c["status"] != string(metav1.ConditionFalse) {
		t.Fatalf("condition after matching report = %v, want False", c)
	}
}

func TestSSHHostKeyForPrefersSpecPin(t *testing.T) {
	edge := &sshEdgeView{}
	edge.Status.SSHHostKey = "ssh-ed25519 AAAAstatus"

	key, policy := sshHostKeyFor(edge)
	if key != "ssh-ed25519 AAAAstatus" || policy != edgeapi.SSHHostKeyPolicyStrict {
		t.Fatalf("status key / default policy = %q / %q", key, policy)
	}

	edge.Spec.SSHHostKey = " ssh-ed25519 AAAApinned \n"
	edge.Spec.SSHHostKeyPolicy = edgeapi.SSHHostKeyPolicyTOFU
	key, policy = sshHostKeyFor(edge)
	if key != "ssh-ed25519 AAAApinned" {
		t.Fatalf("spec pin did not win over status: %q", key)
	}
	if policy != edgeapi.SSHHostKeyPolicyTOFU {
		t.Fatalf("policy = %q, want tofu", policy)
	}
}
