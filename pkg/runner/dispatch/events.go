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
	"encoding/json"
	"math"
	"strings"

	"github.com/railgrid/railgrid/pkg/runner"
)

// stream accumulates what one attempt's event stream said, so the terminal
// receipt only has to supply the phase.
//
// Three things are accumulated because a caller asks for three: everything the
// attempt produced (Summary.Text), the presentation answer (Summary.Final), and
// what it cost (Summary.Usage).
type stream struct {
	obs Observer

	text  strings.Builder
	final string
	usage Usage

	// tools tracks tool-call ids already announced, so a payload seen twice
	// (started then completed) produces one ToolStart and one ToolEnd rather
	// than two of either.
	tools map[string]toolInFlight
	// cursor is the last event cursor consumed, which is where a reconnect
	// resumes and what a checkpoint records.
	cursor uint64
	// clarification is what the harness is asking, when it asked.
	clarification *runner.Clarification
}

type toolInFlight struct {
	name  string
	args  string
	ended bool
}

func newStream(obs Observer, cursor uint64) *stream {
	if obs == nil {
		obs = Noop{}
	}
	return &stream{obs: obs, tools: map[string]toolInFlight{}, cursor: cursor}
}

// observe maps one protocol event onto the observer and the accumulators, and
// reports whether it was terminal.
//
// The mapping is limited by what the wire carries, deliberately and not as a
// simplification. The runner records an adapter event as (type, message, data)
// and collapses every type it does not itself define onto `progress`
// (pkg/runner/runner.go handleHarnessEvent), so Claude Code's `message` and
// `tool_use` events arrive in the SAME shape: a progress event with a string.
// There is no field left that would tell prose from a tool name, so progress
// becomes Text unless its DATA names a tool item — which is what Codex sends,
// and which is why the tool mapping exists at all rather than being left out.
func (s *stream) observe(event runner.Event) (terminal bool) {
	s.cursor = event.Cursor
	switch event.Type {
	case runner.EventAccepted, runner.EventStarted, runner.EventArtifact:
		// Lifecycle and artifact bookkeeping, not progress to show. Artifacts
		// are named on the terminal receipt, which is where a caller that
		// exports them reads them.
		return false
	case runner.EventProgress:
		s.progress(event)
		return false
	case runner.EventCheckpoint:
		// The position a caller persists is its own — the attempt and cursor
		// this follow can be re-joined at — not whatever the harness put in the
		// event. Follow offers it through Observer.Checkpoint.
		return false
	case runner.EventNeedsInput:
		s.observeClarification(event)
		return true
	case runner.EventCompleted, runner.EventFailed, runner.EventCancelled:
		if event.Type == runner.EventCompleted {
			// A completed event carries the runner's own blocker field, which is
			// empty on success; the answer arrived in the harness's result
			// record. Cost, when the harness reported any, may be on either.
			s.observeUsage(event.Data)
		}
		return true
	default:
		s.progress(event)
		return false
	}
}

func (s *stream) progress(event runner.Event) {
	// A harness result record: the final answer plus, for Claude Code, the cost
	// fields. Recognised by its own payload rather than by the event type,
	// which the runner already flattened.
	if final, ok := resultRecord(event.Data); ok {
		s.observeUsage(event.Data)
		if text := strings.TrimSpace(final); text != "" {
			s.final = text
			return
		}
		if text := strings.TrimSpace(event.Message); text != "" {
			s.final = text
		}
		return
	}
	// A turn-completion payload (Codex) carries usage and a status word, not
	// prose. Its message is the phase name, which is not something to stream
	// at a person.
	if turnCompletion(event.Data) {
		s.observeUsage(event.Data)
		return
	}
	if item, ok := toolItem(event.Data); ok {
		s.tool(item)
		return
	}
	if text := event.Message; strings.TrimSpace(text) != "" {
		s.text.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			s.text.WriteString("\n")
		}
		s.obs.Text(text)
	}
}

