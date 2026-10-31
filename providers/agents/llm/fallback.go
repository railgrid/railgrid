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

package llm

import (
	"context"
	"errors"
	"fmt"
	"io"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// FallbackModel wraps an ordered chain of chat models and tries them in turn:
// the first model that responds wins, and a model that fails (provider outage,
// rate limit, timeout, connection error) is skipped so the next one is tried.
//
// Streaming falls back only up to the first token. If a model errors before it
// emits any content — including an HTTP error that a provider surfaces on the
// first Recv rather than from Stream itself — the chain moves on. Once a model
// has streamed content and then fails mid-response, the error is surfaced to
// the caller: we cannot replay a half-finished answer through a different model.
//
// The only error that is never retried is caller cancellation (ctx.Err): if the
// caller went away, trying another provider is pointless.
type FallbackModel struct {
	members []einomodel.BaseChatModel
	// names is parallel to members and only used to make errors legible.
	names []string
}

// NewFallbackModel builds a chain from the given members. A single member is
// returned unwrapped so the common (no-fallback) case carries no overhead and
// preserves the member's own capabilities (e.g. tool binding).
func NewFallbackModel(members []einomodel.BaseChatModel, names []string) einomodel.BaseChatModel {
	if len(members) == 1 {
		return members[0]
	}
	return &FallbackModel{members: members, names: names}
}

var _ einomodel.ToolCallingChatModel = (*FallbackModel)(nil)

// Generate tries each member in order and returns the first success.
func (f *FallbackModel) Generate(ctx context.Context, in []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	indexes, err := f.eligibleMembers(in)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, i := range indexes {
		m := f.members[i]
		msg, err := m.Generate(ctx, in, opts...)
		if err == nil {
			return msg, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		errs = append(errs, fmt.Errorf("%s: %w", f.names[i], err))
	}
	return nil, fmt.Errorf("all %d models failed: %w", len(f.members), errors.Join(errs...))
}

// Stream tries each member in order, peeking the first chunk so an error that
// surfaces on the first Recv still falls back. On success it returns a reader
// that re-emits the peeked chunk ahead of the rest of the stream.
func (f *FallbackModel) Stream(ctx context.Context, in []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	indexes, err := f.eligibleMembers(in)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, i := range indexes {
		m := f.members[i]
		sr, err := m.Stream(ctx, in, opts...)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			errs = append(errs, fmt.Errorf("%s: %w", f.names[i], err))
			continue
		}
		first, ferr := sr.Recv()
		if ferr != nil && !errors.Is(ferr, io.EOF) {
			sr.Close()
			if ctx.Err() != nil {
				return nil, ferr
			}
			errs = append(errs, fmt.Errorf("%s: %w", f.names[i], ferr))
			continue
		}
		// This member produced a first chunk (or a clean, empty EOF). Commit to
		// it and hand back a reader that replays the peeked chunk first.
		return prependChunk(first, ferr, sr), nil
	}
	return nil, fmt.Errorf("all %d models failed to start streaming: %w", len(f.members), errors.Join(errs...))
}

// WithTools binds tools to every member and returns a new chain. A member that
// does not support tool binding is kept as-is (tools are a no-op for it).
func (f *FallbackModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	bound := make([]einomodel.BaseChatModel, len(f.members))
	for i, m := range f.members {
		tcm, ok := m.(einomodel.ToolCallingChatModel)
		if !ok {
			bound[i] = m
			continue
		}
		b, err := tcm.WithTools(tools)
		if err != nil {
			return nil, fmt.Errorf("bind tools to %s: %w", f.names[i], err)
		}
		bound[i] = b
	}
	return &FallbackModel{members: bound, names: f.names}, nil
}

// HasResponsesOrigin reports whether any member can continue Responses state
// stamped with origin. This lets the run-resume boundary fail before executing
// an approved tool when none of the configured credentials can safely resume
// the active model continuation.
func (f *FallbackModel) HasResponsesOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	for _, member := range f.members {
		if provider, ok := member.(interface{ ResponsesOrigin() string }); ok && provider.ResponsesOrigin() == origin {
			return true
		}
		if provider, ok := member.(interface{ HasResponsesOrigin(string) bool }); ok && provider.HasResponsesOrigin(origin) {
			return true
		}
	}
	return false
}

func (f *FallbackModel) eligibleMembers(input []*schema.Message) ([]int, error) {
	origin, hasContinuation, err := responsesContinuationOrigin(input)
	if err != nil {
		return nil, err
	}
	indexes := make([]int, 0, len(f.members))
	for index, member := range f.members {
		if hasContinuation && !modelHasResponsesOrigin(member, origin) {
			continue
		}
		indexes = append(indexes, index)
	}
	if hasContinuation && len(indexes) == 0 {
		return nil, errors.New("no configured model credential matches the active Responses continuation")
	}
	return indexes, nil
}

func modelHasResponsesOrigin(model einomodel.BaseChatModel, origin string) bool {
	if provider, ok := model.(interface{ ResponsesOrigin() string }); ok {
		return provider.ResponsesOrigin() == origin
	}
	if provider, ok := model.(interface{ HasResponsesOrigin(string) bool }); ok {
		return provider.HasResponsesOrigin(origin)
	}
	return false
}

func responsesContinuationOrigin(input []*schema.Message) (string, bool, error) {
	var origin string
	hasContinuation := false
	for _, message := range input {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		calls := HydrateResponsesToolCalls(message)
		state, hasState := message.Extra[responsesToolStateKey]
		stateValue, stateIsString := state.(string)
		hasAggregatedState := hasState && (!stateIsString || stateValue != "")
		for _, call := range calls {
			originValue, originPresent := call.Extra[ResponsesToolOriginExtraKey]
			callOrigin, hasOrigin := originValue.(string)
			_, itemIDPresent := call.Extra[ResponsesToolItemIDExtraKey]
			_, reasoningPresent := call.Extra[ResponsesToolReasoningExtraKey]
			callContinuation := originPresent || itemIDPresent || reasoningPresent
			if hasAggregatedState && !hasOrigin {
				return "", true, errors.New("responses continuation state is missing its credential origin")
			}
			if !callContinuation {
				continue
			}
			if !hasOrigin || callOrigin == "" {
				return "", true, errors.New("responses continuation is missing its credential origin")
			}
			if origin != "" && origin != callOrigin {
				return "", true, errors.New("mixed model credentials cannot share a Responses continuation")
			}
			origin = callOrigin
			hasContinuation = true
		}
	}
	if !hasContinuation {
		return "", false, nil
	}
	return origin, true, nil
}

// prependChunk returns a stream that yields the already-received first chunk and
// then the remainder of rest. firstErr is the error Recv returned alongside
// first (io.EOF for an empty stream, nil otherwise).
func prependChunk(first *schema.Message, firstErr error, rest *schema.StreamReader[*schema.Message]) *schema.StreamReader[*schema.Message] {
	sr, sw := schema.Pipe[*schema.Message](2)
	go func() {
		defer rest.Close()
		defer sw.Close()
		if firstErr == nil {
			if sw.Send(first, nil) {
				return
			}
		}
		for {
			chunk, err := rest.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				sw.Send(nil, err)
				return
			}
			if sw.Send(chunk, nil) {
				return
			}
		}
	}()
	return sr
}
