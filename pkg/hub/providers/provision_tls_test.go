// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func TestMintedProviderKubeconfigVerifiesHubTLS(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer provider-token" {
			t.Error("missing provider bearer")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, ca      string
		dev, succeeds bool
	}{
		{"untrusted rejected", "", false, false},
		{"private CA trusted", caFile, false, true},
		{"explicit dev mode", "", true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			provisioner := NewProvisioner(nil, WithProviderKubeconfigTLS(scenario.ca, scenario.dev))
			raw, err := provisioner.providerKubeconfig(server.URL, "provider-token")
			if err != nil {
				t.Fatal(err)
			}
			config, err := clientcmd.RESTConfigFromKubeConfig(raw)
			if err != nil {
				t.Fatal(err)
			}
			if config.Insecure != scenario.dev {
				t.Fatalf("insecure=%t", config.Insecure)
			}
			client, err := rest.HTTPClientFor(config)
			if err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
			response, err := client.Do(request)
			if response != nil {
				_ = response.Body.Close()
			}
			if (err == nil) != scenario.succeeds {
				t.Fatalf("request error=%v", err)
			}
			if !scenario.succeeds && calls.Load() != before {
				t.Fatal("untrusted endpoint received bearer")
			}
		})
	}
	if _, err := NewProvisioner(nil).providerKubeconfig("http://hub.example", "token"); err == nil {
		t.Fatal("plaintext production origin accepted")
	}
	if err := os.WriteFile(caFile, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewProvisioner(nil, WithProviderKubeconfigTLS(caFile, false)).providerKubeconfig(server.URL, "token"); err == nil {
		t.Fatal("invalid trust bundle accepted")
	}
}
