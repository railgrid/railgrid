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
	"errors"
	"strings"
	"testing"
)

func TestProjectAssistantProviderMCPReadFailureDoesNotBecomeMutation(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "provider-read-failure")
	state := newProjectEinoAssistantRunState()
	var events []projectToolCallStreamEvent
	req := h.req
	req.StreamCallbacks.OnToolCall = func(event projectToolCallStreamEvent) {
		events = append(events, event)
	}

	providerError := "tableRef \"sales.missing_orders\" not found; " + strings.Repeat("opaque-detail ", 80)
	backend := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name: projectToolDatabricksDescribeTable,
			Risk: projectAssistantToolRiskRead,
		},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			return "", errors.New(providerError)
		},
	}
	tool := projectEinoAssistantTool{server: h.server, tool: backend, req: req, runState: state}
	result, err := tool.invokeAllowedTool(context.Background(), "call-provider-read", backend.Spec(), map[string]any{
		"tableRef": "sales.missing_orders",
	})
	if err != nil {
		t.Fatalf("provider read returned error: %v", err)
	}
	if !strings.Contains(result, "Tool call failed: tableRef") || !strings.Contains(result, "sales.missing_orders") {
		t.Fatalf("model result = %q, want bounded provider/tableRef failure", result)
	}
	if len(result) > projectToolInfoLimit {
		t.Fatalf("model result length = %d, want <= %d", len(result), projectToolInfoLimit)
	}
	if attempts := state.CheckpointState().MutationRecoveryAttempts; len(attempts) != 0 {
		t.Fatalf("provider read created mutation recovery state: %#v", attempts)
	}

	var failed projectToolCallStreamEvent
	for _, event := range events {
		if event.Status == "failed" {
			failed = event
			break
		}
	}
	if failed.ID == "" {
		t.Fatalf("events = %#v, want failed provider read event", events)
	}
	if failed.Mutation != nil || failed.MutationError != nil {
		t.Fatalf("failed provider read event = %#v, must not carry mutation metadata", failed)
	}
	if len(failed.Error) == 0 || len(failed.Error) > projectToolInfoLimit || !strings.Contains(failed.Error, "tableRef") {
		t.Fatalf("failed provider read error = %q, want bounded provider detail", failed.Error)
	}
	item := projectAssistantActionFeedItemFromToolCall(failed)
	if item.Diagnostic == nil || item.Diagnostic.Category != "provider" {
		t.Fatalf("provider read feed item = %#v, want provider diagnostic", item)
	}
	if item.Diagnostic.Operation == "mutation" || item.Diagnostic.Code == "mutation_failed" {
		t.Fatalf("provider read feed diagnostic = %#v, must not be mutation classification", item.Diagnostic)
	}

	outcome, ok, outcomeErr := h.req.eventLedger.ToolCallOutcome(context.Background(), "call-provider-read")
	if outcomeErr != nil || !ok {
		t.Fatalf("provider read durable outcome = %#v, ok=%t, err=%v", outcome, ok, outcomeErr)
	}
	if !outcome.Failed || outcome.Result != result {
		t.Fatalf("provider read durable outcome = %#v, want failed result", outcome)
	}
}