func (s *stream) observeClarification(event runner.Event) {
	if c, ok := clarificationOf(event.Data); ok {
		s.clarification = c
		return
	}
	if msg := strings.TrimSpace(event.Message); msg != "" && s.clarification == nil {
		s.clarification = &runner.Clarification{Text: msg}
	}
}

// tool announces a tool item, once as a start and once as an end.
func (s *stream) tool(item harnessItem) {
	seen, known := s.tools[item.ID]
	if !known {
		s.tools[item.ID] = toolInFlight{name: item.Name, args: item.Args}
		s.obs.ToolStart(item.ID, item.Name, item.Args)
		seen = s.tools[item.ID]
	}
	if !item.Done || seen.ended {
		return
	}
	seen.ended = true
	s.tools[item.ID] = seen
	s.obs.ToolEnd(ToolResult{
		ID: item.ID, Name: seen.name, Args: seen.args,
		Result: item.Result, Failed: item.Failed,
	})
}

// endOpenTools closes any tool the stream never reported finishing, so a
// transcript does not keep a call open forever because the attempt ended
// between an item's start and its completion.
func (s *stream) endOpenTools() {
	for id, t := range s.tools {
		if t.ended {
			continue
		}
		t.ended = true
		s.tools[id] = t
		s.obs.ToolEnd(ToolResult{ID: id, Name: t.name, Args: t.args,
			Result: "the attempt ended before this tool call reported a result", Failed: true})
	}
}

// ---- payload readers ---------------------------------------------------------
//
// Every reader below is tolerant by construction: it decodes the subset it
// understands and reports "not mine" for anything else. Harness stream formats
// gain record types between releases, and a turn that failed because a payload
// grew a field would be worse than a turn whose progress was a little coarser.

// resultRecord recognises a harness result record and returns its final text.
// Claude Code's `result` record is the shape; `type` is read only to avoid
// mistaking some other record that happens to carry a "result" key.
func resultRecord(data json.RawMessage) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	var rec struct {
		Type       string          `json:"type"`
		ResultText json.RawMessage `json:"result"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return "", false
	}
	if rec.Type != "result" {
		return "", false
	}
	var text string
	if len(rec.ResultText) > 0 {
		// `result` is a string on a normal turn and an object on some error
		// subtypes; only the string form is an answer.
		_ = json.Unmarshal(rec.ResultText, &text)
	}
	return text, true
}

// turnCompletion recognises a Codex turn/completed payload.
func turnCompletion(data json.RawMessage) bool {
	if len(data) == 0 {
		return false
	}
	var rec struct {
		Turn *struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return false
	}
	return rec.Turn != nil
}

// harnessItem is one tool call as a harness reported it.
type harnessItem struct {
	ID     string
	Name   string
	Args   string
	Result string
	Done   bool
	Failed bool
}

// toolItemTypes are the item types that ARE a tool call. Anything else an item
// payload can be (an assistant message, a reasoning block) is not one, and
// announcing it as a tool would put prose in a transcript's tool column.
var toolItemTypes = map[string]bool{ //nolint:gochecknoglobals // immutable type table
	"command_execution": true,
	"commandExecution":  true,
	"file_change":       true,
	"fileChange":        true,
	"mcp_tool_call":     true,
	"mcpToolCall":       true,
	"tool_call":         true,
	"toolCall":          true,
	"web_search":        true,
	"webSearch":         true,
}

// toolItem recognises an item payload that describes a tool call.
func toolItem(data json.RawMessage) (harnessItem, bool) {
	if len(data) == 0 {
		return harnessItem{}, false
	}
	var rec struct {
		Item *struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Tool    string `json:"tool"`
			Command string `json:"command"`
			Status  string `json:"status"`
			Output  string `json:"output"`
			Result  string `json:"result"`
		} `json:"item"`
	}
	if json.Unmarshal(data, &rec) != nil || rec.Item == nil {
		return harnessItem{}, false
	}
	if !toolItemTypes[rec.Item.Type] {
		return harnessItem{}, false
	}
	item := harnessItem{
		ID:     strings.TrimSpace(rec.Item.ID),
		Name:   firstNonEmpty(rec.Item.Name, rec.Item.Tool, rec.Item.Type),
		Args:   strings.TrimSpace(rec.Item.Command),
		Result: firstNonEmpty(rec.Item.Output, rec.Item.Result),
	}
	if item.ID == "" {
		// Without an id there is nothing to correlate a start with an end, so
		// the item is reported as one completed call rather than as a call that
		// never finishes.
		item.ID = item.Name
		item.Done = true
	}
	switch strings.ToLower(strings.TrimSpace(rec.Item.Status)) {
	case "completed", "success", "succeeded", "done":
		item.Done = true
	case "failed", "error", "aborted":
		item.Done, item.Failed = true, true
	}
	return item, true
}

// clarificationOf reads a clarification out of a needs_input payload.
func clarificationOf(data json.RawMessage) (*runner.Clarification, bool) {
	if len(data) == 0 {
		return nil, false
	}
	var rec struct {
		Clarification *runner.Clarification `json:"clarification"`
		Question      string                `json:"question"`
		Prompt        string                `json:"prompt"`
		ID            string                `json:"id"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return nil, false
	}
	if rec.Clarification != nil && strings.TrimSpace(rec.Clarification.Text) != "" {
		return rec.Clarification, true
	}
	if text := firstNonEmpty(rec.Question, rec.Prompt); text != "" {
		return &runner.Clarification{ID: rec.ID, Text: text}, true
	}
	return nil, false
}

