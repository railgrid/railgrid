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
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"k8s.io/klog/v2"

	"github.com/faroshq/faros/pkg/agent/discovery"
)

// svcTargetHeader carries the provider-computed upstream target for the /svc
// reverse proxy, e.g. "http://127.0.0.1:8123". The provider is the only writer;
// the agent decides whether it will dial the host (see vetSvcHost).
const svcTargetHeader = "X-Faros-Svc-Target"

// svcTLSInsecureHeader is set to "true" by the provider when the Service has
// spec.tlsInsecureSkipVerify. The agent verifies upstream TLS certificates for
// every non-loopback target unless this header is present.
const svcTLSInsecureHeader = "X-Faros-Svc-TLS-Insecure"

// svcPolicyHeader is the response header the agent stamps when the host
// policy had something to say: "warn" when a disallowed target was dialed
// anyway under --svc-policy=warn, "enforce" on the 403 that refuses it.
const svcPolicyHeader = "X-Faros-Svc-Policy"

// servicesResponse is the JSON body of GET /api/v1/services.
type servicesResponse struct {
	Services []discovery.DiscoveredService `json:"services"`
}

// newServicesHandler runs the host service detectors and returns the result.
// It is provider-pulled over the tunnel by the discovery reconciler.
func newServicesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svcs := discovery.Run(r.Context(), discovery.DefaultDetectors())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(servicesResponse{Services: svcs})
	}
}

// newSvcProxyHandler reverse-proxies requests arriving over the tunnel under
// /svc/ to a service named by the X-Faros-Svc-Target header.
//
// The provider resolves a Service CR to a target and sets the header; the agent
// decides what it is willing to dial, and that decision is the SSRF boundary
// for everything a workspace member can put in Service.spec.host:
//
//   - loopback is always allowed;
//   - cluster-DNS names ({name}.{namespace}.svc[.cluster.local]) are allowed
//     in kubernetes mode only, where they resolve inside the cluster's DNS;
//   - other literal IPs, and the addresses other hostnames resolve to, are
//     allowed only when inside --svc-allow-cidr;
//   - link-local (cloud metadata), unspecified and multicast addresses are
//     never allowed, whatever the allow list or policy says.
//
// Hostnames are resolved once and the dial is pinned to the vetted addresses,
// so DNS rebinding cannot swap the target after the check. --svc-policy picks
// what a denial does: enforce answers 403 without dialing, warn dials but logs
// and stamps X-Faros-Svc-Policy: warn, allow-any skips the allow list.
//
// TLS verification is skipped only for loopback targets (host-local
// self-signed certs are common and the hop never leaves the host) or when the
// provider sets X-Faros-Svc-TLS-Insecure from Service.spec.tlsInsecureSkipVerify.
//
// WebSocket/upgrade requests are handled by hijacking and piping raw bytes
// (Home Assistant uses /api/websocket).
func newSvcProxyHandler(cfg svcProxyConfig) http.HandlerFunc {
	policy := cfg.policy()
	return func(w http.ResponseWriter, r *http.Request) {
		logger := klog.Background().WithName("svc-proxy")

		targetRaw := r.Header.Get(svcTargetHeader)
		if targetRaw == "" {
			http.Error(w, "missing "+svcTargetHeader, http.StatusBadRequest)
			return
		}
		target, err := url.Parse(targetRaw)
		if err != nil || target.Host == "" || target.Hostname() == "" {
			http.Error(w, "invalid "+svcTargetHeader, http.StatusBadRequest)
			return
		}

		vetted, err := vetSvcHost(r.Context(), target.Hostname(), cfg)
		if err != nil {
			logger.Error(err, "failed to resolve svc target", "target", targetRaw)
			http.Error(w, "upstream error", http.StatusBadGateway)
			return
		}
		warned := false
		if d := vetted.denied; d != nil {
			switch {
			case d.hard || policy == SvcPolicyEnforce:
				logger.Info("rejecting disallowed svc target", "target", targetRaw, "reason", d.reason,
					"policy", policy, "clusterTargetsAllowed", cfg.allowCluster)
				writeSvcDenied(w, d.reason)
				return
			case policy == SvcPolicyWarn:
				logger.Info("svc target would be denied under --svc-policy=enforce; dialing anyway (warn mode). "+
					"Add the host to --svc-allow-cidr before the default flips to enforce.",
					"target", targetRaw, "reason", d.reason)
				warned = true
			default: // allow-any
				logger.V(2).Info("svc target outside the allow list; --svc-policy=allow-any", "target", targetRaw, "reason", d.reason)
			}
		}

		// The remaining path after /svc is the service-local path.
		svcPath := strings.TrimPrefix(r.URL.Path, "/svc")
		if svcPath == "" {
			svcPath = "/"
		}

		// TLS verification is skipped only on the host's own loopback, or when
		// the Service explicitly opted out (spec.tlsInsecureSkipVerify).
		insecure := vetted.loopback || strings.EqualFold(r.Header.Get(svcTLSInsecureHeader), "true")
		tlsConfig := &tls.Config{
			InsecureSkipVerify: insecure, //nolint:gosec // loopback or explicit per-Service opt-in only
			ServerName:         target.Hostname(),
			MinVersion:         tls.VersionTLS12,
		}
		dial := pinnedDialer(vetted, cfg.dialer())

		// Do not leak the control headers upstream.
		r.Header.Del(svcTargetHeader)
		r.Header.Del(svcTLSInsecureHeader)

		if isUpgradeRequest(r) {
			handleSvcUpgrade(w, r, target, svcPath, dial, tlsConfig, warned, logger)
			return
		}

		proxy := &httputil.ReverseProxy{
			Transport: &http.Transport{
				DialContext:     dial,
				TLSClientConfig: tlsConfig,
			},
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL.Scheme = target.Scheme
				pr.Out.URL.Host = target.Host
				pr.Out.URL.Path = svcPath
				pr.Out.Host = target.Host
				pr.Out.Header.Del(svcTargetHeader)
				pr.Out.Header.Del(svcTLSInsecureHeader)
			},
		}
		proxy.ModifyResponse = func(resp *http.Response) error {
			stampSvcPolicy(resp.Header, warned)
			return nil
		}
		proxy.ServeHTTP(w, r)
	}
}

