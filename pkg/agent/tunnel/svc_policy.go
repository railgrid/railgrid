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
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// SvcPolicy selects what the agent does when a /svc target fails the host
// checks (see SvcProxyOptions).
type SvcPolicy string

const (
	// SvcPolicyEnforce refuses a disallowed target with 403 and never dials it.
	SvcPolicyEnforce SvcPolicy = "enforce"
	// SvcPolicyWarn dials a disallowed target anyway but logs the denial and
	// stamps X-Faros-Svc-Policy: warn on the response, so operators can find
	// the Services that need --svc-allow-cidr before flipping to enforce.
	SvcPolicyWarn SvcPolicy = "warn"
	// SvcPolicyAllowAny disables the allow list entirely (loudly logged at
	// startup). Link-local, unspecified, multicast and the cloud metadata
	// endpoints in svcHardBlockedPrefixes stay blocked even here — those are
	// never overridable.
	SvcPolicyAllowAny SvcPolicy = "allow-any"
)

// DefaultSvcPolicy is the policy used when --svc-policy is not given. This
// release defaults to warn so existing Services that point off the loopback
// keep working while the agent logs what enforce would block; the next
// release flips the default to enforce.
const DefaultSvcPolicy = SvcPolicyWarn

// ParseSvcPolicy validates a --svc-policy value. Empty selects DefaultSvcPolicy.
func ParseSvcPolicy(raw string) (SvcPolicy, error) {
	switch p := SvcPolicy(strings.ToLower(strings.TrimSpace(raw))); p {
	case "":
		return DefaultSvcPolicy, nil
	case SvcPolicyEnforce, SvcPolicyWarn, SvcPolicyAllowAny:
		return p, nil
	default:
		return "", fmt.Errorf("invalid svc policy %q: must be one of enforce, warn, allow-any", raw)
	}
}

