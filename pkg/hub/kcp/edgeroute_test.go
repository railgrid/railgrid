/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kcp

import "testing"

// The backend URL is authored by the tenant's own chart, and this parser is the
// only thing standing between it and a hub-initiated request. Refusing anything
// that is not cluster DNS bounds the worst case to "somewhere inside the
// cluster the tunnel already terminates in".
func TestParseClusterServiceTargetAccepts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		url    string
		svc    string
		ns     string
		port   int32
		scheme string
	}{{
		name: "what the infrastructure chart writes in operator mode",
		url:  "http://infrastructure-faros-infrastructure-provider.faros-infrastructure-provider.svc.cluster.local:8081",
		svc:  "infrastructure-faros-infrastructure-provider",
		ns:   "faros-infrastructure-provider", port: 8081, scheme: "http",
	}, {
		name: "short cluster-DNS form",
		url:  "http://code.faros-provider-code.svc:8083",
		svc:  "code", ns: "faros-provider-code", port: 8083, scheme: "http",
	}, {
		name: "https defaults to 443",
		url:  "https://x.y.svc.cluster.local",
		svc:  "x", ns: "y", port: 443, scheme: "https",
	}, {
		name: "http defaults to 80",
		url:  "http://x.y.svc",
		svc:  "x", ns: "y", port: 80, scheme: "http",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseClusterServiceTarget(tc.url)
			if err != nil {
				t.Fatalf("ParseClusterServiceTarget(%q): %v", tc.url, err)
			}
			if got.Name != tc.svc || got.Namespace != tc.ns || got.Port != tc.port || got.Scheme != tc.scheme {
				t.Errorf("got %+v, want %s/%s:%d %s", got, tc.ns, tc.svc, tc.port, tc.scheme)
			}
		})
	}
}

func TestParseClusterServiceTargetRefuses(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		// The ones that matter: a tenant aiming platform-initiated traffic
		// somewhere other than a Service in their own cluster.
		{"external host", "http://evil.example.com:8081"},
		{"IP literal", "http://10.0.0.5:8081"},
		{"loopback", "http://127.0.0.1:8081"},
		{"link-local metadata", "http://169.254.169.254/latest/meta-data"},
		{"bare hostname", "http://infrastructure:8081"},
		{"two labels only", "http://svc.namespace:8081"},
		{"not a svc name", "http://a.b.notsvc.cluster.local:8081"},
		{"four labels", "http://a.b.c.svc:8081"},
		{"empty service label", "http://.ns.svc:8081"},
		{"non-http scheme", "ssh://x.y.svc:22"},
		{"empty", ""},
		{"garbage", "://"},
		{"port out of range", "http://x.y.svc:70000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ParseClusterServiceTarget(tc.url); err == nil {
				t.Fatalf("accepted %q as a cluster Service target: %+v", tc.url, got)
			}
		})
	}
}

func TestProviderEdgeServiceName(t *testing.T) {
	// Prefixed so a hub-owned Service can never collide with one a tenant
	// created for an appliance of the same name.
	if got := ProviderEdgeServiceName("infrastructure"); got != "provider-infrastructure" {
		t.Errorf("ProviderEdgeServiceName = %q, want provider-infrastructure", got)
	}
}
