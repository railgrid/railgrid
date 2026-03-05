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
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"

	gossh "golang.org/x/crypto/ssh"
)

// TestSSHServer is a minimal embedded SSH server for e2e tests.
// It accepts any authentication by default (security is provided by the revdial
// tunnel that already authenticated the caller) and executes commands via
// exec.Command. It is not safe for production use.
//
// To restrict authentication to specific users/keys, call AddUser or
// AddAnyUserKey before Start.
type TestSSHServer struct {
	Port int

	// mu protects all mutable fields below.
	mu sync.Mutex

	// Auth configuration — set before calling Start.
	// If neither userKeys nor anyUserKeys are configured, NoClientAuth=true
	// (original behaviour).
	userKeys    map[string][][]byte // username -> list of authorised public key marshalled bytes
	anyUserKeys [][]byte            // public key marshalled bytes accepted for any username

	// connectedUsers is appended to on each successful connection.
	connectedUsers []string

	listener net.Listener
}

// NewTestSSHServer creates a TestSSHServer bound to the given port.
func NewTestSSHServer(port int) *TestSSHServer {
	return &TestSSHServer{Port: port}
}

// AddUser configures the server to accept the given public key for the given
// username.  May be called multiple times to add multiple users or multiple
// keys per user.  When at least one call to AddUser or AddAnyUserKey has been
// made, NoClientAuth is disabled and only the configured credentials are
// accepted.
func (s *TestSSHServer) AddUser(username string, pubKey gossh.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userKeys == nil {
		s.userKeys = make(map[string][][]byte)
	}
	s.userKeys[username] = append(s.userKeys[username], pubKey.Marshal())
}

// AddAnyUserKey configures the server to accept the given public key for any
// username.  Useful for SSHUserMappingIdentity tests where the username is
// determined at runtime.
func (s *TestSSHServer) AddAnyUserKey(pubKey gossh.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.anyUserKeys = append(s.anyUserKeys, pubKey.Marshal())
}

// ConnectedUsers returns a snapshot of usernames that have authenticated
// since the server started.  Safe to call concurrently with Start.
func (s *TestSSHServer) ConnectedUsers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]string, len(s.connectedUsers))
	copy(result, s.connectedUsers)
	return result
}

// Start starts the SSH server and returns once it is listening.
func (s *TestSSHServer) Start(ctx context.Context) error {
	hostKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generating host key: %w", err)
	}
	signer, err := gossh.NewSignerFromKey(hostKey)
	if err != nil {
		return fmt.Errorf("creating signer: %w", err)
	}

	s.mu.Lock()
	noAuth := len(s.userKeys) == 0 && len(s.anyUserKeys) == 0
	s.mu.Unlock()

	config := &gossh.ServerConfig{}
	if noAuth {
		// Original behaviour: accept any authentication.
		config.NoClientAuth = true
		config.PasswordCallback = func(_ gossh.ConnMetadata, _ []byte) (*gossh.Permissions, error) {
			return nil, nil
		}
		config.PublicKeyCallback = func(_ gossh.ConnMetadata, _ gossh.PublicKey) (*gossh.Permissions, error) {
			return nil, nil
		}
	} else {
		// Enforce public-key auth with the configured users/keys.
		config.PublicKeyCallback = func(conn gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			keyBytes := key.Marshal()

			s.mu.Lock()
			defer s.mu.Unlock()

			// Any-user keys: accepted regardless of username.
			for _, k := range s.anyUserKeys {
				if bytes.Equal(k, keyBytes) {
					return nil, nil
				}
			}
			// User-specific keys.
			if accepted, ok := s.userKeys[conn.User()]; ok {
				for _, k := range accepted {
					if bytes.Equal(k, keyBytes) {
						return nil, nil
					}
				}
			}
			return nil, fmt.Errorf("unauthorized: no matching key for user %q", conn.User())
		}
	}
	config.AddHostKey(signer)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.Port))
	if err != nil {
		return fmt.Errorf("listening on port %d: %w", s.Port, err)
	}
	s.listener = ln

	go s.serve(ctx, config)
	return nil
}

// Stop closes the listener.
func (s *TestSSHServer) Stop() {
	if s.listener != nil {
		s.listener.Close() //nolint:errcheck
	}
}

func (s *TestSSHServer) serve(ctx context.Context, config *gossh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handleConn(ctx, conn, config)
	}
}

