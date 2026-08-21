// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	producttelemetry "github.com/faroshq/provider-sdk/telemetry"
)

const testProviderToken = "provider-service-account-token"

func writeProviderKubeconfig(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "provider.kubeconfig")
	contents := `apiVersion: v1
kind: Config
clusters:
- name: hub
  cluster:
    server: https://hub.example.invalid
contexts:
- name: provider
  context:
    cluster: hub
    user: provider
current-context: provider
users:
- name: provider
  user:
    token: ` + token + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProductTelemetryDisabledByDefault(t *testing.T) {
	t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "")
	t.Setenv("FAROS_HUB_URL", "https://hub.example.invalid")
	t.Setenv("FAROS_HUB_TOKEN", "heartbeat-token")
	t.Setenv("FAROS_PROVIDER_KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	tracker := newProductTelemetryTracker()
	if _, ok := tracker.(producttelemetry.NoopTracker); !ok {
		t.Fatalf("default tracker = %T, want telemetry.NoopTracker", tracker)
	}
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProviderTelemetryUsesKubeconfigTokenNotHeartbeatToken(t *testing.T) {
	kubeconfig := writeProviderKubeconfig(t, testProviderToken)
	t.Setenv("FAROS_PROVIDER_KUBECONFIG", kubeconfig)
	t.Setenv("FAROS_HUB_TOKEN", "heartbeat-token")
	if got := providerTelemetryToken(); got != testProviderToken {
		t.Fatalf("providerTelemetryToken() = %q, want kubeconfig token", got)
	}
}

func TestProductTelemetryStartupDoesNotLogTokens(t *testing.T) {
	kubeconfig := writeProviderKubeconfig(t, testProviderToken)
	t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "true")
	t.Setenv("FAROS_HUB_URL", "") // force construction failure without a network call
	t.Setenv("FAROS_HUB_TOKEN", "heartbeat-token")
	t.Setenv("FAROS_PROVIDER_KUBECONFIG", kubeconfig)

	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	tracker := newProductTelemetryTracker()
	if _, ok := tracker.(producttelemetry.NoopTracker); !ok {
		t.Fatalf("invalid enabled configuration tracker = %T, want NoopTracker", tracker)
	}
	if got := logs.String(); strings.Contains(got, testProviderToken) || strings.Contains(got, "heartbeat-token") {
		t.Fatalf("telemetry startup log leaked a token: %q", got)
	}
}

func TestProductTelemetryExplicitEnableRecognizesOnlyTrue(t *testing.T) {
	for _, value := range []string{"", "false", "1", "yes"} {
		t.Run("disabled_"+value, func(t *testing.T) {
			t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", value)
			if productTelemetryEnabled() {
				t.Fatalf("productTelemetryEnabled(%q) = true", value)
			}
		})
	}
	t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "true")
	if !productTelemetryEnabled() {
		t.Fatal("productTelemetryEnabled(true) = false")
	}
}
