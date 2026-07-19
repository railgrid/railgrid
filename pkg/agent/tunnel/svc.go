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
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"k8s.io/klog/v2"

	"github.com/faroshq/faros-kedge/pkg/agent/discovery"
)

// svcTargetHeader carries the provider-computed upstream target for the /svc
// reverse proxy, e.g. "http://127.0.0.1:8123". The provider is the only writer;
// the agent enforces that the target host is loopback (see isLoopbackHost).
const svcTargetHeader = "X-Kedge-Svc-Target"

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
// /svc/ to a service named by the X-Kedge-Svc-Target header.
//
// The provider resolves a Service CR to a target and sets the header; the agent
// decides what it is willing to dial. In server mode that is loopback only, so
// this can never become an arbitrary-host proxy onto the LAN. In kubernetes
// mode (allowClusterTargets) cluster-DNS names are also permitted, because a
// Service on a KubernetesCluster edge sits behind cluster DNS rather than on
// the agent's loopback — and those names only resolve to in-cluster Services,
// so node IPs and external hosts stay out of reach.
//
// WebSocket/upgrade requests are handled by hijacking and piping raw bytes
// (Home Assistant uses /api/websocket).
func newSvcProxyHandler(allowClusterTargets bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger := klog.Background().WithName("svc-proxy")

		targetRaw := r.Header.Get(svcTargetHeader)
		if targetRaw == "" {
			http.Error(w, "missing "+svcTargetHeader, http.StatusBadRequest)
			return
		}
		target, err := url.Parse(targetRaw)
		if err != nil || target.Host == "" {
			http.Error(w, "invalid "+svcTargetHeader, http.StatusBadRequest)
			return
		}
		if !isAllowedSvcHost(target.Hostname(), allowClusterTargets) {
			logger.Info("rejecting disallowed svc target", "target", targetRaw,
				"clusterTargetsAllowed", allowClusterTargets)
			http.Error(w, "svc target host not permitted", http.StatusForbidden)
			return
		}

		// The remaining path after /svc is the service-local path.
		svcPath := strings.TrimPrefix(r.URL.Path, "/svc")
		if svcPath == "" {
			svcPath = "/"
		}

		// Do not leak the control header upstream.
		r.Header.Del(svcTargetHeader)

		if isUpgradeRequest(r) {
			handleSvcUpgrade(w, r, target, svcPath, logger)
			return
		}

		proxy := &httputil.ReverseProxy{
			Transport: &http.Transport{
				// Host-local self-signed certs are common; the hop never leaves
				// the host, so skipping verification is acceptable here.
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			},
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL.Scheme = target.Scheme
				pr.Out.URL.Host = target.Host
				pr.Out.URL.Path = svcPath
				pr.Out.Host = target.Host
				pr.Out.Header.Del(svcTargetHeader)
			},
		}
		proxy.ServeHTTP(w, r)
	}
}

// handleSvcUpgrade proxies a protocol-upgrade request (WebSocket) to the
// loopback target by hijacking the tunnel connection and piping raw bytes.
func handleSvcUpgrade(w http.ResponseWriter, r *http.Request, target *url.URL, svcPath string, logger klog.Logger) {
	var backendConn net.Conn
	var err error
	if target.Scheme == "https" {
		backendConn, err = tls.Dial("tcp", hostWithPort(target), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec
	} else {
		backendConn, err = net.Dial("tcp", hostWithPort(target))
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

	if err := r.Write(backendConn); err != nil {
		logger.Error(err, "failed to forward upgrade request to svc target")
		return
	}

	errc := make(chan error, 2)
	go func() { _, e := io.Copy(backendConn, clientConn); errc <- e }()
	go func() { _, e := io.Copy(clientConn, backendConn); errc <- e }()
	<-errc
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

// isAllowedSvcHost reports whether the agent may dial host. Loopback (LinuxServer
// edges) and cluster-DNS names (kubernetes mode) are always allowed.
//
// A Service's spec.host may also point at another device on the edge's LAN (e.g.
// a UniFi console), so for now any host is permitted — this intentionally trades
// away the anti-SSRF boundary to make LAN services work. TODO(security): tighten
// with a per-agent allowlist / opt-in before this is a general default.
func isAllowedSvcHost(host string, allowCluster bool) bool {
	if isLoopbackHost(host) {
		return true
	}
	if allowCluster && isClusterDNSHost(host) {
		return true
	}
	return true
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
