/*
Copyright 2026 The Railgrid Authors.

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

// Package edgectrl holds the reconcilers shared by every connectable edge kind
// (KubernetesCluster, LinuxServer, MacOSServer). For one kind it mints and
// rotates the bootstrap join token, keeps the agent's hub-scoped identity and
// RBAC rules in existence, owns the single writer of the edge's connectivity
// status — derived from the tunnel registry Leases rather than from local
// connection state — and compares the agent's reported version against the
// hub release to maintain the UpgradeAvailable condition. SetupControllers
// wires the whole set onto a multicluster manager.
package edgectrl

import "time"

const (
	// HeartbeatTimeout is the duration after which an edge is considered disconnected.
	// 90s = 3 missed heartbeats at the agent 30s interval; reasonable for production
	// and keeps CI fast (was 5 min, causing CI timeouts).
	HeartbeatTimeout = 90 * time.Second
	// GCTimeout is the duration after which a disconnected edge is garbage collected.
	GCTimeout = 24 * time.Hour
)
