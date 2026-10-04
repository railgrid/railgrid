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
	"time"
)

// Observer receives what an attempt's stream says, as it says it.
//
// Every method is optional in the sense that Noop satisfies all of them: a
// caller that only wants the Outcome passes Noop and gets exactly that. A
// caller with a transcript to keep, or a cancel flag of its own to honour,
// implements the methods it needs and embeds Noop for the rest.
//
// Nothing here is a verdict. The stream is reported, not interpreted: whether
// a tool call should have been allowed, or whether a question is one the
// caller's users may answer, is the caller's business after Follow returns.
type Observer interface {
	// Text receives prose the harness produced, in order. A caller showing a
	// person the turn streams it; one that only records it concatenates it.
	// Summary.Text is the same prose, accumulated, so a caller need not.
	Text(delta string)
	// ToolStart announces a tool call the harness began. ID correlates it
	// with its ToolEnd. A call the harness reported in one piece produces
	// both at once.
	ToolStart(id, name, args string)
	// ToolEnd reports a tool call finishing. An attempt that ended with a call
	// still open produces a ToolEnd for it with Failed set and Result saying
	// so, so a transcript never holds a call open forever.
	ToolEnd(ToolResult)
	// Checkpoint offers a point the attempt can be re-joined at — nothing is
	// half-consumed when it fires. A caller that survives its own restart
	// persists it; one that does not ignores it.
	Checkpoint(Snapshot)
	// Aborted is consulted at every point the follow could stop cleanly: before
	// each reconnect and between quiet streams. A non-nil error stops the
	// follow with that error and Outcome.Receipt as last read. This is how a
	// caller's own durable cancel flag — one that outlives the context it
	// handed in — reaches the loop.
	Aborted(ctx context.Context) error
}

// ToolResult is one finished tool call.
type ToolResult struct {
	ID     string
	Name   string
	Args   string
	Result string
	Failed bool
	// Duration is the elapsed time reported by the harness, when available.
	Duration time.Duration
}

// AssistantResult is the completed assistant response and its active model
// time. It is an optional observer extension for consumers that bill or render
// model timing separately from tool timing.
type AssistantResult struct {
	Content  string
	Complete bool
	Duration time.Duration
}

// AssistantObserver is an optional Observer extension. It is called only when
// the receipt confirms successful completion; incomplete assistant output is
// already available through Text.
type AssistantObserver interface {
	Assistant(AssistantResult)
}

// Noop satisfies Observer and does nothing. Embed it to implement a subset.
type Noop struct{}

func (Noop) Text(string)                      {}
func (Noop) ToolStart(string, string, string) {}
func (Noop) ToolEnd(ToolResult)               {}
func (Noop) Checkpoint(Snapshot)              {}
func (Noop) Aborted(context.Context) error    { return nil }
func (Noop) Assistant(AssistantResult)        {}

var _ Observer = Noop{}
