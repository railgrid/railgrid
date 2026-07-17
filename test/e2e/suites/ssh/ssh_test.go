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

package ssh

import (
	"testing"

	"github.com/faroshq/faros-kedge/test/e2e/cases"
)

// The SSH-to-edge connectivity cases (SSHServerModeConnect,
// SSHDockerServerModeConnect, SSHEdgeURLSet, SSHUserMapping*) are parked while
// edge connectivity is brought up as the standalone edges provider (group
// edges.kedge.faros.sh, kind LinuxServer). They will be relocated to the
// dedicated edges suite that bootstraps that provider. See edgeSkip below and
// docs/edges-providers-testing.md.
func TestSSHServerModeConnect(t *testing.T)       { edgeSkip(t) }
func TestSSHDockerServerModeConnect(t *testing.T) { edgeSkip(t) }
func TestSSHEdgeURLSet(t *testing.T)              { edgeSkip(t) }
func TestSSHUserMappingInherited(t *testing.T)    { edgeSkip(t) }
func TestSSHUserMappingProvided(t *testing.T)     { edgeSkip(t) }
func TestSSHUserMappingIdentity(t *testing.T)     { edgeSkip(t) }

// edgeSkip marks an edge-connectivity test as parked pending the edges suite.
func edgeSkip(t *testing.T) {
	t.Helper()
	t.Skip("edge connectivity moved to the standalone edges provider " +
		"(edges.kedge.faros.sh); e2e coverage pending the dedicated edges suite — " +
		"see docs/edges-providers-testing.md")
}

// Tenancy CRUD + invariants — the SSH suite runs the same single-user
// cases against the hub-only cluster so a regression in the tenant
// REST surface shows up here too.
func TestTenancyOrgCRUD(t *testing.T)             { testenv.Test(t, cases.TenancyOrgCRUD()) }
func TestTenancyWorkspaceCRUD(t *testing.T)       { testenv.Test(t, cases.TenancyWorkspaceCRUD()) }
func TestTenancyServiceAccountCRUD(t *testing.T)  { testenv.Test(t, cases.TenancySACRUD()) }
func TestTenancyServiceAccountToken(t *testing.T) { testenv.Test(t, cases.TenancySATokenAccess()) }
func TestTenancyTenantHeaders(t *testing.T)       { testenv.Test(t, cases.TenancyTenantHeaders()) }
func TestTenancyPersonalOrgSoftDelete(t *testing.T) {
	testenv.Test(t, cases.TenancyPersonalOrgSoftDelete())
}
func TestTenancySoftDeleteHidesOrg(t *testing.T) { testenv.Test(t, cases.TenancySoftDeleteHidesOrg()) }
