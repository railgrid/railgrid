/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package backend

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRateLimitErrorMessage(t *testing.T) {
	retryAt := time.Now().Add(28*time.Second + 400*time.Millisecond)
	cause := errors.New("HTTP 403")
	err := &RateLimitError{RetryAt: retryAt, Err: cause}
	want := "github: rate limited, resets in 28s (at " + retryAt.UTC().Format(time.RFC3339) + "): HTTP 403"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Fatal("cause not unwrapped")
	}
	past := &RateLimitError{RetryAt: time.Now().Add(-time.Minute)}
	if got := past.Error(); !strings.HasPrefix(got, "github: rate limited, resets in 0s (at ") || strings.HasSuffix(got, ": ") {
		t.Fatalf("expired Error() = %q", got)
	}
}
