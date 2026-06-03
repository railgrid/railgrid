/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package infrastructure e2e-tests the tenant isolation guarantees of
// the infrastructure provider's REST surface. We start ONLY the
// provider binary (no hub, no kcp) in stub mode (KRO_KUBECONFIG unset)
// so the suite stays fast and self-contained — the isolation logic
// being tested lives in the provider, not in the hub's tenant
// resolver (which has its own suite under test/e2e/suites/provider).
//
// Tenant identity is asserted via the X-Kedge-Tenant header on each
// request, the same shape the hub backend proxy injects in production.
// Each test case picks a distinct tenant path and verifies that
// another tenant's calls can't read, list, or delete its instances.
package infrastructure

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

var (
	repoRoot     string
	providerURL  string // http://127.0.0.1:<port>
	providerPort = "18082"
)

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
	providerURL = "http://127.0.0.1:" + providerPort

	if portInUse(providerPort) {
		fmt.Fprintf(os.Stderr, "port :%s in use; pkill infrastructure-provider and retry\n", providerPort)
		os.Exit(2)
	}

	if err := build(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}

	dataDir, err := os.MkdirTemp("", "kedge-e2e-infrastructure-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	keepData := os.Getenv("KEDGE_E2E_KEEP_DATA") == "true"

	provLog, _ := os.Create(filepath.Join(dataDir, "provider.log"))
	provCmd := exec.Command(filepath.Join(repoRoot, "bin", "infrastructure-provider"))
	provCmd.Env = append(os.Environ(),
		"PORT="+providerPort,
		// No KRO_KUBECONFIG → stub mode. The stub buckets all CR
		// operations by tenantPath, so isolation can be tested
		// without standing up kind + kro.
		// No KEDGE_DEV_ALLOW_TENANT_QUERY → we MUST set X-Kedge-Tenant
		// on every request (no ?tenant= fallback), exactly like prod.
	)
	provCmd.Stdout = provLog
	provCmd.Stderr = provLog
	provCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := provCmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start provider:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "infrastructure-provider started (pid=%d, port=:%s, log=%s)\n",
		provCmd.Process.Pid, providerPort, provLog.Name())

	cleanup := func() {
		killGroup(provCmd)
		if !keepData {
			_ = os.RemoveAll(dataDir)
		} else {
			fmt.Fprintf(os.Stderr, "logs preserved under %s\n", dataDir)
		}
	}

	if err := waitReady(providerURL+"/healthz", 15*time.Second); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "provider never ready:", err)
		os.Exit(1)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}

func build(root string) error {
	cmd := exec.Command("make", "-C", root, "build-infrastructure-provider")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func portInUse(p string) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+p, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func killGroup(c *exec.Cmd) {
	if c == nil || c.Process == nil {
		return
	}
	_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	_, _ = c.Process.Wait()
}

func waitReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), "ok") {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s waiting for %s", timeout, url)
}

func ctxWithTimeout(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
