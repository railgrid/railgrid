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
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ParseTrustedProxyCIDRs parses the --trusted-proxy-cidrs values. Each entry
// may be a CIDR ("10.0.0.0/8", "fd00::/8") or a bare address, which is taken
// as a single-host prefix. Entries may themselves be comma-separated; blanks
// are ignored.
func ParseTrustedProxyCIDRs(values []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, v := range values {
		for _, raw := range strings.Split(v, ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			if strings.Contains(raw, "/") {
				p, err := netip.ParsePrefix(raw)
				if err != nil {
					return nil, fmt.Errorf("trusted proxy CIDR %q: %w", raw, err)
				}
				out = append(out, p.Masked())
				continue
			}
			a, err := netip.ParseAddr(raw)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy address %q: %w", raw, err)
			}
			a = a.Unmap().WithZone("")
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out, nil
}

// ClientIP derives the address the hub keys its pre-authentication rate
// limits on. The rule is the one every reverse proxy follows when it forwards:
// the proxy APPENDS the peer it saw to X-Forwarded-For, so the only hop the
// hub can vouch for is the right-most one that a trusted proxy wrote. Every
// hop to the left of that is whatever the client chose to send.
//
//   - The connection peer (RemoteAddr) is always the starting point.
//   - When trusted is empty, X-Forwarded-For and X-Real-IP are ignored
//     entirely and the peer address is the answer. A hub deployed behind a
//     proxy without --trusted-proxy-cidrs therefore sees every client as the
//     proxy's own address and throttles them as one; that is the safe way
//     to fail (too much throttling, never too little).
//   - When the peer is inside a trusted prefix, X-Forwarded-For is walked
//     from the right, skipping hops that are themselves trusted proxies, and
//     the first untrusted hop is the client. X-Real-IP is consulted only
//     when that walk yields nothing.
//   - A hop that is not a valid IP ends the walk and the peer address is
//     used, so a malformed header can never mint a fresh bucket.
//
// The result is a canonical IP string (IPv4-mapped IPv6 unmapped, zone
// dropped); when RemoteAddr itself is not an IP it is returned as-is, which
// only happens for non-TCP listeners.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	peerHost := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peerHost = host
	}
	peer, ok := parseIP(peerHost)
	if !ok {
		return strings.TrimSpace(peerHost)
	}
	if len(trusted) == 0 || !isTrustedProxy(peer, trusted) {
		return peer.String()
	}

	// The peer is a trusted proxy: find the right-most hop it (or a trusted
	// proxy in front of it) did not write itself.
	hops := forwardedForHops(r.Header)
	for i := len(hops) - 1; i >= 0; i-- {
		hop, ok := parseIP(hops[i])
		if !ok {
			return peer.String()
		}
		if isTrustedProxy(hop, trusted) {
			continue
		}
		return hop.String()
	}

	if xri, ok := parseIP(r.Header.Get("X-Real-IP")); ok && !isTrustedProxy(xri, trusted) {
		return xri.String()
	}
	return peer.String()
}

// forwardedForHops flattens every X-Forwarded-For header line, in order, into
// its comma-separated hops. Blank hops are dropped so a trailing comma does
// not read as an empty (malformed) client.
func forwardedForHops(h http.Header) []string {
	var hops []string
	for _, line := range h.Values("X-Forwarded-For") {
		for _, hop := range strings.Split(line, ",") {
			if hop = strings.TrimSpace(hop); hop != "" {
				hops = append(hops, hop)
			}
		}
	}
	return hops
}

// parseIP parses s as an IP address in canonical form. Bracketed IPv6
// ("[::1]") and a trailing port on a bracketed literal are accepted, since
// some proxies write hops in host:port form.
func parseIP(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	if strings.HasPrefix(s, "[") {
		if host, _, err := net.SplitHostPort(s); err == nil {
			s = host
		} else {
			s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
		}
	} else if strings.Count(s, ":") == 1 {
		// IPv4 with a port; a bare IPv6 literal has at least two colons.
		if host, _, err := net.SplitHostPort(s); err == nil {
			s = host
		}
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap().WithZone(""), true
}

func isTrustedProxy(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
