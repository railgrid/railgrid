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
	"encoding/json"
	"strings"
	"testing"
)

func TestParseClarificationRejectsSecretMalformedAndOversizedQuestions(t *testing.T) {
	base := map[string]any{
		"threadId":   "thread-1",
		"turnId":     "turn-1",
		"itemId":     "item-1",
		"isBlocking": true,
		"questions": []map[string]any{{
			"id":       "question-1",
			"header":   "Choice",
			"question": "Choose one",
			"options":  []map[string]string{{"label": "One", "description": "The first choice"}},
		}},
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "secret", mutate: func(value map[string]any) {
			value["questions"].([]map[string]any)[0]["isSecret"] = true
		}},
		{name: "empty question", mutate: func(value map[string]any) {
			value["questions"].([]map[string]any)[0]["question"] = ""
		}},
		{name: "ambiguous duplicate IDs", mutate: func(value map[string]any) {
			value["questions"] = []map[string]any{
				value["questions"].([]map[string]any)[0],
				value["questions"].([]map[string]any)[0],
			}
		}},
		{name: "oversized", mutate: func(value map[string]any) {
			value["questions"].([]map[string]any)[0]["question"] = strings.Repeat("x", maxClarificationField+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := cloneMap(base)
			test.mutate(value)
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if clarification, err := parseClarification("thread-1", "turn-1", data); err == nil || clarification != nil {
				t.Fatalf("parseClarification = %+v, %v; want rejection", clarification, err)
			}
		})
	}
}

func TestParseClarificationAcceptsCodexNullableFields(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"threadId":         "thread-1",
		"turnId":           "turn-1",
		"itemId":           "item-1",
		"isBlocking":       true,
		"autoResolutionMs": nil,
		"questions": []map[string]any{{
			"id":       "question-1",
			"header":   "Choice",
			"question": "Choose one",
			"options":  nil,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	clarification, err := parseClarification("thread-1", "turn-1", data)
	if err != nil {
		t.Fatalf("parseClarification: %v", err)
	}
	if clarification == nil || clarification.Text != "Choice: Choose one" {
		t.Fatalf("clarification = %+v", clarification)
	}
}

func TestParseClarificationAcceptsCodexLegacyMissingBlockingFlag(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"itemId":   "item-1",
		"questions": []map[string]any{{
			"id":       "question-1",
			"header":   "Choice",
			"question": "Choose one",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	clarification, err := parseClarification("thread-1", "turn-1", data)
	if err != nil {
		t.Fatalf("parseClarification: %v", err)
	}
	if clarification == nil || clarification.Text != "Choice: Choose one" {
		t.Fatalf("clarification = %+v", clarification)
	}
}

func TestRequestUserInputMethodIsExact(t *testing.T) {
	for _, method := range []string{"item/tool/request_user_input", "tool/requestUserInput", "mcpServer/elicitation/request"} {
		if isRequestUserInput(method) {
			t.Fatalf("method %q was accepted as the exact request-user-input interaction", method)
		}
	}
	if !isRequestUserInput("item/tool/requestUserInput") {
		t.Fatal("Codex request-user-input method was not recognized")
	}
}

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		switch typed := value.(type) {
		case []map[string]any:
			copied := make([]map[string]any, len(typed))
			for index, item := range typed {
				copied[index] = cloneMap(item)
			}
			result[key] = copied
		default:
			result[key] = value
		}
	}
	return result
}
