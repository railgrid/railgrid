package agent

import (
	"context"
	"fmt"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/agent/reconciler"
	agentStatus "github.com/faroshq/faros-kedge/pkg/agent/status"
	"github.com/faroshq/faros-kedge/pkg/agent/tunnel"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

// Options holds configuration for the agent.
type Options struct {
	HubURL        string
	HubKubeconfig string
	HubContext    string
	TunnelURL     string // Separate URL for reverse tunnel (defaults to hubConfig.Host)
	Token         string
	SiteName      string
	Kubeconfig    string
	Context       string
	Labels        map[string]string
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
	hubConfig        *rest.Config
	downstreamConfig *rest.Config
}

// New creates a new agent.
func New(opts *Options) (*Agent, error) {
	if opts.SiteName == "" {
		return nil, fmt.Errorf("site name is required")
	}

	// Build hub config
	var hubConfig *rest.Config
	var err error
	if opts.HubKubeconfig != "" {
		rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: opts.HubKubeconfig}
		overrides := &clientcmd.ConfigOverrides{}
		if opts.HubContext != "" {
			overrides.CurrentContext = opts.HubContext
		}
		hubConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to build hub config from kubeconfig: %w", err)
		}
	} else if opts.HubURL != "" {
		hubConfig = &rest.Config{
			Host:        opts.HubURL,
			BearerToken: opts.Token,
			TLSClientConfig: rest.TLSClientConfig{
				Insecure: true,
			},
		}
	} else {
		return nil, fmt.Errorf("hub URL or hub kubeconfig is required")
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
		hubConfig:        hubConfig,
		downstreamConfig: downstreamConfig,
	}, nil
}

// Run starts the agent and blocks until the context is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting kedge agent",
		"siteName", a.opts.SiteName,
		"labels", a.opts.Labels,
	)

	// Create hub clients
	hubDynamic, err := dynamic.NewForConfig(a.hubConfig)
	if err != nil {
		return fmt.Errorf("creating hub dynamic client: %w", err)
	}
	hubClient := kedgeclient.NewFromDynamic(hubDynamic)

	// Create downstream client
	downstreamClient, err := kubernetes.NewForConfig(a.downstreamConfig)
	if err != nil {
		return fmt.Errorf("creating downstream client: %w", err)
	}

	// Register/update Site on hub
	if err := a.registerSite(ctx, hubClient); err != nil {
		return fmt.Errorf("registering site: %w", err)
	}

	// Start reverse tunnel to hub
	tunnelURL := a.opts.TunnelURL
	if tunnelURL == "" {
		tunnelURL = a.hubConfig.Host
	}
	tunnelState := make(chan bool, 1)
	go tunnel.StartProxyTunnel(ctx, tunnelURL, a.hubConfig.BearerToken, a.opts.SiteName, a.downstreamConfig, tunnelState)

	// Start workload reconciler
	wkr := reconciler.NewWorkloadReconciler(a.opts.SiteName, hubClient, hubDynamic, downstreamClient)
	go func() {
		if err := wkr.Run(ctx); err != nil {
			logger.Error(err, "Workload reconciler failed")
		}
	}()

	// Start status reporter (heartbeat + workload status)
	reporter := agentStatus.NewReporter(a.opts.SiteName, hubClient, downstreamClient)
	go func() {
		if err := reporter.Run(ctx); err != nil {
			logger.Error(err, "Status reporter failed")
		}
	}()

	logger.Info("Agent started successfully")
	<-ctx.Done()
	logger.Info("Agent shutting down")

	return nil
}

func (a *Agent) registerSite(ctx context.Context, client *kedgeclient.Client) error {
	logger := klog.FromContext(ctx)

	site := &kedgev1alpha1.Site{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kedgev1alpha1.SchemeGroupVersion.String(),
			Kind:       "Site",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   a.opts.SiteName,
			Labels: a.opts.Labels,
		},
		Spec: kedgev1alpha1.SiteSpec{
			DisplayName: a.opts.SiteName,
		},
	}

	existing, err := client.Sites().Get(ctx, a.opts.SiteName, metav1.GetOptions{})
	if err != nil {
		// Create new site
		logger.Info("Creating Site", "name", a.opts.SiteName)
		_, err = client.Sites().Create(ctx, site, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating site: %w", err)
		}
	} else {
		// Update existing site labels
		logger.Info("Updating Site", "name", a.opts.SiteName)
		existing.Labels = a.opts.Labels
		_, err = client.Sites().Update(ctx, existing, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("updating site: %w", err)
		}
	}

	// Update status to connected
	now := metav1.Now()
	statusSite := &kedgev1alpha1.Site{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kedgev1alpha1.SchemeGroupVersion.String(),
			Kind:       "Site",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: a.opts.SiteName,
		},
		Status: kedgev1alpha1.SiteStatus{
			Phase:             kedgev1alpha1.SitePhaseConnected,
			TunnelConnected:   true,
			LastHeartbeatTime: &now,
		},
	}
	_, err = client.Sites().UpdateStatus(ctx, statusSite, metav1.UpdateOptions{})
	if err != nil {
		logger.Error(err, "Failed to update site status to connected")
	}

	return nil
}
