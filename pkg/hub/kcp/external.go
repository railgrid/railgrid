package kcp

import (
	"fmt"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ExternalKCP connects to an external KCP instance.
type ExternalKCP struct {
	kubeconfig string
}

// NewExternalKCP creates a new external KCP connection.
func NewExternalKCP(kubeconfig string) *ExternalKCP {
	return &ExternalKCP{kubeconfig: kubeconfig}
}

// GetConfig returns a rest.Config for the external KCP.
func (e *ExternalKCP) GetConfig() (*rest.Config, error) {
	if e.kubeconfig == "" {
		return nil, fmt.Errorf("kubeconfig path is required for external KCP")
	}

	config, err := clientcmd.BuildConfigFromFlags("", e.kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build config from kubeconfig %s: %w", e.kubeconfig, err)
	}

	return config, nil
}
