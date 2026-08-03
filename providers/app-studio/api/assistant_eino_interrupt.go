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
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
)

const (
	projectAssistantInterruptTypePermission = "permission"
	projectAssistantInterruptTypeApproval   = "approval"
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
	ToolCallID            string `json:"toolCallID,omitempty"`
	ToolName              string `json:"toolName,omitempty"`
	ArgumentsInJSON       string `json:"argumentsInJSON,omitempty"`
	CommitWorkspaceDigest string `json:"commitWorkspaceDigest,omitempty"`
}

type projectEinoPermissionResumeData struct {
	Decision        projectAssistantPermissionDecision `json:"decision,omitempty"`
	EditedArguments map[string]any                     `json:"editedArguments,omitempty"`
}

type projectEinoFollowUpInterruptInfo struct {
	ToolCallID string                             `json:"toolCallID,omitempty"`
	Questions  []projectAssistantFollowUpQuestion `json:"questions,omitempty"`
	Prompt     string                             `json:"prompt,omitempty"`
}

type projectEinoFollowUpInterruptState struct {
	ToolCallID string                             `json:"toolCallID,omitempty"`
	Questions  []projectAssistantFollowUpQuestion `json:"questions,omitempty"`
}

type projectEinoFollowUpResumeData struct {
	Answers map[string]projectAssistantFollowUpAnswer `json:"answers,omitempty"`
	Answer  string                                    `json:"answer,omitempty"`
}

type projectAssistantFollowUpQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type projectAssistantFollowUpQuestion struct {
	ID       string                                   `json:"id"`
	Header   string                                   `json:"header,omitempty"`
	Question string                                   `json:"question"`
	IsOther  bool                                     `json:"isOther,omitempty"`
	Options  []projectAssistantFollowUpQuestionOption `json:"options,omitempty"`
}

// UnmarshalJSON preserves resumability for checkpoints created before
// follow-up questions adopted Codex's structured question contract.
func (q *projectAssistantFollowUpQuestion) UnmarshalJSON(raw []byte) error {
	var legacy string
	if err := json.Unmarshal(raw, &legacy); err == nil {
		*q = projectAssistantFollowUpQuestion{Question: strings.TrimSpace(legacy), IsOther: true}
		return nil
	}
	type wire projectAssistantFollowUpQuestion
	var decoded wire
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*q = projectAssistantFollowUpQuestion(decoded)
	return nil
}

type projectAssistantFollowUpAnswer struct {
	Answers []string `json:"answers"`
}

func normalizeProjectAssistantFollowUpQuestions(questions []projectAssistantFollowUpQuestion) []projectAssistantFollowUpQuestion {
	if len(questions) > 3 {
		questions = questions[:3]
	}
	normalized := make([]projectAssistantFollowUpQuestion, 0, len(questions))
	seen := map[string]struct{}{}
	for index, question := range questions {
		question.Question = strings.TrimSpace(question.Question)
		if question.Question == "" {
			continue
		}
		question.ID = strings.TrimSpace(question.ID)
		if question.ID == "" {
			question.ID = fmt.Sprintf("question_%d", index+1)
		}
		if _, exists := seen[question.ID]; exists {
			question.ID = fmt.Sprintf("question_%d", index+1)
		}
		seen[question.ID] = struct{}{}
		question.Header = strings.TrimSpace(question.Header)
		question.IsOther = true
		options := make([]projectAssistantFollowUpQuestionOption, 0, len(question.Options))
		for _, option := range question.Options {
			option.Label = strings.TrimSpace(option.Label)
			option.Description = strings.TrimSpace(option.Description)
			if option.Label != "" && option.Description != "" {
				options = append(options, option)
			}
		}
		if len(options) > 3 {
			options = options[:3]
		}
		question.Options = options
		normalized = append(normalized, question)
	}
	return normalized
}

func projectAssistantFollowUpQuestionsFromArguments(value any) ([]projectAssistantFollowUpQuestion, error) {
	requireOptions := false
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if _, legacy := item.(string); !legacy {
				requireOptions = true
				break
			}
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var questions []projectAssistantFollowUpQuestion
	if err := json.Unmarshal(raw, &questions); err != nil {
		return nil, err
	}
	questions = normalizeProjectAssistantFollowUpQuestions(questions)
	if len(questions) == 0 {
		return nil, fmt.Errorf("follow-up requires at least one question")
	}
	for _, question := range questions {
		if (requireOptions && len(question.Options) < 2) || len(question.Options) == 1 {
			return nil, fmt.Errorf("follow-up question %q requires two or three options", question.ID)
		}
	}
	return questions, nil
}

func cloneProjectAssistantFollowUpQuestions(questions []projectAssistantFollowUpQuestion) []projectAssistantFollowUpQuestion {
	cloned := make([]projectAssistantFollowUpQuestion, len(questions))
	for index, question := range questions {
		cloned[index] = question
		cloned[index].Options = append([]projectAssistantFollowUpQuestionOption(nil), question.Options...)
	}
	return cloned
}

func cloneProjectAssistantFollowUpAnswers(answers map[string]projectAssistantFollowUpAnswer) map[string]projectAssistantFollowUpAnswer {
	if len(answers) == 0 {
		return nil
	}
	cloned := make(map[string]projectAssistantFollowUpAnswer, len(answers))
	for id, answer := range answers {
		cloned[id] = projectAssistantFollowUpAnswer{Answers: append([]string(nil), answer.Answers...)}
	}
	return cloned
}

func projectAssistantFollowUpResponse(
	questions []projectAssistantFollowUpQuestion,
	data *projectEinoFollowUpResumeData,
) (map[string]projectAssistantFollowUpAnswer, error) {
	questions = normalizeProjectAssistantFollowUpQuestions(questions)
	if data == nil {
		return nil, fmt.Errorf("follow-up answer is required")
	}
	answers := cloneProjectAssistantFollowUpAnswers(data.Answers)
	if len(answers) == 0 {
		legacy := strings.TrimSpace(data.Answer)
		if legacy == "" {
			return nil, fmt.Errorf("follow-up answer is required")
		}
		answers = make(map[string]projectAssistantFollowUpAnswer, len(questions))
		for _, question := range questions {
			answers[question.ID] = projectAssistantFollowUpAnswer{Answers: []string{legacy}}
		}
	}
	normalized := make(map[string]projectAssistantFollowUpAnswer, len(questions))
	for _, question := range questions {
		answer, ok := answers[question.ID]
		if !ok {
			return nil, fmt.Errorf("answer for follow-up question %q is required", question.ID)
		}
		values := normalizeProjectAssistantStringList(answer.Answers)
		if len(values) == 0 {
			return nil, fmt.Errorf("answer for follow-up question %q is required", question.ID)
		}
		normalized[question.ID] = projectAssistantFollowUpAnswer{Answers: values}
	}
	return normalized, nil
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

func projectAssistantFollowUpPrompt(questions []projectAssistantFollowUpQuestion) string {
	questions = normalizeProjectAssistantFollowUpQuestions(questions)
	if len(questions) == 0 {
		return "App Studio needs a little more information before continuing."
	}
	if len(questions) == 1 {
		return questions[0].Question
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

// Delete implements adk.CheckPointDeleter so terminal TurnLoop exits cannot
// leave a resumable Eino checkpoint in the in-memory adapter.
func (s *projectEinoAssistantCheckpointStore) Delete(_ context.Context, checkPointID string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.checkpoints, checkPointID)
	return nil
}
