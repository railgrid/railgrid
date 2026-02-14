package kcp

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"
)

// EmbeddedKCP manages an embedded KCP server.
type EmbeddedKCP struct {
	dataDir string
}

// NewEmbeddedKCP creates a new embedded KCP instance.
func NewEmbeddedKCP(dataDir string) *EmbeddedKCP {
	return &EmbeddedKCP{dataDir: dataDir}
}

// Start starts the embedded KCP server.
func (e *EmbeddedKCP) Start(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting embedded KCP", "dataDir", e.dataDir)

	return fmt.Errorf("embedded KCP is not yet implemented; " +
		"please start an external KCP instance and pass its kubeconfig " +
		"via --external-kcp-kubeconfig flag")
}
