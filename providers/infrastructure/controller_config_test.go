/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"os"
	"path/filepath"
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

// clearControllerKubeconfigEnv unsets every variable the resolver consults so a
// developer's own environment cannot leak into the test.
func clearControllerKubeconfigEnv(t *testing.T) {
	t.Helper()
	for _, env := range controllerKubeconfigEnvs {
		t.Setenv(env, "")
	}
	t.Setenv("INFRASTRUCTURE_WORKSPACE_PATH", "")
}

// The regression: the charts set FAROS_PROVIDER_KUBECONFIG on the serve
// container and nothing else. When it was not consulted, serve fell through to
// the in-cluster ServiceAccount and pointed every kcp controller at the HOST
// cluster — which surfaced as leases in "default" being forbidden rather than
// as a missing kubeconfig.
func TestLoadControllerConfigHonorsStandardizedName(t *testing.T) {
	clearControllerKubeconfigEnv(t)
	t.Setenv("FAROS_PROVIDER_KUBECONFIG", writeKubeconfig(t, "provider", "https://kcp.example/clusters/root:faros:providers:infrastructure"))

	cfg, source, err := loadControllerConfigRaw()
	if err != nil {
		t.Fatalf("loadControllerConfigRaw: %v", err)
	}
	if source != "FAROS_PROVIDER_KUBECONFIG" {
		t.Errorf("source = %q, want FAROS_PROVIDER_KUBECONFIG", source)
	}
	if cfg.Host != "https://kcp.example/clusters/root:faros:providers:infrastructure" {
		t.Errorf("Host = %q — resolved the wrong kubeconfig", cfg.Host)
	}
}

// The operator sets only INFRASTRUCTURE_KUBECONFIG, so that path must keep
// working; and the standardized name must win when both are present.
func TestLoadControllerConfigResolutionOrder(t *testing.T) {
	standardized := writeKubeconfig(t, "standardized", "https://standardized.example")
	operator := writeKubeconfig(t, "operator", "https://operator.example")
	legacy := writeKubeconfig(t, "legacy", "https://legacy.example")

	for _, tc := range []struct {
		name       string
		env        map[string]string
		wantSource string
		wantHost   string
	}{{
		name:       "operator-only still works",
		env:        map[string]string{"INFRASTRUCTURE_KUBECONFIG": operator},
		wantSource: "INFRASTRUCTURE_KUBECONFIG",
		wantHost:   "https://operator.example",
	}, {
		name: "standardized wins over the provider-specific names",
		env: map[string]string{
			"FAROS_PROVIDER_KUBECONFIG":            standardized,
			"INFRASTRUCTURE_KUBECONFIG":            operator,
			"INFRASTRUCTURE_CONTROLLER_KUBECONFIG": legacy,
		},
		wantSource: "FAROS_PROVIDER_KUBECONFIG",
		wantHost:   "https://standardized.example",
	}, {
		name:       "legacy override is still an escape hatch",
		env:        map[string]string{"INFRASTRUCTURE_CONTROLLER_KUBECONFIG": legacy},
		wantSource: "INFRASTRUCTURE_CONTROLLER_KUBECONFIG",
		wantHost:   "https://legacy.example",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			clearControllerKubeconfigEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			cfg, source, err := loadControllerConfigRaw()
			if err != nil {
				t.Fatalf("loadControllerConfigRaw: %v", err)
			}
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
			if cfg.Host != tc.wantHost {
				t.Errorf("Host = %q, want %q", cfg.Host, tc.wantHost)
			}
		})
	}
}

// Outside a pod, with nothing configured, the caller must get the sentinel it
// checks for rather than a config pointing somewhere arbitrary.
func TestLoadControllerConfigDisabledWithoutAnySource(t *testing.T) {
	clearControllerKubeconfigEnv(t)
	// rest.InClusterConfig keys off these; unset they yield ErrNotInCluster.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	if _, _, err := loadControllerConfigRaw(); err != errControllerDisabled { //nolint:errorlint // sentinel is returned directly
		t.Fatalf("err = %v, want errControllerDisabled", err)
	}
}
