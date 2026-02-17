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
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

const (
	// HeartbeatInterval is how often the agent sends heartbeats to the hub.
	HeartbeatInterval = 30 * time.Second
	// WorkloadStatusInterval is how often the agent syncs workload status.
	WorkloadStatusInterval = 15 * time.Second
)

// Reporter sends heartbeats and workload status updates to the hub.
type Reporter struct {
	siteName         string
	hubClient        *kedgeclient.Client
	downstreamClient kubernetes.Interface
}

// NewReporter creates a new status reporter.
func NewReporter(siteName string, hubClient *kedgeclient.Client, downstreamClient kubernetes.Interface) *Reporter {
	return &Reporter{
		siteName:         siteName,
		hubClient:        hubClient,
		downstreamClient: downstreamClient,
	}
}

// Run starts the status reporter.
func (r *Reporter) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName("status-reporter")
	logger.Info("Starting status reporter", "siteName", r.siteName)

	heartbeatTicker := time.NewTicker(HeartbeatInterval)
	defer heartbeatTicker.Stop()

	workloadTicker := time.NewTicker(WorkloadStatusInterval)
	defer workloadTicker.Stop()

	// First heartbeat immediately to mark the site as ready on the hub, then start the tickers
	r.sendHeartbeat(ctx, logger)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeatTicker.C:
			r.sendHeartbeat(ctx, logger)
		case <-workloadTicker.C:
			r.reportWorkloadStatus(ctx, logger)
		}
	}
}

func (r *Reporter) sendHeartbeat(ctx context.Context, logger klog.Logger) {
	logger.V(4).Info("Sending heartbeat", "siteName", r.siteName)

	now := metav1.Now()
	patch := map[string]interface{}{
		"status": map[string]interface{}{
			"phase":             string(kedgev1alpha1.SitePhaseReady),
			"tunnelConnected":   true,
			"lastHeartbeatTime": now.Format(time.RFC3339),
		},
	}

	patchData, err := json.Marshal(patch)
	if err != nil {
		logger.Error(err, "Failed to marshal heartbeat patch")
		return
	}

	_, err = r.hubClient.Sites().Patch(ctx, r.siteName, types.MergePatchType, patchData, metav1.PatchOptions{}, "status")
	if err != nil {
		logger.Error(err, "Failed to send heartbeat")
	}
}

func (r *Reporter) reportWorkloadStatus(ctx context.Context, logger klog.Logger) {
	// Find local deployments managed by kedge
	deployments, err := r.downstreamClient.AppsV1().Deployments("default").List(ctx, metav1.ListOptions{
		LabelSelector: "kedge.faros.sh/placement",
	})
	if err != nil {
		logger.Error(err, "Failed to list local deployments")
		return
	}

	for _, d := range deployments.Items {
		placementName := d.Annotations["kedge.faros.sh/placement-name"]
		placementNs := d.Annotations["kedge.faros.sh/placement-namespace"]
		if placementName == "" || placementNs == "" {
			continue
		}

		phase := "Pending"
		if d.Status.ReadyReplicas > 0 && d.Status.ReadyReplicas == d.Status.Replicas {
			phase = "Running"
		} else if d.Status.ReadyReplicas > 0 {
			phase = "Running"
		}

		patch := map[string]interface{}{
			"status": map[string]interface{}{
				"phase":         phase,
				"readyReplicas": d.Status.ReadyReplicas,
			},
		}
		patchData, err := json.Marshal(patch)
		if err != nil {
			continue
		}

		_, err = r.hubClient.Placements(placementNs).Patch(ctx, placementName, types.MergePatchType, patchData, metav1.PatchOptions{}, "status")
		if err != nil {
			if apierrors.IsNotFound(err) {
				// Placement was deleted on the hub — clean up local deployment
				logger.Info("Placement not found on hub, deleting local deployment",
					"placement", fmt.Sprintf("%s/%s", placementNs, placementName),
					"deployment", d.Name)
				if delErr := r.downstreamClient.AppsV1().Deployments(d.Namespace).Delete(ctx, d.Name, metav1.DeleteOptions{}); delErr != nil && !apierrors.IsNotFound(delErr) {
					logger.Error(delErr, "Failed to delete local deployment", "name", d.Name)
				}
				continue
			}
			logger.V(4).Error(err, "Failed to update placement status",
				"placement", fmt.Sprintf("%s/%s", placementNs, placementName))
		}
	}
}
