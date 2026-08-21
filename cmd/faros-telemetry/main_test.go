// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTLSFilesFromEnvRequiresBothOrNeither(t *testing.T) {
	tests := []struct {
		name     string
		certFile string
		keyFile  string
		wantErr  bool
	}{
		{name: "neither", wantErr: false},
		{name: "certificate only", certFile: "/tls/tls.crt", wantErr: true},
		{name: "key only", keyFile: "/tls/tls.key", wantErr: true},
		{name: "both", certFile: "/tls/tls.crt", keyFile: "/tls/tls.key", wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TELEMETRY_TLS_CERT_FILE", tt.certFile)
			t.Setenv("TELEMETRY_TLS_KEY_FILE", tt.keyFile)

			certFile, keyFile, err := tlsFilesFromEnv()
			if (err != nil) != tt.wantErr {
				t.Fatalf("tlsFilesFromEnv() error = %v, wantErr %t", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if certFile != tt.certFile || keyFile != tt.keyFile {
				t.Fatalf("tlsFilesFromEnv() = (%q, %q), want (%q, %q)", certFile, keyFile, tt.certFile, tt.keyFile)
			}
		})
	}
}

func TestServeHTTPUsesConfiguredTLS(t *testing.T) {
	certFile, keyFile := writeTestTLSFiles(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- serveHTTPOnListener(server, listener, certFile, keyFile) }()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // this test uses a generated certificate without a trusted CA.
	url := "https://" + listener.Addr().String()
	var response *http.Response
	var requestErr error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, requestErr = client.Get(url)
		if requestErr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if requestErr != nil {
		_ = server.Close()
		t.Fatalf("HTTPS request failed: %v", requestErr)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("HTTPS status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serveHTTPOnListener() error = %v, want %v", err, http.ErrServerClosed)
	}
}

func writeTestTLSFiles(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certFile := filepath.Join(directory, "tls.crt")
	keyFile := filepath.Join(directory, "tls.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0o600); err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
