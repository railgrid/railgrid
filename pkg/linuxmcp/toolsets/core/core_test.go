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

package core

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/faroshq/faros-kedge/pkg/linuxmcp"
)

// runProcess runs `/bin/sh -c cmd` and returns its full stdout, stderr, and
// exit code.  Reading to memory keeps the SSH test server simple and avoids
// the pipe-vs-goroutine race the prior implementation hit under CI load.
// Safe for tests where output is small; not suitable for production servers.
func runProcess(cmd string) (stdout, stderr []byte, exitCode int) {
	c := exec.Command("/bin/sh", "-c", cmd)
	var outBuf, errBuf strings.Builder
	c.Stdout = &outBuf
	c.Stderr = &errBuf
	err := c.Run()
	stdout = []byte(outBuf.String())
	stderr = []byte(errBuf.String())
	if ee, ok := err.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
	} else if err != nil {
		exitCode = 1
	}
	return stdout, stderr, exitCode
}

// Bring up a tiny SSH server that shells out to /bin/sh, point a Provider at
// it, and exercise read_file end-to-end on a real file in a temp dir.
//
// This intentionally mirrors the sshexec test fixture rather than depending
// on test/e2e/framework — keeps unit tests self-contained.

func startTestSSH(t *testing.T) (addr string, stop func()) {
	t.Helper()
	hostKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	cfg := &gossh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go serveConn(conn, cfg)
		}
	}()
	return l.Addr().String(), func() { _ = l.Close() }
}

func serveConn(c net.Conn, cfg *gossh.ServerConfig) {
	sc, chans, reqs, err := gossh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer sc.Close() //nolint:errcheck
	go gossh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(gossh.UnknownChannelType, "")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go serveSession(ch, chReqs)
	}
}

func serveSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	defer ch.Close() //nolint:errcheck
	for req := range reqs {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		if len(req.Payload) < 4 {
			_ = req.Reply(false, nil)
			return
		}
		cmd := string(req.Payload[4:])
		_ = req.Reply(true, nil)
		// Read stdout/stderr into memory FIRST (before c.Wait), then
		// write to the SSH channel synchronously.  The previous
		// goroutine-based io.Copy was flaky under CI: cmd.Wait() closes
		// the parent's read ends of the pipes the moment the process
		// exits, which on a loaded scheduler can race the io.Copy
		// goroutines and leave the SSH channel buffer empty.  Reading
		// everything up front is safe for a test server (commands have
		// trivial output) and removes the race entirely.
		outBytes, errBytes, exit := runProcess(cmd)
		_, _ = ch.Write(outBytes)
		_, _ = ch.Stderr().Write(errBytes)
		_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Code uint32 }{uint32(exit)}))
		return
	}
}

func providerFor(addr string) *linuxmcp.Provider {
	return linuxmcp.NewProvider(linuxmcp.Config{
		Cluster:        "root",
		EdgeNames:      []string{"test-edge"},
		CommandTimeout: 5 * time.Second,
		MaxOutputBytes: 1 << 16,
		OpenSession: func(_ context.Context, _ string) (*gossh.Client, error) {
			cfg := &gossh.ClientConfig{
				User:            "test",
				Auth:            []gossh.AuthMethod{gossh.Password("")},
				HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec
				Timeout:         2 * time.Second,
			}
			return gossh.Dial("tcp", addr, cfg)
		},
	})
}

func TestReadFile_Roundtrip(t *testing.T) {
	addr, stop := startTestSSH(t)
	defer stop()

	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	want := []byte("hello\nworld\n")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	p := providerFor(addr)
	h := readFileHandler(p)
	_, out, err := h(context.Background(), nil, ReadFileInput{Path: path})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if out.SizeBytes != len(want) {
		t.Errorf("SizeBytes: got %d, want %d", out.SizeBytes, len(want))
	}
	got, err := base64.StdEncoding.DecodeString(out.ContentB64)
	if err != nil {
		t.Fatalf("decode contentB64: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("content: got %q, want %q", got, want)
	}
}

func TestRunCommand_StdoutStderrSeparated(t *testing.T) {
	addr, stop := startTestSSH(t)
	defer stop()

	p := providerFor(addr)
	h := runCommandHandler(p)
	_, out, err := h(context.Background(), nil, RunCommandInput{
		Command: "echo OUT; echo ERR >&2; exit 3",
	})
	if err != nil {
		t.Fatalf("run_command: %v", err)
	}
	if out.ExitCode != 3 {
		t.Errorf("ExitCode: got %d, want 3", out.ExitCode)
	}
	if !strings.Contains(out.Stdout, "OUT") {
		t.Errorf("Stdout: got %q, want to contain OUT", out.Stdout)
	}
	if !strings.Contains(out.Stderr, "ERR") {
		t.Errorf("Stderr: got %q, want to contain ERR", out.Stderr)
	}
}
