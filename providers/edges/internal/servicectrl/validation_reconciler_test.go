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

package servicectrl

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// TestProbeRetryBackoff pins the retry schedule a failing probe follows: a
// Service created before its backend answers is re-probed after 5s, then
// doubling, never slower than the 10-minute cycle; a success resets it; and
// two Services (or the same name in two workspaces) back off independently.
func TestProbeRetryBackoff(t *testing.T) {
	r := newValidationReconciler(nil, nil, "", nil)
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
