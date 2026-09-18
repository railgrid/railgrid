// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package schedulepolicy decides WHEN a Schedule fires. It is pure: no clients,
// no clock of its own. The Schedule reconciler asks it on every reconcile, and
// the HTTP layer (run-now, previews) can ask the same question without
// importing the controller.
package schedulepolicy

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// HeartbeatPrompt wraps a standing checklist so quiet heartbeats stay quiet.
func HeartbeatPrompt(checklist string) string {
	return "Review this standing checklist. If nothing needs attention, reply with exactly OK and nothing else. " +
		"If something is actionable, report it concisely:\n\n" + checklist
}

// Due decides whether a schedule fires now and what its next fire time is.
// permErr marks unrecoverable spec problems (bad cron, wakeup w/o runAt).
//
// For cron/heartbeat: with no status.nextRun the schedule is being armed —
// next is the first fire after now and fire is false. With a nextRun in the
// past (or now) it fires and next is the following occurrence. With a nextRun
// in the future nothing happens and next is zero (the caller already knows
// the planned time from status).
//
// For wakeup: fires once when runAt has passed and never again (a lastRun
// means it already did); next is runAt while it is still in the future.
func Due(sched *agentsv1alpha1.Schedule, now time.Time) (fire bool, next time.Time, permErr error) {
	switch sched.Spec.Type {
	case agentsv1alpha1.ScheduleTypeCron, agentsv1alpha1.ScheduleTypeHeartbeat:
		loc := time.UTC
		if tz := strings.TrimSpace(sched.Spec.TimeZone); tz != "" {
			l, err := time.LoadLocation(tz)
			if err != nil {
				return false, time.Time{}, fmt.Errorf("invalid timeZone %q: %v", tz, err)
			}
			loc = l
		}
		expr, err := cron.ParseStandard(sched.Spec.Schedule)
		if err != nil {
			return false, time.Time{}, fmt.Errorf("invalid cron %q: %v", sched.Spec.Schedule, err)
		}
		nextFromNow := expr.Next(now.In(loc)).UTC()
		if sched.Status.NextRun == nil {
			return false, nextFromNow, nil
		}
		if !now.Before(sched.Status.NextRun.Time) {
			return true, nextFromNow, nil
		}
		return false, time.Time{}, nil
	case agentsv1alpha1.ScheduleTypeWakeup:
		if sched.Status.LastRun != nil {
			return false, time.Time{}, nil // one-shot already fired
		}
		if sched.Spec.RunAt == nil {
			return false, time.Time{}, fmt.Errorf("wakeup schedule has no runAt")
		}
		if !now.Before(sched.Spec.RunAt.Time) {
			return true, time.Time{}, nil
		}
		return false, sched.Spec.RunAt.Time.UTC(), nil
	default:
		return false, time.Time{}, fmt.Errorf("unknown schedule type %q", sched.Spec.Type)
	}
}