// stampSvcPolicy makes X-Faros-Svc-Policy on a proxied response say exactly
// what this hop decided. The header is the agent's verdict, which the provider
// turns into EdgeService conditions (a 403 carrying "enforce" is read as "the
// agent refused spec.host"), so whatever the upstream service put there is
// dropped before the agent's own value — "warn" only when this hop dialed a
// target it would refuse under enforce — is set.
func stampSvcPolicy(h http.Header, warned bool) {
	h.Del(svcPolicyHeader)
	if warned {
		h.Set(svcPolicyHeader, string(SvcPolicyWarn))
	}
}

// writeSvcDenied answers a refused target: 403 with a small JSON body and the
// policy header, so the provider can tell an agent refusal from a 403 the
// service itself returned.
func writeSvcDenied(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(svcPolicyHeader, string(SvcPolicyEnforce))
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":  "target host not allowed",
		"reason": reason,
	})
}

// handleSvcUpgrade proxies a protocol-upgrade request (WebSocket) to the
// vetted target by hijacking the tunnel connection and piping raw bytes. The
// dial is pinned (dial), and TLS is layered on top with tlsConfig so the
// upgrade path verifies certificates exactly like the plain-HTTP path. The
// upstream's response head is parsed and re-emitted so the policy header is
// stamped by this hop (stampSvcPolicy) rather than copied from the service;
// everything after the head is piped untouched.
func handleSvcUpgrade(w http.ResponseWriter, r *http.Request, target *url.URL, svcPath string, dial svcDialer, tlsConfig *tls.Config, warned bool, logger klog.Logger) {
	backendConn, err := dial(r.Context(), "tcp", hostWithPort(target))
	if err == nil && target.Scheme == "https" {
		tc := tls.Client(backendConn, tlsConfig)
		if err = tc.HandshakeContext(r.Context()); err != nil {
			_ = backendConn.Close()
		} else {
			backendConn = tc
		}
	}
	if err != nil {
		logger.Error(err, "failed to connect to svc target", "target", target.String())
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer backendConn.Close() //nolint:errcheck

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		logger.Error(err, "failed to hijack connection for svc upgrade")
		return
	}
	defer clientConn.Close() //nolint:errcheck

	r.URL.Scheme = target.Scheme
	r.URL.Host = target.Host
	r.URL.Path = svcPath
	r.Host = target.Host
	r.Header.Del(svcTargetHeader)
	r.Header.Del(svcTLSInsecureHeader)

	if err := r.Write(backendConn); err != nil {
		logger.Error(err, "failed to forward upgrade request to svc target")
		return
	}

	// Read the response head off the backend, restamp the policy header and
	// hand the head to the client; br keeps any bytes read past the head.
	br := bufio.NewReader(backendConn)
	resp, err := http.ReadResponse(br, r)
	if err != nil {
		logger.Error(err, "failed to read upgrade response from svc target", "target", target.String())
		return
	}
	stampSvcPolicy(resp.Header, warned)
	if err := writeResponseHead(clientConn, resp); err != nil {
		logger.Error(err, "failed to forward upgrade response to caller")
		return
	}

	errc := make(chan error, 2)
	go func() { _, e := io.Copy(backendConn, clientConn); errc <- e }()
	go func() { _, e := io.Copy(clientConn, br); errc <- e }()
	<-errc
}

// writeResponseHead writes resp's status line and headers, byte-faithful to
// what the upstream sent apart from the header edits made on resp.Header. The
// body (if the upgrade was refused with one) is not consumed here; the caller
// pipes the remaining bytes as-is.
func writeResponseHead(w io.Writer, resp *http.Response) error {
	if _, err := fmt.Fprintf(w, "HTTP/%d.%d %s\r\n", resp.ProtoMajor, resp.ProtoMinor, resp.Status); err != nil {
		return err
	}
	if err := resp.Header.Write(w); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\r\n")
	return err
}

// hostWithPort returns host:port, defaulting the port from the scheme.
func hostWithPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "https" {
		return net.JoinHostPort(u.Hostname(), "443")
	}
	return net.JoinHostPort(u.Hostname(), "80")
}

// isLoopbackHost reports whether host is a loopback address or "localhost".
// String comparison is not enough (e.g. "127.0.0.1" vs "127.0.0.2"), so parse
// the IP and check IsLoopback; "localhost" is accepted by name.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isClusterDNSHost reports whether host is a Kubernetes cluster-DNS Service
// name ({name}.{namespace}.svc[.cluster.local]). Such names only resolve inside
// the cluster's DNS, which is what keeps this from becoming a general proxy:
// an IP literal or an external domain never matches. A bare ".svc" or
// ".svc.cluster.local" with no service/namespace in front is rejected.
func isClusterDNSHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if net.ParseIP(h) != nil {
		return false // IP literals never qualify, whatever they look like
	}
	for _, suffix := range []string{".svc", ".svc.cluster.local"} {
		if !strings.HasSuffix(h, suffix) {
			continue
		}
		// Require {name}.{namespace} ahead of the suffix.
		return strings.Count(strings.TrimSuffix(h, suffix), ".") == 1
	}
	return false
}
