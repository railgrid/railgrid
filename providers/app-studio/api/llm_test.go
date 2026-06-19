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
	"reflect"
	"strings"
	"testing"
)

func TestNewProjectEinoAssistantModelFactoryUsesNativeOpenAIModel(t *testing.T) {
	factory := newProjectEinoAssistantModelFactory(&Server{})
	model, err := factory(context.Background(), projectAssistantRunRequest{
		LLM: projectLLMSettings{
			Provider: defaultProjectLLMProvider,
			BaseURL:  "https://llm.example.test/v1",
			Model:    "test-model",
			APIKey:   "test-key",
		},
	}, newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatalf("newProjectEinoAssistantModelFactory returned error: %v", err)
	}
	if got := reflect.TypeOf(model).String(); !strings.Contains(got, "openai.ChatModel") {
		t.Fatalf("model type = %s, want native Eino OpenAI chat model", got)
	}
}

func TestNewProjectEinoAssistantModelFactoryUsesNativeGeminiModel(t *testing.T) {
	factory := newProjectEinoAssistantModelFactory(&Server{})
	model, err := factory(context.Background(), projectAssistantRunRequest{
		LLM: projectLLMSettings{
			Provider: projectLLMProviderGoogle,
			BaseURL:  "https://generativelanguage.googleapis.com",
			Model:    "gemini-2.5-flash",
			APIKey:   "test-key",
		},
	}, newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatalf("newProjectEinoAssistantModelFactory returned error: %v", err)
	}
	if got := reflect.TypeOf(model).String(); !strings.Contains(got, "gemini.ChatModel") {
		t.Fatalf("model type = %s, want native Eino Gemini chat model", got)
	}
}
