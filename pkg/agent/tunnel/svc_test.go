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
	cases := []struct {
		host       string
		serverMode bool // expected when allowCluster=false
		kubeMode   bool // expected when allowCluster=true
	}{
		// Loopback: always fine.
		{"127.0.0.1", true, true},
		{"localhost", true, true},
		{"::1", true, true},

		// Cluster DNS: kubernetes mode only.
		{"home-assistant.home.svc", false, true},
		{"ha.default.svc.cluster.local", false, true},

		// Not reachable in either mode.
		{"10.0.0.5", false, false},             // pod/node IP
		{"192.168.1.10", false, false},         // LAN host
		{"169.254.169.254", false, false},      // cloud metadata
		{"example.com", false, false},          // internet
		{"evil.svc.example.com", false, false}, // .svc not a suffix
		{"svc", false, false},                  // bare
		{".svc", false, false},                 // no name/namespace
		{"cluster.local", false, false},
		{"a.b.c.svc", false, false}, // too many labels before .svc
		{"", false, false},
	}
	for _, tc := range cases {
		if got := isAllowedSvcHost(tc.host, false); got != tc.serverMode {
			t.Errorf("isAllowedSvcHost(%q, allowCluster=false) = %v, want %v", tc.host, got, tc.serverMode)
		}
		if got := isAllowedSvcHost(tc.host, true); got != tc.kubeMode {
			t.Errorf("isAllowedSvcHost(%q, allowCluster=true) = %v, want %v", tc.host, got, tc.kubeMode)
		}
	}
}