// ParseSvcAllowedCIDRs parses --svc-allow-cidr values (each a CIDR such as
// 192.168.1.0/24 or 10.0.0.5/32; a bare IP is treated as a /32 or /128).
func ParseSvcAllowedCIDRs(raw []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			addr, err := netip.ParseAddr(s)
			if err != nil {
				return nil, fmt.Errorf("invalid svc allow CIDR %q: %w", s, err)
			}
			out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("invalid svc allow CIDR %q: %w", s, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// SvcProxyOptions is the operator-configured part of the /svc proxy policy,
// set from --svc-allow-cidr / --svc-policy (or FAROS_AGENT_SVC_ALLOW_CIDR /
// FAROS_AGENT_SVC_POLICY) and threaded from agent.Options down to the tunnel's
// remote server.
type SvcProxyOptions struct {
	// AllowedCIDRs are the literal-IP ranges (and resolved-hostname ranges)
	// the agent may dial besides loopback and, in kubernetes mode, cluster DNS.
	AllowedCIDRs []netip.Prefix
	// Policy decides what happens when a target is outside that set.
	Policy SvcPolicy
}

// policy returns Policy, defaulting an unset value.
func (o SvcProxyOptions) policy() SvcPolicy {
	if o.Policy == "" {
		return DefaultSvcPolicy
	}
	return o.Policy
}

// svcResolver looks up the addresses of a hostname; net.DefaultResolver in
// production, injected in tests.
type svcResolver func(ctx context.Context, host string) ([]netip.Addr, error)

// svcDialer opens a TCP connection; net.Dialer in production, a counter in
// tests.
type svcDialer func(ctx context.Context, network, addr string) (net.Conn, error)

// svcProxyConfig is everything newSvcProxyHandler needs: the operator options,
// the mode-derived cluster-DNS switch, and the resolver/dialer hooks.
type svcProxyConfig struct {
	SvcProxyOptions
	// allowCluster permits {name}.{namespace}.svc[.cluster.local] names. Set in
	// kubernetes mode only (see setupRouter), where such names resolve inside
	// the cluster's DNS.
	allowCluster bool
	resolve      svcResolver
	dial         svcDialer
}

func (c svcProxyConfig) resolver() svcResolver {
	if c.resolve != nil {
		return c.resolve
	}
	return func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
}

func (c svcProxyConfig) dialer() svcDialer {
	if c.dial != nil {
		return c.dial
	}
	d := &net.Dialer{}
	return d.DialContext
}

// svcDenial explains why a target failed the host checks. hard marks the
// never-overridable class (link-local, unspecified, multicast, cloud metadata
// endpoints): warn and allow-any still refuse those.
type svcDenial struct {
	reason string
	hard   bool
}

func (d *svcDenial) Error() string { return d.reason }

// svcVettedTarget is the outcome of vetSvcHost: the addresses the dialer is
// pinned to, whether every one of them is loopback (so TLS verification may be
// skipped), and the denial, if any, for the policy to act on.
type svcVettedTarget struct {
	addrs    []netip.Addr
	loopback bool
	denied   *svcDenial
}

// svcHardBlockedPrefixes lists the cloud metadata endpoints that live outside
// the link-local class and so would otherwise be dialable under warn (the
// default) and allow-any, or by putting them in --svc-allow-cidr. Every entry
// hands out instance credentials to whatever can reach it; none is ever
// overridable. Link-local (169.254.0.0/16, fe80::/10), unspecified and
// multicast are blocked as classes in isHardBlockedAddr and need no entry.
var svcHardBlockedPrefixes = []netip.Prefix{
	// AWS IMDS over IPv6. It is a ULA (fd00::/8), not link-local, so the
	// class checks do not catch it.
	netip.MustParsePrefix("fd00:ec2::254/128"),
	// Alibaba Cloud metadata. Sits inside the CGNAT range (100.64.0.0/10),
	// which operators legitimately allow for cluster networks, so only the
	// endpoint itself is blocked.
	netip.MustParsePrefix("100.100.100.200/32"),
}

// svcNAT64Prefix is the well-known NAT64 prefix (RFC 6052 / RFC 6146). A
// host on a NAT64 network reaches 169.254.169.254 as 64:ff9b::a9fe:a9fe, so
// the embedded IPv4 is classified as if it had been dialed directly.
var svcNAT64Prefix = netip.MustParsePrefix("64:ff9b::/96")

// isHardBlockedAddr reports the addresses no policy may open up: link-local
// (169.254.0.0/16, fe80::/10 — cloud metadata lives here), unspecified,
// multicast, and the svcHardBlockedPrefixes metadata endpoints outside those
// classes. IPv4-mapped IPv6 forms are unmapped first so ::ffff:169.254.1.1
// cannot slip past, and a NAT64 address is judged by the IPv4 it embeds.
func isHardBlockedAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() ||
		a.IsUnspecified() ||
		a.IsMulticast() ||
		a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range svcHardBlockedPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	if svcNAT64Prefix.Contains(a) {
		raw := a.As16()
		return isHardBlockedAddr(netip.AddrFrom4([4]byte{raw[12], raw[13], raw[14], raw[15]}))
	}
	return false
}

// allowAddr applies the policy to one address. clusterName says the address
// came from resolving a cluster-DNS name (allowed as a class in kubernetes
// mode; a ClusterIP is not on any operator allow list).
func (c svcProxyConfig) allowAddr(a netip.Addr, clusterName bool) *svcDenial {
	a = a.Unmap()
	if isHardBlockedAddr(a) {
		return &svcDenial{reason: fmt.Sprintf("%s is link-local, unspecified, multicast or a cloud metadata endpoint; never allowed", a), hard: true}
	}
	if a.IsLoopback() {
		return nil
	}
	if clusterName && c.allowCluster {
		return nil
	}
	for _, p := range c.AllowedCIDRs {
		if p.Contains(a) {
			return nil
		}
	}
	return &svcDenial{reason: fmt.Sprintf("%s is not loopback and not in --svc-allow-cidr", a)}
}

// isAllowedSvcHost classifies a target host without touching DNS: loopback is
// always allowed; cluster-DNS names only when allowCluster (kubernetes mode);
// literal IPs only when inside allow (and never when link-local, unspecified
// or multicast, even if a prefix in allow covers them); any other hostname is
// not decidable here and reports false — vetSvcHost resolves it and applies
// the same address checks to every A/AAAA record.
func isAllowedSvcHost(host string, allowCluster bool, allow []netip.Prefix) bool {
	cfg := svcProxyConfig{SvcProxyOptions: SvcProxyOptions{AllowedCIDRs: allow}, allowCluster: allowCluster}
	if isLoopbackHost(host) {
		return true
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return cfg.allowAddr(a, false) == nil
	}
	return allowCluster && isClusterDNSHost(host)
}

// vetSvcHost decides whether the agent may dial host and, either way, pins
// the addresses a dial must use: literals are used as-is; hostnames are
// resolved once here and every resolved address is checked, so a name that
// points at an allowed address cannot be re-pointed (DNS rebinding) between
// the check and the dial. A resolution failure is returned as a plain error;
// a policy denial is reported in the result's denied field so warn mode can
// still dial the very addresses that were vetted.
func vetSvcHost(ctx context.Context, host string, cfg svcProxyConfig) (svcVettedTarget, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		a = a.Unmap()
		return svcVettedTarget{addrs: []netip.Addr{a}, loopback: a.IsLoopback(), denied: cfg.allowAddr(a, false)}, nil
	}

	clusterName := isClusterDNSHost(host)
	if clusterName && !cfg.allowCluster {
		// Do not resolve: in server mode a .svc name is just a hostname the
		// provider should never have sent.
		return svcVettedTarget{denied: &svcDenial{reason: "cluster-DNS names are only allowed in kubernetes mode"}}, nil
	}

	addrs, err := cfg.resolver()(ctx, host)
	if err != nil || len(addrs) == 0 {
		if host == "localhost" {
			// A host without "localhost" in /etc/hosts should still reach its
			// own loopback.
			addrs = []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")}
		} else if err != nil {
			return svcVettedTarget{}, fmt.Errorf("resolving %q: %w", host, err)
		} else {
			return svcVettedTarget{}, fmt.Errorf("resolving %q: no addresses", host)
		}
	}

	out := svcVettedTarget{loopback: true}
	for _, a := range addrs {
		a = a.Unmap()
		out.addrs = append(out.addrs, a)
		if !a.IsLoopback() {
			out.loopback = false
		}
		if d := cfg.allowAddr(a, clusterName); d != nil {
			// Keep the harder denial: a hard block must win over "not in
			// allow list" so warn mode cannot dial it.
			if out.denied == nil || (d.hard && !out.denied.hard) {
				out.denied = &svcDenial{reason: fmt.Sprintf("%s resolves to %s", host, d.reason), hard: d.hard}
			}
		}
	}
	return out, nil
}

// pinnedDialer returns a DialContext that ignores the requested host and
// dials the vetted addresses (on the requested port) in order, returning the
// first connection that succeeds. Both the ReverseProxy transport and the
// WebSocket upgrade path go through it, so nothing re-resolves the name.
func pinnedDialer(vetted svcVettedTarget, dial svcDialer) svcDialer {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, a := range vetted.addrs {
			conn, err := dial(ctx, network, net.JoinHostPort(a.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no vetted address to dial")
		}
		return nil, lastErr
	}
}
