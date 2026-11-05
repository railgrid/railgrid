// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package edgeref

import "testing"

func TestRunnerCoordinatesDistinguishMacServicesFromProcessIdentity(t *testing.T) {
	for _, selector := range []string{"claude", "codex"} {
		linux := RunnerServiceName(KindLinuxServer, "mac-mini", selector)
		mac := RunnerServiceName(KindMacOSServer, "mac-mini", selector)
		id := RunnerID("mac-mini", selector)
		if linux != "mac-mini-"+selector || mac != "macos-edge-mac-mini-"+selector || id != linux {
			t.Fatalf("%s: Linux Service=%q, Mac Service=%q, runner ID=%q", selector, linux, mac, id)
		}
	}
	for _, tc := range []struct{ kind, edge, selector string }{
		{"KubernetesCluster", "mac-mini", "codex"},
		{KindMacOSServer, "", "codex"},
		{KindMacOSServer, "mac-mini", ""},
	} {
		if got := RunnerServiceName(tc.kind, tc.edge, tc.selector); got != "" {
			t.Errorf("invalid coordinate %+v produced %q", tc, got)
		}
	}
}
