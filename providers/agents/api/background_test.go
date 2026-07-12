// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
)

func mkSched(typ, cronExpr, tz string) *agentsv1alpha1.AgentSchedule {
	s := &agentsv1alpha1.AgentSchedule{}
	s.Spec.Type = typ
	s.Spec.Schedule = cronExpr
	s.Spec.TimeZone = tz
	return s
}

func TestScheduleDue(t *testing.T) {
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)

	t.Run("cron first sight initializes nextRun without firing", func(t *testing.T) {
		s := mkSched("cron", "0 10 * * *", "")
		fire, next, err := scheduleDue(s, now)
		if err != nil || fire {
			t.Fatalf("fire=%v err=%v, want no fire", fire, err)
		}
		if next.IsZero() || !next.Equal(time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)) {
			t.Fatalf("next=%v, want 10:00 UTC today", next)
		}
	})

	t.Run("cron fires once nextRun passes and advances", func(t *testing.T) {
		s := mkSched("cron", "0 * * * *", "")
		nr := metav1.NewTime(now.Add(-time.Minute))
		s.Status.NextRun = &nr
		fire, next, err := scheduleDue(s, now)
		if err != nil || !fire {
			t.Fatalf("fire=%v err=%v, want fire", fire, err)
		}
		if !next.Equal(time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)) {
			t.Fatalf("next=%v, want next hour", next)
		}
	})

	t.Run("cron not due", func(t *testing.T) {
		s := mkSched("cron", "0 * * * *", "")
		nr := metav1.NewTime(now.Add(30 * time.Minute))
		s.Status.NextRun = &nr
		fire, _, err := scheduleDue(s, now)
		if err != nil || fire {
			t.Fatalf("fire=%v err=%v, want no fire", fire, err)
		}
	})

	t.Run("timezone respected", func(t *testing.T) {
		// 09:00 UTC = 12:00 in Vilnius (UTC+3 in July): "0 13 * * *" local →
		// next fire 13:00 Vilnius = 10:00 UTC.
		s := mkSched("cron", "0 13 * * *", "Europe/Vilnius")
		_, next, err := scheduleDue(s, now)
		if err != nil {
			t.Fatal(err)
		}
		if !next.Equal(time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)) {
			t.Fatalf("next=%v, want 10:00 UTC (13:00 Vilnius)", next)
		}
	})

	t.Run("bad cron is a permanent error", func(t *testing.T) {
		s := mkSched("cron", "not-a-cron", "")
		if _, _, err := scheduleDue(s, now); err == nil {
			t.Fatal("want permanent error for invalid cron")
		}
	})

	t.Run("wakeup fires once at runAt then never again", func(t *testing.T) {
		s := mkSched("wakeup", "", "")
		ra := metav1.NewTime(now.Add(-time.Second))
		s.Spec.RunAt = &ra
		fire, _, err := scheduleDue(s, now)
		if err != nil || !fire {
			t.Fatalf("fire=%v err=%v, want fire", fire, err)
		}
		lr := metav1.NewTime(now)
		s.Status.LastRun = &lr
		fire, _, _ = scheduleDue(s, now.Add(time.Hour))
		if fire {
			t.Fatal("wakeup must not fire twice")
		}
	})

	t.Run("wakeup without runAt is permanent error", func(t *testing.T) {
		s := mkSched("wakeup", "", "")
		if _, _, err := scheduleDue(s, now); err == nil {
			t.Fatal("want permanent error")
		}
	})
}
