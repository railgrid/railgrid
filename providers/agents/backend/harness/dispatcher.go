// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package harness

import (
	"context"

	"github.com/railgrid/railgrid/pkg/runner"
	runnerclient "github.com/railgrid/railgrid/pkg/runner/client"
)

// Dispatcher is the runner surface one conversational turn needs.
//
// It exists as an interface for one reason: *client.Client reaches a real
// machine through kcp and an edges Service proxy, and a test that had to stand
// that up would test the plumbing instead of the mapping. Everything here is a
// method of *client.Client with the same name and signature, so the interface is
// a seam rather than an abstraction — Wrap is the only implementation that
// speaks to a runner.
type Dispatcher interface {
	Start(ctx context.Context, req runner.StartRequest) (runner.Receipt, error)
	Resume(ctx context.Context, req runner.ResumeRequest) (runner.Receipt, error)
	Cancel(ctx context.Context, req runner.CancelRequest) (runner.Receipt, error)
	Inspect(ctx context.Context, attemptID string) (runner.Receipt, error)
	Events(ctx context.Context, attemptID string, after uint64) (Stream, error)
}

// Stream is one live read of an attempt's events. Next ends in io.EOF when the
// runner closes a quiet stream, which is ordinary: the caller reconnects from
// its last cursor.
type Stream interface {
	Next(ctx context.Context) (runner.Event, error)
	Close() error
}

// Wrap adapts the shared runner client to Dispatcher. The only adaptation is
// Events, whose concrete *client.EventStream already satisfies Stream.
func Wrap(c *runnerclient.Client) Dispatcher { return clientDispatcher{c: c} }

type clientDispatcher struct{ c *runnerclient.Client }

func (d clientDispatcher) Start(ctx context.Context, req runner.StartRequest) (runner.Receipt, error) {
	return d.c.Start(ctx, req)
}

func (d clientDispatcher) Resume(ctx context.Context, req runner.ResumeRequest) (runner.Receipt, error) {
	return d.c.Resume(ctx, req)
}

func (d clientDispatcher) Cancel(ctx context.Context, req runner.CancelRequest) (runner.Receipt, error) {
	return d.c.Cancel(ctx, req)
}

func (d clientDispatcher) Inspect(ctx context.Context, attemptID string) (runner.Receipt, error) {
	return d.c.Inspect(ctx, attemptID)
}

func (d clientDispatcher) Events(ctx context.Context, attemptID string, after uint64) (Stream, error) {
	return d.c.Events(ctx, attemptID, after)
}
