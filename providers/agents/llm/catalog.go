// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package llm

import "strings"

// ModelInfo is a curated catalog entry: pricing (USD per 1M tokens) and
// capabilities for a model id. Used to (a) compute per-run cost from token
// counts and (b) drive the portal's Models catalog (capability chips, pricing,
// context window).
//
// Pricing is a best-effort 2026 snapshot and drifts — treat the numbers as
// estimates, and prefer a live pricing source before quoting them externally.
// Cost math: 1 USD per 1M tokens == 1 micro-USD per token, so
//
//	usd_micros = round(inTokens*InputPer1M + outTokens*OutputPer1M)
type ModelInfo struct {
	ID            string  `json:"id"`
	Family        string  `json:"family"`
	Label         string  `json:"label,omitempty"`
	ContextWindow int     `json:"contextWindow,omitempty"` // tokens
	InputPer1M    float64 `json:"inputPer1M"`              // USD / 1M input tokens
	OutputPer1M   float64 `json:"outputPer1M"`             // USD / 1M output tokens
	Vision        bool    `json:"vision,omitempty"`
	ToolCall      bool    `json:"toolCall,omitempty"`
	Reasoning     bool    `json:"reasoning,omitempty"`
}

// catalog is the curated model list. Keyed lookups normalize the id (lowercase,
// strip any provider/org prefix like "openai/" or "anthropic/") so an
// OpenAI-compatible gateway id such as "openrouter/anthropic/claude-sonnet-4"
// still resolves.
var catalog = []ModelInfo{
	// OpenAI
	{ID: "gpt-4o", Family: "openai", Label: "GPT-4o", ContextWindow: 128000, InputPer1M: 2.50, OutputPer1M: 10.00, Vision: true, ToolCall: true},
	{ID: "gpt-4o-mini", Family: "openai", Label: "GPT-4o mini", ContextWindow: 128000, InputPer1M: 0.15, OutputPer1M: 0.60, Vision: true, ToolCall: true},
	{ID: "gpt-4.1", Family: "openai", Label: "GPT-4.1", ContextWindow: 1000000, InputPer1M: 2.00, OutputPer1M: 8.00, Vision: true, ToolCall: true},
	{ID: "gpt-4.1-mini", Family: "openai", Label: "GPT-4.1 mini", ContextWindow: 1000000, InputPer1M: 0.40, OutputPer1M: 1.60, Vision: true, ToolCall: true},
	{ID: "gpt-4.1-nano", Family: "openai", Label: "GPT-4.1 nano", ContextWindow: 1000000, InputPer1M: 0.10, OutputPer1M: 0.40, Vision: true, ToolCall: true},
	{ID: "gpt-5", Family: "openai", Label: "GPT-5", ContextWindow: 400000, InputPer1M: 1.25, OutputPer1M: 10.00, Vision: true, ToolCall: true, Reasoning: true},
	{ID: "gpt-5-mini", Family: "openai", Label: "GPT-5 mini", ContextWindow: 400000, InputPer1M: 0.25, OutputPer1M: 2.00, Vision: true, ToolCall: true, Reasoning: true},
	{ID: "o1", Family: "openai", Label: "o1", ContextWindow: 200000, InputPer1M: 15.00, OutputPer1M: 60.00, ToolCall: true, Reasoning: true},
	{ID: "o3", Family: "openai", Label: "o3", ContextWindow: 200000, InputPer1M: 2.00, OutputPer1M: 8.00, ToolCall: true, Reasoning: true},
	{ID: "o3-mini", Family: "openai", Label: "o3-mini", ContextWindow: 200000, InputPer1M: 1.10, OutputPer1M: 4.40, ToolCall: true, Reasoning: true},
	{ID: "o4-mini", Family: "openai", Label: "o4-mini", ContextWindow: 200000, InputPer1M: 1.10, OutputPer1M: 4.40, ToolCall: true, Reasoning: true},
	// Anthropic (via OpenAI-compatible endpoints / OpenRouter)
	{ID: "claude-sonnet-4", Family: "anthropic", Label: "Claude Sonnet 4", ContextWindow: 200000, InputPer1M: 3.00, OutputPer1M: 15.00, Vision: true, ToolCall: true},
	{ID: "claude-opus-4", Family: "anthropic", Label: "Claude Opus 4", ContextWindow: 200000, InputPer1M: 15.00, OutputPer1M: 75.00, Vision: true, ToolCall: true},
	{ID: "claude-3-5-haiku", Family: "anthropic", Label: "Claude 3.5 Haiku", ContextWindow: 200000, InputPer1M: 0.80, OutputPer1M: 4.00, Vision: true, ToolCall: true},
	// Google (via OpenAI-compatible endpoints)
	{ID: "gemini-2.5-pro", Family: "google", Label: "Gemini 2.5 Pro", ContextWindow: 1000000, InputPer1M: 1.25, OutputPer1M: 10.00, Vision: true, ToolCall: true, Reasoning: true},
	{ID: "gemini-2.5-flash", Family: "google", Label: "Gemini 2.5 Flash", ContextWindow: 1000000, InputPer1M: 0.30, OutputPer1M: 2.50, Vision: true, ToolCall: true},
}

var catalogByID = func() map[string]ModelInfo {
	m := make(map[string]ModelInfo, len(catalog))
	for _, mi := range catalog {
		m[mi.ID] = mi
	}
	return m
}()

// Catalog returns the curated model list (copy-safe; callers must not mutate).
func Catalog() []ModelInfo { return catalog }

// normalizeModelID lowercases and strips a leading provider/org prefix, then
// trims common date/version suffixes so "openai/gpt-4o-2024-08-06" and
// "GPT-4o" both resolve to "gpt-4o".
func normalizeModelID(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	return m
}

// LookupModel returns the catalog entry for a model id, matching first on the
// exact normalized id and then on the longest catalog id that is a prefix of it
// (so "gpt-4o-2024-08-06" matches "gpt-4o"). Reports ok=false when unknown.
func LookupModel(model string) (ModelInfo, bool) {
	norm := normalizeModelID(model)
	if norm == "" {
		return ModelInfo{}, false
	}
	if mi, ok := catalogByID[norm]; ok {
		return mi, true
	}
	// Longest-prefix match: pick the most specific catalog id the model starts
	// with (e.g. "gpt-4o-mini-…" must prefer "gpt-4o-mini" over "gpt-4o").
	best := ""
	for id := range catalogByID {
		if strings.HasPrefix(norm, id) && len(id) > len(best) {
			best = id
		}
	}
	if best != "" {
		return catalogByID[best], true
	}
	return ModelInfo{}, false
}

// CostMicros returns the estimated cost in micro-USD for a run of the given
// model with in/out token counts. Unknown models cost 0 (no pricing data →
// don't fabricate a number). 1 USD/1M-tokens == 1 micro-USD/token, so the
// per-1M price is also the micro-USD-per-token rate.
func CostMicros(model string, inTokens, outTokens int64) int64 {
	mi, ok := LookupModel(model)
	if !ok {
		return 0
	}
	cost := float64(inTokens)*mi.InputPer1M + float64(outTokens)*mi.OutputPer1M
	if cost < 0 {
		return 0
	}
	return int64(cost + 0.5) // round to nearest micro-USD
}
