/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package dispatch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/client"
)

// Only read operations use this policy. An unreachable Edge says nothing
// about whether an accepted attempt is still running. Keep following it until
// the caller's deadline/cancellation, rather than dispatching duplicate work.
func retryableRead(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if refusal, ok := Refusal(err); ok {
		return refusal.Code == runner.ErrorUnavailable && refusal.Retryable && !IsSnapshotRequired(err)
	}
	var httpErr *client.HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH)
}

// Back off read failures, while polling the caller's durable cancellation flag
// even at the maximum delay. Context cancellation interrupts the wait immediately.
func waitReadRetry(ctx context.Context, obs Observer, delay *time.Duration) error {
	if *delay == 0 {
		*delay = quietPoll
	} else {
		*delay = min(2**delay, 5*time.Second)
	}
	remaining := *delay
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if obs != nil {
			if err := obs.Aborted(ctx); err != nil {
				return err
			}
		}
		interval := min(remaining, quietPoll)
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		remaining -= interval
	}
	return nil
}

func retryRead[T any](ctx context.Context, obs Observer, read func() (T, error)) (T, error) {
	var zero T
	var delay time.Duration
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if obs != nil {
			if err := obs.Aborted(ctx); err != nil {
				return zero, err
			}
		}
		result, err := read()
		if err == nil || !retryableRead(err) {
			return result, err
		}
		if err := waitReadRetry(ctx, obs, &delay); err != nil {
			return zero, err
		}
	}
}

func inspectWithRetry(ctx context.Context, r Runner, attemptID string, obs Observer) (runner.Receipt, error) {
	return retryRead(ctx, obs, func() (runner.Receipt, error) { return r.Inspect(ctx, attemptID) })
}

func eventsWithRetry(ctx context.Context, r Runner, attemptID string, cursor uint64, obs Observer) (Stream, error) {
	return retryRead(ctx, obs, func() (Stream, error) { return r.Events(ctx, attemptID, cursor) })
}
