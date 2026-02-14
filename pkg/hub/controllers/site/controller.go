package site

import (
	"context"
	"time"

	"k8s.io/klog/v2"
)

const (
	// HeartbeatTimeout is the duration after which a site is considered disconnected.
	HeartbeatTimeout = 5 * time.Minute
	// GCTimeout is the duration after which a disconnected site is garbage collected.
	GCTimeout = 24 * time.Hour
)

// Controller manages Site lifecycle (heartbeat monitoring, GC).
type Controller struct{}

// NewController creates a new site lifecycle controller.
func NewController() *Controller {
	return &Controller{}
}

// Run starts the site lifecycle controller.
func (c *Controller) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting site lifecycle controller")

	// TODO: Watch Site status heartbeats
	// TODO: Mark disconnected if stale
	// TODO: GC if threshold exceeded

	<-ctx.Done()
	return nil
}
