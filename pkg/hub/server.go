package hub

import (
	"context"
	"fmt"
	"net/http"
	"time"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/hub/bootstrap"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/scheduler"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/site"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/status"
	"github.com/faroshq/faros-kedge/pkg/util/connman"
	"github.com/faroshq/faros-kedge/pkg/virtual/builder"
	"github.com/gorilla/mux"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
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
		"listenAddr", s.opts.ListenAddr,
	)

	// 1. Build rest.Config
	config, err := s.buildRestConfig()
	if err != nil {
		return fmt.Errorf("building rest config: %w", err)
	}

	// 2. Bootstrap CRDs
	logger.Info("Installing CRDs")
	if err := bootstrap.InstallCRDs(ctx, config); err != nil {
		return fmt.Errorf("installing CRDs: %w", err)
	}

	// 3. Create dynamic client and informer factory
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	kedgeClient := kedgeclient.NewFromDynamic(dynamicClient)
	informerFactory := kedgeclient.NewInformerFactory(dynamicClient, kedgeclient.DefaultResyncPeriod)

	// 4. Create connection manager for tunnels
	connManager := connman.New()

	// 5. Create HTTP mux
	router := mux.NewRouter()

	// Tunnel handlers
	vws := builder.NewVirtualWorkspaces(connManager, logger)
	router.PathPrefix("/tunnel/").Handler(http.StripPrefix("/tunnel", vws.EdgeProxyHandler()))
	router.PathPrefix("/proxy/").Handler(http.StripPrefix("/proxy", vws.AgentProxyHandler()))

	// Health check
	router.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	router.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	// 6. Create and start controllers
	schedulerCtrl := scheduler.NewScheduler(kedgeClient, informerFactory)
	siteCtrl := site.NewController(kedgeClient, informerFactory)
	statusCtrl := status.NewAggregator(kedgeClient, informerFactory)

	// Start informers
	stopCh := ctx.Done()
	informerFactory.Start(stopCh)
	logger.Info("Waiting for informer cache sync")
	informerFactory.WaitForCacheSync(stopCh)
	logger.Info("Informer caches synced")

	// Start controllers in goroutines
	go func() {
		if err := schedulerCtrl.Run(ctx); err != nil {
			logger.Error(err, "Scheduler controller failed")
		}
	}()
	go func() {
		if err := siteCtrl.Run(ctx); err != nil {
			logger.Error(err, "Site controller failed")
		}
	}()
	go func() {
		if err := statusCtrl.Run(ctx); err != nil {
			logger.Error(err, "Status aggregator failed")
		}
	}()

	// 7. Start HTTP server
	httpServer := &http.Server{
		Addr:              s.opts.ListenAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down HTTP server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error(err, "HTTP server shutdown error")
		}
	}()

	logger.Info("Hub server started successfully", "addr", s.opts.ListenAddr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server error: %w", err)
	}

	return nil
}

func (s *Server) buildRestConfig() (*rest.Config, error) {
	if s.opts.Kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", s.opts.Kubeconfig)
	}
	if s.opts.ExternalKCPKubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", s.opts.ExternalKCPKubeconfig)
	}
	// Try in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to default kubeconfig
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
		return kubeConfig.ClientConfig()
	}
	return config, nil
}
