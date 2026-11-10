/*
Copyright 2026 The Railgrid Authors.

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

package hubclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

// privateCAHub is a TLS httptest server whose certificate chains to nothing
// in the system roots, the way a self-hosted hub on its in-cluster Service
// name does. It returns the server and its certificate as PEM.
func privateCAHub(t *testing.T, handler http.Handler) (*httptest.Server, []byte) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	cert := srv.Certificate()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	return srv, pemBytes
}

func kubeconfigWithCA(t *testing.T, server string, ca []byte) string {
	t.Helper()
	content := strings.Join([]string{
		"apiVersion: v1",
		"kind: Config",
		"current-context: provider",
		"contexts:",
		"- name: provider",
		"  context: {cluster: hub, user: provider}",
		"clusters:",
		"- name: hub",
		"  cluster:",
		"    server: " + server,
		"    certificate-authority-data: " + base64.StdEncoding.EncodeToString(ca),
		"users:",
		"- name: provider",
		"  user: {token: sa-token-from-kubeconfig}",
		"",
	}, "\n")
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearHubTrustEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnvHubCAFile, "")
	t.Setenv(EnvHubCAData, "")
	t.Setenv(EnvProviderKubeconfig, "")
}

func TestHubCAPrefersFileThenDataThenKubeconfig(t *testing.T) {
	clearHubTrustEnv(t)
	if ca, err := HubCA(); err != nil || ca != nil {
		t.Fatalf("nothing configured: ca=%q err=%v, want nil, nil", ca, err)
	}

	t.Setenv(EnvProviderKubeconfig, kubeconfigWithCA(t, "https://hub.example.test", []byte("kubeconfig-ca")))
	if ca, err := HubCA(); err != nil || string(ca) != "kubeconfig-ca" {
		t.Fatalf("kubeconfig: ca=%q err=%v", ca, err)
	}

	t.Setenv(EnvHubCAData, "inline-ca")
	if ca, err := HubCA(); err != nil || string(ca) != "inline-ca" {
		t.Fatalf("inline data must win over the kubeconfig: ca=%q err=%v", ca, err)
	}

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, []byte("file-ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvHubCAFile, caFile)
	if ca, err := HubCA(); err != nil || string(ca) != "file-ca" {
		t.Fatalf("file must win over inline data: ca=%q err=%v", ca, err)
	}

	t.Setenv(EnvHubCAFile, filepath.Join(t.TempDir(), "missing.pem"))
	if _, err := HubCA(); err == nil {
		t.Fatal("an unreadable RAILGRID_HUB_CA_FILE must be an error, not a silent fallback")
	}
}

func TestHubCAFromKubeconfigWithoutCAIsNil(t *testing.T) {
	clearHubTrustEnv(t)
	t.Setenv(EnvProviderKubeconfig, writeKubeconfig(t, kubeconfigWithToken))
	ca, err := HubCA()
	if err != nil || ca != nil {
		t.Fatalf("dev-mode kubeconfig without a CA: ca=%q err=%v, want nil, nil", ca, err)
	}
}

func TestHubTLSConfigTrustsTheKubeconfigCAAndTheSystemRoots(t *testing.T) {
	clearHubTrustEnv(t)
	srv, ca := privateCAHub(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	// Without the CA the private hub is rejected, proving the server really is
	// outside the system roots.
	before, err := HubTransport(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&http.Client{Transport: before}).Get(srv.URL); err == nil {
		t.Fatal("hub with a private CA was accepted before any CA was configured")
	}

	t.Setenv(EnvProviderKubeconfig, kubeconfigWithCA(t, srv.URL, ca))
	transport, err := HubTransport(false)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: transport}).Get(srv.URL)
	if err != nil {
		t.Fatalf("hub verified with the kubeconfig CA: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// The pool is the system pool plus the hub CA, not the hub CA alone.
	sys, sysErr := x509.SystemCertPool()
	if sysErr == nil && sys != nil && transport.TLSClientConfig.RootCAs.Equal(x509.NewCertPool()) {
		t.Fatal("RootCAs lost the system roots")
	}
	if transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x", transport.TLSClientConfig.MinVersion)
	}

	t.Setenv(EnvHubCAData, "not a certificate")
	if _, err := HubTLSConfig(false); err == nil {
		t.Fatal("a bundle with no PEM certificate must be an error")
	}
}

func TestHubTLSConfigInsecureSkipsVerification(t *testing.T) {
	clearHubTrustEnv(t)
	cfg, err := HubTLSConfig(true)
	if err != nil || !cfg.InsecureSkipVerify {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestRunHeartbeatVerifiesAPrivateCAHubThroughTheKubeconfig(t *testing.T) {
	clearHubTrustEnv(t)
	beats := make(chan string, 8)
	srv, ca := privateCAHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beats <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Setenv(EnvProviderKubeconfig, kubeconfigWithCA(t, srv.URL, ca))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunHeartbeat(ctx, HeartbeatConfig{
		HubURL:       srv.URL,
		ProviderName: "quickstart",
		Token:        "sa-token",
		Interval:     time.Hour,
		CanSend:      func() bool { return true },
	})
	select {
	case auth := <-beats:
		if auth != "Bearer sa-token" {
			t.Fatalf("auth = %q", auth)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no beat reached the private-CA hub; the heartbeat did not pick up the kubeconfig CA")
	}
}

func TestApplyHubTLS(t *testing.T) {
	clearHubTrustEnv(t)
	srv, ca := privateCAHub(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	t.Run("nothing configured leaves the config alone", func(t *testing.T) {
		cfg := &rest.Config{Host: srv.URL}
		if err := ApplyHubTLS(cfg, false); err != nil {
			t.Fatal(err)
		}
		if cfg.Transport != nil || cfg.Insecure {
			t.Fatalf("cfg changed: %+v", cfg)
		}
	})
	t.Run("insecure sets the flag", func(t *testing.T) {
		cfg := &rest.Config{Host: srv.URL}
		if err := ApplyHubTLS(cfg, true); err != nil {
			t.Fatal(err)
		}
		if !cfg.Insecure {
			t.Fatal("Insecure not set")
		}
	})
	t.Run("explicit CA on the config wins", func(t *testing.T) {
		t.Setenv(EnvHubCAData, string(ca))
		cfg := &rest.Config{Host: srv.URL, TLSClientConfig: rest.TLSClientConfig{CAData: []byte("caller")}}
		if err := ApplyHubTLS(cfg, false); err != nil {
			t.Fatal(err)
		}
		if cfg.Transport != nil || string(cfg.CAData) != "caller" {
			t.Fatalf("caller's CA replaced: %+v", cfg)
		}
	})
	t.Run("hub CA becomes the transport and the bearer still applies", func(t *testing.T) {
		t.Setenv(EnvProviderKubeconfig, kubeconfigWithCA(t, srv.URL, ca))
		cfg := &rest.Config{Host: srv.URL, BearerToken: "tok"}
		if err := ApplyHubTLS(cfg, false); err != nil {
			t.Fatal(err)
		}
		if cfg.Transport == nil {
			t.Fatal("Transport not set")
		}
		client, err := rest.HTTPClientFor(cfg)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("private-CA hub through rest.Config: %v", err)
		}
		_ = resp.Body.Close()
		again := &rest.Config{Host: srv.URL}
		if err := ApplyHubTLS(again, false); err != nil {
			t.Fatal(err)
		}
		if again.Transport != cfg.Transport {
			t.Fatal("rest.Configs do not share the hub transport")
		}
	})
}
