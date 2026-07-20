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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
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

// Param describes one tool parameter (a pragmatic subset of JSON schema).
type Param struct {
	Type     string // "string" | "integer" | "number" | "boolean"
	Desc     string
	Required bool
	Enum     []string
}

// Tool is one callable function exposed to the model. Exactly one of Params or
// JSONSchema describes the arguments: Params for the built-in families,
// JSONSchema (a raw JSON-schema document) for pass-through tools like MCP.
type Tool struct {
	Name       string
	Desc       string
	Params     map[string]Param
	JSONSchema map[string]any
	// Exec runs the tool with the model-provided JSON arguments and returns
	// the text observation fed back to the model. An error is also fed back (as
	// an error observation) rather than aborting the run. Set this for text-only
	// tools; tools that can return images set ExecRich instead.
	Exec func(ctx context.Context, argsJSON string) (string, error)
	// ExecRich, when non-nil, is used in preference to Exec and may return
	// images (e.g. a camera snapshot) alongside text. The engine feeds the text
	// back as the tool observation and the images as a follow-up user message so
	// vision-capable models can actually see them.
	ExecRich func(ctx context.Context, argsJSON string) (Observation, error)
}

// ToolImage is binary image output from a tool (e.g. a UniFi Protect camera
// snapshot), carried back to the model as vision input. Data is the raw,
// un-encoded image bytes; the engine base64-encodes them for the model.
type ToolImage struct {
	MIMEType string // e.g. "image/jpeg"; defaults to image/jpeg when empty
	Data     []byte
}

// Observation is a rich tool result: a text observation plus any images.
type Observation struct {
	Text   string
	Images []ToolImage
}

// maxTurnImages caps how many tool-returned images are fed back to the model in
// a single turn, so a fan-out of snapshot calls can't blow the token budget.
const maxTurnImages = 8

// ToolEvent reports a tool invocation to the caller (for SSE/UI + audit).
type ToolEvent struct {
	Name     string
	Args     string // raw JSON arguments from the model
	Result   string // observation (or error text)
	Err      bool
	Duration time.Duration
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

// StreamTurn runs one assistant turn (no tools) and streams content deltas.
func (e *Engine) StreamTurn(ctx context.Context, model einomodel.BaseChatModel, msgs []Message, onDelta func(string)) (Result, error) {
	return e.StreamTurnWithTools(ctx, model, msgs, nil, 1, onDelta, nil)
}

// StreamTurnWithTools runs an assistant turn with a tool-call loop: the model
// may call tools, observations are fed back, and the loop continues until the
// model answers without tool calls or maxIters is reached. Content deltas
// stream to onDelta as they arrive (including pre-tool-call narration); each
// tool execution is reported to onTool for UI + audit.
func (e *Engine) StreamTurnWithTools(
	ctx context.Context,
	model einomodel.BaseChatModel,
	msgs []Message,
	tools []Tool,
	maxIters int,
	onDelta func(string),
	onTool func(ToolEvent),
) (Result, error) {
	if model == nil {
		return Result{}, errors.New("engine: nil chat model")
	}
	in := toEino(msgs)
	if len(in) == 0 {
		return Result{}, errors.New("engine: no messages to send")
	}
	if maxIters <= 0 {
		maxIters = 1
	}

	active := model
	byName := map[string]Tool{}
	if len(tools) > 0 {
		tcm, ok := model.(einomodel.ToolCallingChatModel)
		if !ok {
			return Result{}, errors.New("engine: model does not support tool calling")
		}
		infos, err := toToolInfos(tools)
		if err != nil {
			return Result{}, fmt.Errorf("engine: building tool schemas: %w", err)
		}
		bound, err := tcm.WithTools(infos)
		if err != nil {
			return Result{}, fmt.Errorf("engine: binding tools: %w", err)
		}
		active = bound
		for _, t := range tools {
			byName[t.Name] = t
		}
	}

	var content strings.Builder
	var usage Usage

	for iter := 0; iter < maxIters; iter++ {
		full, err := e.streamOnce(ctx, active, in, &content, &usage, onDelta)
		if err != nil {
			return Result{}, err
		}
		if len(full.ToolCalls) == 0 {
			return Result{Content: content.String(), Usage: usage}, nil
		}

		// Feed the assistant's tool-call message back, then execute each call
		// and append its observation. Images returned by tools are collected and
		// appended once, after all tool messages: the OpenAI wire format requires
		// each tool_call to be answered by a contiguous tool message, and image
		// content must ride on a user message (a tool-role message can't carry
		// it), so the images become a single follow-up user turn.
		in = append(in, full)
		var turnImages []ToolImage
		for _, tc := range full.ToolCalls {
			name := tc.Function.Name
			args := tc.Function.Arguments
			started := time.Now()
			var result string
			var failed bool
			if tool, ok := byName[name]; ok {
				switch {
				case tool.ExecRich != nil:
					obs, execErr := tool.ExecRich(ctx, args)
					if execErr != nil {
						result = "error: " + execErr.Error()
						failed = true
					} else {
						result = obs.Text
						turnImages = append(turnImages, obs.Images...)
						if result == "" && len(obs.Images) > 0 {
							result = fmt.Sprintf("[returned %d image(s); shown below]", len(obs.Images))
						}
					}
				case tool.Exec != nil:
					out, execErr := tool.Exec(ctx, args)
					if execErr != nil {
						result = "error: " + execErr.Error()
						failed = true
					} else {
						result = out
					}
				default:
					result = fmt.Sprintf("error: tool %q has no executor", name)
					failed = true
				}
			} else {
				result = fmt.Sprintf("error: unknown tool %q", name)
				failed = true
			}
			if onTool != nil {
				onTool(ToolEvent{Name: name, Args: args, Result: result, Err: failed, Duration: time.Since(started)})
			}
			in = append(in, schema.ToolMessage(result, tc.ID, schema.WithToolName(name)))
		}
		if msg := imageUserMessage(turnImages); msg != nil {
			in = append(in, msg)
		}
	}

	// Ran out of iterations mid-loop: surface what we have plus a marker so
	// the transcript is honest about the truncation.
	content.WriteString("\n\n[stopped: reached the tool-call limit for one turn]")
	return Result{Content: content.String(), Usage: usage}, nil
}

// streamOnce streams a single model response, forwarding content deltas and
// accumulating usage, and returns the concatenated full message (which may
// carry tool calls).
func (e *Engine) streamOnce(ctx context.Context, model einomodel.BaseChatModel, in []*schema.Message, content *strings.Builder, usage *Usage, onDelta func(string)) (*schema.Message, error) {
	stream, err := model.Stream(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("engine: start stream: %w", err)
	}
	defer stream.Close()

	var chunks []*schema.Message
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("engine: stream recv: %w", err)
		}
		if chunk == nil {
			continue
		}
		chunks = append(chunks, chunk)
		if chunk.Content != "" {
			content.WriteString(chunk.Content)
			if onDelta != nil {
				onDelta(chunk.Content)
			}
		}
		if u := chunk.ResponseMeta; u != nil && u.Usage != nil {
			// Providers report cumulative usage on the final chunk of each
			// response; add per response, not per chunk.
			usage.InputTokens += int64(u.Usage.PromptTokens)
			usage.OutputTokens += int64(u.Usage.CompletionTokens)
		}
	}
	if len(chunks) == 0 {
		return nil, errors.New("engine: model returned an empty stream")
	}
	full, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, fmt.Errorf("engine: concatenating stream: %w", err)
	}
	return full, nil
}

