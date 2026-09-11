/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package shared

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/faroshq/provider-code/backend"
)

func TestRateLimitWait(t *testing.T) {
	now := time.Date(2026, 9, 10, 17, 35, 0, 0, time.UTC)
	reset := now.Add(5*time.Minute + 53*time.Second)
	wantMessage := "GitHub rate limit; retrying at 2026-09-10T17:40:53Z"
	for _, tc := range []struct {
		name     string
		err      error
		wantWait time.Duration
		wantMsg  string
		wantOK   bool
	}{
		{name: "other error", err: errors.New("boom")},
		{name: "nil", err: nil},
		{name: "future reset", err: &backend.RateLimitError{RetryAt: reset}, wantWait: reset.Sub(now), wantMsg: wantMessage, wantOK: true},
		{name: "wrapped", err: fmt.Errorf("ensure: %w", &backend.RateLimitError{RetryAt: reset}), wantWait: reset.Sub(now), wantMsg: wantMessage, wantOK: true},
		{name: "reset passed", err: &backend.RateLimitError{RetryAt: now.Add(-time.Minute)}, wantWait: time.Second, wantMsg: "GitHub rate limit; retrying at 2026-09-10T17:34:00Z", wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wait, msg, ok := RateLimitWait(tc.err, now)
			if wait != tc.wantWait || msg != tc.wantMsg || ok != tc.wantOK {
				t.Fatalf("RateLimitWait = %s, %q, %v; want %s, %q, %v", wait, msg, ok, tc.wantWait, tc.wantMsg, tc.wantOK)
			}
		})
	}
}

// The condition message must not change while the controller waits: each
// status write that changes it re-enqueues the object immediately.
func TestRateLimitWaitMessageIsStableAcrossAttempts(t *testing.T) {
	now := time.Date(2026, 9, 10, 17, 35, 0, 0, time.UTC)
	err := &backend.RateLimitError{RetryAt: now.Add(6 * time.Minute)}
	_, first, _ := RateLimitWait(err, now)
	_, later, _ := RateLimitWait(err, now.Add(97*time.Second))
	if first != later {
		t.Fatalf("message changed between attempts: %q then %q", first, later)
	}
}
