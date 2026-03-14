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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Agent manages a kedge-agent process for e2e tests.
type Agent struct {
	bin             string
	workDir         string
	hubKubeconfig   string
	agentKubeconfig string
	edgeName        string
	labels          map[string]string
	cmd             *exec.Cmd
	cancel          context.CancelFunc
}

// NewAgent creates a new Agent.
func NewAgent(workDir, hubKubeconfig, agentKubeconfig, edgeName string) *Agent {
	return &Agent{
		bin:             filepath.Join(workDir, KedgeBin),
		workDir:         workDir,
		hubKubeconfig:   hubKubeconfig,
		agentKubeconfig: agentKubeconfig,
		edgeName:        edgeName,
	}
}

// WithLabels sets site labels the agent will report when registering.
func (a *Agent) WithLabels(labels map[string]string) *Agent {
	a.labels = labels
	return a
}

// Start launches the kedge agent run process. It runs until Stop is called or
// the parent context is cancelled.
func (a *Agent) Start(ctx context.Context) error {
	agentCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	args := []string{
		"agent", "run",
		"--hub-kubeconfig", a.hubKubeconfig,
		"--kubeconfig", a.agentKubeconfig,
		"--tunnel-url", DefaultHubURL,
		"--edge-name", a.edgeName,
		"--hub-insecure-skip-tls-verify",
	}
	if len(a.labels) > 0 {
		var pairs []string
		for k, v := range a.labels {
			pairs = append(pairs, k+"="+v)
		}
		args = append(args, "--labels", strings.Join(pairs, ","))
	}

	cmd := exec.CommandContext(agentCtx, a.bin, args...)
	cmd.Dir = a.workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	a.cmd = cmd

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start kedge agent run: %w", err)
	}

	// Reap the process in the background when context is cancelled.
	go func() {
		_ = cmd.Wait()
	}()

	return nil
}

// Stop terminates the agent process.
func (a *Agent) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

// TokenAgent manages a kedge agent run process authenticated via a bootstrap
// join token instead of a SA-backed hub kubeconfig.
type TokenAgent struct {
	bin             string
	workDir         string
	hubURL          string
	edgeName        string
	token           string
	clusterName     string
	agentKubeconfig string
	agentType       string
	sshUser         string
	sshPassword     string
	cmd             *exec.Cmd
	cancel          context.CancelFunc
}

// NewAgentWithToken creates a TokenAgent that connects to the hub using a bootstrap
// join token (kedge agent run --token). Use agentKubeconfig="" for server-type edges
// that have no downstream Kubernetes cluster.
func NewAgentWithToken(workDir, hubURL, edgeName, token string) *TokenAgent {
	return &TokenAgent{
		bin:       filepath.Join(workDir, KedgeBin),
		workDir:   workDir,
		hubURL:    hubURL,
		edgeName:  edgeName,
		token:     token,
		agentType: "server",
	}
}

// WithAgentKubeconfig sets the kubeconfig for the downstream Kubernetes cluster.
// For server-type edges this is not required.
func (a *TokenAgent) WithAgentKubeconfig(kc string) *TokenAgent {
	a.agentKubeconfig = kc
	return a
}

// WithType overrides the edge type (default "server").
func (a *TokenAgent) WithType(t string) *TokenAgent {
	a.agentType = t
	return a
}

// WithCluster sets the kcp logical cluster name so the agent connects to the
// correct workspace on the hub.  Required when using a join token because the
// token alone does not carry cluster information.
func (a *TokenAgent) WithCluster(clusterName string) *TokenAgent {
	a.clusterName = clusterName
	return a
}

// WithSSHUser sets the SSH username the agent reports to the hub via
// X-Kedge-SSH-User WebSocket header (join-token mode) or the --ssh-user flag.
func (a *TokenAgent) WithSSHUser(user string) *TokenAgent {
	a.sshUser = user
	return a
}

// WithSSHPassword sets the SSH password the agent reports to the hub via
// X-Kedge-SSH-Password WebSocket header (join-token mode) or the --ssh-password flag.
func (a *TokenAgent) WithSSHPassword(pass string) *TokenAgent {
	a.sshPassword = pass
	return a
}

// Start launches the kedge agent run process with the configured join token.
// It runs until Stop is called or the parent context is cancelled.
// When token is empty (e.g. NewReconnectAgent), no --token flag is passed and
// the binary auto-discovers the saved kubeconfig from ~/.kedge/.
func (a *TokenAgent) Start(ctx context.Context) error {
	agentCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	args := []string{
		"agent", "run",
		"--hub-url", a.hubURL,
		"--edge-name", a.edgeName,
		"--hub-insecure-skip-tls-verify",
		"--type", a.agentType,
	}
	// Only pass --token when one is configured; omitting it lets the binary
	// auto-detect a previously saved kubeconfig (reconnect-after-restart flow).
	if a.token != "" {
		args = append(args, "--token", a.token)
	}
	if a.agentKubeconfig != "" {
		args = append(args, "--kubeconfig", a.agentKubeconfig)
	}
	if a.clusterName != "" {
		args = append(args, "--cluster", a.clusterName)
	}
	if a.sshUser != "" {
		args = append(args, "--ssh-user", a.sshUser)
	}
	if a.sshPassword != "" {
		args = append(args, "--ssh-password", a.sshPassword)
	}

	cmd := exec.CommandContext(agentCtx, a.bin, args...)
	cmd.Dir = a.workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	a.cmd = cmd

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start kedge agent run: %w", err)
	}

	// Reap the process in the background when context is cancelled.
	go func() {
		_ = cmd.Wait()
	}()

	return nil
}

// Stop terminates the token agent process.
func (a *TokenAgent) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

// NewReconnectAgent creates a TokenAgent that reconnects to the hub using the
// previously saved kubeconfig (written during the first successful join-token
// exchange). No --token is passed — the binary's built-in auto-detection reads
// the saved kubeconfig from ~/.kedge/agent-<edgeName>.kubeconfig.
//
// Use this to verify the reconnect-after-restart flow end-to-end.
func NewReconnectAgent(workDir, hubURL, edgeName string) *TokenAgent {
	return &TokenAgent{
		bin:       filepath.Join(workDir, KedgeBin),
		workDir:   workDir,
		hubURL:    hubURL,
		edgeName:  edgeName,
		agentType: "server",
		// token intentionally empty — agent auto-detects saved kubeconfig
	}
}

// AgentSavedKubeconfigPath returns the filesystem path where the agent binary
// persists the kubeconfig received via token-exchange.  It mirrors the logic in
// pkg/agent.AgentKubeconfigPath so tests can verify the file was written.
func AgentSavedKubeconfigPath(edgeName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home dir: %w", err)
	}
	return filepath.Join(home, ".kedge", "agent-"+edgeName+".kubeconfig"), nil
}

// WaitForAgentSavedKubeconfig polls until the saved kubeconfig file appears at
// the expected path, or until timeout expires.
func WaitForAgentSavedKubeconfig(ctx context.Context, edgeName string, timeout time.Duration) (string, error) {
	path, err := AgentSavedKubeconfigPath(edgeName)
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return "", fmt.Errorf("saved kubeconfig for edge %q not found at %s within %s", edgeName, path, timeout)
}
