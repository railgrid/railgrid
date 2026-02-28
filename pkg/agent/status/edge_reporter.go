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

// Package status reports agent status back to the hub.
package status

import (
	"context"
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

const (
	// HeartbeatInterval is how often the agent sends heartbeats to the hub.
	HeartbeatInterval = 30 * time.Second
)

// EdgeReporter sends heartbeats for an Edge resource.
// It works for both EdgeTypeKubernetes and EdgeTypeServer.
type EdgeReporter struct {
	edgeName        string
	hubClient       *kedgeclient.Client
	tunnelState     <-chan bool // receives true on connect, false on disconnect; may be nil
	tunnelConnected bool
}

// NewEdgeReporter creates a new EdgeReporter.
// tunnelState is the channel produced by tunnel.StartProxyTunnel; pass nil to
// skip tunnel-state tracking (tunnelConnected will always report false).
func NewEdgeReporter(edgeName string, hubClient *kedgeclient.Client, tunnelState <-chan bool) *EdgeReporter {
	return &EdgeReporter{
		edgeName:    edgeName,
		hubClient:   hubClient,
		tunnelState: tunnelState,
	}
}

// Run starts the edge heartbeat reporter and blocks until ctx is cancelled.
func (r *EdgeReporter) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName("edge-status-reporter")
	logger.Info("Starting edge status reporter", "edgeName", r.edgeName)

	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	// First heartbeat immediately.
	r.sendHeartbeat(ctx, logger)

	for {
		select {
		case <-ctx.Done():
			return nil
		case connected, ok := <-r.tunnelState:
			if ok {
				r.tunnelConnected = connected
				r.sendHeartbeat(ctx, logger)
			}
		case <-ticker.C:
			r.sendHeartbeat(ctx, logger)
		}
	}
}

func (r *EdgeReporter) sendHeartbeat(ctx context.Context, logger klog.Logger) {
	status := kedgev1alpha1.EdgeStatus{
		Phase:     kedgev1alpha1.EdgePhaseReady,
		Connected: r.tunnelConnected,
	}
	// The hub may set Hostname/WorkspaceURL; we only patch the fields we own.
	patch := map[string]interface{}{
		"status": map[string]interface{}{
			"phase":     string(status.Phase),
			"connected": status.Connected,
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		logger.Error(err, "failed to marshal edge status patch")
		return
	}

	_, err = r.hubClient.Edges().Patch(ctx, r.edgeName,
		types.MergePatchType, patchBytes,
		metav1.PatchOptions{}, "status")
	if err != nil {
		logger.Error(err, "failed to update edge status", "edge", r.edgeName)
		return
	}

	logger.V(4).Info("Edge heartbeat sent", "edge", r.edgeName,
		"phase", string(status.Phase), "connected", status.Connected)
}
