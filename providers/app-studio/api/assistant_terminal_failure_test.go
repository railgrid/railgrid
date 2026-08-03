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
	"errors"
	"testing"

	"github.com/cloudwego/eino/adk"
)

func TestProjectAssistantDurableTerminalContentPreservesModelResponse(t *testing.T) {
	for _, err := range []error{nil, adk.ErrExceedMaxIterations, errProjectAssistantSessionBudgetExceeded, errors.New("provider failed")} {
		if got := projectAssistantDurableTerminalContent("Model response.", "partial", err); got != "Model response." {
			t.Fatalf("content for %v = %q, want unchanged model response", err, got)
		}
	}
}

func TestProjectAssistantDurableTerminalContentDoesNotManufactureResponse(t *testing.T) {
	if got := projectAssistantDurableTerminalContent("", "", errProjectAssistantNoOutput); got != "" {
		t.Fatalf("content = %q, want empty response", got)
	}
}
