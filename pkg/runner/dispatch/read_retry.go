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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"k8s.io/klog/v2"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/client"
)

// Only read operations use this policy. An unreachable Edge says nothing
// about whether an accepted attempt is still running. Keep following it until
// recovery, cancellation, or the outage limit, without dispatching duplicate work.
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
		case http.StatusInternalServerError:
			if resourceResolutionTimeout(httpErr.Body) {
				klog.V(1).InfoS("Recognized retryable kcp resource-resolution timeout", "statusCode", httpErr.StatusCode)
				return true
			}
			return false
		case http.StatusRequestTimeout,
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

const maxReadOutage = 5 * time.Minute

// readOutage is shared across opening and draining a stream. Opening a
// connection alone is not evidence that a broken event feed has recovered.
type readOutage struct {
	started time.Time
	delay   time.Duration
	last    error
}

// contextError preserves outage diagnostics regardless of whether cancellation
// is observed in the timer, at the next loop boundary, or by a stream read.
func (o *readOutage) contextError(err error) error {
	if o.last != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return fmt.Errorf("%w (last runner read failure: %v)", err, o.last)
	}
	return err
}

func (o *readOutage) wait(ctx context.Context, obs Observer, last error) error {
	// A context error from a later read must not replace the transport failure.
	if o.last == nil || (!errors.Is(last, context.Canceled) && !errors.Is(last, context.DeadlineExceeded)) {
		o.last = last
	}
	if o.started.IsZero() {
		o.started = time.Now()
	}
	retryCtx, cancel := context.WithDeadline(ctx, o.started.Add(maxReadOutage))
	defer cancel()
	err := waitReadRetry(retryCtx, obs, &o.delay)
	if ctx.Err() != nil {
		return o.contextError(ctx.Err())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("runner read outage exceeded %s: %w", maxReadOutage, o.last)
	}
	return o.contextError(err)
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
		interval := min(remaining, time.Second)
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
	return retryReadDuring(ctx, obs, &readOutage{}, read)
}

func retryReadDuring[T any](ctx context.Context, obs Observer, outage *readOutage, read func() (T, error)) (T, error) {
	var zero T
	for {
		if err := ctx.Err(); err != nil {
			return zero, outage.contextError(err)
		}
		if obs != nil {
			if err := obs.Aborted(ctx); err != nil {
				return zero, outage.contextError(err)
			}
		}
		result, err := read()
		if err == nil || !retryableRead(err) {
			return result, outage.contextError(err)
		}
		if err := outage.wait(ctx, obs, err); err != nil {
			return zero, err
		}
	}
}

func inspectWithRetry(ctx context.Context, r Runner, attemptID string, obs Observer) (runner.Receipt, error) {
	return retryRead(ctx, obs, func() (runner.Receipt, error) { return r.Inspect(ctx, attemptID) })
}

// kcp currently wraps resource-resolution timeouts in InternalError, losing
// their HTTP timeout status. Match this specific Kubernetes Status envelope,
// not arbitrary 500s or bodies that merely mention a timeout. Remove this
// compatibility case when kcp preserves the original timeout classification.
// Captured during Factory smoke run RAI-13 (railgrid/railgrid#888):
// https://github.com/railgrid/railgrid/pull/888
// If kcp rewords the response, this match stops applying; the captured wording
// is pinned by TestKCPResolutionTimeoutRecovery.
func resourceResolutionTimeout(body []byte) bool {
	var status struct {
		Kind       string `json:"kind"`
		APIVersion string `json:"apiVersion"`
		Status     string `json:"status"`
		Reason     string `json:"reason"`
		Code       int    `json:"code"`
		Message    string `json:"message"`
		Details    struct {
			Causes []struct {
				Message string `json:"message"`
			} `json:"causes"`
		} `json:"details"`
	}
	const cause = "error resolving resource: the server was unable to return a response in the time allotted, but may still be processing the request"
	if json.Unmarshal(body, &status) != nil {
		return false
	}
	return status.Kind == "Status" && status.APIVersion == "v1" &&
		status.Status == "Failure" && status.Reason == "InternalError" &&
		status.Code == http.StatusInternalServerError &&
		status.Message == "Internal error occurred: "+cause &&
		len(status.Details.Causes) == 1 && status.Details.Causes[0].Message == cause
}
