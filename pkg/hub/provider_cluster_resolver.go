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

package hub

import (
	"context"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
)

// logicalClusterGVR addresses the well-known per-workspace LogicalCluster
// object named "cluster", whose kcp.io/cluster annotation is the workspace's
// logical-cluster ID.
var logicalClusterGVR = schema.GroupVersionResource{
	Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
}

// clusterIDResolverTTL bounds how long a (tenantPath → clusterID) mapping is
// cached. The mapping is effectively stable for a workspace's lifetime, so a
// generous TTL keeps the warm path free of kcp round-trips while still letting
// a deleted-and-recreated workspace's stale ID age out.
const clusterIDResolverTTL = 10 * time.Minute

// newClusterIDResolver returns a function mapping a tenant workspace path
// (root:kedge:tenants:<org>[:<ws>]) to its kcp logical-cluster ID, for the
// backend proxy to inject as X-Kedge-Cluster. It reads the workspace's
// LogicalCluster "cluster" object via the hub's kcp admin config, scoping the
// host to /clusters/<path> through the front-proxy (which resolves paths).
//
// Results are cached per path with a TTL; resolution failures are not cached so
// a transient kcp hiccup self-heals on the next request.
func newClusterIDResolver(kcpConfig *rest.Config) func(ctx context.Context, tenantPath string) (string, error) {
	type entry struct {
		id        string
		expiresAt time.Time
	}
	var (
		mu  sync.RWMutex
		hot = map[string]entry{}
	)

	return func(ctx context.Context, tenantPath string) (string, error) {
		now := time.Now()

		mu.RLock()
		e, ok := hot[tenantPath]
		mu.RUnlock()
		if ok && now.Before(e.expiresAt) {
			return e.id, nil
		}

		cfg := rest.CopyConfig(kcpConfig)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, tenantPath)
		dyn, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return "", fmt.Errorf("dynamic client for %q: %w", tenantPath, err)
		}
		lc, err := dyn.Resource(logicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("getting LogicalCluster for %q: %w", tenantPath, err)
		}
		id := lc.GetAnnotations()["kcp.io/cluster"]
		if id == "" {
			return "", fmt.Errorf("LogicalCluster for %q has no kcp.io/cluster annotation", tenantPath)
		}

		mu.Lock()
		hot[tenantPath] = entry{id: id, expiresAt: now.Add(clusterIDResolverTTL)}
		mu.Unlock()
		return id, nil
	}
}
