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
func newRemoteServer(downstream *rest.Config) (*http.Server, error) {
	router := setupRouter(downstream)

	return &http.Server{
		Handler: router,
	}, nil
}

// setupRouter configures the mux router for the local server.
func setupRouter(downstream *rest.Config) *mux.Router {
	router := mux.NewRouter()

	// SSH handler
	router.HandleFunc("/ssh", sshHandler).Methods("GET")

	// K8s proxy handler
	router.PathPrefix("/k8s/").HandlerFunc(k8sHandler(downstream))

	// Status/health endpoint
	router.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}).Methods("GET")

	return router
}

// sshHandler handles SSH connections over the tunnel.
func sshHandler(w http.ResponseWriter, r *http.Request) {
	logger := klog.Background().WithName("ssh-handler")
	logger.Info("SSH connection request")

	// TODO: Start SSH server with pty
	// Uses creack/pty + golang.org/x/crypto/ssh
	http.Error(w, "SSH not yet implemented", http.StatusNotImplemented)
}

// k8sHandler creates an HTTP handler that proxies requests to the local Kubernetes API.
func k8sHandler(config *rest.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger := klog.Background().WithName("k8s-handler")

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
			tlsConfig = &tls.Config{}
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
	defer backendConn.Close()

	// Hijack the client connection
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
	defer clientConn.Close()

	// Modify and forward the request to the backend
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

	// Bidirectional copy
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
