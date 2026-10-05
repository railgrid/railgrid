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
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

type admissionProbeStore struct {
	store.Store
	saveErr     error
	budgetReads atomic.Int32
}

func (s *admissionProbeStore) SaveRun(ctx context.Context, scope store.Scope, run store.Run) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.Store.SaveRun(ctx, scope, run)
}
func (s *admissionProbeStore) GetUsage(ctx context.Context, scope store.Scope, agent string, now time.Time, window time.Duration) (store.Usage, error) {
	s.budgetReads.Add(1)
	return s.Store.GetUsage(ctx, scope, agent, now, window)
}

func TestRunAdmissionFailsClosedWhenRecordCannotBeSaved(t *testing.T) {
	for _, key := range []string{"", "retry-key"} {
		t.Run(key, func(t *testing.T) {
			cause := errors.New("store unavailable")
			st := &admissionProbeStore{Store: store.NewMemoryStore(), saveErr: cause}
			s := &Server{store: st, events: newEventBus()}
			scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"}
			a := &agentsv1alpha1.Agent{}
			a.Name = scope.AgentName
			a.Spec.Budget = &agentsv1alpha1.AgentBudget{TokenLimit: 1}
			result, err := s.startRun(t.Context(), scope, a, taskRun{Trigger: "api", Task: "must not execute", IdempotencyKey: key}, runAccess{})
			if !errors.Is(err, cause) || result.ID != "" {
				t.Fatalf("unpersisted run was admitted: %+v, %v", result, err)
			}
			if st.budgetReads.Load() != 0 {
				t.Fatal("unpersisted run began execution")
			}
		})
	}
}

func TestPostgresConcurrentRunAdmissionReusesOneExecution(t *testing.T) {
	dsn := os.Getenv("AGENTS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AGENTS_TEST_POSTGRES_DSN is not set")
	}
	pg, err := store.OpenPostgres(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	if err := pg.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	scope := store.Scope{OrgUUID: "admission-" + uuid.NewString(), WorkspaceUUID: "workspace", AgentName: "agent"}
	t.Cleanup(func() { _ = pg.DeleteAgentData(context.Background(), scope, scope.AgentName) })
	if _, err := pg.AddUsage(t.Context(), scope, scope.AgentName, 2, 0, 0, time.Now(), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	st := &admissionProbeStore{Store: pg}
	s := &Server{store: st, events: newEventBus()}
	a := &agentsv1alpha1.Agent{}
	a.Name = scope.AgentName
	a.Spec.Budget = &agentsv1alpha1.AgentBudget{Window: "day", TokenLimit: 1}
	const callers = 8
	results := make([]runAdmission, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range callers {
		workers.Go(func() {
			<-start
			results[i], errs[i] = s.startRun(t.Context(), scope, a, taskRun{SessionID: "same", Trigger: "api", Task: "budget refuses execution", IdempotencyKey: "same-key"}, runAccess{})
		})
	}
	close(start)
	workers.Wait()
	reused := 0
	for i, result := range results {
		if errs[i] != nil || result.ID == "" || result.ID != results[0].ID {
			t.Fatalf("retry %d created a different run: %+v, err=%v; first=%+v", i, result, errs[i], results[0])
		}
		if result.Reused {
			reused++
		}
	}
	if reused != callers-1 {
		t.Fatalf("reused=%d, want %d", reused, callers-1)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		run, err := pg.GetRun(t.Context(), scope, results[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Phase == store.RunPhaseFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("admitted run did not settle: %+v", run)
		}
		time.Sleep(time.Millisecond)
	}
	if got := st.budgetReads.Load(); got != 1 {
		t.Fatalf("executions=%d, want exactly 1", got)
	}
}
