package status

import (
	"context"
	"time"

	"k8s.io/klog/v2"
)

const (
	// HeartbeatInterval is how often the agent sends heartbeats to the hub.
	HeartbeatInterval = 30 * time.Second
)

// Reporter sends heartbeats and workload status updates to the hub.
type Reporter struct {
	siteName string
}

// NewReporter creates a new status reporter.
func NewReporter(siteName string) *Reporter {
	return &Reporter{siteName: siteName}
}

// Run starts the status reporter.
func (r *Reporter) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting status reporter", "siteName", r.siteName)

	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.sendHeartbeat(ctx)
		}
	}
}

func (r *Reporter) sendHeartbeat(ctx context.Context) {
	logger := klog.FromContext(ctx)
	logger.V(4).Info("Sending heartbeat", "siteName", r.siteName)

	// TODO: Update Site.status.lastHeartbeatTime on hub
}
