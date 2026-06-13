/*
Copyright 2026 The Faros Authors.

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

package hub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
)

type startupRetryPolicy struct {
	Name      string
	Interval  time.Duration
	Timeout   time.Duration
	Retryable func(error) bool
}

func runStartupStepWithRetry(ctx context.Context, policy startupRetryPolicy, fn func(context.Context) error) error {
	if policy.Interval <= 0 {
		return fmt.Errorf("startup retry interval must be positive")
	}
	if policy.Timeout <= 0 {
		return fmt.Errorf("startup retry timeout must be positive")
	}
	if policy.Retryable == nil {
		return fmt.Errorf("startup retry policy for %q must define Retryable", policy.Name)
	}

	logger := klog.FromContext(ctx)
	stepCtx, cancel := context.WithTimeout(ctx, policy.Timeout)
	defer cancel()

	var lastErr error
	for attempt := 1; ; attempt++ {
		if err := fn(stepCtx); err != nil {
			if stepCtx.Err() != nil {
				if lastErr != nil {
					return fmt.Errorf("%s did not complete before context ended: %w", policy.Name, lastErr)
				}
				return fmt.Errorf("%s did not complete before context ended: %w", policy.Name, stepCtx.Err())
			}
			if !policy.Retryable(err) {
				return err
			}
			lastErr = err
			logger.Error(err, "Startup step failed; retrying", "step", policy.Name, "attempt", attempt, "retryIn", policy.Interval.String())
		} else {
			if attempt > 1 {
				logger.Info("Startup step succeeded after retry", "step", policy.Name, "attempts", attempt)
			}
			return nil
		}

		select {
		case <-stepCtx.Done():
			if lastErr != nil {
				return fmt.Errorf("%s did not complete after %s: %w", policy.Name, policy.Timeout, lastErr)
			}
			return fmt.Errorf("%s did not complete after %s: %w", policy.Name, policy.Timeout, stepCtx.Err())
		case <-time.After(policy.Interval):
		}
	}
}

func isRetriableKCPBootstrapError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if apierrors.IsTimeout(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsTooManyRequests(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsInternalError(err) {
		return true
	}

	lowerErr := strings.ToLower(err.Error())
	if apierrors.IsForbidden(err) && strings.Contains(lowerErr, "not yet ready") {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}

	return strings.Contains(lowerErr, "connection reset by peer") ||
		strings.Contains(lowerErr, "connection refused") ||
		strings.Contains(lowerErr, "tls handshake timeout") ||
		strings.Contains(lowerErr, "unexpected eof") ||
		strings.Contains(lowerErr, "server closed idle connection")
}
