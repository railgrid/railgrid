// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"errors"
	"fmt"

	"github.com/cloudwego/eino/schema"
)

// InterruptError is returned by a tool executor to pause the run instead of
// producing an observation — the durable human-in-the-loop gate. The engine
// stops the loop, and the caller persists the returned Checkpoint so the run
// can resume in place once the user decides.
type InterruptError struct {
	// Tool and Args identify the gated call exactly as the model requested it.
	Tool string
	Args string
	// RequestID references the approval request (inbox item) awaiting the user.
	RequestID string
}

func (e *InterruptError) Error() string {
	return fmt.Sprintf("tool %q requires user approval (request %s)", e.Tool, e.RequestID)
}

// Interrupt is the engine's paused state: why it stopped plus the Checkpoint
// needed to resume.
type Interrupt struct {
	Tool       string     `json:"tool"`
	Args       string     `json:"args"`
	RequestID  string     `json:"requestID"`
	Checkpoint Checkpoint `json:"checkpoint"`
}

// Checkpoint is the serializable mid-run state of a tool loop: the full wire
// conversation, the tool calls not yet executed (the first one is the gated
// call), streamed content so far, accumulated usage, and the loop iteration.
type Checkpoint struct {
	Messages []CheckpointMessage `json:"messages"`
	Pending  []PendingCall       `json:"pending"`
	Content  string              `json:"content,omitempty"`
	Usage    Usage               `json:"usage"`
	Iter     int                 `json:"iter"`
}

// PendingCall is one tool call the model requested that has not executed yet.
type PendingCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

// CheckpointMessage is a serializable subset of the wire message: enough to
// rebuild the conversation for the model. Multimodal parts (tool-returned
// images) are degraded to a text note — they are transient vision input, not
// conversation state worth persisting.
type CheckpointMessage struct {
	Role       string               `json:"role"`
	Content    string               `json:"content,omitempty"`
	ToolCalls  []CheckpointToolCall `json:"toolCalls,omitempty"`
	ToolCallID string               `json:"toolCallID,omitempty"`
	ToolName   string               `json:"toolName,omitempty"`
}

// CheckpointToolCall mirrors an assistant message's tool call.
type CheckpointToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

// asInterrupt unwraps an *InterruptError from a tool execution error.
func asInterrupt(err error) *InterruptError {
	var ie *InterruptError
	if errors.As(err, &ie) {
		return ie
	}
	return nil
}

// checkpointMessages converts live wire messages into their serializable form.
func checkpointMessages(in []*schema.Message) []CheckpointMessage {
	out := make([]CheckpointMessage, 0, len(in))
	for _, m := range in {
		cm := CheckpointMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			ToolName:   m.ToolName,
		}
		if cm.Content == "" && (len(m.UserInputMultiContent) > 0 || len(m.MultiContent) > 0) {
			cm.Content = "[multimodal content from tool calls omitted on resume]"
		}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, CheckpointToolCall{
				ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments,
			})
		}
		out = append(out, cm)
	}
	return out
}

// restoreMessages rebuilds wire messages from a checkpoint.
func restoreMessages(in []CheckpointMessage) []*schema.Message {
	out := make([]*schema.Message, 0, len(in))
	for _, cm := range in {
		m := &schema.Message{
			Role:       schema.RoleType(cm.Role),
			Content:    cm.Content,
			ToolCallID: cm.ToolCallID,
			ToolName:   cm.ToolName,
		}
		for _, tc := range cm.ToolCalls {
			m.ToolCalls = append(m.ToolCalls, schema.ToolCall{
				ID: tc.ID, Type: "function",
				Function: schema.FunctionCall{Name: tc.Name, Arguments: tc.Args},
			})
		}
		out = append(out, m)
	}
	return out
}

func pendingFromToolCalls(tcs []schema.ToolCall) []PendingCall {
	out := make([]PendingCall, 0, len(tcs))
	for _, tc := range tcs {
		out = append(out, PendingCall{ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments})
	}
	return out
}
