// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/providerrelay"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

func TestProviderRelayRequiresTrustedShardAndSignsBoundRoute(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	key, _ := providerrelay.SigningKey(root)
	path := "/clusters/consumer/apis/planner.example/v1/boards/demo/run"
	requests := make(chan *http.Request, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "provider-session=secret; Path=/")
		w.Header().Set("Clear-Site-Data", "*")
		w.Header().Set("Content-Security-Policy", "default-src *")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL + "/clusters/root")
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "edges", BackendURL: base, EndpointsValid: true})
	reg.Upsert(Provider{Name: "planner", OrgUUID: testOrg, CatalogEntryCluster: "catalog", EndpointsValid: true, EdgeRoute: &EdgeRoute{Cluster: "hosting", ServiceName: "provider-planner"}})
	proxy := NewBackendProxy(reg, logr.Discard())
	proxy.SetKCPFrontDoor(base, http.DefaultTransport)
	allowed := true
	h := &ProviderRelay{Registry: reg, Proxy: proxy, Keys: serviceaccounts.StaticProofKeySource(root), Authorize: func(_ context.Context, p Provider, r dataplane.SubresourceRequest) error {
		if !allowed {
			return fmt.Errorf("denied")
		}
		if p.OrgUUID != testOrg || r.ClusterID != "consumer" {
			t.Fatal("wrong authorization coordinates")
		}
		return nil
	}}
	endpoint, _ := ProviderDataPlaneURL(EmbeddedProviderRelayURL, "catalog", "planner")
	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, endpoint+path+"?component=main", nil)
		r.Header.Set(dataplane.HeaderRemoteUser, "alice")
		r.Header.Set(dataplane.HeaderRemoteGroup, "users")
		r.Header.Set("Authorization", "sensitive-hub-token")
		r.Header.Set(dataplane.HeaderUpstreamAuthorization, "spoofed")
		return r
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request())
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("public ingress status %d", w.Code)
	}
	client := &http.Client{Transport: EmbeddedRelayTransport(http.DefaultTransport, h), Timeout: 3 * time.Second}
	r := request()
	r.RequestURI = ""
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("relay status %d", resp.StatusCode)
	}
	for _, name := range []string{"Set-Cookie", "Clear-Site-Data"} {
		if value := resp.Header.Get(name); value != "" {
			t.Errorf("relay %s = %q, want stripped", name, value)
		}
	}
	if got := resp.Header.Get("Content-Security-Policy"); got != orgProviderContentSecurityPolicy {
		t.Errorf("relay Content-Security-Policy = %q, want forced policy", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("relay X-Content-Type-Options = %q, want nosniff", got)
	}
	out := <-requests
	if out.Header.Get("Authorization") != "" || out.Header.Get(dataplane.HeaderRemoteUser) != "" || out.Header.Get(dataplane.HeaderUpstreamAuthorization) != "" {
		t.Fatal("caller or hub credentials crossed relay hop")
	}
	wantPath := "/clusters/hosting/apis/edges.railgrid.ai/v1alpha1/services/provider-planner/proxy" + path
	if out.URL.Path != wantPath {
		t.Fatalf("hop path %s", out.URL.Path)
	}
	claims, err := providerrelay.Verify(key.Public().(ed25519.PublicKey), out.Header.Get(providerrelay.Header), "hosting", "provider-planner", "POST", path, "component=main", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claims.Identity.User != "alice" {
		t.Fatal("original caller lost")
	}
	allowed = false
	r = request()
	r.RequestURI = ""
	resp, err = client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign binding status %d", resp.StatusCode)
	}
	select {
	case <-requests:
		t.Fatal("denied request reached tunnel")
	default:
	}
}

func TestEmbeddedRelayStreamsAndUpgrades(t *testing.T) {
	t.Run("stream", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Context().Value(trustedShardRelay{}) != true {
				t.Error("not trusted")
			}
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
		client := &http.Client{Transport: EmbeddedRelayTransport(http.DefaultTransport, h), Timeout: time.Second}
		resp, err := client.Get(EmbeddedProviderRelayURL + ProviderRelayPrefix + "catalog/provider/stream")
		if err != nil {
			t.Fatalf("headers did not flush before completion: %v", err)
		}
		_ = resp.Body.Close()
	})
	t.Run("upgrade", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: relay-test\r\n\r\n")
			_ = rw.Flush()
			data := make([]byte, 4)
			if _, err := io.ReadFull(rw, data); err == nil {
				_, _ = conn.Write(data)
			}
		})
		client := &http.Client{Transport: EmbeddedRelayTransport(http.DefaultTransport, h)}
		r, _ := http.NewRequest(http.MethodGet, EmbeddedProviderRelayURL+ProviderRelayPrefix+"catalog/provider/upgrade", nil)
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "relay-test")
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 101 {
			t.Fatalf("upgrade status %d", resp.StatusCode)
		}
		rw, ok := resp.Body.(io.ReadWriteCloser)
		if !ok {
			t.Fatal("upgrade lost writable connection")
		}
		if _, err := rw.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, 4)
		if _, err := io.ReadFull(rw, reply); err != nil || string(reply) != "ping" {
			t.Fatalf("upgrade echo %q %v", reply, err)
		}
	})
}

func TestShardRelayRequiresExactTrustedCertificate(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "shard-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	certificate := func(name string, issuer *x509.Certificate, signer ed25519.PrivateKey) tls.Certificate {
		pub, key, _ := ed25519.GenerateKey(rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, pub, signer)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}
	config, err := ShardRelayTLSConfig(caFile, []string{"shard-1"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(AuthenticatedShardHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(trustedShardRelay{}) != true {
			t.Error("no authenticated marker")
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.TLS = config
	srv.StartTLS()
	defer srv.Close()
	_, untrustedKey, _ := ed25519.GenerateKey(rand.Reader)
	for _, tc := range []struct {
		name  string
		cert  *tls.Certificate
		allow bool
	}{{"none", nil, false}, {"wrong-ca", ptrCert(certificate("shard-1", ca, untrustedKey)), false}, {"wrong-name", ptrCert(certificate("other", ca, priv)), false}, {"valid", ptrCert(certificate("shard-1", ca, priv)), true}} {
		t.Run(tc.name, func(t *testing.T) {
			tr := srv.Client().Transport.(*http.Transport).Clone()
			tr.TLSClientConfig = tr.TLSClientConfig.Clone()
			if tc.cert != nil {
				tr.TLSClientConfig.Certificates = []tls.Certificate{*tc.cert}
			}
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
			resp, err := client.Get(srv.URL + ProviderRelayPrefix + "catalog/provider")
			if tc.allow {
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusNoContent {
					t.Fatal(resp.StatusCode)
				}
			} else if err == nil {
				_ = resp.Body.Close()
				t.Fatal("accepted untrusted certificate")
			}
		})
	}
}
func ptrCert(c tls.Certificate) *tls.Certificate { return &c }

func TestProviderRelayRejectsAmbiguousEndpoints(t *testing.T) {
	for _, base := range []string{"https://user:pass@hub", "https://hub/prefix", "https://hub?query=1", "https://hub#fragment", "file:///tmp/relay"} {
		if _, err := ProviderDataPlaneURL(base, "catalog", "planner"); err == nil {
			t.Errorf("accepted %s", base)
		}
	}
	for _, name := range []string{"../evil", "planner%2Fother", "Planner", "planner.other", strings.Repeat("x", 64)} {
		if _, err := ProviderDataPlaneURL("https://hub", "catalog", name); err == nil {
			t.Errorf("accepted provider %s", name)
		}
	}
}
