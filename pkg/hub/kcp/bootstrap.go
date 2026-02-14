package kcp

import (
	"context"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// Bootstrapper sets up the KCP workspace hierarchy and API exports.
type Bootstrapper struct {
	config *rest.Config
}

// NewBootstrapper creates a new bootstrapper.
func NewBootstrapper(config *rest.Config) *Bootstrapper {
	return &Bootstrapper{config: config}
}

// Bootstrap creates the workspace hierarchy:
//
//	root:kedge                     - Root kedge workspace
//	root:kedge:providers           - Holds APIExport "kedge.faros.sh"
//	root:kedge:tenants             - Parent for tenant workspaces
//	  root:kedge:tenants:{userID}  - Per-user workspace (created on login)
func (b *Bootstrapper) Bootstrap(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	logger.Info("Bootstrapping KCP workspace hierarchy")

	// TODO: Create workspace hierarchy
	// TODO: Apply APIResourceSchema for VirtualWorkload, Site, Placement, User
	// TODO: Create APIExport "kedge.faros.sh"
	// TODO: Create APIBinding in tenant workspaces

	logger.Info("Bootstrap complete")
	return nil
}
