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
	"fmt"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ExternalKCP connects to an external kcp instance.
type ExternalKCP struct {
	kubeconfig string
}

// NewExternalKCP creates a new external kcp connection.
func NewExternalKCP(kubeconfig string) *ExternalKCP {
	return &ExternalKCP{kubeconfig: kubeconfig}
}

// GetConfig returns a rest.Config for the external kcp.
func (e *ExternalKCP) GetConfig() (*rest.Config, error) {
	if e.kubeconfig == "" {
		return nil, fmt.Errorf("kubeconfig path is required for external kcp")
	}

	config, err := clientcmd.BuildConfigFromFlags("", e.kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build config from kubeconfig %s: %w", e.kubeconfig, err)
	}

	return config, nil
}
