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

package kcp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kcp-dev/embeddedetcd"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/server"
	serveroptions "github.com/kcp-dev/kcp/pkg/server/options"
)

// EmbeddedKCPOptions contains configuration for the embedded kcp server.
type EmbeddedKCPOptions struct {
	RootDir          string
	SecurePort       int
	BatteriesInclude []string
}

// EmbeddedKCP wraps a kcp server that runs in-process.
type EmbeddedKCP struct {
	opts   EmbeddedKCPOptions
	server *server.Server

	// readyCh is closed when kcp is ready to serve requests.
	readyCh chan struct{}
	// adminConfig is the rest.Config for the kcp admin user.
	adminConfig *rest.Config
}

// NewEmbeddedKCP creates a new embedded kcp instance.
func NewEmbeddedKCP(opts EmbeddedKCPOptions) *EmbeddedKCP {
	if opts.RootDir == "" {
		opts.RootDir = ".kcp"
	}
	if opts.SecurePort == 0 {
		opts.SecurePort = 6443
	}
	if len(opts.BatteriesInclude) == 0 {
		opts.BatteriesInclude = []string{"admin", "user"}
	}
	return &EmbeddedKCP{
		opts:    opts,
		readyCh: make(chan struct{}),
	}
}

// Run starts the embedded kcp server and blocks until context is cancelled.
// It returns an error if the server fails to start.
func (e *EmbeddedKCP) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting embedded kcp server", "rootDir", e.opts.RootDir, "securePort", e.opts.SecurePort)

	// Create kcp server options.
	kcpOpts := serveroptions.NewOptions(e.opts.RootDir)

	// Configure secure serving port.
	kcpOpts.GenericControlPlane.SecureServing.BindPort = e.opts.SecurePort

	// Configure batteries.
	kcpOpts.Extra.BatteriesIncluded = e.opts.BatteriesInclude

	// Enable embedded etcd.
	kcpOpts.EmbeddedEtcd.Enabled = true

	// Complete options.
	completedOpts, err := kcpOpts.Complete(ctx, e.opts.RootDir)
	if err != nil {
		return fmt.Errorf("completing kcp options: %w", err)
	}

	// Validate options.
	if errs := completedOpts.Validate(); len(errs) > 0 {
		return fmt.Errorf("validating kcp options: %v", errs)
	}

	logger.Info("Running kcp with batteries", "batteries", strings.Join(completedOpts.Extra.BatteriesIncluded, ","))

	// Create server config.
	serverConfig, err := server.NewConfig(ctx, *completedOpts)
	if err != nil {
		return fmt.Errorf("creating kcp server config: %w", err)
	}

	// Complete the config.
	completedConfig, err := serverConfig.Complete()
	if err != nil {
		return fmt.Errorf("completing kcp server config: %w", err)
	}

	// Start embedded etcd if configured.
	if completedConfig.EmbeddedEtcd.Config != nil {
		logger.Info("Starting embedded etcd")
		if err := embeddedetcd.NewServer(completedConfig.EmbeddedEtcd).Run(ctx); err != nil {
			return fmt.Errorf("starting embedded etcd: %w", err)
		}
	}

	// Create the kcp server.
	e.server, err = server.NewServer(completedConfig)
	if err != nil {
		return fmt.Errorf("creating kcp server: %w", err)
	}

	// Add a post-start hook to signal readiness.
	if err := e.server.AddPostStartHook("kedge-kcp-ready", func(hookContext genericapiserver.PostStartHookContext) error {
		// Wait for kcp phase 1 bootstrap to complete.
		e.server.WaitForPhase1Finished()

		// Build admin config from the generated admin kubeconfig.
		adminKubeconfigPath := filepath.Join(e.opts.RootDir, "admin.kubeconfig")
		adminConfig, err := clientcmd.BuildConfigFromFlags("", adminKubeconfigPath)
		if err != nil {
			logger.Error(err, "Failed to load admin kubeconfig, using loopback")
			e.adminConfig = rest.CopyConfig(hookContext.LoopbackClientConfig)
		} else {
			e.adminConfig = adminConfig
		}

		logger.Info("kcp server is ready")
		close(e.readyCh)
		return nil
	}); err != nil {
		return fmt.Errorf("adding post-start hook: %w", err)
	}

	// Run the server (blocks until context is cancelled).
	return e.server.Run(ctx)
}

// Ready returns a channel that is closed when kcp is ready to serve requests.
func (e *EmbeddedKCP) Ready() <-chan struct{} {
	return e.readyCh
}

// AdminConfig returns a rest.Config for the kcp admin user.
// This should only be called after Ready() returns.
func (e *EmbeddedKCP) AdminConfig() *rest.Config {
	return e.adminConfig
}

// AdminKubeconfigPath returns the path to the admin kubeconfig file.
func (e *EmbeddedKCP) AdminKubeconfigPath() string {
	return filepath.Join(e.opts.RootDir, "admin.kubeconfig")
}
