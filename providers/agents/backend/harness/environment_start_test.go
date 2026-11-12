// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0

package harness

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"

	"github.com/railgrid/provider-agents/backend"
)

type environmentStartRunner struct {
	*fakeRunner
	start func(context.Context, runner.StartRequest) (runner.Receipt, error)
}

func (r environmentStartRunner) Start(ctx context.Context, req runner.StartRequest) (runner.Receipt, error) {
	return r.start(ctx, req)
}

func TestEnvironmentStartRetriesOnlyUnacceptedRefusals(t *testing.T) {
	var requests []runner.StartRequest
	r := environmentStartRunner{fakeRunner: &fakeRunner{}, start: func(_ context.Context, req runner.StartRequest) (runner.Receipt, error) {
		requests = append(requests, req)
		if len(requests) == 1 {
			return runner.Receipt{}, &runner.Error{Code: runner.ErrorUnavailable, Retryable: true, Message: "cleanup pending"}
		}
		return runner.Receipt{AttemptID: req.AttemptID, Phase: runner.PhaseStarting}, nil
	}}
	b := New(Config{Runner: r})
	req := runner.StartRequest{RequestID: "request", AttemptID: "attempt", TaskID: "task", AttemptEpoch: 1, EnvironmentID: "warm", BaseCommit: testBase, ExportGitResult: true}
	got, err := b.startWithEnvironmentRetry(t.Context(), req)
	if err != nil || got.AttemptID != req.AttemptID || len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatalf("retry changed dispatch identity or lost acceptance: attempts=%d receipt=%+v error=%v", len(requests), got, err)
	}
}

func TestEnvironmentStartDoesNotRetryUncertainOrPermanentFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		environment string
	}{
		{"transport acceptance unknown", errors.New("connection reset"), "warm"},
		{"accepted refusal", &runner.Error{Code: runner.ErrorUnavailable, Retryable: true, Receipt: &runner.Receipt{AttemptID: "accepted"}}, "warm"},
		{"permanent failure", &runner.Error{Code: runner.ErrorUnavailable}, "warm"},
		{"invalid request", &runner.Error{Code: runner.ErrorInvalidRequest, Retryable: true}, "warm"},
		{"ephemeral unchanged", &runner.Error{Code: runner.ErrorUnavailable, Retryable: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := environmentStartRunner{fakeRunner: &fakeRunner{}, start: func(context.Context, runner.StartRequest) (runner.Receipt, error) {
				calls++
				return runner.Receipt{}, tc.err
			}}
			_, err := New(Config{Runner: r}).startWithEnvironmentRetry(t.Context(), runner.StartRequest{EnvironmentID: tc.environment})
			if calls != 1 || !errors.Is(err, tc.err) {
				t.Fatalf("failure retried or replaced: calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestEnvironmentStartHonorsParentDeadline(t *testing.T) {
	calls := 0
	r := environmentStartRunner{fakeRunner: &fakeRunner{}, start: func(context.Context, runner.StartRequest) (runner.Receipt, error) {
		calls++
		return runner.Receipt{}, &runner.Error{Code: runner.ErrorBusy, Retryable: true}
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := New(Config{Runner: r}).startWithEnvironmentRetry(ctx, runner.StartRequest{EnvironmentID: "warm"})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("deadline ignored: calls=%d error=%v", calls, err)
	}
}

func TestEnvironmentTurnStillVerifiesSnapshotAfterStartRetry(t *testing.T) {
	for _, base := range []string{testBase, testCommit} {
		t.Run(base, func(t *testing.T) {
			doc := gitResult(t, gitResultDocument{Version: gitResultVersion, TaskID: "run-1", AttemptID: "run-1", AttemptEpoch: 1, BaseCommit: base, NoChanges: true})
			f := &fakeRunner{receipt: completedReceipt(artifactRef("json", GitResultJSONName, doc)), events: completedEvents}
			calls := 0
			r := environmentStartRunner{fakeRunner: f, start: func(ctx context.Context, req runner.StartRequest) (runner.Receipt, error) {
				calls++
				if calls == 1 {
					return runner.Receipt{}, &runner.Error{Code: runner.ErrorUnavailable, Retryable: true}
				}
				return f.Start(ctx, req)
			}}
			repo := testRepository()
			repo.EnvironmentID = "warm-command"
			cfg := repositoryConfig(f, &fakeArtifacts{bodies: map[string][]byte{"json": doc}}, repo)
			cfg.Runner = r
			out, err := New(cfg).Turn(t.Context(), testRun(), backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "verify"}}}, &recordingSink{})
			if calls != 2 {
				t.Fatalf("starts=%d", calls)
			}
			if base == testBase {
				if err != nil || out.Status != backend.StatusCompleted || out.Result == nil || out.Result.BaseCommit != testBase {
					t.Fatalf("verified result lost: status=%s error=%v", out.Status, err)
				}
			} else if err == nil {
				t.Fatal("wrong-base snapshot accepted after warm retry")
			}
		})
	}
}
