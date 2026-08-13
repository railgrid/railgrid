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

package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/faroshq/faros/pkg/apiurl"
)

var catalogEntryGVR = schema.GroupVersionResource{
	Group: "providers.faros.sh", Version: "v1alpha1", Resource: "catalogentries",
}

// ClusterResolver reports the logical cluster a provider's CatalogEntry lives
// in. The catalog reconciler records it from the cluster it observed the entry
// in, so the recorder patches the object where it actually is.
type ClusterResolver interface {
	CatalogEntryCluster(name string) (string, bool)
}

// NewCatalogHeartbeatRecorder returns a HeartbeatRecorder that stamps
// CatalogEntry.status in the provider's own workspace
// (root:faros:providers/<name>), where each provider's `init` self-registers
// its entry.
//
// Addressing the entry by cluster rather than by a fixed path matters: an
// earlier version wrote to root:faros:system:providers, which serves the API but
// holds no entries, so every beat 404'd and provider liveness never crossed
// replicas.
//
// A merge patch (rather than a read-modify-write) keeps concurrent beats from
// different providers — and a beat racing the catalog reconciler's own status
// write — from conflicting.
func NewCatalogHeartbeatRecorder(kcpConfig *rest.Config, clusters ClusterResolver) (HeartbeatRecorder, error) {
	if kcpConfig == nil {
		return nil, fmt.Errorf("kcp config is required")
	}
	if clusters == nil {
		return nil, fmt.Errorf("cluster resolver is required")
	}
	return func(ctx context.Context, name, version string, at time.Time) error {
		cluster, ok := clusters.CatalogEntryCluster(name)
		if !ok || cluster == "" {
			// The catalog watch has not seen this provider's entry yet. The
			// beat still landed in the registry; only the cross-replica
			// broadcast is skipped, and the next beat retries.
			return fmt.Errorf("no CatalogEntry cluster known for provider %q yet", name)
		}
		cfg := rest.CopyConfig(kcpConfig)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, cluster)
		client, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return fmt.Errorf("creating client for cluster %s: %w", cluster, err)
		}
		// metav1.Time serialises as RFC3339 at second precision; match it
		// exactly so the round trip through the API is lossless.
		status := map[string]any{"lastHeartbeat": at.UTC().Format(time.RFC3339)}
		if version != "" {
			status["reportedVersion"] = version
		}
		patch, err := json.Marshal(map[string]any{"status": status})
		if err != nil {
			return fmt.Errorf("encoding heartbeat patch: %w", err)
		}
		if _, err := client.Resource(catalogEntryGVR).Patch(
			ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}, "status",
		); err != nil {
			return fmt.Errorf("patching CatalogEntry %q status in cluster %s: %w", name, cluster, err)
		}
		return nil
	}, nil
}
