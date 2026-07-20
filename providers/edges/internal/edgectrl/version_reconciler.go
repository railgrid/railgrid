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

package edgectrl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	edgeapi "github.com/faroshq/provider-edges/internal/edgeapi"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

const (
	// versionCheckInterval is the steady-state requeue cadence for re-evaluating
	// an edge's agent version against the hub release.
	versionCheckInterval = 10 * time.Minute
	// versionRetryInterval is the shorter requeue used when the hub version could
	// not be fetched, so a transient hub outage doesn't stall upgrade detection
	// for a full check interval.
	versionRetryInterval = 2 * time.Minute
)

// HubVersionCache fetches the hub's current release from its unauthenticated
// /version endpoint and caches it for a TTL, so the per-edge version reconcilers
// (one per kind, potentially many edges) share a single upstream lookup rather
// than hammering the hub on every reconcile.
type HubVersionCache struct {
	url    string
	client *http.Client
	ttl    time.Duration

	mu      sync.Mutex
	version string
	fetched time.Time
}

// NewHubVersionCache builds a cache pointed at hubExternalURL + "/version". When
// caData is provided it is trusted for the TLS handshake (the hub serves the
// front-proxy CA, same as the RBAC reconciler's agent-kubeconfig generation).
func NewHubVersionCache(hubExternalURL string, caData []byte, ttl time.Duration) *HubVersionCache {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(caData) > 0 {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(caData) {
			tlsCfg.RootCAs = pool
		}
	}
	return &HubVersionCache{
		url:    strings.TrimRight(hubExternalURL, "/") + "/version",
		client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}},
		ttl:    ttl,
	}
}

// Get returns the hub's release version, refetching when the cached value is
// stale. On a fetch error the previous fetch time is left untouched so the next
// call retries rather than serving a stale value indefinitely.
func (h *HubVersionCache) Get(ctx context.Context) (string, error) {
	h.mu.Lock()
	if h.version != "" && time.Since(h.fetched) < h.ttl {
		v := h.version
		h.mu.Unlock()
		return v, nil
	}
	h.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url, nil)
	if err != nil {
		return "", err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching hub version: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("hub /version returned %s", resp.Status)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil {
		return "", fmt.Errorf("decoding hub version: %w", err)
	}

	h.mu.Lock()
	h.version = body.Version
	h.fetched = time.Now()
	h.mu.Unlock()
	return body.Version, nil
}

// VersionReconciler compares each connectable's reported agent version against
// the hub release and maintains the UpgradeAvailable condition.
type VersionReconciler struct {
	mgr    mcmanager.Manager
	newObj func() edgeapi.Connectable
	latest func(context.Context) (string, error)
}

// SetupVersionWithManager registers the version controller for one connectable
// kind. latest yields the hub's current release version (typically a cached
// HubVersionCache.Get).
func SetupVersionWithManager(mgr mcmanager.Manager, gvr schema.GroupVersionResource, newObj func() edgeapi.Connectable, latest func(context.Context) (string, error)) error {
	r := &VersionReconciler{mgr: mgr, newObj: newObj, latest: latest}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("version-" + gvr.Resource).
		For(newObj()).
		Complete(r)
}

// Reconcile keeps the UpgradeAvailable condition in sync with the agent-vs-hub
// version delta. It only writes status when the condition actually changes, so a
// steady state costs one periodic Get (usually cache-served) and no API writes.
func (r *VersionReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("edge", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	edge := r.newObj()
	if err := c.Get(ctx, req.NamespacedName, edge); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	cs := edge.GetConnectionStatus()

	// Nothing to compare until the agent has heartbeated a version at least once.
	if cs.AgentVersion == "" {
		return ctrl.Result{RequeueAfter: versionCheckInterval}, nil
	}

	latest, err := r.latest(ctx)
	if err != nil {
		// A transient hub-version fetch failure must not flap the condition;
		// leave it as-is and retry sooner than the steady-state interval.
		logger.V(2).Info("could not determine hub version, skipping upgrade check", "err", err)
		return ctrl.Result{RequeueAfter: versionRetryInterval}, nil
	}

	desired := upgradeCondition(cs.AgentVersion, latest)
	existing := meta.FindStatusCondition(cs.Conditions, edgeapi.ConnectionConditionUpgradeAvailable)
	if existing != nil &&
		existing.Status == desired.Status &&
		existing.Reason == desired.Reason &&
		existing.Message == desired.Message {
		return ctrl.Result{RequeueAfter: versionCheckInterval}, nil
	}

	meta.SetStatusCondition(&cs.Conditions, desired)
	if err := c.Status().Update(ctx, edge); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating upgrade condition: %w", err)
	}
	logger.Info("Updated upgrade condition", "status", desired.Status, "agentVersion", cs.AgentVersion, "hubVersion", latest)
	return ctrl.Result{RequeueAfter: versionCheckInterval}, nil
}

// upgradeCondition builds the desired UpgradeAvailable condition. The True
// message embeds the target version in a "upgrade available to <version>."
// suffix the portal parses to render upgrade commands.
func upgradeCondition(agentVersion, hubVersion string) metav1.Condition {
	cond := metav1.Condition{
		Type:               edgeapi.ConnectionConditionUpgradeAvailable,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
	if agentOutdated(agentVersion, hubVersion) {
		cond.Status = metav1.ConditionTrue
		cond.Reason = "NewVersionAvailable"
		cond.Message = fmt.Sprintf("Agent is running %s; upgrade available to %s.", agentVersion, hubVersion)
	} else {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "UpToDate"
		cond.Message = fmt.Sprintf("Agent is running the current release (%s).", agentVersion)
	}
	return cond
}

// agentOutdated reports whether the agent should be upgraded: both versions must
// be known, neither may be the placeholder "dev" build, and they must differ.
// Mirrors the portal's historical isAgentOutdated so behaviour is unchanged.
func agentOutdated(agentVersion, hubVersion string) bool {
	if agentVersion == "" || hubVersion == "" {
		return false
	}
	if agentVersion == "dev" || hubVersion == "dev" {
		return false
	}
	return agentVersion != hubVersion
}
