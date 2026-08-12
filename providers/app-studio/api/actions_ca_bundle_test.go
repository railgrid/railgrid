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

package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testActionsCABundle(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "faros test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create test CA certificate: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestLoadActionsCABundleFromEnv(t *testing.T) {
	bundle := testActionsCABundle(t)
	t.Run("unset", func(t *testing.T) {
		t.Setenv("FAROS_ACTIONS_CA_BUNDLE_FILE", "")
		t.Setenv("FAROS_ACTIONS_CA_BUNDLE", "")
		got, err := loadActionsCABundleFromEnv()
		if err != nil || got != "" {
			t.Fatalf("load unset = %q, %v; want empty/nil", got, err)
		}
	})
	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(path, []byte("\r\n"+bundle+"\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("FAROS_ACTIONS_CA_BUNDLE_FILE", path)
		t.Setenv("FAROS_ACTIONS_CA_BUNDLE", "")
		got, err := loadActionsCABundleFromEnv()
		if err != nil || got != strings.TrimSpace(bundle) {
			t.Fatalf("load file = %q, %v; want normalized certificate/nil", got, err)
		}
	})
	t.Run("matching direct value", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(path, []byte(bundle), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("FAROS_ACTIONS_CA_BUNDLE_FILE", path)
		t.Setenv("FAROS_ACTIONS_CA_BUNDLE", "\n"+bundle+"\n")
		got, err := loadActionsCABundleFromEnv()
		if err != nil || got != strings.TrimSpace(bundle) {
			t.Fatalf("load matching values = %q, %v; want normalized certificate/nil", got, err)
		}
	})
	t.Run("mismatched sources", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(path, []byte(bundle), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("FAROS_ACTIONS_CA_BUNDLE_FILE", path)
		t.Setenv("FAROS_ACTIONS_CA_BUNDLE", "not-the-file")
		if _, err := loadActionsCABundleFromEnv(); err == nil || !strings.Contains(err.Error(), "disagree") {
			t.Fatalf("mismatched sources error = %v, want disagreement", err)
		}
	})
	t.Run("invalid direct value", func(t *testing.T) {
		t.Setenv("FAROS_ACTIONS_CA_BUNDLE_FILE", "")
		t.Setenv("FAROS_ACTIONS_CA_BUNDLE", "not-pem")
		if _, err := loadActionsCABundleFromEnv(); err == nil || !strings.Contains(err.Error(), "no PEM certificates") {
			t.Fatalf("invalid bundle error = %v, want PEM validation", err)
		}
	})
}
