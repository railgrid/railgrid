// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestRunProgressConcurrentLifecycleWrites(t *testing.T) {
	for _, kind := range []string{"memory", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var st Store = NewMemoryStore()
			scope := Scope{OrgUUID: "org", WorkspaceUUID: "stress", AgentName: "coder"}
			if kind == "postgres" {
				pg := openTestPostgres(t)
				st, scope = pg, pgScope(t, pg)
			}
			ctx := t.Context()
			now := time.Now().UTC().Truncate(time.Microsecond)
			stale := Run{ID: "concurrent", AgentName: scope.AgentName, SessionID: "s", Phase: RunPhaseRunning, CreatedAt: now, UpdatedAt: now}
			if err := st.SaveRun(ctx, scope, stale); err != nil {
				t.Fatal(err)
			}
			if err := st.PutRunProgress(ctx, scope, stale.ID, []Message{{ID: "partial", RunID: stale.ID, Role: "assistant", Content: "0"}}); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			failures := make(chan error, 10)
			var workers sync.WaitGroup
			launch := func(work func() error) {
				workers.Add(1)
				go func() {
					defer workers.Done()
					<-start
					if err := work(); err != nil {
						failures <- err
					}
				}()
			}
			launch(func() error {
				for i := range 200 {
					if err := st.PutRunProgress(ctx, scope, stale.ID, []Message{{ID: "partial", RunID: stale.ID, Role: "assistant", Content: strconv.Itoa(i)}}); err != nil {
						return err
					}
				}
				return nil
			})
			// These copies predate every progress update and cancellation.
			for range 4 {
				launch(func() error {
					for range 150 {
						if err := st.SaveRun(ctx, scope, stale); err != nil {
							return err
						}
					}
					return nil
				})
			}
			for range 4 {
				launch(func() error {
					previous := -1
					for range 200 {
						rows, err := st.GetRunProgress(ctx, scope, stale.ID)
						if err != nil {
							return err
						}
						if len(rows) != 1 || rows[0].ID != "partial" || rows[0].RunID != stale.ID {
							return fmt.Errorf("incomplete snapshot: %+v", rows)
						}
						sequence, err := strconv.Atoi(rows[0].Content)
						if err != nil || sequence < previous {
							return fmt.Errorf("snapshot rolled back from %d to %q", previous, rows[0].Content)
						}
						previous = sequence
						rows[0].Content = "reader-local mutation"
					}
					return nil
				})
			}
			launch(func() error { return st.RequestCancel(ctx, scope, stale.ID, now, "") })
			close(start)
			workers.Wait()
			close(failures)
			for err := range failures {
				t.Error(err)
			}
			rows, err := st.GetRunProgress(ctx, scope, stale.ID)
			if err != nil || len(rows) != 1 || rows[0].Content != "199" {
				t.Fatalf("latest progress lost: %+v, %v", rows, err)
			}
			run, err := st.GetRun(ctx, scope, stale.ID)
			if err != nil || !run.CancelRequested || run.CancelRequestedAt == nil || !run.CancelRequestedAt.Equal(now) {
				t.Fatalf("cancellation lost: %+v, %v", run, err)
			}
		})
	}
}

func TestRunProgressIsolationAndIndependentLifecycleWrites(t *testing.T) {
	for _, kind := range []string{"memory", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var st Store = NewMemoryStore()
			scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coder"}
			if kind == "postgres" {
				pg := openTestPostgres(t)
				st, scope = pg, pgScope(t, pg)
			}
			ctx := t.Context()
			// PostgreSQL timestamps retain microseconds, unlike Go time.Time.
			now := time.Now().UTC().Truncate(time.Microsecond)
			run := Run{ID: "r", AgentName: scope.AgentName, SessionID: "s", Phase: RunPhaseRunning, CreatedAt: now, UpdatedAt: now}
			if err := st.SaveRun(ctx, scope, run); err != nil {
				t.Fatal(err)
			}
			row := Message{ID: "text", Role: "assistant", Content: "Working", RunID: "r", CreatedAt: now}
			if err := st.PutRunProgress(ctx, scope, "r", []Message{row}); err != nil {
				t.Fatal(err)
			}
			if err := st.RequestCancel(ctx, scope, "r", now, ""); err != nil {
				t.Fatal(err)
			}
			// A lifecycle write from a copy predating progress and cancellation
			// must not erase either independently written field.
			if err := st.SaveRun(ctx, scope, run); err != nil {
				t.Fatal(err)
			}
			rows, err := st.GetRunProgress(ctx, scope, "r")
			if err != nil || len(rows) != 1 || rows[0].Content != "Working" {
				t.Fatalf("progress = %+v, %v", rows, err)
			}
			rows[0].Content = "mutated by reader"
			rows, err = st.GetRunProgress(ctx, scope, "r")
			if err != nil || rows[0].Content != "Working" {
				t.Fatalf("mutable progress snapshot: %+v, %v", rows, err)
			}
			stored, err := st.GetRun(ctx, scope, "r")
			if err != nil || !stored.CancelRequested || !stored.UpdatedAt.Equal(now) {
				t.Fatalf("progress changed lifecycle: %+v, %v", stored, err)
			}
			for _, foreign := range []Scope{
				{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID + "-other", AgentName: scope.AgentName},
				{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, AgentName: scope.AgentName + "-other"},
			} {
				if _, err := st.GetRunProgress(ctx, foreign, "r"); err == nil {
					t.Fatal("foreign progress read succeeded")
				}
				if err := st.PutRunProgress(ctx, foreign, "r", nil); err == nil {
					t.Fatal("foreign progress write succeeded")
				}
			}
			if err := st.PutRunProgress(ctx, scope, "r", nil); err != nil {
				t.Fatal(err)
			}
			rows, err = st.GetRunProgress(ctx, scope, "r")
			if err != nil || len(rows) != 0 {
				t.Fatalf("reset progress = %+v, %v", rows, err)
			}
		})
	}
}
