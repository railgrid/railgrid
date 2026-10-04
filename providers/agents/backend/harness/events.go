// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package harness

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"

	"github.com/railgrid/provider-agents/backend"
)

// turnState accumulates what one attempt's event stream said, so the terminal
// receipt only has to supply the phase.
//
// Three things are accumulated, and they are three because the seam asks for
// three: everything the turn produced (Outcome.Text), the presentation answer
// (Outcome.Final), and what it cost (Outcome.Usage).
type turnState struct {
	sink backend.EventSink

	text  strings.Builder
	final string
	cost  backend.Cost

	// tools tracks tool-call ids already announced, so a payload seen twice
	// (started then completed) produces one ToolStart and one ToolEnd rather
	// than two of either.
	tools map[string]toolInFlight
	// Codex reports a whole-turn duration and completed command items report
	// their own durations. The assistant segment gets only the remainder so
	// adding its time and the tool times does not count the same work twice.
	toolDuration time.Duration
	turnDuration time.Duration
	turnStarted  bool
	// Codex tokenUsage.total is cumulative for its native thread. Track the
	// latest total snapshot so an identical update does not add tokenUsage.last
	// twice; distinct cumulative snapshots identify distinct model calls.
	codexTotalTokens int64
	codexTotalSeen   bool
	codexLast        string
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

func newTurnState(sink backend.EventSink) *turnState {
	return &turnState{sink: sink, tools: map[string]toolInFlight{}}
}

// observe maps one protocol event onto the sink and the accumulators, and
// reports whether it was terminal.
//
// The mapping is limited by what the wire carries, deliberately and not as a
// simplification. The runner records an adapter event as
// (type, message, data) and collapses every type it does not itself define onto
// `progress` (pkg/runner/runner.go handleHarnessEvent), so Claude Code's
// `message` and `tool_use` events arrive in the SAME shape: a progress event
// with a string. There is no field left that would tell prose from a tool name,
// so progress becomes a Delta unless its DATA names a tool item — which is what
// Codex sends, and which is why the tool mapping exists at all rather than being
// left out.
func (s *turnState) observe(event runner.Event) (terminal bool) {
	s.cursor = event.Cursor
	switch event.Type {
	case runner.EventAccepted, runner.EventStarted, runner.EventArtifact:
		// Lifecycle and artifact bookkeeping. A conversational turn exports no
		// artifacts (a workspace attempt cannot export a Git result), so an
		// artifact event is somebody else's attempt shape and is not progress to
		// show.
		return false
	case runner.EventProgress:
		s.progress(event)
		return false
	case runner.EventCheckpoint:
		// The state the provider persists is OURS — the attempt and session this
		// turn can be re-joined at — not whatever the harness put in the event.
		// The caller supplies it, so the recorder is wired in follow().
		return false
	case runner.EventNeedsInput:
		s.observeClarification(event)
		return true
	case runner.EventCompleted, runner.EventFailed, runner.EventCancelled:
		if event.Type == runner.EventCompleted {
			// A completed event carries the runner's own blocker field, which is
			// empty on success; the answer arrived in the harness's result
			// record. Cost, when the harness reported any, may be on either.
			s.observeCodexTurn(event.Data)
			s.observeUsage(event.Data)
		}
		return true
	default:
		s.progress(event)
		return false
	}
}

func (s *turnState) progress(event runner.Event) {
	s.observeCodexTurn(event.Data)
	// Some harnesses emit usage as a separate progress update rather than on
	// the final result record (Codex reports tokenUsage this way).
	s.observeUsage(event.Data)
	// A harness result record: the final answer plus, for Claude Code, the cost
	// fields. Recognised by its own payload rather than by the event type, which
	// the runner already flattened.
	if final, ok := resultRecord(event.Data); ok {
		if text := strings.TrimSpace(final); text != "" {
			s.final = text
			return
		}
		if text := strings.TrimSpace(event.Message); text != "" {
			s.final = text
		}
		return
	}
	// A turn-completion payload (Codex) carries a status word, not prose. Its
	// message is the phase name, which is not something to stream at a person.
	if turnCompletion(event.Data) {
		return
	}
	// The runner records tool approval decisions as ordinary progress messages.
	// They describe control flow, not assistant-authored transcript content.
	if permissionLifecycleMessage(event.Message) {
		return
	}
	// Codex's app-server exposes the final assistant message as a completed
	// item after streaming its text through item/agentMessage/delta. Keep that
	// canonical message for Outcome.Final, but do not stream or append it again:
	// the deltas already make up the transcript text.
	if text, ok := assistantMessageItem(event.Data); ok {
		if text != "" {
			s.final = text
		}
		return
	}
	if item, ok := toolItem(event.Data); ok {
		s.tool(item)
		return
	}
	// A Codex thread/start result is lifecycle data. Its human-readable message
	// is useful to a runner log, but it is not assistant content.
	if codexSessionStarted(event.Data) || strings.TrimSpace(event.Message) == "Codex session ready" {
		return
	}
	// Codex sends the same text in Message and in Data.delta. The JSON-RPC
	// payload identifies this as an app-server text delta, so concatenate it
	// byte-for-byte instead of applying the line-oriented behavior used for
	// generic harness progress.
	if delta, ok := codexMessageDelta(event.Data); ok {
		if event.Message != delta {
			return
		}
		if delta != "" {
			s.text.WriteString(delta)
			s.sink.Delta(delta)
		}
		return
	}
	if text := event.Message; strings.TrimSpace(text) != "" {
		s.text.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			s.text.WriteString("\n")
		}
		s.sink.Delta(text)
	}
}

