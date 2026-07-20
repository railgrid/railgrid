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

// Package servicectrl reconciles Service objects: a discovery controller
// that pulls host services from each connected LinuxServer agent and a
// validation controller that checks configured credentials against the service.
package servicectrl

import (
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/faroshq/provider-edges/internal/events"
)

// Options configures the servicectrl controllers.
type Options struct {
	// EdgeProxyPublicPath is the public consumer-egress base (behind the hub
	// backend proxy), e.g. /services/providers/edges/edgeproxy. Stamped into
	// Service status.URL. Empty disables URL stamping.
	EdgeProxyPublicPath string
	// Events, when set, drives the per-Service event subscribers (currently
	// UniFi Protect): the validation reconciler starts one when a Service is
	// Ready and stops it when the Service is deleted or goes NotReady. Nil
	// disables event subscriptions.
	Events *events.Manager
}

// SetupWithManager registers both Service controllers on the multicluster
// manager, sharing the tunnel ConnManager for agent dials.
func SetupWithManager(mgr mcmanager.Manager, connManager ConnManager, opts Options) error {
	if err := SetupDiscoveryWithManager(mgr, connManager); err != nil {
		return err
	}
	return SetupValidationWithManager(mgr, connManager, opts.EdgeProxyPublicPath, opts.Events)
}
