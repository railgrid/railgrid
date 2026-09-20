// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package llm

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The deny-list is the whole point of the change, so it is asserted id by id
// against the list OpenAI actually serves. A model that reaches the picker and
// then 404s on the first turn is the bug this exists to prevent.
func TestChatCapable(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
		why  string
	}{
		// Chat models, including the two near-misses that must survive.
		{id: "gpt-4o", want: true},
		{id: "gpt-4o-mini", want: true},
		{id: "gpt-4.1", want: true},
		{id: "gpt-4o-2024-08-06", want: true, why: "a dated snapshot is the same model"},
		{id: "gpt-5", want: true},
		{id: "gpt-5.1", want: true},
		{id: "o3-mini", want: true},
		{id: "gpt-4o-search-preview", want: true, why: "search-preview IS a chat model"},
		{id: "gpt-5.3-chat-latest", want: true, why: "chat-latest IS a chat model"},
		{id: "openai/gpt-4o", want: true, why: "a gateway prefix is not part of the id"},
		{id: "models/gemini-2.5-pro", want: true, why: "a catalog id overrules the -pro rule"},
		{id: "gemini-3-pro", want: true, why: "a catalog id overrules the -pro rule"},
		{id: "claude-sonnet-4-5", want: true},
		{id: "some-gateways-own-model-v2", want: true, why: "an unknown id is offered, not hidden"},

		// Responses-only families.
		{id: "gpt-5.3-codex", want: false},
		{id: "codex-mini-latest", want: false},
		{id: "gpt-5-pro", want: false},
		{id: "o3-pro", want: false},
		{id: "o3-deep-research", want: false},
		{id: "computer-use-preview", want: false},

		// Not chat at all.
		{id: "tts-1", want: false},
		{id: "gpt-4o-mini-tts", want: false},
		{id: "gpt-4o-transcribe", want: false},
		{id: "whisper-1", want: false},
		{id: "text-embedding-3-small", want: false},
		{id: "dall-e-3", want: false},
		{id: "gpt-image-1", want: false},
		{id: "gpt-4o-realtime-preview", want: false},
		{id: "gpt-4o-audio-preview", want: false},
		{id: "omni-moderation-latest", want: false},
		{id: "babbage-002", want: false},
		{id: "davinci-002", want: false},
		{id: "gpt-3.5-turbo-instruct", want: false},
		{id: "chatgpt-4o-latest", want: false},
		{id: "gpt-4o-search-api", want: false},
		{id: "sora-2", want: false},

		{id: "", want: false},
		{id: "   ", want: false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			if got := ChatCapable(tc.id); got != tc.want {
				t.Fatalf("ChatCapable(%q) = %v, want %v%s", tc.id, got, tc.want, hint(tc.why))
			}
		})
	}
}

func hint(why string) string {
	if why == "" {
		return ""
	}
	return " — " + why
}

// Token matching, not substring matching: "pro" must not fire on "preview",
// and "image" must not fire on a model whose name merely contains the letters.
func TestChatCapableMatchesWholeTokens(t *testing.T) {
	for _, id := range []string{"gpt-4o-preview", "prometheus-chat", "imagenet-chat-v1", "ttsx-chat"} {
		if !ChatCapable(id) {
			t.Fatalf("ChatCapable(%q) = false; a deny token matched inside a word", id)
		}
	}
}

// The order is the contract the portal's grouping and status.models both read:
// catalog-known first in catalog order, then the rest alphabetically, with
// duplicates and blanks gone.
func TestFilterChatModels(t *testing.T) {
	got := FilterChatModels([]string{
		"whisper-1", "zeta-chat", "gpt-4o-mini", "dall-e-3", "gpt-4o",
		"gpt-4o", "", "  ", "alpha-chat", "gpt-5.3-codex", "text-embedding-3-small",
		"gpt-5", "gpt-4o-mini-tts", "o3-pro", "mistral-large",
	})
	want := []string{
		// Catalog order: gpt-4o, gpt-4o-mini, …, gpt-5 (see modelcatalog).
		"gpt-4o", "gpt-4o-mini", "gpt-5",
		// Everything else, alphabetically.
		"alpha-chat", "mistral-large", "zeta-chat",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("FilterChatModels() =\n  %v\nwant\n  %v", got, want)
	}
}

// status.models is written from this, and a list whose order moved between
// identical probes would make every reconcile a status write.
func TestFilterChatModelsIsStable(t *testing.T) {
	in := []string{"gpt-5", "zeta", "gpt-4o", "alpha", "gpt-4o-mini"}
	first := FilterChatModels(in)
	for i := 0; i < 5; i++ {
		if !slices.Equal(FilterChatModels(in), first) {
			t.Fatal("FilterChatModels is not stable across calls")
		}
	}
	if len(first) == 0 {
		t.Fatal("expected a non-empty result")
	}
}

// A run that dies on the upstream's "use v1/responses instead" is a model to
// change, and the message has to say which credential owns it — while keeping
// the provider's own words for anyone pasting it into a support thread.
func TestExplainChatCompletionsRefusal(t *testing.T) {
	upstream := errors.New(`engine: start stream: error, status code: 404, message: This model is not supported in the v1/chat/completions endpoint. Use the v1/responses endpoint instead.`)
	got := ExplainChatCompletionsRefusal(upstream, "dev")
	if got == nil {
		t.Fatal("expected the error to survive")
	}
	for _, want := range []string{"dev", "v1/responses", "pick another model"} {
		if !strings.Contains(strings.ToLower(got.Error()), strings.ToLower(want)) {
			t.Fatalf("message %q does not mention %q", got.Error(), want)
		}
	}
	if !errors.Is(got, upstream) {
		t.Fatal("the upstream error must stay unwrappable")
	}

	deprecated := fmt.Errorf("engine: start stream: %w", errors.New("The model `gpt-5.3-chat-latest` is deprecated"))
	if !ChatCompletionsRefusal(deprecated) {
		t.Fatal("a deprecation is a model problem too")
	}

	// Everything else is passed through untouched: dressing a real failure up
	// as a configuration problem sends people to the wrong place.
	other := errors.New("engine: start stream: connection refused")
	if ExplainChatCompletionsRefusal(other, "dev") != other {
		t.Fatal("an unrelated error must not be wrapped")
	}
	// No credential to name, nothing to add.
	if ExplainChatCompletionsRefusal(upstream, "  ") != upstream {
		t.Fatal("an unnamed credential must not be wrapped")
	}
	if ExplainChatCompletionsRefusal(nil, "dev") != nil {
		t.Fatal("nil must stay nil")
	}
}
