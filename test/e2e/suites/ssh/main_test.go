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

// Package ssh implements e2e tests for kedge SSH server-mode functionality.
// It requires only a hub cluster (no agent clusters) since SSH tests start
// their own server-mode agents as subprocesses.
package ssh

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

var testenv env.Environment

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	cfg, err := envconf.NewFromFlags()
	if err != nil {
		panic("failed to parse e2e flags: " + err.Error())
	}

	testenv = env.NewWithConfig(cfg)

	if os.Getenv("KEDGE_USE_EXISTING_CLUSTERS") == "true" {
		testenv.Setup(framework.UseExistingClusters(repoRoot))
	} else {
		// SSH tests only need the hub — use agentCount=1 (CLI minimum) to avoid
		// creating the two extra kind clusters the standalone suite needs.
		testenv.Setup(framework.SetupClustersWithAgentCount(repoRoot, 1))
		testenv.Finish(framework.TeardownClustersWithAgentCount(repoRoot, 1))
	}

	os.Exit(testenv.Run(m))
}