func (s *TestSSHServer) handleConn(ctx context.Context, c net.Conn, config *gossh.ServerConfig) {
	serverConn, chans, reqs, err := gossh.NewServerConn(c, config)
	if err != nil {
		return
	}
	defer serverConn.Close() //nolint:errcheck

	// Track the connected username.
	username := serverConn.User()
	s.mu.Lock()
	s.connectedUsers = append(s.connectedUsers, username)
	s.mu.Unlock()

	go gossh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(gossh.UnknownChannelType, "unsupported channel type") //nolint:errcheck
			continue
		}
		ch, requests, err := newChan.Accept()
		if err != nil {
			return
		}
		go s.handleSession(ctx, ch, requests, username)
	}
}

func (s *TestSSHServer) handleSession(ctx context.Context, ch gossh.Channel, requests <-chan *gossh.Request, username string) {
	defer ch.Close() //nolint:errcheck

	for req := range requests {
		switch req.Type {
		case "exec":
			// Parse the command from the exec request payload.
			if len(req.Payload) < 4 {
				req.Reply(false, nil) //nolint:errcheck
				continue
			}
			cmdLen := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 |
				int(req.Payload[2])<<8 | int(req.Payload[3])
			if len(req.Payload) < 4+cmdLen {
				req.Reply(false, nil) //nolint:errcheck
				continue
			}
			cmdStr := string(req.Payload[4 : 4+cmdLen])
			req.Reply(true, nil) //nolint:errcheck

			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdStr)
			cmd.Env = s.buildEnv(username)
			cmd.Stdout = ch
			cmd.Stderr = ch.Stderr()
			if err := cmd.Run(); err != nil {
				// Send exit-status so the SSH client knows the command finished.
				exitCode := 1
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				}
				payload := gossh.Marshal(struct{ Code uint32 }{uint32(exitCode)})
				ch.SendRequest("exit-status", false, payload) //nolint:errcheck
			} else {
				payload := gossh.Marshal(struct{ Code uint32 }{0})
				ch.SendRequest("exit-status", false, payload) //nolint:errcheck
			}
			return

		case "shell":
			// For interactive shells, connect stdout directly to /bin/sh.
			// We use StdinPipe so that cmd.Wait() does NOT wait for stdin to be
			// drained — the io.Copy goroutine is cleaned up when ch is closed
			// by the deferred ch.Close() after cmd.Run returns.
			req.Reply(true, nil) //nolint:errcheck
			cmd := exec.CommandContext(ctx, "/bin/sh")
			cmd.Env = s.buildEnv(username)
			stdinPipe, err := cmd.StdinPipe()
			if err != nil {
				return
			}
			cmd.Stdout = ch
			cmd.Stderr = ch.Stderr()

			// Copy SSH channel input to the shell stdin asynchronously.
			// This goroutine exits when ch is closed (deferred below).
			go func() {
				io.Copy(stdinPipe, ch) //nolint:errcheck
				stdinPipe.Close()      //nolint:errcheck
			}()

			if err := cmd.Run(); err != nil {
				exitCode := 1
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				}
				payload := gossh.Marshal(struct{ Code uint32 }{uint32(exitCode)})
				ch.SendRequest("exit-status", false, payload) //nolint:errcheck
			} else {
				payload := gossh.Marshal(struct{ Code uint32 }{0})
				ch.SendRequest("exit-status", false, payload) //nolint:errcheck
			}
			return

		case "pty-req":
			// Accept PTY requests (needed for interactive sessions).
			req.Reply(true, nil) //nolint:errcheck

		case "window-change":
			req.Reply(false, nil) //nolint:errcheck

		default:
			if req.WantReply {
				req.Reply(false, nil) //nolint:errcheck
			}
		}
	}
}

// buildEnv returns an environment for spawned commands that includes the
// current process environment plus USER=<username> so that `echo $USER` and
// similar commands reflect the authenticated SSH username rather than the test
// runner's OS user.
func (s *TestSSHServer) buildEnv(username string) []string {
	env := os.Environ()
	result := make([]string, 0, len(env)+1)
	for _, e := range env {
		if len(e) > 5 && e[:5] == "USER=" {
			continue // will be overridden below
		}
		result = append(result, e)
	}
	result = append(result, "USER="+username)
	return result
}

// sshChannelStderr implements io.Writer for the channel stderr.
type sshChannelStderr struct{ ch gossh.Channel }

func (e *sshChannelStderr) Write(p []byte) (int, error) { return e.ch.Stderr().Write(p) }

// Ensure Channel.Stderr() is accessible (it returns an io.ReadWriter).
var _ io.Writer = &sshChannelStderr{}
