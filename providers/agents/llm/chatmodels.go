// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package llm

import (
	"regexp"
	"sort"
	"strings"
)

// Agent-model discovery curation.
//
// On the official OpenAI endpoint, model routing selects Eino's Responses
// adapter for the GPT-6 family and for earlier catalog models when a selected
// reasoning effort requires it. Custom gateways default to Chat Completions
// unless a run already has a pinned protocol. This does not make every
// Responses-only model family supported. GET {baseURL}/models still lists
// modalities and specialized model families outside the agent interface, so
// discovery needs curation.
//
// So discovery is curated here, once, and DiscoverModels is the only caller —
// which is what keeps ModelCredential.status.models (written by the
// reconciler) and the `discover` verb's answer the same list rather than two.
//
// The rule is a DENY-list, not an allow-list, on purpose: the credential
// points at any OpenAI-compatible endpoint, and an allow-list would hide every
// model a gateway serves that this repo has never heard of. A deny-list only
// removes ids whose name says what they are.

// nonChatTokens are hyphen-delimited id tokens that mark a model as something
// other than a supported agent chat model. Matching is delimiter-aware (see
// hasIDToken), so "tts" matches "tts-1" and "gpt-4o-mini-tts" but never the
// middle of a longer word.
//
// What each one removes, with the OpenAI ids as the worked example:
//
//	tts, audio            tts-1, gpt-4o-mini-tts, gpt-4o-audio-preview
//	transcribe, whisper   gpt-4o-transcribe, whisper-1
//	embedding             text-embedding-3-small
//	dall-e, image         dall-e-3, gpt-image-1
//	realtime              gpt-realtime, gpt-4o-realtime-preview
//	moderation            omni-moderation-latest
//	babbage, davinci      babbage-002, davinci-002 (legacy completions)
//	instruct              gpt-3.5-turbo-instruct (completions, not chat)
//	codex                 gpt-5.3-codex, codex-mini-latest (/v1/responses only)
//	pro                   gpt-5-pro, o3-pro (/v1/responses only)
//	deep-research         o3-deep-research (/v1/responses only)
//	computer-use          computer-use-preview (/v1/responses only)
//	search-api            the search-api tool models, not a chat model
//	sora                  sora-2 (video generation)
//	chatgpt               chatgpt-4o-latest (the product snapshot, not an API model)
//
// Two near-misses are deliberately NOT here, because both ARE chat models on
// Chat Completions: "*-search-preview" (gpt-4o-search-preview) and
// "*-chat-latest" (gpt-5.3-chat-latest). "search-api" is listed as a whole
// two-token phrase so it cannot catch "search-preview", and "chat" is not a
// token at all.
//
// These examples are filtered even though Agents has a Responses adapter:
// routing is limited to GPT-6 models and earlier cataloged models whose
// selected reasoning effort uses Responses. Catalog entries override this
// deny-list for model ids Agents has explicitly included.
var nonChatTokens = []string{
	"audio",
	"babbage",
	"chatgpt",
	"codex",
	"computer-use",
	"dall-e",
	"davinci",
	"deep-research",
	"embedding",
	"image",
	"instruct",
	"moderation",
	"pro",
	"realtime",
	"search-api",
	"sora",
	"transcribe",
	"tts",
	"whisper",
}

// catalogRankByID maps a curated catalog id to its position in Catalog(), so
// FilterChatModels can order the models this repo actually knows about ahead of
// whatever else an endpoint happens to serve. The key is the NORMALIZED id, so
// a gateway's "models/gemini-2.5-pro" or "openai/gpt-4o" still lands on it.
var catalogRankByID = func() map[string]int {
	m := make(map[string]int, len(Catalog()))
	for i, mi := range Catalog() {
		m[normalizeModelID(mi.ID)] = i
	}
	return m
}()

// normalizeModelID lowercases a model id and strips any provider/org prefix, so
// "openai/GPT-4o", "models/gemini-2.5-pro" and "openrouter/anthropic/claude-sonnet-4"
// reduce to the bare id the deny-list and the catalog are written against.
func normalizeModelID(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
	return s
}

// hasIDToken reports whether tok appears in id as a whole hyphen-delimited
// token (or token sequence). Substring matching would be wrong in both
// directions: it would let "pro" match "gpt-4o-preview", and a token list of
// full ids would miss the suffixes these families actually use.
func hasIDToken(id, tok string) bool {
	for i := 0; i+len(tok) <= len(id); i++ {
		if id[i:i+len(tok)] != tok {
			continue
		}
		if i > 0 && id[i-1] != '-' {
			continue
		}
		if end := i + len(tok); end < len(id) && id[end] != '-' {
			continue
		}
		return true
	}
	return false
}

