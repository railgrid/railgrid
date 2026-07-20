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

	"k8s.io/apimachinery/pkg/runtime/schema"

	edgeapi "github.com/faroshq/provider-edges/internal/edgeapi"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

// Options configures the RBAC reconciler's agent-kubeconfig generation.
type Options struct {
	HubExternalURL string
	HubCAData      []byte
	DevMode        bool
	// LatestAgentVersion yields the hub's current release version. When set, the
	// version reconciler maintains the UpgradeAvailable condition by comparing it
	// against each edge's reported status.agentVersion. Nil disables the check.
	LatestAgentVersion func(context.Context) (string, error)
}

// SetupControllers registers the token, RBAC, and lifecycle reconcilers for one
// connectable kind on the multicluster manager. An edge-type provider calls this
// once with its kind's GVR + Kind + a factory that yields its concrete type
// (which must implement edgeapi.Connectable), plus the tunnel's ConnManager so
// the lifecycle reconciler can cross-check tunnel liveness.
func SetupControllers(
	mgr mcmanager.Manager,
	gvr schema.GroupVersionResource,
	kind string,
	newObj func() edgeapi.Connectable,
	connManager ConnManager,
	opts Options,
) error {
	if err := SetupTokenWithManager(mgr, gvr, newObj); err != nil {
		return err
	}
	if err := SetupRBACWithManager(mgr, gvr, kind, newObj, opts.HubExternalURL, opts.HubCAData, opts.DevMode); err != nil {
		return err
	}
	if opts.LatestAgentVersion != nil {
		if err := SetupVersionWithManager(mgr, gvr, newObj, opts.LatestAgentVersion); err != nil {
			return err
		}
	}
	return SetupLifecycleWithManager(mgr, gvr, newObj, connManager)
}
