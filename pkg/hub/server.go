package hub

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"
)

// Server is the kedge hub server orchestrator.
type Server struct {
	opts *Options
}

// NewServer creates a new hub server.
func NewServer(opts *Options) (*Server, error) {
	if opts == nil {
		return nil, fmt.Errorf("options must not be nil")
	}
	return &Server{opts: opts}, nil
}

// Run starts the hub server and blocks until the context is cancelled.
func (s *Server) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting kedge hub server",
		"dataDir", s.opts.DataDir,
		"listenAddr", s.opts.ListenAddr,
	)

	// 1. Start KCP (embedded or external)
	if s.opts.ExternalKCPKubeconfig != "" {
		logger.Info("Using external KCP", "kubeconfig", s.opts.ExternalKCPKubeconfig)
	} else {
		logger.Info("Starting embedded KCP")
	}

	// 2. Bootstrap workspace hierarchy + APIExports
	logger.Info("Bootstrapping workspace hierarchy")

	// 3. Build virtual workspaces (edge-proxy, agent-proxy, cluster-proxy)
	logger.Info("Building virtual workspaces")

	// 4. Start controllers (scheduler, site lifecycle, status aggregator)
	logger.Info("Starting controllers")

	// 5. Start auth HTTP handler (Dex callbacks)
	logger.Info("Starting auth handler")

	// 6. Block until ctx cancelled
	logger.Info("Hub server started successfully")
	<-ctx.Done()
	logger.Info("Hub server shutting down")

	return nil
}