// toToolInfos converts engine tools to Eino tool schemas. Built-in families
// use the lightweight ParameterInfo map; pass-through tools (MCP) carry a raw
// JSON schema document.
func toToolInfos(tools []Tool) ([]*schema.ToolInfo, error) {
	out := make([]*schema.ToolInfo, 0, len(tools))
	for _, t := range tools {
		info := &schema.ToolInfo{Name: t.Name, Desc: t.Desc}
		switch {
		case t.JSONSchema != nil:
			raw, err := json.Marshal(t.JSONSchema)
			if err != nil {
				return nil, fmt.Errorf("tool %s: marshal schema: %w", t.Name, err)
			}
			js := &jsonschema.Schema{}
			if err := json.Unmarshal(raw, js); err != nil {
				return nil, fmt.Errorf("tool %s: parse schema: %w", t.Name, err)
			}
			info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(js)
		default:
			params := map[string]*schema.ParameterInfo{}
			for name, p := range t.Params {
				params[name] = &schema.ParameterInfo{
					Type:     einoDataType(p.Type),
					Desc:     p.Desc,
					Required: p.Required,
					Enum:     p.Enum,
				}
			}
			info.ParamsOneOf = schema.NewParamsOneOfByParams(params)
		}
		out = append(out, info)
	}
	return out, nil
}

func einoDataType(t string) schema.DataType {
	switch t {
	case "integer":
		return schema.Integer
	case "number":
		return schema.Number
	case "boolean":
		return schema.Boolean
	case "array":
		return schema.Array
	case "object":
		return schema.Object
	default:
		return schema.String
	}
}

// imageUserMessage builds a synthetic user turn carrying tool-returned images
// as vision input, or nil when there are none. Images beyond maxTurnImages are
// dropped with a note so the truncation is honest rather than silent.
func imageUserMessage(imgs []ToolImage) *schema.Message {
	if len(imgs) == 0 {
		return nil
	}
	note := "Images returned by the preceding tool call(s):"
	if len(imgs) > maxTurnImages {
		note += fmt.Sprintf(" (showing the first %d of %d)", maxTurnImages, len(imgs))
		imgs = imgs[:maxTurnImages]
	}
	parts := []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeText, Text: note}}
	for _, img := range imgs {
		if len(img.Data) == 0 {
			continue
		}
		b64 := base64.StdEncoding.EncodeToString(img.Data)
		mime := img.MIMEType
		if mime == "" {
			mime = "image/jpeg"
		}
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{Base64Data: &b64, MIMEType: mime},
				Detail:            schema.ImageURLDetailAuto,
			},
		})
	}
	if len(parts) == 1 { // text note only: every image was empty
		return nil
	}
	return &schema.Message{Role: schema.User, UserInputMultiContent: parts}
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
