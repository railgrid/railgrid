// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package engine runs the agent chat loop on Eino. This milestone implements a
// streaming single-turn completion (system prompt + history + user message);
// the tool-call loop, checkpoints, and sub-agent delegation build on this in
// later milestones. The package is provider-agnostic and SDK-portable.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Role constants for engine messages (aligned with Eino's schema roles).
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is a role-tagged turn in the conversation the engine runs over.
type Message struct {
	Role    string
	Content string
}

// Usage reports token consumption for a completed turn, when the provider
// returns it.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

// Result is the outcome of a completed streaming turn.
type Result struct {
	Content string
	Usage   Usage
}

// Engine runs turns against a chat model. It holds no per-request state, so a
// single Engine is safe for concurrent use.
type Engine struct{}

// New returns an Engine.
func New() *Engine { return &Engine{} }

// StreamTurn runs one assistant turn and streams content deltas to onDelta as
// they arrive. It returns the full assistant message and usage once complete.
// model is built per-request from the caller's resolved profile (see the llm
// package) so each tenant uses its own credentials.
func (e *Engine) StreamTurn(ctx context.Context, model einomodel.BaseChatModel, msgs []Message, onDelta func(string)) (Result, error) {
	if model == nil {
		return Result{}, errors.New("engine: nil chat model")
	}
	in := toEino(msgs)
	if len(in) == 0 {
		return Result{}, errors.New("engine: no messages to send")
	}

	stream, err := model.Stream(ctx, in)
	if err != nil {
		return Result{}, fmt.Errorf("engine: start stream: %w", err)
	}
	defer stream.Close()

	var content strings.Builder
	var usage Usage
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, fmt.Errorf("engine: stream recv: %w", err)
		}
		if chunk == nil {
			continue
		}
		if chunk.Content != "" {
			content.WriteString(chunk.Content)
			if onDelta != nil {
				onDelta(chunk.Content)
			}
		}
		if u := chunk.ResponseMeta; u != nil && u.Usage != nil {
			usage.InputTokens = int64(u.Usage.PromptTokens)
			usage.OutputTokens = int64(u.Usage.CompletionTokens)
		}
	}
	return Result{Content: content.String(), Usage: usage}, nil
}

func toEino(msgs []Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleSystem:
			out = append(out, schema.SystemMessage(m.Content))
		case RoleUser:
			out = append(out, schema.UserMessage(m.Content))
		case RoleAssistant:
			out = append(out, schema.AssistantMessage(m.Content, nil))
		default:
			// Unknown roles are treated as user content so nothing is dropped
			// silently; tool messages get first-class handling in the tool
			// milestone.
			out = append(out, schema.UserMessage(m.Content))
		}
	}
	return out
}
