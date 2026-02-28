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
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

const (
	// ContainerSSHImage is the Docker image used for the container-based SSH
	// server test.  We use a plain Ubuntu image and install openssh-server at
	// container start time so that we have full control over the sshd
	// configuration (in particular: PermitRootLogin + PermitEmptyPasswords).
	ContainerSSHImage = "ubuntu:22.04"

	// ContainerSSHPort is the port sshd listens on inside the container.
	// We use 2222 to avoid conflicts with any host sshd on port 22.
	ContainerSSHPort = 2222
)

// ServerContainer manages a Docker container running lscr.io/linuxserver/openssh-server
// alongside a kedge server-mode agent.  The container runs with --network host so
// the agent can reach the hub at kedge.localhost:8443.
type ServerContainer struct {
	// Name is the Docker container name.
	Name string
	// ServerName is the kedge Server resource name to register on the hub.
	ServerName string
	// HubURL is the URL of the kedge hub, reachable from the runner's network.
	HubURL string
	// Token is the bearer token for the agent.
	Token string
	// AgentBin is the host path to the kedge binary.
	AgentBin string
}

// Start launches the container, waits for sshd, copies the agent, and starts it.
func (s *ServerContainer) Start(ctx context.Context) error {
	// 1. Start a plain Ubuntu container with --network host.
	//    We install openssh-server and configure it to allow root login with an
	//    empty password — the security boundary is the kedge tunnel auth, not sshd.
	sshdScript := strings.Join([]string{
		"apt-get update -q",
		"DEBIAN_FRONTEND=noninteractive apt-get install -y -q openssh-server",
		"mkdir -p /run/sshd",
		"passwd -d root",
		fmt.Sprintf(
			"printf 'Port %d\\nPermitRootLogin yes\\nPasswordAuthentication yes\\nPermitEmptyPasswords yes\\nUsePAM no\\n' > /etc/ssh/sshd_config",
			ContainerSSHPort),
		fmt.Sprintf("/usr/sbin/sshd -D -p %d", ContainerSSHPort),
	}, " && ")
	if _, err := runDockerCmd(ctx,
		"run", "-d",
		"--name", s.Name,
		"--network", "host",
		ContainerSSHImage,
		"sh", "-c", sshdScript,
	); err != nil {
		return fmt.Errorf("creating container %s: %w", s.Name, err)
	}

	// 2. Wait until sshd is accepting connections on ContainerSSHPort.
	//    apt-get + sshd startup takes a moment.
	if err := waitForTCPPort(ctx, ContainerSSHPort, 90*time.Second); err != nil {
		logs, _ := s.containerLogs(ctx)
		return fmt.Errorf("sshd not ready on port %d: %w\ncontainer logs:\n%s",
			ContainerSSHPort, err, logs)
	}

	// 3. Copy the kedge binary into the container.
	if _, err := runDockerCmd(ctx, "cp", s.AgentBin, s.Name+":/kedge"); err != nil {
		return fmt.Errorf("copying agent binary: %w", err)
	}

	// 4. Start the agent in server mode (background).
	// Use POSIX-compatible redirection (Ubuntu uses dash as /bin/sh, which does
	// not support bash-only &> syntax; use > file 2>&1 & instead).
	agentCmd := fmt.Sprintf(
		"/kedge agent join --mode=server --hub-url=%s --token=%s --site-name=%s"+
			" --hub-insecure-skip-tls-verify --ssh-proxy-port=%d"+
			" > /var/log/kedge-agent.log 2>&1 &",
		s.HubURL, s.Token, s.ServerName, ContainerSSHPort,
	)
	if _, err := s.exec(ctx, "sh", "-c", agentCmd); err != nil {
		return fmt.Errorf("starting agent: %w", err)
	}

	return nil
}

// Stop removes the container.
func (s *ServerContainer) Stop(ctx context.Context) error {
	_, _ = runDockerCmd(ctx, "rm", "-f", s.Name)
	return nil
}

// AgentLogs returns the agent log from inside the container.
func (s *ServerContainer) AgentLogs(ctx context.Context) (string, error) {
	return s.exec(ctx, "cat", "/var/log/kedge-agent.log")
}

// WaitForAgentReady polls until the agent log shows the tunnel is connected.
func (s *ServerContainer) WaitForAgentReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		logs, err := s.AgentLogs(ctx)
		if err == nil &&
			strings.Contains(logs, "Agent started successfully") &&
			strings.Contains(logs, "Tunnel connection established") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	logs, _ := s.AgentLogs(ctx)
	return fmt.Errorf("agent/tunnel in container %s not ready within %s; logs:\n%s",
		s.Name, timeout, logs)
}

func (s *ServerContainer) exec(ctx context.Context, cmd string, args ...string) (string, error) {
	return runDockerCmd(ctx, append([]string{"exec", s.Name, cmd}, args...)...)
}

func (s *ServerContainer) containerLogs(ctx context.Context) (string, error) {
	return runDockerCmd(ctx, "logs", s.Name)
}

// runDockerCmd runs a docker subcommand and returns combined output.
func runDockerCmd(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %w\noutput: %s",
			strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}

// waitForTCPPort polls until the given localhost port accepts connections.
func waitForTCPPort(ctx context.Context, port int, timeout time.Duration) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close() //nolint:errcheck
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("port %d not ready after %s", port, timeout)
}

// serverContainerKey is the context key for a ServerContainer.
type serverContainerKey struct{}

// WithServerContainer stores a ServerContainer in the context.
func WithServerContainer(ctx context.Context, c *ServerContainer) context.Context {
	return context.WithValue(ctx, serverContainerKey{}, c)
}

// ServerContainerFromContext retrieves a ServerContainer from the context.
func ServerContainerFromContext(ctx context.Context) (*ServerContainer, bool) {
	v, ok := ctx.Value(serverContainerKey{}).(*ServerContainer)
	return v, ok && v != nil
}
