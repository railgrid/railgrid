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

package standalone

import (
	"testing"

	"github.com/faroshq/faros-kedge/test/e2e/cases"
)

// The tests below delegate to shared case builders so the same logic runs
// in every suite that imports cases/. Suite-specific setup lives in main_test.go.

func TestHubHealth(t *testing.T)        { testenv.Test(t, cases.HubHealth()) }
func TestStaticTokenLogin(t *testing.T) { testenv.Test(t, cases.StaticTokenLogin()) }

// edgeSkip marks an edge-connectivity test as parked while edge connectivity is
// brought up as the standalone edges provider (group edges.kedge.faros.sh,
// kinds KubernetesCluster/LinuxServer). These agent/join-token/proxy/workload/
// MCP/SSH cases will be relocated to the dedicated edges suite that bootstraps
// that provider. See docs/edges-providers-testing.md.
func edgeSkip(t *testing.T) {
	t.Helper()
	t.Skip("edge connectivity moved to the standalone edges provider " +
		"(edges.kedge.faros.sh); e2e coverage pending the dedicated edges suite — " +
		"see docs/edges-providers-testing.md")
}

func TestEdgeLifecycle(t *testing.T)              { edgeSkip(t) }
func TestAgentEdgeJoin(t *testing.T)              { edgeSkip(t) }
func TestEdgeTunnelResilience(t *testing.T)       { edgeSkip(t) }
func TestEdgeURLSet(t *testing.T)                 { edgeSkip(t) }
func TestK8sProxyAccess(t *testing.T)             { edgeSkip(t) }
func TestK8sProxyWrite(t *testing.T)              { edgeSkip(t) }
func TestK8sProxyExec(t *testing.T)               { edgeSkip(t) }
func TestWorkloadDeployment(t *testing.T)         { edgeSkip(t) }
func TestProxyUnauthenticated(t *testing.T)       { edgeSkip(t) }
func TestTwoAgentsJoin(t *testing.T)              { edgeSkip(t) }
func TestLabelBasedScheduling(t *testing.T)       { edgeSkip(t) }
func TestWorkloadIsolation(t *testing.T)          { edgeSkip(t) }
func TestEdgeFailoverIsolation(t *testing.T)      { edgeSkip(t) }
func TestEdgeReconnect(t *testing.T)              { edgeSkip(t) }
func TestEdgeListAccuracyUnderChurn(t *testing.T) { edgeSkip(t) }
func TestProxyInvalidToken(t *testing.T)          { edgeSkip(t) }
func TestK8sProxyWriteIsolation(t *testing.T)     { edgeSkip(t) }

func TestJoinTokenIsSetAfterEdgeCreation(t *testing.T)           { edgeSkip(t) }
func TestAgentConnectsWithJoinToken(t *testing.T)                { edgeSkip(t) }
func TestInvalidJoinTokenReturns401(t *testing.T)                { edgeSkip(t) }
func TestJoinTokenClearedAfterRegistration(t *testing.T)         { edgeSkip(t) }
func TestJoinTokenReconnectWithSavedKubeconfig(t *testing.T)     { edgeSkip(t) }
func TestTokenReconcilerNoReissueAfterRegistration(t *testing.T) { edgeSkip(t) }
func TestJoinTokenKubernetesMode(t *testing.T)                   { edgeSkip(t) }
func TestAgentJoinKubernetes(t *testing.T)                       { edgeSkip(t) }
func TestAgentHelmInstall(t *testing.T)                          { edgeSkip(t) }
func TestMCPEndpoint(t *testing.T)                               { edgeSkip(t) }
func TestMCPURL(t *testing.T)                                    { edgeSkip(t) }
func TestJoinTokenSSHCredentialsStoredAfterConnect(t *testing.T) { edgeSkip(t) }
func TestAgentCLIFlow(t *testing.T)                              { edgeSkip(t) }

// Tenancy CRUD + invariants — single-user lifecycles that don't need a
// second identity. The TenancySATokenCrossWorkspace case is OIDC-only
// and stays out of this suite.
func TestTenancyOrgCRUD(t *testing.T)             { testenv.Test(t, cases.TenancyOrgCRUD()) }
func TestTenancyWorkspaceCRUD(t *testing.T)       { testenv.Test(t, cases.TenancyWorkspaceCRUD()) }
func TestTenancyServiceAccountCRUD(t *testing.T)  { testenv.Test(t, cases.TenancySACRUD()) }
func TestTenancyServiceAccountToken(t *testing.T) { testenv.Test(t, cases.TenancySATokenAccess()) }
func TestTenancyTenantHeaders(t *testing.T)       { testenv.Test(t, cases.TenancyTenantHeaders()) }
func TestTenancyPersonalOrgSoftDelete(t *testing.T) {
	testenv.Test(t, cases.TenancyPersonalOrgSoftDelete())
}
func TestTenancySoftDeleteHidesOrg(t *testing.T) { testenv.Test(t, cases.TenancySoftDeleteHidesOrg()) }
