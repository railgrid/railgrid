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
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// The install e2e suites execute the hack/install/ scripts VERBATIM — the same
// scripts docs/install-external-kcp.md and docs/install-embedded-kcp.md quote
// step by step. That is the point: the suites keep the installation guides
// honest. Do not re-implement an install step here; fix the script (and the
// doc) instead.

const (
	// DefaultInstallClusterName is the kind cluster the install suites create.
	// Distinct from the docs default ("faros") so a developer's manual
	// walkthrough and an e2e run never fight over the same cluster.
	DefaultInstallClusterName = "faros-e2e-install"

	// InstallStateDirName is the state directory (extracted kubeconfigs,
	// port-forward pidfiles) the install suites pass to the scripts.
	InstallStateDirName = ".faros-install-e2e"

	// InstallGatewayAddr is the local address of the Envoy gateway
	// port-forward started by hack/install/port-forward.sh.
	InstallGatewayAddr = "127.0.0.1:8443"

	// InstallHubURL matches the hack/install default HUB_EXTERNAL_URL. Plain
	// localhost (not faros.localhost): *.localhost subdomains don't resolve on
	// stock macOS, and the install flow should run anywhere the docs do.
	InstallHubURL = "https://localhost:9443"
)

// InstallStateDir returns the state directory used by the install suites.
func InstallStateDir(repoRoot string) string {
	return filepath.Join(repoRoot, InstallStateDirName)
}

// installScriptEnv assembles the environment for a hack/install script run.
// Image overrides flow from the same FAROS_HUB_IMAGE* variables the other e2e
// suites use (set by the Makefile targets after docker build).
func installScriptEnv(repoRoot string) []string {
	stateDir := InstallStateDir(repoRoot)
	e := append(os.Environ(),
		"FAROS_INSTALL_CLUSTER="+DefaultInstallClusterName,
		"FAROS_INSTALL_STATE_DIR="+stateDir,
		"FAROS_STATIC_TOKEN="+DevToken,
	)
	if image := os.Getenv(hubImageEnv); image != "" {
		e = append(e, "HUB_IMAGE="+image, "HUB_KIND_LOAD=true")
	}
	if tag := os.Getenv(hubImageTagEnv); tag != "" {
		e = append(e, "HUB_IMAGE_TAG="+tag)
	}
	if policy := os.Getenv(hubImagePullPolicyEnv); policy != "" {
		e = append(e, "HUB_IMAGE_PULL_POLICY="+policy)
	}
	return e
}

// RunInstallScript executes one hack/install/<name> script, streaming output.
func RunInstallScript(ctx context.Context, repoRoot, name string) error {
	script := filepath.Join(repoRoot, "hack", "install", name)
	fmt.Printf("--- running %s\n", script)
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Dir = repoRoot
	cmd.Env = installScriptEnv(repoRoot)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install script %s failed: %w", name, err)
	}
	return nil
}

// StartInstallPortForwards (re)starts the gateway + hub port-forwards.
func StartInstallPortForwards(ctx context.Context, repoRoot string) error {
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(repoRoot, "hack", "install", "port-forward.sh"), "start")
	cmd.Dir = repoRoot
	cmd.Env = installScriptEnv(repoRoot)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// SetupInstallFlow returns an env.Func that runs the given hack/install
// scripts in order, starts the port-forwards, waits for the hub and tenant
// API, and stores a ClusterEnv. Pass the exact script list a doc prescribes,
// e.g. external: 01,02,03,04,05,06,07 — embedded: 01,03,08.
func SetupInstallFlow(repoRoot string, scripts []string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		for _, s := range scripts {
			if err := RunInstallScript(ctx, repoRoot, s); err != nil {
				return ctx, err
			}
		}

		if err := StartInstallPortForwards(ctx, repoRoot); err != nil {
			return ctx, fmt.Errorf("starting port-forwards: %w", err)
		}

		stateDir := InstallStateDir(repoRoot)
		clusterEnv := &ClusterEnv{
			HubClusterName: DefaultInstallClusterName,
			HubKubeconfig:  filepath.Join(stateDir, "hub.kubeconfig"),
			HubURL:         InstallHubURL,
			Token:          DevToken,
			WorkDir:        repoRoot,
			KCPKubeconfig:  filepath.Join(stateDir, "kcp-frontproxy.kubeconfig"),
		}

		healthCtx, healthCancel := context.WithTimeout(ctx, 5*time.Minute)
		defer healthCancel()
		if err := WaitForHubReady(healthCtx, clusterEnv.HubURL); err != nil {
			return ctx, fmt.Errorf("hub did not become healthy after install: %w", err)
		}

		client := NewFarosClient(repoRoot, clusterEnv.HubKubeconfig, clusterEnv.HubURL)
		apiCtx, apiCancel := context.WithTimeout(ctx, 5*time.Minute)
		defer apiCancel()
		if err := WaitForTenantAPI(apiCtx, client, clusterEnv.HubURL, clusterEnv.Token); err != nil {
			return ctx, fmt.Errorf("tenant API did not become available after install: %w", err)
		}

		return WithClusterEnv(ctx, clusterEnv), nil
	}
}

// TeardownInstallFlow deletes the install kind cluster and state via
// hack/install/teardown.sh, honouring --keep-clusters.
func TeardownInstallFlow(repoRoot string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		if KeepClusters {
			fmt.Println("--keep-clusters set: skipping install teardown")
			return ctx, nil
		}
		cmd := exec.CommandContext(ctx, "bash", filepath.Join(repoRoot, "hack", "install", "teardown.sh"))
		cmd.Dir = repoRoot
		cmd.Env = installScriptEnv(repoRoot)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("WARNING: install teardown failed (cluster may remain): %v\n", err)
		}
		return ctx, nil
	}
}

// GatewayGet performs an HTTPS GET through the local Envoy gateway
// port-forward, dialing InstallGatewayAddr while presenting the given SNI
// hostname — exactly what a client resolving <host> to the gateway would do.
// Returns the HTTP status code.
func GatewayGet(ctx context.Context, host, path string) (int, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: 10 * time.Second}
			return d.DialContext(ctx, network, InstallGatewayAddr)
		},
		TLSClientConfig: &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true, //nolint:gosec // self-signed dev certs
		},
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}
