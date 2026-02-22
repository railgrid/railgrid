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

// Package testfakes provides shared fake implementations for unit testing
// controllers that depend on multicluster-runtime manager and cluster interfaces.
package testfakes

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

// Manager is a minimal fake mcmanager.Manager for unit tests.
// It embeds the full interface so that only GetCluster needs to be implemented.
// Any call to an unimplemented method will panic — this is intentional and
// surfaces accidental dependencies in tests.
type Manager struct {
	mcmanager.Manager
	FakeCluster cluster.Cluster
}

// NewManager creates a Manager wired to the given fake client.
func NewManager(c client.Client) *Manager {
	return &Manager{FakeCluster: &FakeCluster{C: c}}
}

// GetCluster returns the configured test cluster regardless of clusterName.
func (m *Manager) GetCluster(_ context.Context, _ string) (cluster.Cluster, error) {
	return m.FakeCluster, nil
}

// FakeCluster is a minimal fake cluster.Cluster for unit tests.
// It embeds the full interface; only GetClient is implemented.
type FakeCluster struct {
	cluster.Cluster
	C client.Client
}

// GetClient returns the injected test client.
func (c *FakeCluster) GetClient() client.Client {
	return c.C
}
