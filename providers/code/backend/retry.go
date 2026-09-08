/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package backend

import (
	"fmt"
	"time"
)

// RateLimitError reports when a host will accept another request. Controllers
// use RetryAt to schedule reconciliation without occupying a worker while
// waiting. Ordinary backend failures remain ordinary errors for workqueue retry.
// The backend must still enforce the deadline across callers sharing a budget.
type RateLimitError struct {
	RetryAt time.Time
	Err     error
}

func (e *RateLimitError) Error() string {
	message := fmt.Sprintf("host requests paused until %s", e.RetryAt.UTC().Format(time.RFC3339))
	if e.Err != nil {
		return message + ": " + e.Err.Error()
	}
	return message
}
func (e *RateLimitError) Unwrap() error { return e.Err }
