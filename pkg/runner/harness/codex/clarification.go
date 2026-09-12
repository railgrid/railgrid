/*
Copyright 2026 The Faros Authors.

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

package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/faroshq/faros/pkg/runner/harness"
)

const (
	maxClarificationPayload   = 64 << 10
	maxClarificationText      = 8 << 10
	maxClarificationField     = 2 << 10
	maxClarificationQuestions = 3
	maxClarificationOptions   = 8
)

type requestUserInputParams struct {
	ThreadID         string                     `json:"threadId"`
	TurnID           string                     `json:"turnId"`
	ItemID           string                     `json:"itemId"`
	Questions        []requestUserInputQuestion `json:"questions"`
	IsBlocking       *bool                      `json:"isBlocking"`
	AutoResolutionMs *uint64                    `json:"autoResolutionMs"`
}

type requestUserInputQuestion struct {
	ID       string                   `json:"id"`
	Header   string                   `json:"header"`
	Question string                   `json:"question"`
	IsOther  bool                     `json:"isOther"`
	IsSecret bool                     `json:"isSecret"`
	Options  []requestUserInputOption `json:"options"`
}

type requestUserInputOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

func parseClarification(sessionID, turnID string, data json.RawMessage) (*harness.Clarification, error) {
	if len(data) == 0 || len(data) > maxClarificationPayload || !utf8.Valid(data) {
		return nil, errors.New("request-user-input payload is empty, invalid, or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var params requestUserInputParams
	if err := decoder.Decode(&params); err != nil {
		return nil, errors.New("request-user-input payload is malformed")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("request-user-input payload contains trailing data")
	}
	if params.ThreadID == "" || params.TurnID == "" || params.ItemID == "" {
		return nil, errors.New("request-user-input payload is missing session identity")
	}
	if len(params.ThreadID) > maxClarificationField || len(params.TurnID) > maxClarificationField || len(params.ItemID) > maxClarificationField {
		return nil, errors.New("request-user-input identity is oversized")
	}
	if sessionID != "" && params.ThreadID != sessionID {
		return nil, errors.New("request-user-input payload referenced a foreign session")
	}
	if turnID != "" && params.TurnID != turnID {
		return nil, errors.New("request-user-input payload referenced a foreign turn")
	}
	// The installed Codex app-server deserializer defaults an omitted
	// isBlocking field to true for legacy request payloads. Preserve that
	// runtime compatibility while rejecting an explicit nonblocking request.
	if params.IsBlocking != nil && !*params.IsBlocking {
		return nil, errors.New("nonblocking request-user-input payload is not resumable")
	}
	if len(params.Questions) == 0 || len(params.Questions) > maxClarificationQuestions {
		return nil, errors.New("request-user-input payload has an unsupported question count")
	}

	var text strings.Builder
	seenQuestionIDs := make(map[string]struct{}, len(params.Questions))
	for index, question := range params.Questions {
		if question.IsSecret {
			return nil, errors.New("secret request-user-input payload is not a product question")
		}
		if !validClarificationText(question.ID) || !validClarificationText(question.Header) || !validClarificationText(question.Question) {
			return nil, errors.New("request-user-input question is empty, invalid, or oversized")
		}
		if _, exists := seenQuestionIDs[question.ID]; exists {
			return nil, errors.New("request-user-input question IDs are ambiguous")
		}
		seenQuestionIDs[question.ID] = struct{}{}
		if len(question.Options) > maxClarificationOptions {
			return nil, errors.New("request-user-input option count is oversized")
		}
		if index > 0 {
			text.WriteString("\n\n")
		}
		text.WriteString(question.Header)
		text.WriteString(": ")
		text.WriteString(question.Question)
		if len(question.Options) > 0 {
			text.WriteString("\nOptions:")
			for _, option := range question.Options {
				if !validClarificationText(option.Label) || !validClarificationText(option.Description) {
					return nil, errors.New("request-user-input option is empty, invalid, or oversized")
				}
				text.WriteString("\n- ")
				text.WriteString(option.Label)
				text.WriteString(": ")
				text.WriteString(option.Description)
			}
		}
		if text.Len() > maxClarificationText {
			return nil, errors.New("request-user-input question text is oversized")
		}
	}

	return &harness.Clarification{
		ID:   stableClarificationID(params.ThreadID, params.TurnID, params.ItemID),
		Text: text.String(),
	}, nil
}

func validClarificationText(value string) bool {
	return value != "" && strings.TrimSpace(value) != "" && len(value) <= maxClarificationField && utf8.ValidString(value)
}

func stableClarificationID(sessionID, turnID, itemID string) string {
	digest := sha256.Sum256([]byte(sessionID + "\x00" + turnID + "\x00" + itemID))
	return "clarification-" + hex.EncodeToString(digest[:])
}

// normalizeAsyncQuestion recognizes Codex's structured asynchronous question
// notification. Ordinary agent prose is never interpreted as a question.
// A recognized but invalid item normalizes to an invalid payload, so the common
// parser holds it as operator input rather than publishing an unsafe question.
func normalizeAsyncQuestion(data json.RawMessage) (json.RawMessage, bool) {
	var envelope struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Item     struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Delivery  string          `json:"delivery"`
			Questions json.RawMessage `json:"questions"`
		} `json:"item"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Item.Type != "agentMessage" || envelope.Item.Delivery != "async" || len(envelope.Item.Questions) == 0 || string(envelope.Item.Questions) == "null" {
		return nil, false
	}
	if len(data) > maxClarificationPayload || !utf8.Valid(data) {
		return nil, true
	}
	var questions []struct {
		Title   string   `json:"title"`
		Options []string `json:"options"`
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Item.Questions))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&questions) != nil || len(questions) == 0 || len(questions) > maxClarificationQuestions {
		return nil, true
	}
	params := requestUserInputParams{ThreadID: envelope.ThreadID, TurnID: envelope.TurnID, ItemID: envelope.Item.ID}
	for i, q := range questions {
		question := requestUserInputQuestion{ID: fmt.Sprintf("question-%d", i+1), Header: "Product question", Question: q.Title}
		if len(q.Options) > maxClarificationOptions {
			return nil, true
		}
		for _, option := range q.Options {
			question.Options = append(question.Options, requestUserInputOption{Label: option, Description: option})
		}
		params.Questions = append(params.Questions, question)
	}
	normalized, err := json.Marshal(params)
	if err != nil {
		return nil, true
	}
	return normalized, true
}
