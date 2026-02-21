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
)

// Agent manages a kedge-agent process for e2e tests.
type Agent struct {
	bin             string
	workDir         string
	hubKubeconfig   string
	agentKubeconfig string
	siteName        string
	cmd             *exec.Cmd
	cancel          context.CancelFunc
}

// NewAgent creates a new Agent.
func NewAgent(workDir, hubKubeconfig, agentKubeconfig, siteName string) *Agent {
	return &Agent{
		bin:             filepath.Join(workDir, "bin/kedge-agent"),
		workDir:         workDir,
		hubKubeconfig:   hubKubeconfig,
		agentKubeconfig: agentKubeconfig,
		siteName:        siteName,
	}
}

// Start launches the kedge-agent process. It runs until Stop is called or the
// parent context is cancelled.
func (a *Agent) Start(ctx context.Context) error {
	agentCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	args := []string{
		"--hub-kubeconfig", a.hubKubeconfig,
		"--kubeconfig", a.agentKubeconfig,
		"--tunnel-url", DefaultHubURL,
		"--site-name", a.siteName,
	}

	cmd := exec.CommandContext(agentCtx, a.bin, args...)
	cmd.Dir = a.workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	a.cmd = cmd

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start kedge-agent: %w", err)
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
