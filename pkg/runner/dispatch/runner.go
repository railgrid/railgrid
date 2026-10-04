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

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/client"
)

// Runner is the runner surface the lifecycle needs.
//
// It is an interface for one reason: *client.Client reaches a real machine
// through kcp and an edges Service proxy, and a test that had to stand that up
// would be testing the plumbing instead of the rules. Every method is a method
// of *client.Client with the same name and signature, so this is a seam rather
// than an abstraction — Wrap is the only implementation that speaks to a runner.
type Runner interface {
	Start(ctx context.Context, req runner.StartRequest) (runner.Receipt, error)
	Resume(ctx context.Context, req runner.ResumeRequest) (runner.Receipt, error)
	Cancel(ctx context.Context, req runner.CancelRequest) (runner.Receipt, error)
	Inspect(ctx context.Context, attemptID string) (runner.Receipt, error)
	Events(ctx context.Context, attemptID string, after uint64) (Stream, error)
}

// Stream is one live read of an attempt's events. Next ends in io.EOF when the
// runner closes a quiet stream, which is ordinary: the runner does that after a
// silent interval so a caller resumes from its cursor instead of holding one
// connection open for the length of a coding turn.
type Stream interface {
	Next(ctx context.Context) (runner.Event, error)
	Close() error
}

// Wrap adapts the shared runner client to Runner. The only adaptation is
// Events, whose concrete *client.EventStream already satisfies Stream.
func Wrap(c *client.Client) Runner { return wrapped{c: c} }

type wrapped struct{ c *client.Client }

func (w wrapped) Start(ctx context.Context, req runner.StartRequest) (runner.Receipt, error) {
	return w.c.Start(ctx, req)
}

func (w wrapped) Resume(ctx context.Context, req runner.ResumeRequest) (runner.Receipt, error) {
	return w.c.Resume(ctx, req)
}

func (w wrapped) Cancel(ctx context.Context, req runner.CancelRequest) (runner.Receipt, error) {
	return w.c.Cancel(ctx, req)
}

func (w wrapped) Inspect(ctx context.Context, attemptID string) (runner.Receipt, error) {
	return w.c.Inspect(ctx, attemptID)
}

func (w wrapped) Events(ctx context.Context, attemptID string, after uint64) (Stream, error) {
	return w.c.Events(ctx, attemptID, after)
}
