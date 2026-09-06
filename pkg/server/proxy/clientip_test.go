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

package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func mustPrefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	ps, err := ParseTrustedProxyCIDRs(cidrs)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func TestClientIP(t *testing.T) {
	proxies := mustPrefixes(t, "10.0.0.0/8", "fd00::/8", "192.0.2.10")

	tests := []struct {
		name       string
		remoteAddr string
		xff        []string
		xri        string
		trusted    []netip.Prefix
		want       string
	}{
		{
			name:       "no proxies: RemoteAddr only",
			remoteAddr: "203.0.113.7:4242",
			want:       "203.0.113.7",
		},
		{
			name:       "no proxies: spoofed XFF and X-Real-IP ignored",
			remoteAddr: "203.0.113.7:4242",
			xff:        []string{"1.1.1.1"},
			xri:        "2.2.2.2",
			want:       "203.0.113.7",
		},
		{
			name:       "untrusted peer: leading XFF spoof ignored",
			remoteAddr: "203.0.113.7:4242",
			xff:        []string{"1.1.1.1, 10.0.0.5"},
			trusted:    proxies,
			want:       "203.0.113.7",
		},
		{
			name:       "untrusted peer: X-Real-IP ignored",
			remoteAddr: "203.0.113.7:4242",
			xri:        "1.1.1.1",
			trusted:    proxies,
			want:       "203.0.113.7",
		},
		{
			name:       "trusted peer: last hop wins over client-supplied prefix",
			remoteAddr: "10.0.0.5:4242",
			xff:        []string{"1.1.1.1, 198.51.100.9"},
			trusted:    proxies,
			want:       "198.51.100.9",
		},
		{
			name:       "trusted peer: chain of two trusted hops skipped",
			remoteAddr: "10.0.0.5:4242",
			xff:        []string{"1.1.1.1, 198.51.100.9, fd00::1, 10.1.2.3"},
			trusted:    proxies,
			want:       "198.51.100.9",
		},
		{
			name:       "trusted peer: hops split across header lines",
			remoteAddr: "10.0.0.5:4242",
			xff:        []string{"1.1.1.1", "198.51.100.9, 10.1.2.3"},
			trusted:    proxies,
			want:       "198.51.100.9",
		},
		{
			name:       "trusted peer: every hop trusted falls back to X-Real-IP",
			remoteAddr: "10.0.0.5:4242",
			xff:        []string{"10.1.2.3"},
			xri:        "198.51.100.9",
			trusted:    proxies,
			want:       "198.51.100.9",
		},
		{
			name:       "trusted peer: no headers at all keys on the proxy",
			remoteAddr: "10.0.0.5:4242",
			trusted:    proxies,
			want:       "10.0.0.5",
		},
		{
			name:       "trusted peer: malformed hop cannot mint a bucket",
			remoteAddr: "10.0.0.5:4242",
			xff:        []string{"1.1.1.1, not-an-ip"},
			trusted:    proxies,
			want:       "10.0.0.5",
		},
		{
			name:       "trusted peer: trailing comma is not a malformed hop",
			remoteAddr: "10.0.0.5:4242",
			xff:        []string{"198.51.100.9,"},
			trusted:    proxies,
			want:       "198.51.100.9",
		},
		{
			name:       "single-host trusted entry",
			remoteAddr: "192.0.2.10:4242",
			xff:        []string{"198.51.100.9"},
			trusted:    proxies,
			want:       "198.51.100.9",
		},
		{
			name:       "IPv6 peer with bracketed hop and port",
			remoteAddr: "[fd00::5]:4242",
			xff:        []string{"[2001:db8::9]:1234"},
			trusted:    proxies,
			want:       "2001:db8::9",
		},
		{
			name:       "IPv4-mapped peer is unmapped and matched",
			remoteAddr: "[::ffff:10.0.0.5]:4242",
			xff:        []string{"::ffff:198.51.100.9"},
			trusted:    proxies,
			want:       "198.51.100.9",
		},
		{
			name:       "IPv4 hop with port",
			remoteAddr: "10.0.0.5:4242",
			xff:        []string{"198.51.100.9:5555"},
			trusted:    proxies,
			want:       "198.51.100.9",
		},
		{
			name:       "non-IP RemoteAddr returned as-is",
			remoteAddr: "@",
			xff:        []string{"1.1.1.1"},
			trusted:    proxies,
			want:       "@",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remoteAddr
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if tc.xri != "" {
				r.Header.Set("X-Real-IP", tc.xri)
			}
			if got := ClientIP(r, tc.trusted); got != tc.want {
				t.Fatalf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseTrustedProxyCIDRs(t *testing.T) {
	got, err := ParseTrustedProxyCIDRs([]string{"10.0.0.0/8, fd00::/8", "", " 192.0.2.10 ", "::ffff:192.0.2.11"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "fd00::/8", "192.0.2.10/32", "192.0.2.11/32"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Fatalf("prefix %d = %s, want %s", i, got[i], want[i])
		}
	}
	for _, bad := range []string{"10.0.0.0/33", "nope", "10.0.0.0/"} {
		if _, err := ParseTrustedProxyCIDRs([]string{bad}); err == nil {
			t.Fatalf("%q: expected error", bad)
		}
	}
}

func TestBoundedKeysCapAndIdle(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := NewBoundedKeys[int](3, time.Minute)

	b.Put("a", 1, base)
	b.Put("b", 2, base.Add(time.Second))
	b.Put("c", 3, base.Add(2*time.Second))
	if _, ok := b.Get("a", base.Add(3*time.Second)); !ok {
		t.Fatal("a should still be present")
	}
	// At capacity: b is now the least recently used and must go.
	b.Put("d", 4, base.Add(4*time.Second))
	if b.Len() != 3 {
		t.Fatalf("Len = %d, want 3", b.Len())
	}
	if _, ok := b.Get("b", base.Add(4*time.Second)); ok {
		t.Fatal("b should have been evicted as least recently used")
	}
	if _, ok := b.Get("a", base.Add(4*time.Second)); !ok {
		t.Fatal("a was touched and must survive")
	}

	// A minute later everything is idle and a single Put sweeps it all.
	b.Put("e", 5, base.Add(2*time.Minute))
	if b.Len() != 1 {
		t.Fatalf("after idle sweep Len = %d, want 1", b.Len())
	}
	if _, ok := b.Get("e", base.Add(2*time.Minute)); !ok {
		t.Fatal("e should be present")
	}
}
