// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package schedulepolicy

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

func mkSched(typ, cronExpr, tz string) *agentsv1alpha1.Schedule {
	s := &agentsv1alpha1.Schedule{}
	s.Spec.Type = typ
	s.Spec.Schedule = cronExpr
	s.Spec.TimeZone = tz
	return s
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)

	t.Run("cron first sight initializes nextRun without firing", func(t *testing.T) {
		s := mkSched("cron", "0 10 * * *", "")
		fire, next, err := Due(s, now)
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
		fire, next, err := Due(s, now)
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
		fire, next, err := Due(s, now)
		if err != nil || fire {
			t.Fatalf("fire=%v err=%v, want no fire", fire, err)
		}
		if !next.IsZero() {
			t.Fatalf("next=%v, want zero (the planned time is already on status)", next)
		}
	})

	t.Run("timezone respected", func(t *testing.T) {
		// 09:00 UTC = 12:00 in Vilnius (UTC+3 in July): "0 13 * * *" local →
		// next fire 13:00 Vilnius = 10:00 UTC.
		s := mkSched("cron", "0 13 * * *", "Europe/Vilnius")
		_, next, err := Due(s, now)
		if err != nil {
			t.Fatal(err)
		}
		if !next.Equal(time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)) {
			t.Fatalf("next=%v, want 10:00 UTC (13:00 Vilnius)", next)
		}
	})

	t.Run("edited cron re-arms from new spec once stale nextRun is dropped", func(t *testing.T) {
		// Regression: a schedule first created with a "soon" test cron fires and
		// stores nextRun at that old cadence. The user then retimes it to
		// "0 9 * * *" Europe/Vilnius. The reconciler detects the generation bump
		// and drops the stale nextRun; Due must then re-derive the next fire from
		// the NEW spec (09:00 Vilnius = 06:00 UTC) rather than honoring the old
		// value or firing immediately.
		s := mkSched("cron", "0 9 * * *", "Europe/Vilnius")
		stale := metav1.NewTime(now.Add(-2 * time.Hour)) // old cadence, in the past
		s.Status.NextRun = &stale

		// The reconciler nils NextRun when generation != observedGeneration.
		s.Status.NextRun = nil

		fire, next, err := Due(s, now)
		if err != nil || fire {
			t.Fatalf("fire=%v err=%v, want re-arm without firing", fire, err)
		}
		// now is 09:00 UTC = 12:00 Vilnius, so 09:00 Vilnius already passed today;
		// the next fire is tomorrow 09:00 Vilnius = 06:00 UTC on the 14th.
		if !next.Equal(time.Date(2026, 7, 14, 6, 0, 0, 0, time.UTC)) {
			t.Fatalf("next=%v, want 06:00 UTC on the 14th (09:00 Vilnius)", next)
		}
	})

	t.Run("bad cron is a permanent error", func(t *testing.T) {
		s := mkSched("cron", "not-a-cron", "")
		if _, _, err := Due(s, now); err == nil {
			t.Fatal("want permanent error for invalid cron")
		}
	})

	t.Run("bad timezone is a permanent error", func(t *testing.T) {
		s := mkSched("cron", "0 * * * *", "Mars/Olympus")
		if _, _, err := Due(s, now); err == nil {
			t.Fatal("want permanent error for invalid timezone")
		}
	})

	t.Run("wakeup fires once at runAt then never again", func(t *testing.T) {
		s := mkSched("wakeup", "", "")
		ra := metav1.NewTime(now.Add(-time.Second))
		s.Spec.RunAt = &ra
		fire, _, err := Due(s, now)
		if err != nil || !fire {
			t.Fatalf("fire=%v err=%v, want fire", fire, err)
		}
		lr := metav1.NewTime(now)
		s.Status.LastRun = &lr
		fire, _, _ = Due(s, now.Add(time.Hour))
		if fire {
			t.Fatal("wakeup must not fire twice")
		}
	})

	t.Run("wakeup in the future reports runAt as next", func(t *testing.T) {
		s := mkSched("wakeup", "", "")
		ra := metav1.NewTime(now.Add(2 * time.Hour))
		s.Spec.RunAt = &ra
		fire, next, err := Due(s, now)
		if err != nil || fire {
			t.Fatalf("fire=%v err=%v, want no fire", fire, err)
		}
		if !next.Equal(ra.Time) {
			t.Fatalf("next=%v, want runAt %v", next, ra.Time)
		}
	})

	t.Run("wakeup without runAt is permanent error", func(t *testing.T) {
		s := mkSched("wakeup", "", "")
		if _, _, err := Due(s, now); err == nil {
			t.Fatal("want permanent error")
		}
	})

	t.Run("unknown type is permanent error", func(t *testing.T) {
		s := mkSched("weird", "", "")
		if _, _, err := Due(s, now); err == nil {
			t.Fatal("want permanent error")
		}
	})
}

func TestHeartbeatPromptCarriesChecklist(t *testing.T) {
	got := HeartbeatPrompt("- inbox\n- PRs")
	if !strings.Contains(got, "reply with exactly OK") || !strings.Contains(got, "- inbox\n- PRs") {
		t.Fatalf("prompt should ask for a quiet OK and carry the checklist, got %q", got)
	}
}
