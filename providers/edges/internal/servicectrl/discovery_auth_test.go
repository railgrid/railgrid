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

package servicectrl

import (
	"testing"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
)

// TestADiscoveredRunnerIsCreatedWithNoCredential: spec.auth defaults to
// "secret", which is right for the service types a tenant supplies a token for
// and wrong for a runner — the AGENT injects the runner's own bearer on the host
// side, so there is no Secret to name. Left at the default, the published object
// asks a tenant for a credential that does not exist and carries a permanent
// CredentialsValid=Unknown.
func TestADiscoveredRunnerIsCreatedWithNoCredential(t *testing.T) {
	if got := discoveredAuthMode("runner"); got != edgesv1alpha1.ServiceAuthNone {
		t.Errorf("discoveredAuthMode(runner) = %q, want %q", got, edgesv1alpha1.ServiceAuthNone)
	}
}

// TestEveryOtherTypeKeepsTheCRDDefault: only an explicit "no credential" is
// stamped. Returning a value for a type that DOES authenticate would silently
// downgrade it, which is the opposite mistake and a worse one.
func TestEveryOtherTypeKeepsTheCRDDefault(t *testing.T) {
	for _, serviceType := range []string{"home-assistant", "grafana", "generic", "not-a-type", ""} {
		if got := discoveredAuthMode(serviceType); got != "" {
			t.Errorf("discoveredAuthMode(%q) = %q, want the CRD default to apply", serviceType, got)
		}
	}
}
