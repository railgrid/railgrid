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

package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"

	producttelemetry "github.com/faroshq/provider-sdk/telemetry"
)

const (
	// Keep this local to the standalone provider; importing the repository's
	// generated catalog would couple the provider module to the hub module.
	edgesFirstReadyAction = "edge_first_ready"
	edgesResourceIDDomain = "faros/edges/resource/v1"
)

// edgeReadyTelemetry owns the process-local activation claim. The hub's
// analytics layer also deduplicates this workspace-unique event, but the
// provider avoids queueing repeated reconnects in the first place. A failed
// Track is deliberately not recorded in claims, so a later authoritative
// Ready update can retry it.
type edgeReadyTelemetry struct {
	mu      sync.Mutex
	tracker producttelemetry.Tracker
	claims  map[string]struct{}
	failed  map[string]struct{}
}

func newEdgeReadyTelemetry(tracker producttelemetry.Tracker) *edgeReadyTelemetry {
	if tracker == nil {
		tracker = producttelemetry.NoopTracker{}
	}
	return &edgeReadyTelemetry{
		tracker: tracker,
		claims:  make(map[string]struct{}),
		failed:  make(map[string]struct{}),
	}
}

func (t *edgeReadyTelemetry) setTracker(tracker producttelemetry.Tracker) {
	if tracker == nil {
		tracker = producttelemetry.NoopTracker{}
	}
	t.mu.Lock()
	t.tracker = tracker
	t.mu.Unlock()
}

// shouldTrack reports whether a successful status write should attempt this
// event. A status that was already connected/Ready is not a new activation,
// except when a previous Track failed and was left retryable.
func (t *edgeReadyTelemetry) shouldTrack(tenant string, wasReady bool) bool {
	if t == nil || strings.TrimSpace(tenant) == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.claims[tenant]; ok {
		return false
	}
	if _, ok := t.failed[tenant]; ok {
		return true
	}
	return !wasReady
}

// track emits one workspace-scoped event. The mutex covers Track so two
// concurrent tunnel opens cannot both pass the claim check. Tracker calls are
// locally bounded by the SDK; telemetry failures never escape to product
// operations, and a failed call leaves the claim available for retry.
func (t *edgeReadyTelemetry) track(ctx context.Context, gvr schema.GroupVersionResource, tenant, name string) {
	if t == nil {
		return
	}
	tenant = strings.TrimSpace(tenant)
	name = strings.TrimSpace(name)
	edgeType, ok := edgeTelemetryType(gvr)
	if tenant == "" || name == "" || !ok {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.claims[tenant]; ok {
		return
	}
	if t.tracker == nil {
		t.tracker = producttelemetry.NoopTracker{}
	}

	// The background tunnel path has only the tenant logical-cluster ID. Report
	// it truthfully as an opaque scope rather than duplicating it into org and
	// workspace fields whose semantics it cannot prove.
	event := producttelemetry.Event{
		Action:     edgesFirstReadyAction,
		ScopeID:    tenant,
		ResourceID: edgeResourceID(tenant, gvr, name),
		Properties: map[string]any{
			"edge_type": edgeType,
			"outcome":   "ready",
		},
	}
	if err := t.tracker.Track(ctx, event); err != nil {
		// Roll back the failed attempt. Do not retain a claim that analytics
		// could not accept into its queue.
		t.failed[tenant] = struct{}{}
		return
	}
	delete(t.failed, tenant)
	t.claims[tenant] = struct{}{}
}

func edgeTelemetryType(gvr schema.GroupVersionResource) (string, bool) {
	switch gvr.Resource {
	case "kubernetesclusters":
		return "kubernetes_cluster", true
	case "linuxservers":
		return "linux_server", true
	default:
		return "", false
	}
}

// edgeResourceID is an opaque, provider-local identity for one edge. Length
// framing makes concatenation unambiguous, while the fixed domain keeps this
// digest separate from every other Faros resource identity. The human edge
// name is never sent as an event identifier.
func edgeResourceID(tenant string, gvr schema.GroupVersionResource, name string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(edgesResourceIDDomain))
	for _, value := range []string{
		strings.TrimSpace(tenant),
		gvr.String(),
		strings.TrimSpace(name),
	} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(value))
	}
	return hex.EncodeToString(h.Sum(nil))
}
