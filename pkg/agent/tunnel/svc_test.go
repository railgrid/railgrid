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

package tunnel

import "testing"

func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"::1", true},
		{"0.0.0.0", false},
		{"10.0.0.5", false},
		{"192.168.1.10", false},
		{"example.com", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isLoopbackHost(tc.host); got != tc.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// TestIsAllowedSvcHost pins the SSRF boundary: server mode must never dial off
// the loopback, and kubernetes mode must widen only to cluster-DNS Service
// names — never to node IPs, the LAN, or the internet.
func TestIsAllowedSvcHost(t *testing.T) {
	// isAllowedSvcHost is currently permissive: a Service's spec.host may point at
	// any device on the edge's LAN (e.g. a UniFi console), so every host is
	// allowed regardless of mode. Loopback + cluster-DNS remain allowed for their
	// original reasons. TODO(security): when a per-agent allowlist lands, restore
	// rejection of arbitrary LAN/metadata/internet hosts (see TestIsClusterDNSHost
	// for the classification coverage that gating will rely on).
	hosts := []string{
		"127.0.0.1", "localhost", "::1", // loopback
		"home-assistant.home.svc", "ha.default.svc.cluster.local", // cluster DNS
		"10.0.0.5", "192.168.1.10", "169.254.169.254", "example.com", // LAN / metadata / internet
		"", // empty
	}
	for _, h := range hosts {
		if got := isAllowedSvcHost(h, false); !got {
			t.Errorf("isAllowedSvcHost(%q, allowCluster=false) = false, want true (permissive)", h)
		}
		if got := isAllowedSvcHost(h, true); !got {
			t.Errorf("isAllowedSvcHost(%q, allowCluster=true) = false, want true (permissive)", h)
		}
	}
}

func TestIsClusterDNSHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"home-assistant.home.svc", true},
		{"ha.default.svc.cluster.local", true},

		{"10.0.0.5", false},             // IP literal
		{"192.168.1.10", false},         // LAN host
		{"169.254.169.254", false},      // cloud metadata
		{"example.com", false},          // internet
		{"evil.svc.example.com", false}, // .svc not a suffix
		{"svc", false},                  // bare
		{".svc", false},                 // no name/namespace
		{"cluster.local", false},
		{"a.b.c.svc", false}, // too many labels before .svc
		{"", false},
	}
	for _, tc := range cases {
		if got := isClusterDNSHost(tc.host); got != tc.want {
			t.Errorf("isClusterDNSHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
