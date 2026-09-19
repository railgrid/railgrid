/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeKubeconfig drops a minimal but valid kubeconfig on disk and returns its
// path. The server URL identifies which file a resolution picked.
func writeKubeconfig(t *testing.T, name, server string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	content := `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: ` + server + `
contexts:
- name: c
  context:
    cluster: c
    user: u
current-context: c
users:
- name: u
  user:
    token: t
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

// clearProviderKubeconfigEnv unsets every variable serve ever consulted — the
// standardized name it still reads and the retired provider-specific ones — so
// a developer's own environment cannot leak into the test.
func clearProviderKubeconfigEnv(t *testing.T) {
	t.Helper()
	for _, env := range []string{
		providerKubeconfigEnv,
		"INFRASTRUCTURE_KUBECONFIG",
		"INFRASTRUCTURE_CONTROLLER_KUBECONFIG",
		"KUBECONFIG",
		"INFRASTRUCTURE_WORKSPACE_PATH",
	} {
		t.Setenv(env, "")
	}
}

// The charts set RAILGRID_PROVIDER_KUBECONFIG on the serve container and
// nothing else, and that is now the only name serve honors.
func TestLoadControllerConfigReadsStandardizedName(t *testing.T) {
	clearProviderKubeconfigEnv(t)
	want := "https://kcp.example/clusters/root:railgrid:providers:infrastructure"
	t.Setenv(providerKubeconfigEnv, writeKubeconfig(t, "provider", want))

	cfg, err := loadControllerConfig()
	if err != nil {
		t.Fatalf("loadControllerConfig: %v", err)
	}
	if cfg.Host != want {
		t.Errorf("Host = %q, want %q", cfg.Host, want)
	}
}

// The point of PR 1: serve has no second-choice credential. Every retired
// source — the operator's own name, the legacy override, a stray KUBECONFIG,
// and (implicitly) the in-cluster ServiceAccount — must fail rather than let
// serve run with something `init` did not mint for it.
func TestLoadControllerConfigRejectsRetiredSources(t *testing.T) {
	for _, env := range []string{
		"INFRASTRUCTURE_KUBECONFIG",
		"INFRASTRUCTURE_CONTROLLER_KUBECONFIG",
		"KUBECONFIG",
	} {
		t.Run(env, func(t *testing.T) {
			clearProviderKubeconfigEnv(t)
			t.Setenv(env, writeKubeconfig(t, "retired", "https://retired.example"))

			cfg, err := loadControllerConfig()
			if err == nil {
				t.Fatalf("loadControllerConfig accepted %s (host=%s); serve must require %s", env, cfg.Host, providerKubeconfigEnv)
			}
			if !strings.Contains(err.Error(), providerKubeconfigEnv) {
				t.Errorf("error = %q, want it to name %s", err, providerKubeconfigEnv)
			}
		})
	}
}

// A root-scoped kubeconfig used to be usable for serve by pointing
// INFRASTRUCTURE_WORKSPACE_PATH at the provider workspace. That hint is gone:
// the variable must have no effect on the host serve connects to.
func TestLoadControllerConfigIgnoresWorkspacePathHint(t *testing.T) {
	clearProviderKubeconfigEnv(t)
	t.Setenv(providerKubeconfigEnv, writeKubeconfig(t, "root", "https://kcp.example/clusters/root"))
	t.Setenv("INFRASTRUCTURE_WORKSPACE_PATH", "root:railgrid:providers:infrastructure")

	cfg, err := loadControllerConfig()
	if err != nil {
		t.Fatalf("loadControllerConfig: %v", err)
	}
	if cfg.Host != "https://kcp.example/clusters/root" {
		t.Errorf("Host = %q — INFRASTRUCTURE_WORKSPACE_PATH still retargets serve", cfg.Host)
	}
}

// With nothing configured the caller gets an actionable error naming the
// variable and the command that produces its value, not a config pointing
// somewhere arbitrary.
func TestLoadControllerConfigFailsWithoutProviderKubeconfig(t *testing.T) {
	clearProviderKubeconfigEnv(t)

	_, err := loadControllerConfig()
	if err == nil {
		t.Fatal("loadControllerConfig succeeded with no provider kubeconfig")
	}
	if !strings.Contains(err.Error(), providerKubeconfigEnv) || !strings.Contains(err.Error(), "init") {
		t.Errorf("error = %q, want it to name %s and `init`", err, providerKubeconfigEnv)
	}
}
