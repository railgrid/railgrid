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

package api

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type projectAssistantSandboxTestContextKey struct{}

type projectAssistantSandboxEnsureResult struct {
	sandbox *projectAssistantRunSandbox
	err     error
}

func TestProjectAssistantRunStateEnsureSandboxConcurrentCallsShareOneAttempt(t *testing.T) {
	runCtx := context.WithValue(context.Background(), projectAssistantSandboxTestContextKey{}, "run")
	state := newProjectEinoAssistantRunState()
	started := make(chan struct{})
	proceed := make(chan struct{})
	sandbox := &projectAssistantRunSandbox{}
	var calls atomic.Int32
	var releases atomic.Int32
	state.ConfigureSandboxCapabilityWithContext(runCtx, CodingSandboxEligibility{Eligible: true}, func(ctx context.Context) (*projectAssistantRunSandbox, func(), error) {
		if got := ctx.Value(projectAssistantSandboxTestContextKey{}); got != "run" {
			return nil, nil, fmt.Errorf("initializer context value = %v, want run context", got)
		}
		if calls.Add(1) != 1 {
			return nil, nil, errors.New("initializer called more than once concurrently")
		}
		close(started)
		<-proceed
		return sandbox, func() { releases.Add(1) }, nil
	})

	const callers = 8
	results := make(chan projectAssistantSandboxEnsureResult, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			got, err := state.EnsureSandbox(context.Background())
			results <- projectAssistantSandboxEnsureResult{sandbox: got, err: err}
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("sandbox initializer did not start")
	}
	close(proceed)
	wg.Wait()
	close(results)
	for result := range results {
		if result.err != nil || result.sandbox != sandbox {
			t.Fatalf("EnsureSandbox result = (%p, %v), want (%p, nil)", result.sandbox, result.err, sandbox)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("initializer calls = %d, want one", got)
	}
	if got, err := state.EnsureSandbox(context.Background()); err != nil || got != sandbox {
		t.Fatalf("published EnsureSandbox result = (%p, %v), want (%p, nil)", got, err, sandbox)
	}
	if release := state.SandboxRelease(); release == nil {
		t.Fatal("published sandbox release is nil")
	} else {
		release()
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("release calls = %d, want one", got)
	}
}

func TestProjectAssistantRunStateEnsureSandboxCanceledWaiterDoesNotCancelSetup(t *testing.T) {
	runCtx := context.WithValue(context.Background(), projectAssistantSandboxTestContextKey{}, "run")
	state := newProjectEinoAssistantRunState()
	started := make(chan struct{})
	proceed := make(chan struct{})
	setupCanceled := make(chan struct{})
	sandbox := &projectAssistantRunSandbox{}
	var calls atomic.Int32
	state.ConfigureSandboxCapabilityWithContext(runCtx, CodingSandboxEligibility{Eligible: true}, func(ctx context.Context) (*projectAssistantRunSandbox, func(), error) {
		if got := ctx.Value(projectAssistantSandboxTestContextKey{}); got != "run" {
			return nil, nil, fmt.Errorf("initializer context value = %v, want run context", got)
		}
		calls.Add(1)
		close(started)
		select {
		case <-ctx.Done():
			close(setupCanceled)
			return nil, nil, ctx.Err()
		case <-proceed:
			return sandbox, nil, nil
		}
	})

	ownerResult := make(chan projectAssistantSandboxEnsureResult, 1)
	go func() {
		got, err := state.EnsureSandbox(context.Background())
		ownerResult <- projectAssistantSandboxEnsureResult{sandbox: got, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("sandbox initializer did not start")
	}

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterResult := make(chan projectAssistantSandboxEnsureResult, 1)
	go func() {
		got, err := state.EnsureSandbox(waiterCtx)
		waiterResult <- projectAssistantSandboxEnsureResult{sandbox: got, err: err}
	}()
	cancelWaiter()
	select {
	case result := <-waiterResult:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("canceled waiter error = %v, want context.Canceled", result.err)
		}
		if result.sandbox != nil {
			t.Fatalf("canceled waiter sandbox = %p, want nil", result.sandbox)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not return")
	}
	select {
	case <-setupCanceled:
		t.Fatal("canceled waiter canceled shared setup")
	default:
	}

	close(proceed)
	select {
	case result := <-ownerResult:
		if result.err != nil || result.sandbox != sandbox {
			t.Fatalf("owner result = (%p, %v), want (%p, nil)", result.sandbox, result.err, sandbox)
		}
	case <-time.After(time.Second):
		t.Fatal("owner setup did not finish")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("initializer calls = %d, want one", got)
	}
}

func TestProjectAssistantRunStateEnsureSandboxRetriesTransientFailure(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	transient := errors.New("transient sandbox setup failure")
	sandbox := &projectAssistantRunSandbox{}
	var calls atomic.Int32
	state.ConfigureSandboxCapabilityWithContext(context.Background(), CodingSandboxEligibility{Eligible: true}, func(context.Context) (*projectAssistantRunSandbox, func(), error) {
		if calls.Add(1) == 1 {
			return nil, nil, transient
		}
		return sandbox, nil, nil
	})

	if got, err := state.EnsureSandbox(context.Background()); !errors.Is(err, transient) || got != nil {
		t.Fatalf("first EnsureSandbox result = (%p, %v), want (nil, transient)", got, err)
	}
	if got, err := state.EnsureSandbox(context.Background()); err != nil || got != sandbox {
		t.Fatalf("retry EnsureSandbox result = (%p, %v), want (%p, nil)", got, err, sandbox)
	}
	if got, err := state.EnsureSandbox(context.Background()); err != nil || got != sandbox {
		t.Fatalf("published EnsureSandbox result = (%p, %v), want (%p, nil)", got, err, sandbox)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("initializer calls = %d, want one failed attempt plus one retry", got)
	}
}

func TestProjectAssistantCommandReceiptSurvivesCheckpointAndBecomesStaleAfterMutation(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	exitCode := 7
	state.RecordCommandReceipt("call-command-check", "failed", &projectAssistantExecMetadata{
		Component:      "backend",
		Status:         "failed",
		SourceRevision: 19,
		SyncStatus:     "synced",
		ExitCode:       &exitCode,
	})

	exitCode = 99
	if got := state.CompletionEvidence().CommandReceipts[0].ExitCode; got == nil || *got != 7 {
		t.Fatalf("state receipt aliased caller exit code: %v", got)
	}
	checkpoint := state.CheckpointState()
	if len(checkpoint.CommandReceipts) != 1 || checkpoint.CommandReceipts[0].ExitCode == nil || *checkpoint.CommandReceipts[0].ExitCode != 7 {
		t.Fatalf("checkpoint command receipt = %#v, want independent exit-code copy", checkpoint.CommandReceipts)
	}
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	*checkpoint.CommandReceipts[0].ExitCode = 88
	evidence := restored.CompletionEvidence()
	if len(evidence.CommandReceipts) != 1 {
		t.Fatalf("restored command receipts = %#v, want one durable receipt", evidence.CommandReceipts)
	}
	receipt := evidence.CommandReceipts[0]
	if receipt.ReferenceID != projectAssistantActionPublicID("call-command-check") || receipt.Status != "failed" ||
		receipt.Component != "backend" || receipt.WorkspaceSourceRevision != 19 || receipt.MutationRevision != 1 ||
		receipt.SourceRelation != "dispatch_revision_recorded" || receipt.ExitCode == nil || *receipt.ExitCode != 7 {
		t.Fatalf("restored command receipt = %#v, want bounded failure provenance", receipt)
	}

	restored.RecordSourceMutation()
	receipt = restored.CompletionEvidence().CommandReceipts[0]
	if receipt.SourceRelation != "stale" {
		t.Fatalf("command receipt relation after a newer source mutation = %q, want stale", receipt.SourceRelation)
	}
}

func TestProjectAssistantCommandReceiptDoesNotInferFreshnessFromPrestartFailures(t *testing.T) {
	for _, tc := range []struct {
		id     string
		status string
	}{
		{id: "call-blocked-before-start", status: "blocked"},
		{id: "call-start-error-without-session", status: "error"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			state := newProjectEinoAssistantRunState()
			state.RecordCommandReceipt(tc.id, "failed", &projectAssistantExecMetadata{
				Component:      "backend",
				Status:         tc.status,
				SourceRevision: 9,
			})
			receipts := state.CompletionEvidence().CommandReceipts
			if len(receipts) != 1 || receipts[0].SourceRelation != "dispatch_revision_recorded" {
				t.Fatalf("pre-start receipt = %#v, want dispatch revision only without a freshness claim", receipts)
			}
		})
	}
}

func TestProjectAssistantCommandReceiptPreservesStaleStatusAndSuccessfulExitCode(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	exitCode := 0
	state.RecordCommandReceipt("call-stale-check", "failed", &projectAssistantExecMetadata{
		Component:      "backend",
		Status:         "stale",
		SourceRevision: 19,
		SourceStale:    true,
		SyncStatus:     "stale",
		ExitCode:       &exitCode,
	})
	evidence := state.CompletionEvidence()
	if len(evidence.CommandReceipts) != 1 {
		t.Fatalf("command receipts = %#v, want stale receipt", evidence.CommandReceipts)
	}
	receipt := evidence.CommandReceipts[0]
	if receipt.Status != "stale" || receipt.ExitCode == nil || *receipt.ExitCode != 0 || receipt.SyncStatus != "stale" {
		t.Fatalf("stale command receipt = %#v, want stale applicability with exit code 0 preserved", receipt)
	}
}

func TestProjectAssistantRunStateUnknownBrowserReceiptCannotVerifyInteraction(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()

	state.RecordToolMessage(chatMessage{
		Role: "tool", Name: browserMCPToolSnapshot,
		Content: nativeBrowserValidSnapshotReceipt,
	})
	prior := state.CompletionEvidence()
	if !prior.PreviewRenderedStateObserved || prior.PreviewEvidenceOutcome != "rendered_verified" {
		t.Fatalf("prior snapshot evidence = %#v, want rendered verification", prior)
	}

	state.RecordToolMessage(chatMessage{
		Role: "tool", Name: "browser_click",
		Content: `{"status":"outcome_unknown","outcome":"unknown","replayed":false}`,
	})
	if state.NativeBrowserInteractionPending() {
		t.Fatal("outcome-unknown interaction left a pending interaction")
	}
	if got := state.CompletionEvidence(); !reflect.DeepEqual(got, prior) {
		t.Fatalf("outcome-unknown receipt changed prior evidence: got %#v, want %#v", got, prior)
	}

	state.RecordToolMessage(chatMessage{
		Role: "tool", Name: browserMCPToolSnapshot,
		Content: nativeBrowserValidSnapshotReceipt,
	})
	got := state.CompletionEvidence()
	if got.PreviewInteractionVerified || got.PreviewEvidenceOutcome == "interactions_verified" {
		t.Fatalf("snapshot verified an outcome-unknown interaction: %#v", got)
	}
}

func TestProjectAssistantRunStateNotExecutedBrowserReceiptDoesNotBecomeInteractionEvidence(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	state.RecordToolMessage(chatMessage{
		Role: "tool", Name: "browser_click",
		Content: `{"status":"not_executed","outcome":"not_executed","replayed":false,"error":"preflight failed"}`,
	})
	if state.NativeBrowserInteractionPending() {
		t.Fatal("not_executed interaction left a pending interaction")
	}
	if evidence := state.CompletionEvidence(); evidence.PreviewInteractionVerified || evidence.PreviewEvidenceOutcome != "failed" {
		t.Fatalf("not_executed interaction evidence = %#v, want failed and unverified", evidence)
	}

	state.RecordToolMessage(chatMessage{Role: "tool", Name: browserMCPToolSnapshot, Content: nativeBrowserValidSnapshotReceipt})
	if evidence := state.CompletionEvidence(); evidence.PreviewInteractionVerified || evidence.PreviewEvidenceOutcome == "interactions_verified" {
		t.Fatalf("later snapshot certified a click that was never dispatched: %#v", evidence)
	}
}

func TestProjectAssistantRunStateUnverifiableBrowserObservationIsNotEvidence(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	unverifiable := `{"status":"unverifiable","outcome":"unknown","requiresSnapshot":true,"content":[{"type":"text","text":"- Page URL: https://demo.preview.example/\n- Page Snapshot:\n- generic [ref=e1]:"}]}`
	state.RecordToolMessage(chatMessage{Role: "tool", Name: browserMCPToolSnapshot, Content: unverifiable})
	if got := state.CompletionEvidence(); got.PreviewRenderedStateObserved || got.PreviewInteractionVerified || got.PreviewEvidenceOutcome != "" {
		t.Fatalf("unverifiable observation became evidence: %#v", got)
	}

	state.RecordToolMessage(chatMessage{Role: "tool", Name: browserMCPToolSnapshot, Content: nativeBrowserValidSnapshotReceipt})
	prior := state.CompletionEvidence()
	state.RecordToolMessage(chatMessage{Role: "tool", Name: browserMCPToolSnapshot, Content: unverifiable})
	if got := state.CompletionEvidence(); !reflect.DeepEqual(got, prior) {
		t.Fatalf("unverifiable observation erased prior evidence: got %#v, want %#v", got, prior)
	}
}

func TestProjectAssistantRunStateUnverifiableSessionLossClearsPendingInteraction(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	state.RecordToolMessage(chatMessage{Role: "tool", Name: "browser_click", Content: `{"isError":false,"content":[{"type":"text","text":"clicked"}]}`})
	if !state.NativeBrowserInteractionPending() {
		t.Fatal("successful interaction did not become pending")
	}

	state.RecordToolMessage(chatMessage{
		Role: "tool", Name: browserMCPToolSnapshot,
		Content: `{"status":"unverifiable","outcome":"unknown","requiresSnapshot":true,"error":"preview browser tools/call: status 404: Session not found"}`,
	})
	if state.NativeBrowserInteractionPending() {
		t.Fatal("production-shaped unverifiable receipt retained the pending interaction")
	}
	if evidence := state.CompletionEvidence(); evidence.PreviewInteractionVerified || evidence.PreviewEvidenceOutcome == "interactions_verified" {
		t.Fatalf("unverifiable receipt certified interaction evidence: %#v", evidence)
	}
}
