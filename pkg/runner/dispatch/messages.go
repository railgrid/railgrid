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
	"strings"
)

// Message is a completed, public assistant message, separate from whole-turn
// timing. Native reasoning payloads are deliberately not treated as prose.
type Message struct {
	ID      string `json:"id,omitempty"`
	Phase   string `json:"phase"` // commentary | final
	Content string `json:"content"`
}

// MessageObserver optionally receives native message boundaries. Text still
// streams normally; this boundary classifies that provisional text without
// replaying its deltas. AssistantObserver retains its whole-turn timing role.
type MessageObserver interface {
	Message(Message)
}

func completedMessage(data json.RawMessage) (Message, bool) {
	var record struct {
		Event string `json:"_railgridEvent"`
		Type  string `json:"type"`
		ID    string `json:"id"`
		Phase string `json:"phase"`
		Text  string `json:"text"`
		Item  *struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Phase  string `json:"phase"`
			Status string `json:"status"`
			Text   string `json:"text"`
		} `json:"item"`
	}
	if len(data) == 0 || json.Unmarshal(data, &record) != nil {
		return Message{}, false
	}
	if record.Type == "railgrid.assistant_message" {
		if record.Phase != "commentary" && record.Phase != "final" {
			return Message{}, false
		}
		return Message{ID: record.ID, Phase: record.Phase, Content: record.Text}, true
	}
	item := record.Item
	if item == nil || (item.Type != "agentMessage" && item.Type != "assistant_message" && item.Type != "assistantMessage") {
		return Message{}, false
	}
	if record.Event == "item/started" {
		return Message{}, false
	}
	phase := strings.ToLower(item.Phase)
	status := strings.ToLower(item.Status)
	// Older runners preserve the item payload but not its notification method.
	// Their completed commentary carries the canonical, nonempty text; started
	// items carry an empty string. New adapters stamp the method explicitly.
	completed := record.Event == "item/completed" || status == "completed" || status == "complete" ||
		(item.Text != "" && (phase == "commentary" || phase == "final_answer" || phase == "finalanswer"))
	if !completed {
		return Message{}, false
	}
	if phase == "commentary" {
		return Message{ID: item.ID, Phase: "commentary", Content: item.Text}, true
	}
	if phase == "final_answer" || phase == "finalanswer" || status == "completed" || status == "complete" {
		return Message{ID: item.ID, Phase: "final", Content: item.Text}, true
	}
	return Message{}, false
}

func (s *stream) message(message Message) {
	key := s.codexTurnID + "/" + message.ID
	if message.ID != "" {
		if s.messages == nil {
			s.messages = map[string]bool{}
		}
		if s.messages[key] {
			return
		}
		s.messages[key] = true
	}
	if message.Phase == "final" && message.Content != "" {
		s.final = message.Content
	}
	if message.ID != "" && key == s.lastTextID && message.Phase == "commentary" {
		s.lastTextClassified = true
	}
	if observer, ok := s.obs.(MessageObserver); ok {
		observer.Message(message)
	}
}

func deltaMessageID(data json.RawMessage) string {
	var record struct {
		ID string `json:"itemId"`
	}
	_ = json.Unmarshal(data, &record)
	return record.ID
}
