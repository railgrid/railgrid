// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package pullrequest

import (
	"testing"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRetiredCommandDoesNotRetainActivePollingCadence(t *testing.T) {
	now := time.Date(2026, 11, 9, 19, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name           string
		until          time.Time
		interval, want time.Duration
	}{
		{"active", now.Add(time.Hour), 30 * time.Second, 30 * time.Second},
		{"expired", now.Add(-time.Second), 30 * time.Second, defaultObserveInterval},
		{"deadline boundary", now, 30 * time.Second, defaultObserveInterval},
		{"longer configured interval preserved", now.Add(-time.Second), 10 * time.Minute, 10 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr := resource(nil)
			pr.Spec.ObserveInterval = &metav1.Duration{Duration: tc.interval}
			pr.Spec.Command = &api.PullRequestCommand{Deadline: metav1.NewTime(tc.until)}
			if got := observeIntervalAt(pr, now); got != tc.want {
				t.Fatalf("interval=%s want %s", got, tc.want)
			}
		})
	}
}

func TestRevokedTaskUsesHistoricalCadenceUntilNewCommand(t *testing.T) {
	now := time.Date(2026, 11, 9, 19, 0, 0, 0, time.UTC)
	pr := resource(nil)
	pr.Spec.ObserveInterval = &metav1.Duration{Duration: 30 * time.Second}
	pr.Spec.TaskAuthorizationRevocations = []api.PullRequestTaskRevocation{{SourceUID: "retired-task"}}
	if got := observeIntervalAt(pr, now); got != defaultObserveInterval {
		t.Fatalf("retired interval=%s", got)
	}
	pr.Spec.Command = &api.PullRequestCommand{Deadline: metav1.NewTime(now.Add(time.Hour))}
	if got := observeIntervalAt(pr, now); got != 30*time.Second {
		t.Fatalf("new active command interval=%s", got)
	}
}
