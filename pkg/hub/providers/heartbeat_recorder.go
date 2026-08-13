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
	"github.com/faroshq/faros/pkg/kcppaths"
)

var catalogEntryGVR = schema.GroupVersionResource{
	Group: "providers.faros.sh", Version: "v1alpha1", Resource: "catalogentries",
}

// NewCatalogHeartbeatRecorder returns a HeartbeatRecorder that stamps
// CatalogEntry.status in root:faros:system:providers, where the entries live.
//
// A merge patch (rather than a read-modify-write) keeps concurrent beats from
// different providers — and a beat racing the catalog reconciler's own status
// write — from conflicting.
func NewCatalogHeartbeatRecorder(kcpConfig *rest.Config) (HeartbeatRecorder, error) {
	if kcpConfig == nil {
		return nil, fmt.Errorf("kcp config is required")
	}
	cfg := rest.CopyConfig(kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.SystemProviders)
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating system:providers client: %w", err)
	}
	return func(ctx context.Context, name, version string, at time.Time) error {
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
			return fmt.Errorf("patching CatalogEntry %q status: %w", name, err)
		}
		return nil
	}, nil
}
