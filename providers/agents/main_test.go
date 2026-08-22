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
	"context"
	"encoding/base64"
	"encoding/pem"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	producttelemetry "github.com/faroshq/provider-sdk/telemetry"
)

const testProviderToken = "provider-service-account-token"

func writeProviderKubeconfig(t *testing.T, token string) string {
	return writeProviderKubeconfigWithCA(t, token, nil, "")
}

func writeProviderKubeconfigWithCA(t *testing.T, token string, caData []byte, caFile string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "provider.kubeconfig")
	clusterCA := ""
	if caFile != "" {
		clusterCA = "    certificate-authority: " + caFile + "\n"
	} else if len(caData) > 0 {
		clusterCA = "    certificate-authority-data: " + base64.StdEncoding.EncodeToString(caData) + "\n"
	}
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
	contents = strings.Replace(contents, "    server: https://hub.example.invalid\n", "    server: https://hub.example.invalid\n"+clusterCA, 1)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProductTelemetryUsesProviderKubeconfigCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})

	for _, tc := range []struct {
		name   string
		caFile bool
	}{
		{name: "data"},
		{name: "file", caFile: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var kubeconfig string
			if tc.caFile {
				caPath := filepath.Join(t.TempDir(), "hub-ca.pem")
				if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
					t.Fatal(err)
				}
				kubeconfig = writeProviderKubeconfigWithCA(t, testProviderToken, nil, caPath)
			} else {
				kubeconfig = writeProviderKubeconfigWithCA(t, testProviderToken, caPEM, "")
			}
			t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "true")
			t.Setenv("FAROS_HUB_URL", server.URL)
			t.Setenv("FAROS_PROVIDER_KUBECONFIG", kubeconfig)

			tracker := newProductTelemetryTracker()
			if _, ok := tracker.(producttelemetry.NoopTracker); ok {
				t.Fatal("private-CA telemetry unexpectedly disabled")
			}
			if err := tracker.Track(context.Background(), producttelemetry.Event{Action: "agent_created"}); err != nil {
				t.Fatalf("Track() error = %v", err)
			}
			if err := tracker.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		})
	}
}

func TestProductTelemetryInvalidKubeconfigCAFallsBackToNoop(t *testing.T) {
	serverCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serverCalls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	kubeconfig := writeProviderKubeconfigWithCA(t, testProviderToken, []byte("not a certificate"), "")
	t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "true")
	t.Setenv("FAROS_HUB_URL", server.URL)
	t.Setenv("FAROS_PROVIDER_KUBECONFIG", kubeconfig)

	tracker := newProductTelemetryTracker()
	if _, ok := tracker.(producttelemetry.NoopTracker); !ok {
		t.Fatalf("invalid configured CA tracker = %T, want NoopTracker", tracker)
	}
	if err := tracker.Track(context.Background(), producttelemetry.Event{Action: "agent_created"}); err != nil {
		t.Fatalf("noop Track() error = %v", err)
	}
	if serverCalls != 0 {
		t.Fatalf("invalid configured CA made %d telemetry calls", serverCalls)
	}
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
