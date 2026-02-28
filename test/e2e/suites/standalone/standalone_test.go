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

func TestHubHealth(t *testing.T)            { testenv.Test(t, cases.HubHealth()) }
func TestStaticTokenLogin(t *testing.T)     { testenv.Test(t, cases.StaticTokenLogin()) }
func TestEdgeLifecycle(t *testing.T)        { testenv.Test(t, cases.EdgeLifecycle()) }
func TestAgentEdgeJoin(t *testing.T)        { testenv.Test(t, cases.AgentEdgeJoin()) }
func TestEdgeTunnelResilience(t *testing.T) { testenv.Test(t, cases.EdgeTunnelResilience()) }

// Multi-site tests — require 2 agent clusters (DefaultAgentCount=2).
func TestTwoAgentsJoin(t *testing.T)         { testenv.Test(t, cases.TwoAgentsJoin()) }
func TestLabelBasedScheduling(t *testing.T)  { testenv.Test(t, cases.LabelBasedScheduling()) }
func TestWorkloadIsolation(t *testing.T)     { testenv.Test(t, cases.WorkloadIsolation()) }
func TestSiteFailoverIsolation(t *testing.T) { testenv.Test(t, cases.SiteFailoverIsolation()) }
func TestSiteReconnect(t *testing.T)         { testenv.Test(t, cases.SiteReconnect()) }
func TestEdgeListAccuracyUnderChurn(t *testing.T) {
	testenv.Test(t, cases.EdgeListAccuracyUnderChurn())
}
