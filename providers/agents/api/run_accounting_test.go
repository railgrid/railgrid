// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

func TestFailedHarnessTurnBillsPartialUsage(t *testing.T) {
	dispatcher := &accountingHarnessDispatcher{
		events:    []runner.Event{accountingUsageEvent(1, 600, 10, 610)},
		eventsErr: errors.New("runner event stream failed after usage"),
	}
	s, run := newHarnessRunLifecycleFixture(t, dispatcher, 0)
	result, err := s.runTurn(context.Background(), run, nil)
	if err == nil || result.Phase != store.RunPhaseFailed {
		t.Fatalf("runTurn result=%+v err=%v, want failed after partial usage", result, err)
	}
	if result.Usage.InputTokens != 600 || result.Usage.OutputTokens != 10 {
		t.Fatalf("failed run usage = %+v, want 600 input and 10 output", result.Usage)
	}
	stored, err := s.store.GetRun(context.Background(), run.Scope, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != store.RunPhaseFailed || stored.InputTokens != 600 || stored.OutputTokens != 10 {
		t.Fatalf("failed run record = %+v, want failed with partial token totals", stored)
	}
	usage, err := s.store.GetUsage(context.Background(), run.Scope, run.Agent.Name, time.Now().UTC(), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 600 || usage.OutputTokens != 10 {
		t.Fatalf("rolling usage = %+v, want the observed 600/10 tokens billed", usage)
	}
}

func TestFailedResumedHarnessTurnKeepsPriorUsageAndBillsOnlyDelta(t *testing.T) {
	for _, test := range []struct {
		name       string
		inspectErr error
		lastIn     int64
		lastOut    int64
		wantIn     int64
		wantOut    int64
	}{
		{name: "partial usage after resume", lastIn: 80, lastOut: 8, wantIn: 380, wantOut: 12},
		{name: "failure before snapshot restore", inspectErr: errors.New("runner inspect failed"), wantIn: 300, wantOut: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			prior := backend.Cost{Tokens: backend.Tokens{InputTokens: 300, OutputTokens: 4}, CostMicros: 1234}
			dispatcher := &accountingHarnessDispatcher{inspectErr: test.inspectErr}
			if test.inspectErr == nil {
				dispatcher.events = []runner.Event{accountingUsageEvent(1, test.lastIn, test.lastOut, 300+test.lastIn+4+test.lastOut)}
				dispatcher.eventsErr = errors.New("runner event stream failed after resumed usage")
			}
			s, run := newHarnessRunLifecycleFixture(t, dispatcher, 0)
			run.RunID = "run-resume"
			now := time.Now().UTC()
			if _, err := s.store.AddUsage(context.Background(), run.Scope, run.Agent.Name, prior.InputTokens, prior.OutputTokens, prior.CostMicros, now, 30*24*time.Hour); err != nil {
				t.Fatal(err)
			}
			startedAt := now.Add(-time.Minute)
			if err := s.store.SaveRun(context.Background(), run.Scope, store.Run{
				ID: run.RunID, AgentName: run.Agent.Name, SessionID: run.SessionID,
				Trigger: run.Trigger, Phase: store.RunPhaseRunning,
				InputTokens: prior.InputTokens, OutputTokens: prior.OutputTokens, USDMicros: prior.CostMicros,
				CreatedAt: startedAt, UpdatedAt: now, StartedAt: &startedAt,
			}); err != nil {
				t.Fatal(err)
			}
			rawState, err := json.Marshal(backendharness.State{
				TaskID: harnessTaskID(run.Agent.Name, run.SessionID), AttemptID: run.RunID,
				Epoch: 1, SessionID: "thread",
				BackendKey: harnessBackendKey(run.ClusterID,
					run.Agent.Spec.Harness().EdgeRef.Kind, run.Agent.Spec.Harness().EdgeRef.Name,
					llm.HarnessAdvertisedName(agentsv1alpha1.ModelProviderCodex)),
				Spent:    prior,
				Snapshot: &backendharness.StateSnapshot{Version: 1, TurnStarted: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			cont := &continuation{
				Checkpoint: runCheckpoint{Backend: agentsv1alpha1.AgentBackendHarness, Harness: rawState},
				Billed:     prior, StartedAt: startedAt, Tracker: newTurnProgressTracker(0),
			}

			result, err := s.runTurn(context.Background(), run, cont)
			if err == nil || result.Phase != store.RunPhaseFailed {
				t.Fatalf("runTurn result=%+v err=%v, want failed resume", result, err)
			}
			if result.Usage.InputTokens != test.wantIn || result.Usage.OutputTokens != test.wantOut || result.Usage.USDMicros != prior.CostMicros {
				t.Fatalf("failed resumed result usage = %+v, want %d/%d tokens and prior cost %d", result.Usage, test.wantIn, test.wantOut, prior.CostMicros)
			}
			stored, err := s.store.GetRun(context.Background(), run.Scope, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.InputTokens != test.wantIn || stored.OutputTokens != test.wantOut || stored.USDMicros != prior.CostMicros {
				t.Fatalf("failed resumed run record = %+v, want cumulative totals %d/%d and cost %d", stored, test.wantIn, test.wantOut, prior.CostMicros)
			}
			usage, err := s.store.GetUsage(context.Background(), run.Scope, run.Agent.Name, time.Now().UTC(), 30*24*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			wantBilledIn, wantBilledOut := test.lastIn, test.lastOut
			if test.inspectErr != nil {
				wantBilledIn, wantBilledOut = 0, 0
			}
			if usage.InputTokens != prior.InputTokens+wantBilledIn || usage.OutputTokens != prior.OutputTokens+wantBilledOut || usage.USDMicros != prior.CostMicros {
				t.Fatalf("rolling usage = %+v, want prior plus only %d/%d new tokens", usage, wantBilledIn, wantBilledOut)
			}
		})
	}
}

func accountingUsageEvent(cursor uint64, inputTokens, outputTokens, totalTokens int64) runner.Event {
	data := fmt.Sprintf(`{"turn":{"id":"turn-1","status":"inProgress"},"tokenUsage":{"last":{"inputTokens":%d,"outputTokens":%d},"total":{"totalTokens":%d,"inputTokens":%d,"outputTokens":%d}}}`,
		inputTokens, outputTokens, totalTokens, inputTokens, outputTokens)
	return runner.Event{Cursor: cursor, AttemptEpoch: 1, Type: runner.EventProgress, Data: json.RawMessage(data)}
}

type accountingHarnessDispatcher struct {
	events     []runner.Event
	eventsErr  error
	inspectErr error
}

func (d *accountingHarnessDispatcher) Start(_ context.Context, req runner.StartRequest) (runner.Receipt, error) {
	return runner.Receipt{AttemptID: req.AttemptID, AttemptEpoch: req.AttemptEpoch, SessionID: "thread", Phase: runner.PhaseRunning}, nil
}

func (*accountingHarnessDispatcher) Resume(_ context.Context, req runner.ResumeRequest) (runner.Receipt, error) {
	return runner.Receipt{AttemptID: req.AttemptID, AttemptEpoch: req.AttemptEpoch, SessionID: "thread", Phase: runner.PhaseRunning}, nil
}

func (*accountingHarnessDispatcher) Cancel(_ context.Context, req runner.CancelRequest) (runner.Receipt, error) {
	return runner.Receipt{AttemptID: req.AttemptID, AttemptEpoch: req.AttemptEpoch, SessionID: "thread", Phase: runner.PhaseCancelled}, nil
}

func (d *accountingHarnessDispatcher) Inspect(_ context.Context, attemptID string) (runner.Receipt, error) {
	if d.inspectErr != nil {
		return runner.Receipt{}, d.inspectErr
	}
	return runner.Receipt{AttemptID: attemptID, AttemptEpoch: 1, SessionID: "thread", Phase: runner.PhaseRunning}, nil
}

func (d *accountingHarnessDispatcher) Events(_ context.Context, _ string, after uint64) (backendharness.Stream, error) {
	var events []runner.Event
	for _, event := range d.events {
		if event.Cursor > after {
			events = append(events, event)
		}
	}
	return &accountingHarnessStream{events: events, err: d.eventsErr}, nil
}

type accountingHarnessStream struct {
	events []runner.Event
	err    error
	index  int
}

func (s *accountingHarnessStream) Next(ctx context.Context) (runner.Event, error) {
	if err := ctx.Err(); err != nil {
		return runner.Event{}, err
	}
	if s.index < len(s.events) {
		event := s.events[s.index]
		s.index++
		return event, nil
	}
	if s.err != nil {
		return runner.Event{}, s.err
	}
	return runner.Event{}, io.EOF
}

func (*accountingHarnessStream) Close() error { return nil }
