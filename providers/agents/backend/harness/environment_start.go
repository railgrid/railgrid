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

// An idle environment's approved cleanup can take up to five minutes. Retry
// only authenticated refusals proving that no attempt was accepted. The same
// request identity is used throughout; transport failures and receipts remain
// the existing recovery path's responsibility.
func (b *Backend) startWithEnvironmentRetry(ctx context.Context, req runner.StartRequest) (runner.Receipt, error) {
	if req.EnvironmentID == "" {
		return b.cfg.Runner.Start(ctx, req)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	delay := 250 * time.Millisecond
	for {
		if err := waitCtx.Err(); err != nil {
			return runner.Receipt{}, err
		}
		receipt, err := b.cfg.Runner.Start(waitCtx, req)
		refusal, refused := dispatch.Refusal(err)
		if err == nil || !refused || !refusal.Retryable || refusal.Receipt != nil || receipt.AttemptID != "" || receipt.TaskID != "" || receipt.Phase != "" || (refusal.Code != runner.ErrorUnavailable && refusal.Code != runner.ErrorBusy) {
			return receipt, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return runner.Receipt{}, waitCtx.Err()
		case <-timer.C:
		}
		delay = min(2*delay, 2*time.Second)
	}
}
