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

package api

import (
	"context"
	"encoding/gob"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
)

const (
	projectAssistantInterruptTypePermission = "permission"
	projectAssistantInterruptTypeFollowUp   = "follow_up"
)

type projectEinoPermissionInterruptInfo struct {
	ToolCallID      string                   `json:"toolCallID,omitempty"`
	ToolName        string                   `json:"toolName,omitempty"`
	ArgumentsInJSON string                   `json:"argumentsInJSON,omitempty"`
	Reason          string                   `json:"reason,omitempty"`
	Risk            projectAssistantToolRisk `json:"risk,omitempty"`
}

type projectEinoPermissionInterruptState struct {
	ToolCallID      string `json:"toolCallID,omitempty"`
	ToolName        string `json:"toolName,omitempty"`
	ArgumentsInJSON string `json:"argumentsInJSON,omitempty"`
}

type projectEinoPermissionResumeData struct {
	Decision        projectAssistantPermissionDecision `json:"decision,omitempty"`
	EditedArguments map[string]any                     `json:"editedArguments,omitempty"`
}

type projectEinoFollowUpInterruptInfo struct {
	ToolCallID string   `json:"toolCallID,omitempty"`
	Questions  []string `json:"questions,omitempty"`
	Prompt     string   `json:"prompt,omitempty"`
}

type projectEinoFollowUpInterruptState struct {
	ToolCallID string   `json:"toolCallID,omitempty"`
	Questions  []string `json:"questions,omitempty"`
}

type projectEinoFollowUpResumeData struct {
	Answer string `json:"answer,omitempty"`
}

func init() {
	gob.Register(map[string]any{})
	gob.Register([]any{})
	schema.RegisterName[*projectEinoPermissionInterruptInfo]("faros_app_studio_eino_permission_interrupt_info")
	schema.RegisterName[*projectEinoPermissionInterruptState]("faros_app_studio_eino_permission_interrupt_state")
	schema.RegisterName[*projectEinoPermissionResumeData]("faros_app_studio_eino_permission_resume_data")
	schema.RegisterName[*projectEinoFollowUpInterruptInfo]("faros_app_studio_eino_follow_up_interrupt_info")
	schema.RegisterName[*projectEinoFollowUpInterruptState]("faros_app_studio_eino_follow_up_interrupt_state")
	schema.RegisterName[*projectEinoFollowUpResumeData]("faros_app_studio_eino_follow_up_resume_data")
}

func projectAssistantFollowUpPrompt(questions []string) string {
	questions = normalizeProjectAssistantStringList(questions)
	if len(questions) == 0 {
		return "App Studio needs a little more information before continuing."
	}
	if len(questions) == 1 {
		return strings.TrimSpace(questions[0])
	}
	return "App Studio needs a little more information before continuing."
}

type projectEinoAssistantCheckpointStore struct {
	mu          sync.Mutex
	checkpoints map[string][]byte
}

func newProjectEinoAssistantCheckpointStore() *projectEinoAssistantCheckpointStore {
	return &projectEinoAssistantCheckpointStore{
		checkpoints: map[string][]byte{},
	}
}

func newProjectEinoAssistantCheckpointStoreWithCheckpoint(id string, checkpoint []byte) *projectEinoAssistantCheckpointStore {
	store := newProjectEinoAssistantCheckpointStore()
	if id != "" && len(checkpoint) > 0 {
		store.checkpoints[id] = append([]byte(nil), checkpoint...)
	}
	return store
}

func (s *projectEinoAssistantCheckpointStore) Get(_ context.Context, checkPointID string) ([]byte, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	checkpoint, ok := s.checkpoints[checkPointID]
	return append([]byte(nil), checkpoint...), ok, nil
}

func (s *projectEinoAssistantCheckpointStore) Set(_ context.Context, checkPointID string, checkPoint []byte) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkpoints == nil {
		s.checkpoints = map[string][]byte{}
	}
	s.checkpoints[checkPointID] = append([]byte(nil), checkPoint...)
	return nil
}