func permissionLifecycleMessage(message string) bool {
	switch strings.ToLower(strings.TrimSpace(message)) {
	case "permission granted", "permission denied":
		return true
	default:
		return false
	}
}

// codexMessageDelta recognizes the data shape Codex's app-server emits for
// item/agentMessage/delta. The type field was flattened to runner progress by
// the adapter boundary, so this payload is how the normalizer distinguishes a
// text fragment from ordinary progress prose.
func codexMessageDelta(data json.RawMessage) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	var rec struct {
		Delta *string `json:"delta"`
	}
	if json.Unmarshal(data, &rec) != nil || rec.Delta == nil {
		return "", false
	}
	return *rec.Delta, true
}

// assistantMessageItem recognizes Codex's canonical completed assistant item.
// It is retained as the answer boundary without being replayed as a second
// delta. Codex uses phase=final_answer on this record; status is accepted for
// app-server versions that report an explicit completed state instead.
func assistantMessageItem(data json.RawMessage) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	var rec struct {
		Item *struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			Phase  string `json:"phase"`
			Status string `json:"status"`
		} `json:"item"`
	}
	if json.Unmarshal(data, &rec) != nil || rec.Item == nil {
		return "", false
	}
	if rec.Item.Type != "agentMessage" && rec.Item.Type != "assistant_message" && rec.Item.Type != "assistantMessage" {
		return "", false
	}
	phase := strings.ToLower(strings.TrimSpace(rec.Item.Phase))
	status := strings.ToLower(strings.TrimSpace(rec.Item.Status))
	if phase != "final_answer" && phase != "finalanswer" && status != "completed" && status != "complete" {
		return "", false
	}
	return rec.Item.Text, true
}

// codexSessionStarted recognizes the thread/start result in the Codex
// app-server protocol. runner preserves its payload while normalizing the
// adapter's lifecycle event to progress.
func codexSessionStarted(data json.RawMessage) bool {
	if len(data) == 0 {
		return false
	}
	var rec struct {
		Thread json.RawMessage `json:"thread"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return false
	}
	return len(rec.Thread) > 0 && strings.TrimSpace(string(rec.Thread)) != "null"
}

func (s *turnState) observeClarification(event runner.Event) {
	if c, ok := clarificationOf(event.Data); ok {
		s.clarification = c
		return
	}
	if msg := strings.TrimSpace(event.Message); msg != "" && s.clarification == nil {
		s.clarification = &runner.Clarification{Text: msg}
	}
}

// tool announces a tool item, once as a start and once as an end.
func (s *turnState) tool(item harnessItem) {
	seen, known := s.tools[item.ID]
	if !known {
		s.tools[item.ID] = toolInFlight{name: item.Name, args: item.Args}
		s.sink.ToolStart(item.ID, item.Name, item.Args)
		seen = s.tools[item.ID]
	}
	if !item.Done || seen.ended {
		return
	}
	seen.ended = true
	s.tools[item.ID] = seen
	s.toolDuration += item.Duration
	s.sink.ToolEnd(backend.ToolEvent{
		ID: item.ID, Name: seen.name, Args: seen.args,
		Result: item.Result, Err: item.Failed, Duration: item.Duration,
	})
}

// endOpenTools closes any tool the stream never reported finishing, so a
// transcript does not keep a call open forever because the attempt ended between
// an item's start and its completion.
func (s *turnState) endOpenTools() {
	for id, t := range s.tools {
		if t.ended {
			continue
		}
		t.ended = true
		s.tools[id] = t
		s.sink.ToolEnd(backend.ToolEvent{ID: id, Name: t.name, Args: t.args,
			Result: "the attempt ended before this tool call reported a result", Err: true})
	}
}

// ---- payload readers ---------------------------------------------------------
//
// Every reader below is tolerant by construction: it decodes the subset it
// understands and reports "not mine" for anything else. Harness stream formats
// gain record types between releases, and a turn that failed because a payload
// grew a field would be worse than a turn whose progress was a little coarser.

// resultRecord recognises a harness result record and returns its final text.
// Claude Code's `result` record is the shape; `subtype` and `is_error` are read
// only to avoid mistaking some other record that happens to carry a "result"
// key.
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
		Turn *turnEnvelope `json:"turn"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return false
	}
	return rec.Turn != nil
}

