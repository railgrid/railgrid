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

// Package servicectrl reconciles Service objects: discovery controllers
// that pull host services from each connected LinuxServer or MacOSServer agent and a
// validation controller that checks configured credentials against the service.
package servicectrl

import (
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

// SetupWithManager registers both Service controllers on the multicluster
// manager, sharing the tunnel ConnManager for agent dials. The validation
// reconciler stamps each Service's status.URL / status.mcpURL with the
// hub-relative kube path of its verbs; nothing about that is configurable,
// because the grammar has one spelling.
func SetupWithManager(mgr mcmanager.Manager, connManager ConnManager) error {
	if err := SetupDiscoveryWithManager(mgr, connManager); err != nil {
		return err
	}
	return SetupValidationWithManager(mgr, connManager)
}
