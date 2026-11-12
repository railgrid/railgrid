// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/provider-agents/store"
)

func TestRecoverReturnsRetryableForFreshLocalLeaseDespiteRegistry(t *testing.T) {
	ctx := context.Background()
	f := newRecoverFixture(t)
	run := f.seedRun(t, "live-local", store.RunPhaseRunning, time.Hour, true, 0)
	processOwner := "provider-incarnation-a"
	owner := newExecutionOwner(processOwner)
	f.s.cfg.ProcessID = processOwner
	if _, err := f.s.store.AcquireRunLease(ctx, f.scope, run.ID, owner, run.Attempt, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	f.s.liveRuns.register(run.ID, func() {})
	defer f.s.liveRuns.unregister(run.ID)

	bg := &background{server: f.s}
	err := bg.RecoverRun(ctx, "cluster-1", f.scope.AgentName, run.ID)
	if err == nil || !strings.Contains(err.Error(), "still has a live execution lease") {
		t.Fatalf("RecoverRun error = %v, want retryable fresh-lease refusal", err)
	}
	got := f.phaseOf(t, run.ID)
	if got.Phase != store.RunPhaseRunning || got.ExecutionOwner != owner || got.Attempt != run.Attempt {
		t.Fatalf("fresh local lease changed during declined recovery: %+v", got)
	}
}

func TestRecoverExpiredLocalLeaseDespiteRegistry(t *testing.T) {
	ctx := context.Background()
	f := newRecoverFixture(t)
	run := f.seedRun(t, "expired-local", store.RunPhaseRunning, time.Hour, false, 0)
	processOwner := "provider-incarnation-a"
	owner := newExecutionOwner(processOwner)
	f.s.cfg.ProcessID = processOwner
	if _, err := f.s.store.AcquireRunLease(ctx, f.scope, run.ID, owner, run.Attempt, run.UpdatedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	f.s.liveRuns.register(run.ID, func() {})
	defer f.s.liveRuns.unregister(run.ID)

	bg := &background{server: f.s}
	if err := bg.RecoverRun(ctx, "cluster-1", f.scope.AgentName, run.ID); err != nil {
		t.Fatal(err)
	}
	got := f.phaseOf(t, run.ID)
	if got.Phase != store.RunPhaseFailed || !executionOwnerBelongsToProcess(got.ExecutionOwner, processOwner) || got.ExecutionOwner == owner || got.Attempt != run.Attempt+1 {
		t.Fatalf("expired local lease was not claimed and settled under a new epoch: %+v", got)
	}
}