// observeCodexTurn tracks the app-server turn boundary and its measured whole
// turn duration. Tool durations are subtracted when the assistant boundary is
// reported so the provider's worked-time accumulator does not double-count
// command execution.
func (s *turnState) observeCodexTurn(data json.RawMessage) {
	if len(data) == 0 {
		return
	}
	var rec struct {
		Turn *turnEnvelope `json:"turn"`
	}
	if json.Unmarshal(data, &rec) != nil || rec.Turn == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(rec.Turn.Status)) {
	case "inprogress", "in_progress", "running", "started", "completed":
		s.turnStarted = true
	}
	if strings.EqualFold(strings.TrimSpace(rec.Turn.Status), "completed") {
		if duration := durationFromMillis(rec.Turn.DurationMS); duration > 0 {
			s.turnDuration = duration
		}
	}
}

// harnessItem is one tool call as a harness reported it.
type harnessItem struct {
	ID       string
	Name     string
	Args     string
	Result   string
	Duration time.Duration
	Done     bool
	Failed   bool
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
			ID               string `json:"id"`
			Type             string `json:"type"`
			Name             string `json:"name"`
			Tool             string `json:"tool"`
			Command          string `json:"command"`
			Status           string `json:"status"`
			Output           string `json:"output"`
			Result           string `json:"result"`
			AggregatedOutput string `json:"aggregatedOutput"`
			ExitCode         *int64 `json:"exitCode"`
			DurationMS       *int64 `json:"durationMs"`
		} `json:"item"`
	}
	if json.Unmarshal(data, &rec) != nil || rec.Item == nil {
		return harnessItem{}, false
	}
	if !toolItemTypes[rec.Item.Type] {
		return harnessItem{}, false
	}
	item := harnessItem{
		ID:       strings.TrimSpace(rec.Item.ID),
		Name:     firstNonEmpty(rec.Item.Name, rec.Item.Tool, rec.Item.Type),
		Args:     strings.TrimSpace(rec.Item.Command),
		Result:   firstNonBlank(rec.Item.AggregatedOutput, rec.Item.Output, rec.Item.Result),
		Duration: durationFromMillis(rec.Item.DurationMS),
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
	if rec.Item.ExitCode != nil && *rec.Item.ExitCode != 0 {
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
// Codex reports tokens and no price. Where there is no cost, the cost stays zero
// — a number invented from a token count and a guessed rate would be wrong in a
// billing column, and the agent is still bounded by its turn and duration limits
// (see the budget check in api/run.go, which counts tokens as well as dollars).
func (s *turnState) observeUsage(data json.RawMessage) {
	if len(data) == 0 {
		return
	}
	var rec struct {
		TotalCostUSD *float64      `json:"total_cost_usd"`
		CostUSD      *float64      `json:"cost_usd"`
		Usage        *tokenCounts  `json:"usage"`
		Turn         *turnEnvelope `json:"turn"`
		TokenUsage   *tokenUsage   `json:"tokenUsage"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return
	}
	if rec.TokenUsage != nil {
		s.observeCodexUsage(rec.TokenUsage)
		return
	}
	counts := rec.Usage
	if counts == nil && rec.Turn != nil {
		counts = rec.Turn.Usage
	}
	if counts != nil {
		s.cost.InputTokens += counts.input()
		s.cost.OutputTokens += counts.output()
	}
	cost := rec.TotalCostUSD
	if cost == nil {
		cost = rec.CostUSD
	}
	if cost == nil && rec.Turn != nil {
		cost = rec.Turn.TotalCostUSD
	}
	if cost != nil && *cost > 0 && !math.IsInf(*cost, 0) && !math.IsNaN(*cost) {
		s.cost.CostMicros += int64(math.Round(*cost * 1e6))
	}
}

// observeCodexUsage adds each distinct model-call update once. tokenUsage.last
// is for one model call; total is cumulative across a native thread and is used
// only to detect duplicate updates and identify a new call.
func (s *turnState) observeCodexUsage(usage *tokenUsage) {
	if !s.turnStarted || usage == nil || usage.Last == nil {
		return
	}
	if total, ok := usage.Total.cumulativeTokens(); ok {
		if s.codexTotalSeen && total <= s.codexTotalTokens {
			return
		}
		s.codexTotalTokens, s.codexTotalSeen = total, true
	} else {
		// Older app-server versions may omit total. De-duplicate identical
		// last-call reports rather than billing a repeated notification twice.
		raw, err := json.Marshal(usage.Last)
		if err != nil || string(raw) == s.codexLast {
			return
		}
		s.codexLast = string(raw)
	}
	s.cost.InputTokens += usage.Last.input()
	s.cost.OutputTokens += usage.Last.output()
}

type turnEnvelope struct {
	Status       string       `json:"status"`
	DurationMS   *int64       `json:"durationMs"`
	Usage        *tokenCounts `json:"usage"`
	TotalCostUSD *float64     `json:"total_cost_usd"`
}

// tokenUsage is the Codex app-server's last-model-call report plus the
// cumulative native-thread total. The normalizer bills each last-call update
// once and uses total only to recognize a repeated update.
type tokenUsage struct {
	Last  *tokenCounts `json:"last"`
	Total *tokenCounts `json:"total"`
}

// tokenCounts reads both spellings. Claude's snake_case cache fields are
// additive to input_tokens; Codex's camelCase cached/reasoning fields are
// subsets of inputTokens/outputTokens and are intentionally not added again.
type tokenCounts struct {
	TotalTokens       *int64 `json:"totalTokens"`
	InputTokens       *int64 `json:"input_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	InputTokensCamel  *int64 `json:"inputTokens"`
	OutputTokensCamel *int64 `json:"outputTokens"`
	// Cache reads and writes are input tokens the harness was charged for, so
	// they are counted as input rather than dropped.
	CacheCreation *int64 `json:"cache_creation_input_tokens"`
	CacheRead     *int64 `json:"cache_read_input_tokens"`
}

func (t *tokenCounts) input() int64 {
	return deref(t.InputTokens) + deref(t.InputTokensCamel) +
		deref(t.CacheCreation) + deref(t.CacheRead)
}

func (t *tokenCounts) output() int64 {
	return deref(t.OutputTokens) + deref(t.OutputTokensCamel)
}

func (t *tokenCounts) cumulativeTokens() (int64, bool) {
	if t == nil {
		return 0, false
	}
	if t.TotalTokens != nil && *t.TotalTokens >= 0 {
		return *t.TotalTokens, true
	}
	if t.InputTokens != nil || t.OutputTokens != nil || t.InputTokensCamel != nil || t.OutputTokensCamel != nil {
		return t.input() + t.output(), true
	}
	return 0, false
}

func deref(v *int64) int64 {
	if v == nil || *v < 0 {
		return 0
	}
	return *v
}

func durationFromMillis(value *int64) time.Duration {
	if value == nil || *value <= 0 {
		return 0
	}
	const maxDurationMillis = int64(1<<63-1) / int64(time.Millisecond)
	if *value > maxDurationMillis {
		return time.Duration(1<<63 - 1)
	}
	return time.Duration(*value) * time.Millisecond
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// firstNonBlank returns the original content so command output keeps its
// leading and trailing whitespace, including a final newline.
func firstNonBlank(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
