// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	producttelemetry "github.com/faroshq/provider-sdk/telemetry"
	"k8s.io/client-go/rest"
)

func TestProductTelemetryUsesConfiguredHubCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})

	for _, tc := range []struct {
		name      string
		configure func(*testing.T, []byte)
		cfg       *rest.Config
	}{
		{
			name: "env-data",
			configure: func(t *testing.T, caPEM []byte) {
				t.Setenv("FAROS_HUB_CA_DATA", string(caPEM))
			},
		},
		{
			name: "env-file",
			configure: func(t *testing.T, caPEM []byte) {
				path := filepath.Join(t.TempDir(), "hub-ca.pem")
				if err := os.WriteFile(path, caPEM, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("FAROS_HUB_CA_FILE", path)
			},
		},
		{
			name: "provider-kubeconfig-data",
			cfg:  &rest.Config{TLSClientConfig: rest.TLSClientConfig{CAData: caPEM}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "true")
			t.Setenv("FAROS_HUB_CA_DATA", "")
			t.Setenv("FAROS_HUB_CA_FILE", "")
			if tc.configure != nil {
				tc.configure(t, caPEM)
			}

			tracker := newProductTelemetryTrackerWithConfig(server.URL, "provider-token", tc.cfg)
			if _, ok := tracker.(producttelemetry.NoopTracker); ok {
				t.Fatal("private-CA telemetry unexpectedly disabled")
			}
			if err := tracker.Track(context.Background(), producttelemetry.Event{Action: "edge_first_ready"}); err != nil {
				t.Fatalf("Track() error = %v", err)
			}
			if err := tracker.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		})
	}
}

func TestProductTelemetryInvalidConfiguredHubCAFallsBackToNoop(t *testing.T) {
	serverCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serverCalls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "true")
	t.Setenv("FAROS_HUB_CA_DATA", "not a certificate")
	t.Setenv("FAROS_HUB_CA_FILE", "")

	tracker := newProductTelemetryTracker(server.URL, "provider-token")
	if _, ok := tracker.(producttelemetry.NoopTracker); !ok {
		t.Fatalf("invalid configured CA tracker = %T, want NoopTracker", tracker)
	}
	if err := tracker.Track(context.Background(), producttelemetry.Event{Action: "edge_first_ready"}); err != nil {
		t.Fatalf("noop Track() error = %v", err)
	}
	if serverCalls != 0 {
		t.Fatalf("invalid configured CA made %d telemetry calls", serverCalls)
	}
}

func TestProductTelemetryHubCAEnvironmentOverridesKubeconfigCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "true")
	t.Setenv("FAROS_HUB_CA_DATA", string(caPEM))
	t.Setenv("FAROS_HUB_CA_FILE", "")

	for _, cfg := range []*rest.Config{
		{TLSClientConfig: rest.TLSClientConfig{CAData: []byte("not a certificate")}},
		{TLSClientConfig: rest.TLSClientConfig{CAFile: filepath.Join(t.TempDir(), "missing-ca.pem")}},
	} {
		// An explicit environment CA is authoritative when a mounted kubeconfig
		// carries stale, unrelated, or unreadable CA configuration.
		tracker := newProductTelemetryTrackerWithConfig(server.URL, "provider-token", cfg)
		if _, ok := tracker.(producttelemetry.NoopTracker); ok {
			t.Fatal("environment CA did not override kubeconfig CA")
		}
		if err := tracker.Track(context.Background(), producttelemetry.Event{Action: "edge_first_ready"}); err != nil {
			t.Fatalf("Track() error = %v", err)
		}
		if err := tracker.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}
}

func TestProductTelemetryDisabledMakesNoCalls(t *testing.T) {
	serverCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serverCalls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	t.Setenv("FAROS_PRODUCT_TELEMETRY_ENABLED", "")
	t.Setenv("FAROS_HUB_CA_DATA", "not a certificate")

	tracker := newProductTelemetryTracker(server.URL, "provider-token")
	if _, ok := tracker.(producttelemetry.NoopTracker); !ok {
		t.Fatalf("disabled tracker = %T, want NoopTracker", tracker)
	}
	if err := tracker.Track(context.Background(), producttelemetry.Event{Action: "edge_first_ready"}); err != nil {
		t.Fatalf("noop Track() error = %v", err)
	}
	if serverCalls != 0 {
		t.Fatalf("disabled telemetry made %d calls", serverCalls)
	}
}
