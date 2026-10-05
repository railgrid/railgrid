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
	"errors"
	"fmt"

	"github.com/railgrid/railgrid/pkg/runner"
)

// Describe wraps a runner failure with what was being attempted, keeping the
// original for errors.As: callers branch on *runner.Error's Code and on
// *client.HTTPError's status, never on a message.
func Describe(what string, err error) error {
	var protocol *runner.Error
	if errors.As(err, &protocol) {
		return fmt.Errorf("%s: %s (%s): %w", what, protocol.Message, protocol.Code, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// Refusal extracts the protocol error a runner answered with, if it did.
//
// The shared client already decides this: a failed response whose body carries
// a protocol Code comes back as *runner.Error, and everything else — a
// timeout, a proxy 502, a kcp Status, an empty body — is an HTTPError or a
// transport error. So the distinction every dispatch fence rests on — "refused,
// nothing ran" against "outcome unknown" — is a type assertion here and not a
// hopeful unmarshal of whatever bytes came back.
func Refusal(err error) (*runner.Error, bool) {
	var refusal *runner.Error
	if !errors.As(err, &refusal) || refusal.Code == "" {
		return nil, false
	}
	return refusal, true
}

// IsAttemptUnknown reports the ONE answer that permits a first start: the
// runner, reached and authenticated, says it has never heard of this attempt.
//
// It is a protocol answer rather than a transport outcome, and the difference
// is the whole fence. A timeout, a 503 or an empty body may be hiding work the
// runner accepted and is doing; only this says there is none.
func IsAttemptUnknown(err error) bool {
	refusal, ok := Refusal(err)
	if !ok {
		return false
	}
	return refusal.Code == runner.ErrorUnavailable && !refusal.Retryable && refusal.Receipt == nil
}

// IsSnapshotRequired reports a cursor the runner no longer holds. The caller's
// view is incomplete and only Inspect can restore it; Follow does that itself,
// and this exists for a caller driving the primitives by hand.
func IsSnapshotRequired(err error) bool {
	refusal, ok := Refusal(err)
	if !ok {
		return false
	}
	return refusal.SnapshotRequired || refusal.Code == runner.ErrorCursorExpired
}

// IsNothingToCancel reports a cancel that had nothing to stop: an attempt the
// runner never heard of, or one whose epoch is already obsolete because it
// finished. Neither is a failure to cancel — there is nothing running.
func IsNothingToCancel(err error) bool {
	refusal, ok := Refusal(err)
	if !ok {
		return false
	}
	switch refusal.Code {
	case runner.ErrorStaleAttempt:
		return refusal.Receipt != nil && refusal.Receipt.Phase.IsTerminal()
	case runner.ErrorUnavailable:
		// A non-retryable refusal with no receipt is the runner's authenticated
		// "attempt not found" answer. Retryable unavailable responses include
		// failures to persist a cancellation fence; treating those as success
		// could let a delayed Start run after the coordinator closes its record.
		return !refusal.Retryable && refusal.Receipt == nil
	default:
		return false
	}
}

// IsUnstartable reports a refusal that the same request can never get past:
// the runner does not offer what the request requires. Retrying the same
// bytes is pointless; the caller's remedy is to choose differently.
func IsUnstartable(err error) bool {
	refusal, ok := Refusal(err)
	if !ok {
		return false
	}
	switch refusal.Code {
	case runner.ErrorUnsupportedCapability, runner.ErrorUnsupportedVersion:
		return true
	default:
		return false
	}
}

// Ended renders a terminal receipt as the error it stands for: nil for a
// completed attempt, and for a failed or cancelled one an error carrying what
// the runner said. Callers that treat "the harness stopped" and "the harness
// broke" differently check the receipt's Phase; this is for the message.
func Ended(receipt runner.Receipt) error {
	switch receipt.Phase {
	case runner.PhaseCancelled:
		if receipt.Blocker != "" {
			return fmt.Errorf("the harness turn was stopped: %s", receipt.Blocker)
		}
		return errors.New("the harness turn was stopped")
	case runner.PhaseFailed:
		if receipt.LastError != nil && receipt.LastError.Message != "" {
			return fmt.Errorf("the harness turn failed: %s (%s)", receipt.LastError.Message, receipt.LastError.Code)
		}
		if receipt.Blocker != "" {
			return fmt.Errorf("the harness turn failed: %s", receipt.Blocker)
		}
		return errors.New("the harness turn failed")
	default:
		return nil
	}
}
