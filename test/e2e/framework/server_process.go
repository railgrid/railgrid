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
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTestSSHPort is the port used by the embedded test SSH server.
	// High enough to be unprivileged and unlikely to conflict on CI runners.
	DefaultTestSSHPort = 2222
)

// ServerProcess runs a kedge server-mode agent as a local subprocess together
// with an embedded test SSH server. This replaces the Docker-based
// ServerContainer and avoids all external SSH daemon configuration issues.
type ServerProcess struct {
	// ServerName is the kedge Server resource name to register on the hub.
	ServerName string
	// HubURL is the URL of the kedge hub (base URL, no /clusters/ path).
	// Used only when HubKubeconfig is empty.
	HubURL string
	// HubKubeconfig is the path to a kubeconfig whose server URL contains the
	// kcp workspace cluster path (e.g. https://hub:8443/clusters/abc123).
	// When set the agent uses --hub-kubeconfig instead of --hub-url so that the
	// cluster name is correctly derived from the URL.  Always set this in e2e
	// tests to avoid the cluster-name mismatch bug with static tokens.
	HubKubeconfig string
	// Token is the bearer token for the agent.
	Token string
	// AgentBin is the path to the kedge binary.
	AgentBin string
	// SSHPort is the port for the embedded test SSH server. Defaults to
	// DefaultTestSSHPort if zero.
	SSHPort int

	sshServer *TestSSHServer
	agentCmd  *exec.Cmd
	logBuf    *safeLogBuffer
	cancel    context.CancelFunc
}

// Start launches the embedded SSH server and the agent subprocess.
func (s *ServerProcess) Start(ctx context.Context) error {
	port := s.SSHPort
	if port == 0 {
		port = DefaultTestSSHPort
	}

	// 1. Start the embedded test SSH server.
	s.sshServer = NewTestSSHServer(port)
	if err := s.sshServer.Start(ctx); err != nil {
		return fmt.Errorf("starting test SSH server on port %d: %w", port, err)
	}

	// 2. Start the agent in server mode, pointing it at our test SSH server.
	agentCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.logBuf = &safeLogBuffer{}

	// Build args: prefer --hub-kubeconfig (cluster-scoped URL) over
	// --hub-url + --token so the agent can derive the correct kcp cluster
	// name from the kubeconfig's server URL.
	args := []string{
		"agent", "join",
		"--type=server",
		"--edge-name=" + s.ServerName,
		"--hub-insecure-skip-tls-verify",
		fmt.Sprintf("--ssh-proxy-port=%d", port),
	}
	if s.HubKubeconfig != "" {
		args = append(args,
			"--hub-kubeconfig="+s.HubKubeconfig,
			"--tunnel-url="+DefaultHubURL,
		)
	} else {
		args = append(args,
			"--hub-url="+s.HubURL,
			"--token="+s.Token,
		)
	}

	s.agentCmd = exec.CommandContext(agentCtx, s.AgentBin, args...)
	s.agentCmd.Stdout = s.logBuf
	s.agentCmd.Stderr = s.logBuf

	if err := s.agentCmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("starting agent subprocess: %w", err)
	}

	return nil
}

// Stop kills the agent subprocess and shuts down the SSH server.
func (s *ServerProcess) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.sshServer != nil {
		s.sshServer.Stop()
	}
}

// Logs returns the combined stdout+stderr of the agent subprocess.
func (s *ServerProcess) Logs() string {
	if s.logBuf == nil {
		return ""
	}
	return s.logBuf.String()
}

// WaitForAgentReady polls until the agent has started AND the revdial tunnel
// is connected (i.e. the hub can reach back to this agent for SSH sessions).
func (s *ServerProcess) WaitForAgentReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		logs := s.Logs()
		if strings.Contains(logs, "Agent started successfully") &&
			strings.Contains(logs, "Tunnel connection established") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("agent/tunnel did not become ready within %s; logs:\n%s", timeout, s.Logs())
}

// safeLogBuffer is a goroutine-safe bytes.Buffer.
type safeLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// serverProcessKey is the context key for ServerProcess.
type serverProcessKey struct{}

// WithServerProcess stores a ServerProcess in the context.
func WithServerProcess(ctx context.Context, p *ServerProcess) context.Context {
	return context.WithValue(ctx, serverProcessKey{}, p)
}

// ServerProcessFromContext retrieves a ServerProcess from the context.
func ServerProcessFromContext(ctx context.Context) (*ServerProcess, bool) {
	v, ok := ctx.Value(serverProcessKey{}).(*ServerProcess)
	return v, ok && v != nil
}
