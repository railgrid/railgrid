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

	// TODO: Initialize and start KCP via server.NewServer(completedConfig) with embedded etcd
	return fmt.Errorf("embedded KCP not yet implemented")
}
