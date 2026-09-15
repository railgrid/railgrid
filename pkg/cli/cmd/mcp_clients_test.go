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

package cmd

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writePEM(t *testing.T, der []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeTLS(t *testing.T) {
	// httptest's certificate is self-signed for example.com.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	empty := x509.NewCertPool()

	trust, err := probeTLS(addr, "example.com", empty, "")
	if err != nil || trust != trustNone {
		t.Fatalf("untrusted certificate: trust=%v err=%v, want trustNone", trust, err)
	}

	caFile := writePEM(t, srv.Certificate().Raw)
	trust, err = probeTLS(addr, "example.com", empty, caFile)
	if err != nil || trust != trustCA {
		t.Fatalf("with its CA: trust=%v err=%v, want trustCA", trust, err)
	}

	system := x509.NewCertPool()
	system.AddCert(srv.Certificate())
	if trust, err = probeTLS(addr, "example.com", system, ""); err != nil || trust != trustSystem {
		t.Fatalf("system-trusted: trust=%v err=%v, want trustSystem", trust, err)
	}

	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer other.Close()
	wrong := writePEM(t, other.Certificate().Raw)
	if _, err := probeTLS(addr, "example.com", empty, wrong); err == nil || !strings.Contains(err.Error(), "does not verify") {
		// Both httptest servers share one certificate, so only assert when
		// they differ.
		if !reflect.DeepEqual(other.Certificate().Raw, srv.Certificate().Raw) {
			t.Fatalf("wrong CA must be reported, got %v", err)
		}
	}

	if _, err := probeTLS("127.0.0.1:1", "example.com", empty, ""); err == nil {
		t.Fatal("a closed port is a connection error, not an untrusted certificate")
	}
}

func TestClientMCPArgs(t *testing.T) {
	ep := &mcpEndpoint{URL: "https://hub.example:9443/services/mcpserver/c1/apis/railgrid.ai/v1alpha1/mcpservers/default/mcp", Token: "tok"}

	remove, add := claudeMCPArgs("railgrid-default", "user", ep)
	if !reflect.DeepEqual(remove, []string{"mcp", "remove", "railgrid-default", "--scope", "user"}) {
		t.Errorf("claude remove = %v", remove)
	}
	if !reflect.DeepEqual(add, []string{"mcp", "add", "--transport", "http", "--scope", "user", "railgrid-default", ep.URL, "--header", "Authorization: Bearer tok"}) {
		t.Errorf("claude add = %v", add)
	}

	remove, add = codexMCPArgs("railgrid-default", ep)
	if !reflect.DeepEqual(remove, []string{"mcp", "remove", "railgrid-default"}) {
		t.Errorf("codex remove = %v", remove)
	}
	if !reflect.DeepEqual(add, []string{"mcp", "add", "railgrid-default", "--url", ep.URL, "--bearer-token-env-var", codexTokenEnvVar}) {
		t.Errorf("codex add = %v", add)
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin([]string{"mcp", "add", "--header", "Authorization: Bearer a'b", "https://h:1/x"})
	want := `mcp add --header 'Authorization: Bearer a'\''b' https://h:1/x`
	if got != want {
		t.Fatalf("shellJoin = %s, want %s", got, want)
	}
}

func TestWriteCABundle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	system := filepath.Join(t.TempDir(), "system.pem")
	if err := os.WriteFile(system, []byte("SYSTEM-ROOTS"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", system)
	ca := filepath.Join(t.TempDir(), "hub-ca.pem")
	if err := os.WriteFile(ca, []byte("HUB-CA\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	path, err := writeCABundle("https://console.127.0.0.1.sslip.io:9443/mcp", ca)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".railgrid", "ca", "console.127.0.0.1.sslip.io.pem"); path != want {
		t.Errorf("bundle path = %s, want %s", path, want)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "SYSTEM-ROOTS\nHUB-CA\n" {
		t.Errorf("bundle = %q, want the system roots followed by the hub CA", b)
	}
}

func TestMCPClientServerName(t *testing.T) {
	if got := (&mcpClientOptions{mcpserverName: "default"}).serverName(); got != "railgrid-default" {
		t.Errorf("default name = %s", got)
	}
	if got := (&mcpClientOptions{mcpserverName: "default", name: "hub"}).serverName(); got != "hub" {
		t.Errorf("--name = %s", got)
	}
}