// observeUsage adds whatever a payload reported spending.
//
// Claude Code's result record carries both tokens and a price it was charged;
// Codex reports tokens and no price. Where there is no cost the cost stays
// zero — see Usage.
func (s *stream) observeUsage(data json.RawMessage) {
	if len(data) == 0 {
		return
	}
	var rec struct {
		TotalCostUSD *float64      `json:"total_cost_usd"`
		CostUSD      *float64      `json:"cost_usd"`
		Usage        *tokenCounts  `json:"usage"`
		Turn         *turnEnvelope `json:"turn"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return
	}
	counts := rec.Usage
	if counts == nil && rec.Turn != nil {
		counts = rec.Turn.Usage
	}
	if counts != nil {
		s.usage.InputTokens += counts.input()
		s.usage.OutputTokens += counts.output()
	}
	cost := rec.TotalCostUSD
	if cost == nil {
		cost = rec.CostUSD
	}
	if cost == nil && rec.Turn != nil {
		cost = rec.Turn.TotalCostUSD
	}
	if cost != nil && *cost > 0 && !math.IsInf(*cost, 0) && !math.IsNaN(*cost) {
		s.usage.CostMicros += int64(math.Round(*cost * 1e6))
	}
}

type turnEnvelope struct {
	Usage        *tokenCounts `json:"usage"`
	TotalCostUSD *float64     `json:"total_cost_usd"`
}

// tokenCounts reads both spellings, because the two harnesses disagree: Claude
// Code's stream is snake_case and Codex's JSON-RPC is camelCase.
type tokenCounts struct {
	InputTokens       *int64 `json:"input_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	InputTokensCamel  *int64 `json:"inputTokens"`
	OutputTokensCamel *int64 `json:"outputTokens"`
	// Cache reads and writes are input tokens the harness was charged for, so
	// they are counted as input rather than dropped.
	CacheCreation      *int64 `json:"cache_creation_input_tokens"`
	CacheRead          *int64 `json:"cache_read_input_tokens"`
	CachedInputCamel   *int64 `json:"cachedInputTokens"`
	ReasoningOutputCam *int64 `json:"reasoningOutputTokens"`
}

func (t *tokenCounts) input() int64 {
	return deref(t.InputTokens) + deref(t.InputTokensCamel) +
		deref(t.CacheCreation) + deref(t.CacheRead) + deref(t.CachedInputCamel)
}

func (t *tokenCounts) output() int64 {
	return deref(t.OutputTokens) + deref(t.OutputTokensCamel) + deref(t.ReasoningOutputCam)
}

func deref(v *int64) int64 {
	if v == nil || *v < 0 {
		return 0
	}
	return *v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
