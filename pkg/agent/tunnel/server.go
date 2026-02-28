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

// Package tunnel implements reverse-dial tunneling between agent and hub.
package tunnel

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/gorilla/mux"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// newRemoteServer creates the local HTTP server that is served on the revdial.Listener.
// It handles requests from the hub that are tunneled back to the agent.
func newRemoteServer(downstream *rest.Config, sshPort int) (*http.Server, error) {
	router := setupRouter(downstream, sshPort)
	return &http.Server{Handler: router}, nil
}

// setupRouter configures the mux router for the local server.
func setupRouter(downstream *rest.Config, sshPort int) *mux.Router {
	router := mux.NewRouter()

	// SSH handler — proxies the revdial connection to the host sshd on sshPort.
	router.HandleFunc("/ssh", newSSHHandler(sshPort)).Methods("GET")

	// K8s proxy handler — only registered when a downstream k8s config is present.
	// In server mode (downstream == nil) k8s proxying is not available.
	if downstream != nil {
		router.PathPrefix("/k8s/").HandlerFunc(k8sHandler(downstream))
	} else {
		router.PathPrefix("/k8s/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "k8s proxy not available in server mode", http.StatusServiceUnavailable)
		})
	}

	// Status/health endpoint
	router.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}).Methods("GET")

	return router
}

// newSSHHandler returns an http.HandlerFunc that proxies SSH connections arriving
// over the revdial tunnel to the host SSH daemon on localhost:<sshPort>.
//
// Protocol: the hub sends GET /ssh with "Upgrade: ssh-tunnel" headers.
// The agent responds with 101 Switching Protocols, then hijacks the connection
// and pipes raw bytes to the local sshd.  After the 101 response the hub speaks
// the full SSH protocol directly — no additional framing is needed.
func newSSHHandler(sshPort int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger := klog.Background().WithName("ssh-handler")
		logger.Info("SSH connection request received", "sshPort", sshPort)

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			logger.Error(nil, "ResponseWriter does not support hijacking")
			http.Error(w, "hijacking not supported", http.StatusInternalServerError)
			return
		}

		// Send 101 Switching Protocols BEFORE hijacking so the hub knows the
		// HTTP layer is done and raw SSH traffic can begin.
		w.Header().Set("Connection", "Upgrade")
		w.Header().Set("Upgrade", "ssh-tunnel")
		w.WriteHeader(http.StatusSwitchingProtocols)

		tunnelConn, brw, err := hijacker.Hijack()
		if err != nil {
			logger.Error(err, "failed to hijack connection")
			return
		}
		defer tunnelConn.Close() //nolint:errcheck

		// Flush the buffered 101 response headers to the wire.
		if err := brw.Flush(); err != nil {
			logger.Error(err, "failed to flush 101 response")
			return
		}

		// Dial the host sshd.
		addr := fmt.Sprintf("localhost:%d", sshPort)
		sshdConn, err := net.Dial("tcp", addr)
		if err != nil {
			logger.Error(err, "failed to connect to sshd", "addr", addr)
			// Cannot write HTTP error after hijack; just close.
			return
		}
		defer sshdConn.Close() //nolint:errcheck

		logger.Info("SSH tunnel established", "remote", r.RemoteAddr, "sshPort", sshPort)

		// Bidirectional pipe: hub <-> revdial conn <-> sshd
		errc := make(chan error, 2)
		go func() {
			_, copyErr := io.Copy(sshdConn, tunnelConn)
			errc <- copyErr
		}()
		go func() {
			_, copyErr := io.Copy(tunnelConn, sshdConn)
			errc <- copyErr
		}()

		// Wait for either side to finish (EOF or error).
		if err := <-errc; err != nil {
			logger.V(4).Info("SSH tunnel copy finished", "reason", err)
		}
		logger.Info("SSH tunnel closed", "remote", r.RemoteAddr)
	}
}

// k8sHandler creates an HTTP handler that proxies requests to the local Kubernetes API.
func k8sHandler(config *rest.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger := klog.Background().WithName("k8s-handler")
		logger.Info("K8s API request received", "path", r.URL.Path)

		// Strip the /k8s prefix
		k8sPath := strings.TrimPrefix(r.URL.Path, "/k8s")
		if k8sPath == "" {
			k8sPath = "/"
		}

		// Check if this is an upgrade request (exec, port-forward)
		if isUpgradeRequest(r) {
			handleK8sUpgrade(w, r, config, k8sPath)
			return
		}

		// Build target URL
		target, err := url.Parse(config.Host)
		if err != nil {
			logger.Error(err, "failed to parse K8s API URL")
			http.Error(w, "invalid K8s API URL", http.StatusInternalServerError)
			return
		}

		// Create reverse proxy
		proxy := httputil.NewSingleHostReverseProxy(target)

		// Configure TLS
		tlsConfig, err := rest.TLSConfigFor(config)
		if err != nil {
			logger.Error(err, "failed to create TLS config")
			http.Error(w, "TLS config error", http.StatusInternalServerError)
			return
		}
		if tlsConfig == nil {
			tlsConfig = &tls.Config{} //nolint:gosec
		}

		proxy.Transport = &http.Transport{
			TLSClientConfig: tlsConfig,
		}

		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = k8sPath
			req.Host = target.Host

			// Add bearer token if configured
			if config.BearerToken != "" {
				req.Header.Set("Authorization", "Bearer "+config.BearerToken)
			}
		}

		proxy.ServeHTTP(w, r)
	}
}

// handleK8sUpgrade handles protocol upgrade requests (exec, port-forward).
func handleK8sUpgrade(w http.ResponseWriter, r *http.Request, config *rest.Config, k8sPath string) {
	logger := klog.Background().WithName("k8s-upgrade")

	target, err := url.Parse(config.Host)
	if err != nil {
		logger.Error(err, "failed to parse K8s API URL")
		http.Error(w, "invalid K8s API URL", http.StatusInternalServerError)
		return
	}

	// Dial the K8s API server
	tlsConfig, err := rest.TLSConfigFor(config)
	if err != nil {
		logger.Error(err, "failed to create TLS config")
		http.Error(w, "TLS config error", http.StatusInternalServerError)
		return
	}

	var backendConn net.Conn
	if tlsConfig != nil {
		backendConn, err = tls.Dial("tcp", target.Host, tlsConfig)
	} else {
		backendConn, err = net.Dial("tcp", target.Host)
	}
	if err != nil {
		logger.Error(err, "failed to connect to K8s API")
		http.Error(w, fmt.Sprintf("failed to connect to K8s API: %v", err), http.StatusBadGateway)
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
		logger.Error(err, "failed to hijack connection")
		return
	}
	defer clientConn.Close() //nolint:errcheck

	r.URL.Path = k8sPath
	r.URL.Host = target.Host
	r.URL.Scheme = target.Scheme
	if config.BearerToken != "" {
		r.Header.Set("Authorization", "Bearer "+config.BearerToken)
	}

	if err := r.Write(backendConn); err != nil {
		logger.Error(err, "failed to write request to backend")
		return
	}

	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(backendConn, clientConn)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(clientConn, backendConn)
		errc <- err
	}()

	<-errc
}

// isUpgradeRequest checks if the request wants a protocol upgrade.
func isUpgradeRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Connection"), "Upgrade")
}
