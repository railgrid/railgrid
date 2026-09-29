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
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
)

// TestProbeRetryBackoff pins the retry schedule a failing probe follows: a
// Service created before its backend answers is re-probed after 5s, then
// doubling, never slower than the 10-minute cycle; a success resets it; and
// two Services (or the same name in two workspaces) back off independently.
func TestProbeRetryBackoff(t *testing.T) {
	r := newValidationReconciler(nil, nil)
	a := retryKey(mcreconcile.Request{ClusterName: "ws-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "kiosk"}}})
	b := retryKey(mcreconcile.Request{ClusterName: "ws-b", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "kiosk"}}})
	if a == b {
		t.Fatal("retry keys must be workspace-scoped")
	}

	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second,
		160 * time.Second, 320 * time.Second, validationResyncInterval, validationResyncInterval}
	for i, w := range want {
		if got := r.nextRetry(a); got != w {
			t.Fatalf("failure %d: nextRetry = %s, want %s", i+1, got, w)
		}
	}
	if got := r.nextRetry(b); got != probeRetryInitial {
		t.Fatalf("independent key started at %s, want %s", got, probeRetryInitial)
	}

	r.resetRetry(a)
	if got := r.nextRetry(a); got != probeRetryInitial {
		t.Fatalf("after reset nextRetry = %s, want %s", got, probeRetryInitial)
	}
	if got := r.nextRetry(b); got != 2*probeRetryInitial {
		t.Fatalf("reset of a leaked into b: %s", got)
	}

	// Every retry is bounded by the cycle so a permanently broken Service
	// never probes slower than a healthy one re-validates.
	for i := 0; i < 20; i++ {
		if got := r.nextRetry(a); got > validationResyncInterval || got < probeRetryInitial {
			t.Fatalf("retry %d out of bounds: %s", i, got)
		}
	}
}

// status.harness is what lets a hub-side caller choose a ready runner without
// holding the runner's bearer, so what is read out of the capabilities document
// is a contract with pkg/runner's Capabilities — pinned here against the exact
// JSON a runner emits.
func TestStampRunnerHarnessReadsTheCapabilitiesDocument(t *testing.T) {
	const body = `{
	  "protocolVersion": "runner/v1",
	  "runnerID": "r-1",
	  "version": "0.4.0",
	  "os": "darwin",
	  "architecture": "arm64",
	  "harnesses": [{"name": "claude", "version": "2.1.273", "ready": true}],
	  "capacity": {"maximum": 1, "used": 0},
	  "ready": true
	}`
	es := &edgesv1alpha1.Service{}
	stampRunnerHarness(es, strings.NewReader(body))
	if es.Status.Harness == nil {
		t.Fatal("no harness status was stamped")
	}
	if es.Status.Harness.Name != "claude" || es.Status.Harness.Version != "2.1.273" || !es.Status.Harness.Ready {
		t.Fatalf("harness status = %+v, want claude 2.1.273 ready", es.Status.Harness)
	}
}

// A runner whose harness is not ready still answers runner/v1 and still refuses
// every attempt, so the reasons are the useful part of the answer. They must
// come from the HARNESS entry, not from the runner's top-level readiness.
func TestStampRunnerHarnessPrefersTheHarnessReadiness(t *testing.T) {
	const body = `{"harnesses":[{"name":"codex","version":"","ready":false,"reasons":["codex not found on PATH"]}],"ready":true}`
	es := &edgesv1alpha1.Service{}
	stampRunnerHarness(es, strings.NewReader(body))
	if es.Status.Harness.Ready {
		t.Fatal("a not-ready harness was reported ready because the runner itself was")
	}
	if len(es.Status.Harness.Reasons) != 1 || es.Status.Harness.Reasons[0] != "codex not found on PATH" {
		t.Fatalf("reasons = %v, want the harness's own", es.Status.Harness.Reasons)
	}
}

// A runner with no harness at all is a real state (nothing installed, or the
// supervisor is still starting). Leaving the field nil would read as "never
// probed", which is the opposite of what we just learned.
func TestStampRunnerHarnessFallsBackToTheRunnersOwnReadiness(t *testing.T) {
	es := &edgesv1alpha1.Service{}
	stampRunnerHarness(es, strings.NewReader(`{"ready":false,"reasons":["no harness enabled"]}`))
	if es.Status.Harness == nil || es.Status.Harness.Ready || es.Status.Harness.Name != "" {
		t.Fatalf("harness status = %+v, want an unnamed not-ready harness", es.Status.Harness)
	}
	if len(es.Status.Harness.Reasons) != 1 {
		t.Fatalf("reasons = %v, want the runner's own", es.Status.Harness.Reasons)
	}

	// A body that is not a capabilities document leaves the previous answer
	// alone rather than replacing it with an empty one.
	prev := &edgesv1alpha1.ServiceHarnessStatus{Name: "claude", Ready: true}
	es.Status.Harness = prev
	stampRunnerHarness(es, strings.NewReader("not json"))
	if es.Status.Harness != prev {
		t.Fatalf("an undecodable body overwrote the harness status: %+v", es.Status.Harness)
	}
}
