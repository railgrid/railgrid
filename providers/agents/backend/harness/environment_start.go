// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0

package harness

import (
	"context"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/dispatch"
)

// environmentStartWindow is how long a warm environment's refusals are retried
// in place; an idle environment's approved cleanup can take up to five minutes.
var environmentStartWindow = 5 * time.Minute //nolint:gochecknoglobals // test seam

// startWithEnvironmentRetry retries only authenticated refusals proving that
// no attempt was accepted, with the same request identity throughout;
// transport failures and receipts remain the existing recovery path's
// responsibility.
//
// The window bounds the retries, not any one Start: a request the runner may
// be accepting is never cut off by it. When the window is spent the last
// refusal is returned as it is, so the run-level retry still recognises it and
// requeues an unattended run instead of failing it with a timeout. Only the
// caller's own context ends the wait with a context error.
func (b *Backend) startWithEnvironmentRetry(ctx context.Context, req runner.StartRequest) (runner.Receipt, error) {
	if req.EnvironmentID == "" {
		return b.cfg.Runner.Start(ctx, req)
	}
	deadline := time.Now().Add(environmentStartWindow)
	delay := 250 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return runner.Receipt{}, err
		}
		receipt, err := b.cfg.Runner.Start(ctx, req)
		refusal, refused := dispatch.Refusal(err)
		if err == nil || !refused || !refusal.Retryable || refusal.Receipt != nil || receipt.AttemptID != "" || receipt.TaskID != "" || receipt.Phase != "" || (refusal.Code != runner.ErrorUnavailable && refusal.Code != runner.ErrorBusy) {
			return receipt, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return receipt, err
		}
		timer := time.NewTimer(min(delay, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return runner.Receipt{}, ctx.Err()
		case <-timer.C:
		}
		delay = min(2*delay, 2*time.Second)
	}
}