// ChatCapable reports whether a discovered model id is retained as an agent
// model candidate by the current discovery curation.
//
// A curated catalog id (llm.Catalog(), matched exactly after normalization) is
// always retained: the catalog is the hand-written list of models this repo
// runs agents on, so it overrules the deny-list. That is what keeps
// gemini-2.5-pro and gemini-3-pro — real chat models whose ids end in the same
// "-pro" that marks OpenAI's responses-only family — in the candidate list.
//
// Anything else is retained unless one of nonChatTokens names it. An unknown
// id from an unknown gateway is therefore offered, which is the right
// default: this curation is not a compatibility check, and the `test` verb is
// what actually proves a model answers.
func ChatCapable(id string) bool {
	norm := normalizeModelID(id)
	if norm == "" {
		return false
	}
	if _, ok := catalogRankByID[norm]; ok {
		return true
	}
	for _, tok := range nonChatTokens {
		if hasIDToken(norm, tok) {
			return false
		}
	}
	return true
}

// FilterChatModels returns the model-candidate subset retained by discovery,
// deduplicated and in a stable order: catalog-known ids first, in catalog
// order, then everything else alphabetically.
//
// Catalog-known first because that is the answer to "which of these 130 should
// I pick?" — the portal groups on the same split (Recommended / Other models
// this endpoint serves). Stable because this list is written to
// ModelCredential.status.models, and an order that moved between probes would
// make every reconcile a status write.
func FilterChatModels(ids []string) []string {
	type ranked struct {
		id   string
		rank int
	}
	var known []ranked
	var other []string
	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] || !ChatCapable(id) {
			continue
		}
		seen[id] = true
		// LookupModel, not the exact map: a dated snapshot ("gpt-4o-2024-08-06")
		// is the same family as its catalog entry and belongs beside it.
		if mi, ok := LookupModel(id); ok {
			known = append(known, ranked{id: id, rank: catalogRankByID[normalizeModelID(mi.ID)]})
			continue
		}
		other = append(other, id)
	}
	sort.SliceStable(known, func(i, j int) bool {
		if known[i].rank != known[j].rank {
			return known[i].rank < known[j].rank
		}
		return known[i].id < known[j].id
	})
	sort.Strings(other)
	out := make([]string, 0, len(known)+len(other))
	for _, k := range known {
		out = append(out, k.id)
	}
	return append(out, other...)
}

// ---- run-time recovery copy -------------------------------------------------

// modelUnavailableFragments are fragments of the upstream's own refusal when
// the selected model cannot serve the request. They are matched on the error
// text because that is all there is: the SDK surfaces the provider's 404/400
// body as an opaque error. These errors can come from either transport, and
// picking a different model is the available recovery.
//
//	"not supported in the v1/chat/completions endpoint"  — a responses-only
//	  model (gpt-5.3-codex, gpt-5-pro, o3-deep-research). OpenAI's full text
//	  adds "Use the v1/responses endpoint instead"; the current routing does
//	  not select Responses for these unsupported families.
//	modelLifecycleRefusal matches retirement/deprecation language when the
//	model itself is the subject, regardless of transport.
var modelUnavailableFragments = []string{
	"not supported in the v1/chat/completions endpoint",
	"not supported in the chat completions",
	"model_not_supported",
}

// modelLifecycleRefusal does not classify a deprecated parameter as an
// unavailable model; for example, "parameter reasoning is deprecated" remains
// an upstream diagnostic. A quoted or unquoted model ID is optional.
var modelLifecycleRefusal = regexp.MustCompile(`(?:^|[^a-z0-9_])(?:the\s+)?model(?:\s+(?:['"\x60])?[a-z0-9][a-z0-9._:/-]*(?:['"\x60])?)?\s+(?:is|has been)\s+(?:deprecated|retired)\b`)

// IsModelUnavailable reports whether err says the selected model cannot serve
// the request, such as an unsupported transport, a retired model, or a model
// the provider marks as unsupported. It recognizes errors from both Chat
// Completions and Responses requests.
func IsModelUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if modelLifecycleRefusal.MatchString(msg) {
		return true
	}
	for _, frag := range modelUnavailableFragments {
		if strings.Contains(msg, frag) {
			return true
		}
	}
	return false
}

// ExplainModelUnavailable wraps a run failure the upstream refused for model
// reasons with recovery copy naming the credential to fix. Any other error —
// and an empty credential name — is returned untouched, so genuine failures
// are never dressed up as a configuration problem.
//
// The upstream sentence is kept so the provider's protocol-specific details
// remain available to anyone diagnosing the failure.
func ExplainModelUnavailable(err error, credential string) error {
	credential = strings.TrimSpace(credential)
	if credential == "" || !IsModelUnavailable(err) {
		return err
	}
	return &ModelUnavailableError{Credential: credential, Err: err}
}

// ModelUnavailableError is a run failure caused by an unavailable model id on
// a credential, rendered with a recovery step.
type ModelUnavailableError struct {
	// Credential is the ModelCredential whose spec.model may need to change.
	Credential string
	Err        error
}

func (e *ModelUnavailableError) Error() string {
	return "model credential " + e.Credential + " cannot use its model: " + e.Err.Error() +
		" — open Models → " + e.Credential + " → Edit and choose a model supported by this endpoint, then test it before saving."
}

func (e *ModelUnavailableError) Unwrap() error { return e.Err }
