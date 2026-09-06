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

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

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

func prefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	out, err := ParseSvcAllowedCIDRs(cidrs)
	if err != nil {
		t.Fatalf("ParseSvcAllowedCIDRs(%v): %v", cidrs, err)
	}
	return out
}

// TestIsAllowedSvcHost pins the SSRF boundary: loopback always, cluster DNS
// only in kubernetes mode, literal IPs only inside the allow list, and the
// link-local / unspecified / multicast classes never — not even when an allow
// list covers them.
func TestIsAllowedSvcHost(t *testing.T) {
	lan := prefixes(t, "192.168.1.0/24")
	wide := prefixes(t, "0.0.0.0/0", "::/0")

	cases := []struct {
		name         string
		host         string
		allowCluster bool
		allow        []netip.Prefix
		want         bool
	}{
		// loopback, both modes, no allow list
		{"loopback v4 server", "127.0.0.1", false, nil, true},
		{"loopback v4 cluster", "127.0.0.1", true, nil, true},
		{"loopback name", "localhost", false, nil, true},
		{"loopback v6", "::1", false, nil, true},
		{"loopback other octet", "127.0.0.2", true, nil, true},

		// LAN, metadata, internet: false in both modes without an allow list
		{"lan server", "192.168.1.10", false, nil, false},
		{"lan cluster", "192.168.1.10", true, nil, false},
		{"rfc1918 cluster no allow list", "10.0.0.5", true, nil, false},
		{"rfc1918 server", "10.0.0.5", false, nil, false},
		{"metadata server", "169.254.169.254", false, nil, false},
		{"metadata cluster", "169.254.169.254", true, nil, false},
		{"internet server", "example.com", false, nil, false},
		{"internet cluster", "example.com", true, nil, false},
		{"empty", "", false, nil, false},

		// allow list
		{"lan in allow list", "192.168.1.1", false, lan, true},
		{"lan outside allow list", "192.168.2.1", false, lan, false},
		{"metadata inside allow list", "169.254.169.254", false, prefixes(t, "169.254.0.0/16"), false},
		{"metadata inside 0/0", "169.254.169.254", true, wide, false},
		{"v6 link-local inside allow list", "fe80::1", false, prefixes(t, "fe80::/10"), false},
		{"unspecified inside 0/0", "0.0.0.0", false, wide, false},
		{"multicast inside 0/0", "224.0.0.1", false, wide, false},
		{"mapped v4 metadata inside allow list", "::ffff:169.254.169.254", false, wide, false},
		{"mapped v4 lan inside allow list", "::ffff:192.168.1.1", false, lan, true},

		// cloud metadata endpoints that are not link-local: never, even in 0/0
		{"aws imds v6 inside ::/0", "fd00:ec2::254", false, wide, false},
		{"alibaba metadata inside 0/0", "100.100.100.200", false, wide, false},
		{"alibaba metadata inside its /32", "100.100.100.200", true, prefixes(t, "100.100.100.200/32"), false},
		{"nat64 link-local inside ::/0", "64:ff9b::a9fe:a9fe", false, wide, false},
		{"nat64 link-local dotted inside ::/0", "64:ff9b::169.254.169.254", true, wide, false},
		{"nat64 alibaba inside ::/0", "64:ff9b::6464:64c8", false, wide, false},
		{"nat64 lan inside ::/0", "64:ff9b::c0a8:101", false, wide, true},
		{"mapped alibaba inside ::/0", "::ffff:100.100.100.200", false, wide, false},
		{"cgnat neighbour inside 0/0", "100.100.100.201", false, wide, true},

		// cluster DNS only in kubernetes mode
		{"cluster dns server", "svc.ns.svc.cluster.local", false, nil, false},
		{"cluster dns cluster", "svc.ns.svc.cluster.local", true, nil, true},
		{"short cluster dns cluster", "home-assistant.home.svc", true, nil, true},
		{"cluster dns lookalike", "evil.svc.example.com", true, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAllowedSvcHost(tc.host, tc.allowCluster, tc.allow); got != tc.want {
				t.Errorf("isAllowedSvcHost(%q, allowCluster=%v, allow=%v) = %v, want %v",
					tc.host, tc.allowCluster, tc.allow, got, tc.want)
			}
		})
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

func TestParseSvcPolicy(t *testing.T) {
	for raw, want := range map[string]SvcPolicy{"": DefaultSvcPolicy, "enforce": SvcPolicyEnforce, " Warn ": SvcPolicyWarn, "allow-any": SvcPolicyAllowAny} {
		got, err := ParseSvcPolicy(raw)
		if err != nil || got != want {
			t.Errorf("ParseSvcPolicy(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := ParseSvcPolicy("yolo"); err == nil {
		t.Error("ParseSvcPolicy(yolo) = nil error, want error")
	}
}

func TestParseSvcAllowedCIDRs(t *testing.T) {
	got, err := ParseSvcAllowedCIDRs([]string{"192.168.1.0/24", " 10.0.0.5 ", "", "fd00::/8"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.0/24", "10.0.0.5/32", "fd00::/8"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	for _, bad := range []string{"nope", "192.168.1.0/33", "192.168.1.0/"} {
		if _, err := ParseSvcAllowedCIDRs([]string{bad}); err == nil {
			t.Errorf("ParseSvcAllowedCIDRs(%q) = nil error, want error", bad)
		}
	}
}

// fixedResolver answers hostname lookups from a table; unknown names error.
func fixedResolver(table map[string][]string) svcResolver {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		ips, ok := table[host]
		if !ok {
			return nil, errors.New("no such host: " + host)
		}
		var out []netip.Addr
		for _, ip := range ips {
			out = append(out, netip.MustParseAddr(ip))
		}
		return out, nil
	}
}

// TestVetSvcHostResolved covers the DNS side: every resolved address is
// checked, a hostname that resolves to a blocked address is refused (hard),
// and the vetted addresses are what a dial gets pinned to.
func TestVetSvcHostResolved(t *testing.T) {
	resolver := fixedResolver(map[string][]string{
		"metadata.evil":  {"169.254.169.254"},
		"rebind.evil":    {"192.168.1.20", "169.254.169.254"},
		"unifi.lan":      {"192.168.1.1"},
		"printer.lan":    {"192.168.2.7"},
		"localhost":      {"127.0.0.1", "::1"},
		"ha.home.svc":    {"10.96.0.12"},
		"ha.home.svc.":   {"10.96.0.12"},
		"dual.lan":       {"192.168.1.5", "fd00::5"},
		"multicast.evil": {"224.0.0.251"},
	})
	lan := prefixes(t, "192.168.1.0/24")

	cases := []struct {
		name         string
		host         string
		allowCluster bool
		allow        []netip.Prefix
		wantDenied   bool
		wantHard     bool
		wantLoopback bool
		wantAddrs    []string
		wantErr      bool
	}{
		{name: "metadata hostname is hard-blocked", host: "metadata.evil", allow: prefixes(t, "0.0.0.0/0"), wantDenied: true, wantHard: true, wantAddrs: []string{"169.254.169.254"}},
		{name: "one bad record poisons the name", host: "rebind.evil", allow: lan, wantDenied: true, wantHard: true, wantAddrs: []string{"192.168.1.20", "169.254.169.254"}},
		{name: "multicast hostname is hard-blocked", host: "multicast.evil", wantDenied: true, wantHard: true, wantAddrs: []string{"224.0.0.251"}},
		{name: "hostname inside allow list", host: "unifi.lan", allow: lan, wantAddrs: []string{"192.168.1.1"}},
		{name: "hostname outside allow list", host: "printer.lan", allow: lan, wantDenied: true, wantAddrs: []string{"192.168.2.7"}},
		{name: "hostname with one record outside allow list", host: "dual.lan", allow: lan, wantDenied: true, wantAddrs: []string{"192.168.1.5", "fd00::5"}},
		{name: "localhost resolves to loopback", host: "localhost", wantLoopback: true, wantAddrs: []string{"127.0.0.1", "::1"}},
		{name: "cluster dns in kubernetes mode", host: "ha.home.svc", allowCluster: true, wantAddrs: []string{"10.96.0.12"}},
		{name: "cluster dns in server mode is not resolved", host: "ha.home.svc", wantDenied: true},
		{name: "literal ip is not resolved", host: "192.168.1.1", allow: lan, wantAddrs: []string{"192.168.1.1"}},
		{name: "literal loopback", host: "127.0.0.1", wantLoopback: true, wantAddrs: []string{"127.0.0.1"}},
		{name: "literal metadata", host: "169.254.169.254", allow: prefixes(t, "169.254.0.0/16"), wantDenied: true, wantHard: true, wantAddrs: []string{"169.254.169.254"}},
		{name: "unresolvable", host: "nope.invalid", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := svcProxyConfig{SvcProxyOptions: SvcProxyOptions{AllowedCIDRs: tc.allow}, allowCluster: tc.allowCluster, resolve: resolver}
			got, err := vetSvcHost(context.Background(), tc.host, cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("vetSvcHost(%q) = %+v, nil; want error", tc.host, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("vetSvcHost(%q): %v", tc.host, err)
			}
			if (got.denied != nil) != tc.wantDenied {
				t.Errorf("denied = %v, want %v", got.denied, tc.wantDenied)
			}
			if got.denied != nil && got.denied.hard != tc.wantHard {
				t.Errorf("denied.hard = %v, want %v (%s)", got.denied.hard, tc.wantHard, got.denied.reason)
			}
			if got.loopback != tc.wantLoopback {
				t.Errorf("loopback = %v, want %v", got.loopback, tc.wantLoopback)
			}
			var addrs []string
			for _, a := range got.addrs {
				addrs = append(addrs, a.String())
			}
			if strings.Join(addrs, ",") != strings.Join(tc.wantAddrs, ",") {
				t.Errorf("addrs = %v, want %v", addrs, tc.wantAddrs)
			}
		})
	}
}

// countingDialer wraps net.Dialer and records every address dialed. fail, if
// set, short-circuits matching addresses with an error (still counted) so a
// test can stand in an unreachable record without waiting for a timeout.
type countingDialer struct {
	n     atomic.Int32
	addrs []string
	fail  func(addr string) bool
}

func (c *countingDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	c.n.Add(1)
	c.addrs = append(c.addrs, addr)
	if c.fail != nil && c.fail(addr) {
		return nil, errors.New("unreachable (test)")
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// svcProxyFixture stands up a loopback upstream (httptest binds 127.0.0.1)
// and returns a helper that sends one request through newSvcProxyHandler to
// the given target, plus the counting dialer the handler was given.
func svcProxyFixture(t *testing.T, cfg svcProxyConfig) (upstream *httptest.Server, do func(target string) *http.Response, dialer *countingDialer) {
	t.Helper()
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range []string{svcTargetHeader, svcTLSInsecureHeader} {
			if v := r.Header.Get(h); v != "" {
				t.Errorf("control header %s leaked upstream: %q", h, v)
			}
		}
		_, _ = io.WriteString(w, "hello from "+r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	dialer = &countingDialer{}
	cfg.dial = dialer.dial
	h := newSvcProxyHandler(cfg)

	do = func(target string) *http.Response {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/svc/api/ping", nil)
		req.Header.Set(svcTargetHeader, target)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result()
	}
	return upstream, do, dialer
}

// upstreamPort returns the port an httptest upstream listens on.
func upstreamPort(t *testing.T, upstream *httptest.Server) string {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return string(body)
}

// TestSvcProxyHandlerEnforceDeniesWithoutDialing is the acceptance case: under
// enforce, a LAN / metadata / unresolved-cluster target gets a 403 with the
// JSON body and the agent never opens a socket.
func TestSvcProxyHandlerEnforceDeniesWithoutDialing(t *testing.T) {
	cfg := svcProxyConfig{SvcProxyOptions: SvcProxyOptions{Policy: SvcPolicyEnforce},
		resolve: fixedResolver(map[string][]string{"metadata.evil": {"169.254.169.254"}, "printer.lan": {"192.168.2.7"}})}
	_, do, dialer := svcProxyFixture(t, cfg)

	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://192.168.1.10:8080",
		"http://10.0.0.5:6443",
		"http://metadata.evil:80",
		"http://printer.lan:9100",
		"http://ha.home.svc:8123", // cluster DNS in server mode
	} {
		resp := do(target)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403 (body %q)", target, resp.StatusCode, body)
			continue
		}
		if got := resp.Header.Get(svcPolicyHeader); got != string(SvcPolicyEnforce) {
			t.Errorf("%s: %s = %q, want %q", target, svcPolicyHeader, got, SvcPolicyEnforce)
		}
		var js struct {
			Error  string `json:"error"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(body), &js); err != nil {
			t.Errorf("%s: body %q is not JSON: %v", target, body, err)
		} else if js.Error != "target host not allowed" || js.Reason == "" {
			t.Errorf("%s: body = %+v, want error=target host not allowed and a reason", target, js)
		}
	}
	if n := dialer.n.Load(); n != 0 {
		t.Errorf("dialed %d times (%v), want 0", n, dialer.addrs)
	}
}

// TestSvcProxyHandlerHardBlockIgnoresPolicy: link-local and the cloud
// metadata endpoints that live outside link-local stay refused under warn and
// allow-any, even when an allow list covers them.
func TestSvcProxyHandlerHardBlockIgnoresPolicy(t *testing.T) {
	targets := []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://[fd00:ec2::254]/latest/meta-data/",          // AWS IMDS over IPv6 (ULA)
		"http://100.100.100.200/latest/meta-data/",          // Alibaba Cloud metadata
		"http://[64:ff9b::a9fe:a9fe]/latest/meta-data/",     // NAT64 of 169.254.169.254
		"http://[::ffff:100.100.100.200]/latest/meta-data/", // IPv4-mapped
	}
	for _, policy := range []SvcPolicy{SvcPolicyWarn, SvcPolicyAllowAny} {
		for _, target := range targets {
			cfg := svcProxyConfig{SvcProxyOptions: SvcProxyOptions{Policy: policy, AllowedCIDRs: prefixes(t, "0.0.0.0/0", "::/0")}}
			_, do, dialer := svcProxyFixture(t, cfg)
			// Never let a failing case reach the network.
			dialer.fail = func(string) bool { return true }
			resp := do(target)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("policy %s, %s: status = %d, want 403", policy, target, resp.StatusCode)
			}
			if n := dialer.n.Load(); n != 0 {
				t.Errorf("policy %s, %s: dialed %d times (%v), want 0", policy, target, n, dialer.addrs)
			}
		}
	}
}

// TestSvcProxyHandlerLoopbackDials: a literal loopback target and the
// "localhost" name (resolved through the injected resolver) both reach the
// upstream under enforce with no allow list, with exactly one dial and no
// policy header.
func TestSvcProxyHandlerLoopbackDials(t *testing.T) {
	cfg := svcProxyConfig{SvcProxyOptions: SvcProxyOptions{Policy: SvcPolicyEnforce},
		resolve: fixedResolver(map[string][]string{"localhost": {"127.0.0.1"}})}
	for _, name := range []string{"127.0.0.1", "localhost"} {
		upstream, do, dialer := svcProxyFixture(t, cfg)
		resp := do("http://" + net.JoinHostPort(name, upstreamPort(t, upstream)))
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "hello from /api/ping") {
			t.Fatalf("%s: status = %d body = %q", name, resp.StatusCode, body)
		}
		if got := resp.Header.Get(svcPolicyHeader); got != "" {
			t.Errorf("%s: unexpected %s = %q on an allowed target", name, svcPolicyHeader, got)
		}
		if n := dialer.n.Load(); n != 1 {
			t.Errorf("%s: dialed %d times, want 1", name, n)
		}
	}
}

// TestSvcProxyHandlerWarnDialsAndStamps: a target outside the allow list is
// still dialed under warn, pinned to the resolved records, and the response
// carries X-Faros-Svc-Policy: warn. The fake LAN name resolves to an
// unreachable TEST-NET address first and the loopback upstream second, which
// also exercises the pinned dialer's fallback across records.
func TestSvcProxyHandlerWarnDialsAndStamps(t *testing.T) {
	cfg := svcProxyConfig{SvcProxyOptions: SvcProxyOptions{Policy: SvcPolicyWarn},
		resolve: fixedResolver(map[string][]string{"printer.lan": {"192.0.2.1", "127.0.0.1"}})}
	upstream, do, dialer := svcProxyFixture(t, cfg)
	dialer.fail = func(addr string) bool { return strings.HasPrefix(addr, "192.0.2.1:") }
	port := upstreamPort(t, upstream)

	resp := do("http://printer.lan:" + port)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "hello from /api/ping") {
		t.Fatalf("status = %d body = %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get(svcPolicyHeader); got != string(SvcPolicyWarn) {
		t.Errorf("%s = %q, want %q", svcPolicyHeader, got, SvcPolicyWarn)
	}
	want := []string{"192.0.2.1:" + port, "127.0.0.1:" + port}
	if strings.Join(dialer.addrs, ",") != strings.Join(want, ",") {
		t.Errorf("dialed %v, want pinned %v", dialer.addrs, want)
	}
}

// TestSvcProxyHandlerAllowListDials: an allow-listed non-loopback range is
// dialable under enforce (the upstream is loopback, so the test allow-lists
// 127.0.0.0/8 through a resolver that answers a LAN-looking name with it).
func TestSvcProxyHandlerAllowListDials(t *testing.T) {
	cfg := svcProxyConfig{SvcProxyOptions: SvcProxyOptions{Policy: SvcPolicyEnforce, AllowedCIDRs: prefixes(t, "127.0.0.0/8")},
		resolve: fixedResolver(map[string][]string{"unifi.lan": {"127.0.0.1"}})}
	upstream, do, dialer := svcProxyFixture(t, cfg)
	resp := do("http://unifi.lan:" + upstreamPort(t, upstream))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if n := dialer.n.Load(); n != 1 {
		t.Errorf("dialed %d times, want 1", n)
	}
}

func TestSvcProxyHandlerBadTarget(t *testing.T) {
	cfg := svcProxyConfig{SvcProxyOptions: SvcProxyOptions{Policy: SvcPolicyEnforce}}
	_, do, dialer := svcProxyFixture(t, cfg)
	for _, target := range []string{"", "not a url", "http://", "/relative"} {
		resp := do(target)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400", target, resp.StatusCode)
		}
	}
	if n := dialer.n.Load(); n != 0 {
		t.Errorf("dialed %d times, want 0", n)
	}
}
