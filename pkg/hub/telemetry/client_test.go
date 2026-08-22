/*
Copyright 2026 The Faros Authors.

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

package telemetry

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewHTTPClientWithCAFileLeavesDefaultTransportForEmptyPath(t *testing.T) {
	client, err := NewHTTPClientWithCAFile("")
	if err != nil {
		t.Fatalf("NewHTTPClientWithCAFile(\"\") error = %v", err)
	}
	if client != nil {
		t.Fatal("empty CA path returned a client; public-CA behavior should use the default client")
	}
}

func TestNewHTTPClientWithCAFileRejectsUnreadableAndMalformedFiles(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.pem")
	if _, err := NewHTTPClientWithCAFile(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreadable CA error = %v, want os.ErrNotExist", err)
	}

	malformed := filepath.Join(t.TempDir(), "malformed.pem")
	if err := os.WriteFile(malformed, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewHTTPClientWithCAFile(malformed)
	if err == nil || !strings.Contains(err.Error(), "contains no valid PEM certificates") {
		t.Fatalf("malformed CA error = %v, want explicit PEM validation error", err)
	}
}

func TestNewHTTPClientWithCAFileAppendsCertificateToDedicatedTransport(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "telemetry-test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Minute),
		IsCA:                  true,
		DNSNames:              []string{"telemetry-test-ca"},
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	caFile := filepath.Join(t.TempDir(), "receiver-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	client, err := NewHTTPClientWithCAFile(caFile)
	if err != nil {
		t.Fatalf("NewHTTPClientWithCAFile() error = %v", err)
	}
	if client == nil {
		t.Fatal("CA file returned a nil client")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport = %T, want *http.Transport", client.Transport)
	}
	if transport == http.DefaultTransport {
		t.Fatal("CA client reused http.DefaultTransport")
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs == nil {
		t.Fatal("CA client did not install a dedicated RootCAs pool")
	}

	// Verify the supplied self-signed certificate against the configured roots
	// with server authentication. Since it is not a system root, successful
	// verification proves the PEM file was appended to the TLS trust pool.
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     transport.TLSClientConfig.RootCAs,
		DNSName:   "telemetry-test-ca",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("appended receiver CA does not establish trust: %v", err)
	}
}
