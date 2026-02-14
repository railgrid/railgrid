package agent

import (
	"context"
	"fmt"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

// Options holds configuration for the agent.
type Options struct {
	HubURL     string
	Token      string
	SiteName   string
	Kubeconfig string
	Context    string
	Labels     map[string]string
}

// NewOptions returns default agent options.
func NewOptions() *Options {
	return &Options{
		Labels: make(map[string]string),
	}
}

// Agent is the kedge agent that connects a site to the hub.
type Agent struct {
	opts             *Options
	downstreamConfig *rest.Config
}

// New creates a new agent.
func New(opts *Options) (*Agent, error) {
	if opts.HubURL == "" {
		return nil, fmt.Errorf("hub URL is required")
	}
	if opts.SiteName == "" {
		return nil, fmt.Errorf("site name is required")
	}

	// Build downstream (target cluster) config
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		rules.ExplicitPath = opts.Kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if opts.Context != "" {
		overrides.CurrentContext = opts.Context
	}
	downstreamConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build downstream config: %w", err)
	}

	return &Agent{
		opts:             opts,
		downstreamConfig: downstreamConfig,
	}, nil
}

// Run starts the agent and blocks until the context is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting kedge agent",
		"hubURL", a.opts.HubURL,
		"siteName", a.opts.SiteName,
		"labels", a.opts.Labels,
	)

	// 1. Connect tunnel: WebSocket dial to hub's edge-proxy VW
	logger.Info("Connecting tunnel to hub")

	// 2. Create revdial.Listener from connection
	// 3. Start local HTTP server on revdial.Listener
	// 4. Register/update Site on hub
	// 5. Start workload reconciler
	// 6. Start status reporter (heartbeat + workload status)
	// 7. Reconnect loop with exponential backoff

	logger.Info("Agent started successfully")
	<-ctx.Done()
	logger.Info("Agent shutting down")

	return nil
}
