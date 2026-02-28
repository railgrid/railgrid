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

package ssh

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	gossh "golang.org/x/crypto/ssh"
	"k8s.io/klog/v2"
)

// testSSHServer is a minimal SSH server for testing session teardown.
type testSSHServer struct {
	listener net.Listener
	port     int
}

func newTestSSHServer(t *testing.T) *testSSHServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("creating signer: %v", err)
	}

	cfg := &gossh.ServerConfig{
		NoClientAuth: true,
		PasswordCallback: func(_ gossh.ConnMetadata, _ []byte) (*gossh.Permissions, error) {
			return nil, nil
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	srv := &testSSHServer{listener: ln, port: ln.Addr().(*net.TCPAddr).Port}
	go srv.serve(cfg)
	return srv
}

func (s *testSSHServer) stop() { s.listener.Close() } //nolint:errcheck

func (s *testSSHServer) serve(cfg *gossh.ServerConfig) {
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(c, cfg)
	}
}

func (s *testSSHServer) handleConn(c net.Conn, cfg *gossh.ServerConfig) {
	sc, chans, reqs, err := gossh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer sc.Close() //nolint:errcheck
	go gossh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(gossh.UnknownChannelType, "unsupported") //nolint:errcheck
			continue
		}
		ch, reqs2, err := newChan.Accept()
		if err != nil {
			return
		}
		go s.handleSession(ch, reqs2)
	}
}

func (s *testSSHServer) handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	defer ch.Close() //nolint:errcheck

	for req := range reqs {
		switch req.Type {
		case "pty-req":
			req.Reply(true, nil) //nolint:errcheck
		case "shell":
			req.Reply(true, nil) //nolint:errcheck
			cmd := exec.Command("/bin/sh")
			// Use StdinPipe so cmd.Wait does not block on the stdin copier.
			// The goroutine below exits when ch is closed (deferred above).
			stdinPipe, err := cmd.StdinPipe()
			if err != nil {
				return
			}
			cmd.Stdout = ch
			cmd.Stderr = ch.Stderr()

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
		default:
			if req.WantReply {
				req.Reply(false, nil) //nolint:errcheck
			}
		}
	}
}

// TestSSHSessionTeardown verifies that SocketSSHSession.Run returns promptly
// when the remote shell exits (i.e., no deadlock in session teardown).
func TestSSHSessionTeardown(t *testing.T) {
	srv := newTestSSHServer(t)
	defer srv.stop()

	// Create SSH client.
	sshCfg := &gossh.ClientConfig{
		User:            "test",
		Auth:            []gossh.AuthMethod{gossh.Password("")},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec
	}
	sshClient, err := gossh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", srv.port), sshCfg)
	if err != nil {
		t.Fatalf("dialling SSH server: %v", err)
	}
	defer sshClient.Close() //nolint:errcheck

	// Create a WebSocket server/client pair (in-process).
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	var serverConn *websocket.Conn
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		serverConn, err = upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrading: %v", err)
		}
	}))
	defer wsServer.Close()

	wsURL := "ws" + wsServer.URL[4:] // http → ws
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dialling WS: %v", err)
	}
	defer clientConn.Close() //nolint:errcheck

	// Wait until the handler has upgraded.
	time.Sleep(50 * time.Millisecond)
	if serverConn == nil {
		t.Fatal("server WebSocket conn not set")
	}

	// Create the SocketSSHSession using the server-side WS connection.
	logger := klog.NewKlogr()
	session, err := NewSocketSSHSession(logger, 80, 24, sshClient, serverConn)
	if err != nil {
		t.Fatalf("creating SSH session: %v", err)
	}

	// Run the session in the background.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx)
		session.Close()
	}()

	// Give the shell a moment to start, then send a command that exits.
	time.Sleep(200 * time.Millisecond)

	cmdMsg, _ := json.Marshal(wsMsg{
		Type: wsMsgCmd,
		Cmd:  base64.StdEncoding.EncodeToString([]byte("exit 0\n")),
	})
	if err := clientConn.WriteMessage(websocket.TextMessage, cmdMsg); err != nil {
		t.Fatalf("writing cmd: %v", err)
	}

	// Drain client output until the WebSocket closes (server closes it after flush).
	for {
		if err := clientConn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			break
		}
		_, _, err := clientConn.ReadMessage()
		if err != nil {
			break // WebSocket closed — session is done
		}
	}

	// The session should complete well within 10 s.
	select {
	case err := <-done:
		if err != nil {
			t.Logf("session.Run returned error (acceptable): %v", err)
		}
		t.Log("SSH session torn down cleanly ✓")
	case <-time.After(8 * time.Second):
		t.Fatal("DEADLOCK: session.Run did not return within 8s after shell exit")
	}
}
