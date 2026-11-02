// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
)

// EmbeddedRelayTransport intercepts only the non-network embedded relay origin.
// net.Pipe retains net/http streaming and upgrade support without listening on
// any socket. All other shard destinations retain their original transport.
func EmbeddedRelayTransport(next http.RoundTripper, handler http.Handler) http.RoundTripper {
	inprocess := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		s := &http.Server{Handler: trustedProviderRelay(handler)}
		s.SetKeepAlivesEnabled(false)
		go func() { _ = s.Serve(&oneConnectionListener{conn: server}) }()
		return client, nil
	}}
	return relayRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme == "http" && r.URL.Host == "railgrid-provider-relay.internal" && r.URL.User == nil && strings.HasPrefix(r.URL.Path, ProviderRelayPrefix) {
			return inprocess.RoundTrip(r)
		}
		return next.RoundTrip(r)
	})
}

type relayRoundTripper func(*http.Request) (*http.Response, error)

func (f relayRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type oneConnectionListener struct{ conn net.Conn }

func (l *oneConnectionListener) Accept() (net.Conn, error) {
	if l.conn == nil {
		return nil, io.EOF
	}
	c := l.conn
	l.conn = nil
	return c, nil
}
func (l *oneConnectionListener) Close() error   { return nil }
func (l *oneConnectionListener) Addr() net.Addr { return relayAddr("in-process") }

type relayAddr string

func (a relayAddr) Network() string { return "pipe" }
func (a relayAddr) String() string  { return string(a) }

// ShardRelayTLSConfig authenticates only explicitly named shard certificates.
// It is used on a dedicated listener, never on the browser-facing listener.
func ShardRelayTLSConfig(caFile string, allowedNames []string) (*tls.Config, error) {
	if caFile == "" || len(allowedNames) == 0 {
		return nil, fmt.Errorf("provider relay requires a shard client CA and allowed certificate names")
	}
	raw, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("invalid shard client CA")
	}
	allowed := map[string]bool{}
	for _, name := range allowedNames {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("empty allowed shard certificate name")
		}
		allowed[name] = true
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 || !allowed[cs.PeerCertificates[0].Subject.CommonName] {
			return fmt.Errorf("untrusted shard certificate identity")
		}
		return nil
	}}, nil
}

// AuthenticatedShardHandler is safe only behind ShardRelayTLSConfig; it checks
// verified chains again so mounting this handler on ordinary HTTP fails closed.
func AuthenticatedShardHandler(next http.Handler) http.Handler {
	trusted := trustedProviderRelay(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "shard authentication required", http.StatusUnauthorized)
			return
		}
		trusted.ServeHTTP(w, r)
	})
}
