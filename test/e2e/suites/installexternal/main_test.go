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

// Package installexternal covers docs/install-external-kcp.md end to end: it
// executes the hack/install scripts the guide quotes (kind cluster →
// cert-manager → Envoy Gateway → etcd → kcp-operator → two-shard kcp → faros
// hub against that external kcp) and then asserts the documented verify steps.
package installexternal

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	"github.com/faroshq/faros/test/e2e/framework"
)

var testenv env.Environment

// installScripts is the exact sequence docs/install-external-kcp.md
// prescribes. Keep in sync with the doc.
var installScripts = []string{
	"01-kind-cluster.sh",
	"02-cert-manager.sh",
	"03-envoy-gateway.sh",
	"04-etcd.sh",
	"05-kcp-operator.sh",
	"06-kcp-shards.sh",
	"07-faros-hub-external.sh",
}

func TestMain(m *testing.M) {
	// Opt-in only: this suite provisions its own kind cluster and takes tens
	// of minutes; the dedicated Make target sets the gate. This keeps the
	// suite out of broad `go test ./test/e2e/suites/...` sweeps (e2e-all).
	if os.Getenv("FAROS_E2E_INSTALL") != "true" {
		fmt.Println("skipping install e2e suite: FAROS_E2E_INSTALL != true (run via `make e2e-install-external`)")
		os.Exit(0)
	}

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	cfg, err := envconf.NewFromFlags()
	if err != nil {
		panic("failed to parse e2e flags: " + err.Error())
	}

	testenv = env.NewWithConfig(cfg)
	testenv.Setup(framework.SetupInstallFlow(repoRoot, installScripts))
	testenv.Finish(framework.TeardownInstallFlow(repoRoot))

	os.Exit(testenv.Run(m))
}
