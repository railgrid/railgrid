// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	einoschema "github.com/cloudwego/eino/schema"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
)

const (
	projectAssistantThreadTitleTimeout        = 12 * time.Second
	projectAssistantThreadTitleMaxWords       = 7
	projectAssistantThreadTitleMinWords       = 3
	projectAssistantThreadTitleMaxBytes       = 96
	projectAssistantThreadTitlePromptMaxBytes = 4096
)

const projectAssistantThreadTitleSystemPrompt = `Create a terse title for this conversation.
Return only the title, with 3 to 7 words. Do not use quotes, markdown, a prefix,
instructions, or a sentence. Summarize the user's request, not your response.`

// assistantThreadTitleNeedsGeneration is deliberately based on the canonical
// thread event stream rather than project-global messages. A thread gets one
// title attempt, on its first user message, even when the project has older
// conversations.
func (s *Server) assistantThreadTitleNeedsGeneration(ctx context.Context, scope store.Scope, thread store.AssistantThread) bool {
	if strings.TrimSpace(thread.Title) != "" || thread.Status == store.AssistantThreadStatusArchived {
		return false
	}
	events, err := s.loadAllAssistantThreadEvents(ctx, scope, thread.ID)
	if err != nil {
		return false
	}
	for _, event := range events {
		if event.Type == assistantThreadEventUserMessage {
			return false
		}
		var envelope struct {
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		if json.Unmarshal(event.Payload, &envelope) == nil && envelope.Item.Type == assistantThreadEventUserMessage {
			return false
		}
	}
	return true
}

// startAssistantThreadTitleGeneration detaches the one-off title request from
// the durable turn request. The client may disconnect immediately after the
// turn is accepted; this bounded background operation must still finish (or
// fail harmlessly) without changing turn state.
func (s *Server) startAssistantThreadTitleGeneration(c *asclient.Client, scope store.Scope, id identity, thread store.AssistantThread, prompt string) {
	if s == nil || s.store == nil || (c == nil && s.assistantThreadTitleGenerator == nil) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), projectAssistantThreadTitleTimeout)
		defer cancel()
		title, err := s.generateAssistantThreadTitle(ctx, c, prompt)
		if err != nil {
			return
		}
		title = sanitizeAssistantThreadTitle(title)
		if title == "" {
			return
		}
		_, changed, err := s.store.SetAssistantThreadTitleIfEmpty(ctx, scope, thread.ID, id.user, title, store.AssistantThreadEvent{
			ThreadID: thread.ID,
			Type:     assistantThreadEventThreadUpdated,
		})
		if err != nil || !changed {
			return
		}
		// The store commits this event atomically with the compare-and-set. Its
		// payload is intentionally added after the CAS so a manual rename cannot
		// be overwritten by a stale model response.
	}()
}

func (s *Server) generateAssistantThreadTitle(ctx context.Context, c *asclient.Client, prompt string) (string, error) {
	prompt = truncateAssistantThreadTitlePrompt(prompt)
	if prompt == "" {
		return "", errors.New("assistant thread title prompt is empty")
	}
	if s != nil && s.assistantThreadTitleGenerator != nil {
		return s.assistantThreadTitleGenerator(ctx, c, prompt)
	}
	if c == nil {
		return "", errors.New("project client is not configured")
	}
	settings, err := readProjectLLMSettings(ctx, c)
	if err != nil {
		return "", err
	}
	if err := normalizeProjectLLMSettings(&settings); err != nil {
		return "", err
	}
	if strings.TrimSpace(settings.APIKey) == "" {
		return "", errProjectLLMNotConfigured
	}
	model, err := newProjectEinoChatModel(ctx, settings)
	if err != nil {
		return "", err
	}
	reply, err := model.Generate(ctx, []*einoschema.Message{
		einoschema.SystemMessage(projectAssistantThreadTitleSystemPrompt),
		einoschema.UserMessage(prompt),
	}, projectTemperatureOptions(settings.Model, 0.1)...)
	if err != nil {
		return "", err
	}
	if reply == nil {
		return "", errors.New("project LLM returned an empty title")
	}
	return reply.Content, nil
}

func truncateAssistantThreadTitlePrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if len(prompt) <= projectAssistantThreadTitlePromptMaxBytes {
		return prompt
	}
	cut := prompt[:projectAssistantThreadTitlePromptMaxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut)
}

// sanitizeAssistantThreadTitle accepts only a compact, printable title. An
// invalid or underspecified model response is discarded rather than replaced
// with a misleading fallback; the durable turn remains unaffected.
func sanitizeAssistantThreadTitle(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	if len(raw) >= len("title:") && strings.EqualFold(raw[:len("title:")], "title:") {
		raw = strings.TrimSpace(raw[len("title:"):])
	}
	raw = strings.Trim(raw, "`\"'“”‘’")
	var normalized strings.Builder
	for _, r := range raw {
		switch {
		case unicode.IsControl(r):
			normalized.WriteByte(' ')
		case unicode.IsGraphic(r) || unicode.IsSpace(r):
			normalized.WriteRune(r)
		}
	}
	words := strings.Fields(normalized.String())
	if len(words) < projectAssistantThreadTitleMinWords {
		return ""
	}
	if len(words) > projectAssistantThreadTitleMaxWords {
		words = words[:projectAssistantThreadTitleMaxWords]
	}
	for len(words) >= projectAssistantThreadTitleMinWords {
		title := strings.TrimSpace(strings.Join(words, " "))
		title = strings.Trim(title, "`\"'“”‘’.,:;!?-–—")
		if title != "" && len(title) <= projectAssistantThreadTitleMaxBytes && utf8.ValidString(title) {
			return title
		}
		words = words[:len(words)-1]
	}
	return ""
}
