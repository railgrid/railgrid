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
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectEinoAssistantInlineCommentaryPublishesOnlyCompletedToolAdjacentProse(t *testing.T) {
	toolCall := schema.ToolCall{
		ID:   "call-read",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      projectToolReadFile,
			Arguments: `{"file_path":"secret.txt"}`,
		},
	}
	tests := []struct {
		name      string
		output    *adk.TypedMessageVariant[*schema.Message]
		wantText  string
		wantCalls int
		wantChunk string
	}{
		{
			name: "stream completion",
			output: &adk.TypedMessageVariant[*schema.Message]{
				IsStreaming: true,
				Role:        schema.Assistant,
				MessageStream: schema.StreamReaderFromArray([]*schema.Message{
					schema.AssistantMessage("I will inspect ", nil),
					schema.AssistantMessage("the project first.", []schema.ToolCall{toolCall}),
				}),
			},
			wantText:  "I will inspect the project first.",
			wantCalls: 1,
		},
		{
			name: "nonstream completion",
			output: &adk.TypedMessageVariant[*schema.Message]{
				Role:    schema.Assistant,
				Message: schema.AssistantMessage("I will inspect the project first.", []schema.ToolCall{toolCall}),
			},
			wantText:  "I will inspect the project first.",
			wantCalls: 1,
		},
		{
			name: "tool call without prose",
			output: &adk.TypedMessageVariant[*schema.Message]{
				Role:    schema.Assistant,
				Message: schema.AssistantMessage("", []schema.ToolCall{toolCall}),
			},
			wantCalls: 0,
		},
		{
			name: "tool free answer stays terminal content",
			output: &adk.TypedMessageVariant[*schema.Message]{
				Role:    schema.Assistant,
				Message: schema.AssistantMessage("final answer", nil),
			},
			wantText:  "",
			wantCalls: 0,
			wantChunk: "final answer",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var commentary []string
			var chunks []string
			message, err := projectEinoAssistantMessageOutput(context.Background(), test.output, projectAssistantStreamCallbacks{
				OnCommentary: func(message string) { commentary = append(commentary, message) },
				OnChunk:      func(chunk string) { chunks = append(chunks, chunk) },
			}, nil)
			if err != nil {
				t.Fatalf("message output: %v", err)
			}
			if message == nil {
				t.Fatal("message output is nil")
			}
			if len(commentary) != test.wantCalls {
				t.Fatalf("commentary = %#v, want %d callback(s)", commentary, test.wantCalls)
			}
			if test.wantText != "" && (len(commentary) != 1 || commentary[0] != test.wantText) {
				t.Fatalf("commentary = %#v, want %#v", commentary, []string{test.wantText})
			}
			if test.wantChunk == "" && len(chunks) != 0 {
				t.Fatalf("tool-adjacent output was emitted as terminal chunks: %#v", chunks)
			}
			if test.wantChunk != "" && (len(chunks) != 1 || chunks[0] != test.wantChunk) {
				t.Fatalf("terminal chunks = %#v, want %#v", chunks, []string{test.wantChunk})
			}
			if test.name == "tool free answer stays terminal content" && message.Content != "final answer" {
				t.Fatalf("terminal content = %q, want final answer", message.Content)
			}
		})
	}

	var invalid []string
	projectEinoAssistantPublishInlineCommentary(schema.Assistant, schema.AssistantMessage("bad\u0001text", []schema.ToolCall{toolCall}), projectAssistantStreamCallbacks{
		OnCommentary: func(message string) { invalid = append(invalid, message) },
	}, nil)
	if len(invalid) != 0 {
		t.Fatalf("invalid commentary was published: %#v", invalid)
	}
}

func TestProjectEinoAssistantInlineCommentarySharesProgressAcceptanceLedger(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.NextModelCallOrdinal()
	toolCall := schema.ToolCall{ID: "call-read", Type: "function", Function: schema.FunctionCall{Name: projectToolReadFile}}
	message := schema.AssistantMessage("I will inspect the current source first.", []schema.ToolCall{toolCall})
	var commentary []string
	callbacks := projectAssistantStreamCallbacks{OnCommentary: func(message string) {
		commentary = append(commentary, message)
	}}

	projectEinoAssistantPublishInlineCommentary(schema.Assistant, message, callbacks, runState)
	projectEinoAssistantPublishInlineCommentary(schema.Assistant, message, callbacks, runState)
	if len(commentary) != 1 {
		t.Fatalf("commentary = %#v, want one accepted callback across replay", commentary)
	}
	if runState.AcceptedProgressCount() != 1 {
		t.Fatalf("accepted progress count = %d, want 1", runState.AcceptedProgressCount())
	}
	if runState.AcceptProgressMessage(commentary[0]) {
		t.Fatal("report_progress duplicate was accepted after inline commentary")
	}
}

func TestProjectEinoAssistantLiveContextUpdatesOnlyChangedNamedSections(t *testing.T) {
	previous := map[string]string{"project": "project-v1", "tools": "tools-v1", "removed": "old"}
	sections := []projectEinoAssistantLiveContextSection{
		{name: "project", content: "project-v2"},
		{name: "tools", content: "tools-v1"},
		{name: "skills", content: "skills-v1"},
	}
	updates := projectEinoAssistantLiveContextUpdates(sections, previous)
	if len(updates) != 3 {
		t.Fatalf("updates = %#v, want changed, added, and cleared sections", updates)
	}
	joined := ""
	for _, update := range updates {
		joined += update.Content + "\n"
	}
	for _, want := range []string{"Section: project\nproject-v2", "Section: skills\nskills-v1", "Section: removed\nSection cleared."} {
		if !strings.Contains(joined, want) {
			t.Fatalf("updates omit %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "tools-v1") {
		t.Fatalf("unchanged tools section was re-emitted: %s", joined)
	}
	if got := projectEinoAssistantLiveContextUpdates(sections, projectEinoAssistantCloneLiveContextSections(sections)); len(got) != 0 {
		t.Fatalf("no-op update = %#v, want none", got)
	}
}

func TestProjectEinoAssistantProjectContextUpdateIncludesChangedMemoryOnly(t *testing.T) {
	previous := "static instructions\nProject metadata:\n- Display name: Demo\n\nProject memory:\nGoals:\n- old"
	changed := "static instructions\nProject metadata:\n- Display name: Demo\n\nProject memory:\nGoals:\n- new"
	update := projectEinoAssistantProjectContextUpdateContent(changed, previous)
	if !strings.Contains(update, "Project memory:\nGoals:\n- new") {
		t.Fatalf("memory-only update was omitted: %q", update)
	}
	unchanged := projectEinoAssistantProjectContextUpdateContent(previous, previous)
	if strings.Contains(unchanged, "Project memory:") {
		t.Fatalf("unchanged memory was replayed: %q", unchanged)
	}
}

func TestProjectAssistantAuditV2RetainsUncappedRollupAndBackwardDecodes(t *testing.T) {
	run := &store.AssistantRun{ID: "run-audit-v2"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	total := projectAssistantAuditMaxModelCalls + 3
	for ordinal := 1; ordinal <= total; ordinal++ {
		inputBytes := int64(100 + ordinal)
		if err := recorder.recordModelCall(context.Background(), ordinal, 0, 0, nil, nil, nil, inputBytes); err != nil {
			t.Fatal(err)
		}
		response := schema.AssistantMessage("do-not-store-this-response", nil)
		response.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens:       10,
			PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 3},
			CompletionTokens:   4,
			TotalTokens:        14,
		}}
		if err := recorder.recordModelResult(context.Background(), ordinal, response); err != nil {
			t.Fatal(err)
		}
	}
	recorder.recordModelRetryAttempt(context.Background())

	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.Version != projectAssistantAuditVersion {
		t.Fatalf("audit version = %d, want %d", audit.Version, projectAssistantAuditVersion)
	}
	if len(audit.ModelCalls) != projectAssistantAuditMaxModelCalls {
		t.Fatalf("retained model calls = %d, want %d", len(audit.ModelCalls), projectAssistantAuditMaxModelCalls)
	}
	stats := audit.ModelCallStats
	if stats == nil {
		t.Fatal("model-call rollup missing")
	}
	if stats.TotalCalls != total || stats.RetainedCalls != projectAssistantAuditMaxModelCalls || stats.DroppedCalls != 3 || stats.RetryAttempts != 1 {
		t.Fatalf("model-call rollup = %#v", stats)
	}
	if stats.InputBytes != int64(total*100+total*(total+1)/2) ||
		stats.PromptTokens != int64(total*10) ||
		stats.CachedPromptTokens != int64(total*3) ||
		stats.CompletionTokens != int64(total*4) ||
		stats.TotalTokens != int64(total*14) ||
		stats.MissingUsageCalls != 0 {
		t.Fatalf("usage rollup = %#v", stats)
	}
	if strings.Contains(string(run.Audit), "do-not-store-this-response") {
		t.Fatalf("raw model response leaked into audit: %s", run.Audit)
	}
	// Eino may follow a stream callback carrying usage with an ordinary output
	// callback that omits ResponseMeta. The later nil must not regress truth.
	if err := recorder.recordModelResult(context.Background(), total, schema.AssistantMessage("same response", nil)); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.ModelCallStats.MissingUsageCalls != 0 {
		t.Fatalf("later metadata-free callback regressed usage rollup: %#v", audit.ModelCallStats)
	}

	legacy := &store.AssistantRun{ID: "run-audit-v1", Audit: []byte(`{"version":1,"modelCalls":[{"ordinal":1}]}`)}
	legacyRecorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, legacy, time.Now().UTC())
	var legacyAudit projectAssistantRunAudit
	if err := json.Unmarshal(legacy.Audit, &legacyAudit); err != nil {
		t.Fatal(err)
	}
	if legacyAudit.Version != projectAssistantAuditVersion || legacyAudit.ModelCallStats != nil {
		t.Fatalf("legacy audit migration = %#v, want v2 without empty rollup", legacyAudit)
	}
	legacyRecorder.recordModelRetryAttempt(context.Background())
	if err := json.Unmarshal(legacy.Audit, &legacyAudit); err != nil {
		t.Fatal(err)
	}
	if legacyAudit.ModelCallStats == nil || legacyAudit.ModelCallStats.RetryAttempts != 1 {
		t.Fatalf("legacy rollup after event = %#v", legacyAudit.ModelCallStats)
	}
}

func TestProjectEinoAssistantRetryRecordsAuditAttempt(t *testing.T) {
	run := &store.AssistantRun{ID: "run-retry-audit"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	config := projectEinoAssistantModelRetryConfig(projectAssistantRunRequest{auditRecorder: recorder}, nil)
	decision := config.ShouldRetry(context.Background(), &adk.RetryContext{
		RetryAttempt: 1,
		Err:          &projectEinoAssistantModelTimeoutError{Code: "model_first_response_timeout"},
	})
	if !decision.Retry {
		t.Fatalf("retry decision = %#v, want retry", decision)
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.ModelCallStats == nil || audit.ModelCallStats.RetryAttempts != 1 {
		t.Fatalf("retry audit = %#v", audit.ModelCallStats)
	}
}

func TestProjectEinoAssistantOptimizationModeIsStickyAcrossCheckpointResume(t *testing.T) {
	t.Setenv(projectEinoAssistantOptimizationEnv, "unknown")
	if got := projectEinoAssistantOptimizationModeFromEnvironment(); got != "" {
		t.Fatalf("unknown environment mode = %q, want legacy", got)
	}
	t.Setenv(projectEinoAssistantOptimizationEnv, "codex_poc")
	if got := projectEinoAssistantOptimizationModeFromEnvironment(); got != projectEinoAssistantOptimizationCodexPOC {
		t.Fatalf("POC environment mode = %q", got)
	}
	state := newProjectEinoAssistantRunState()
	state.SetAgentOptimizationMode(" CoDeX_PoC ")
	if !state.CodexPOCEnabled() {
		t.Fatal("codex POC mode was not normalized")
	}
	checkpoint := state.CheckpointState()
	t.Setenv(projectEinoAssistantOptimizationEnv, "")

	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	if !restored.CodexPOCEnabled() {
		t.Fatal("checkpoint resume did not preserve the run's optimization mode")
	}

	legacy := newProjectEinoAssistantRunState()
	legacy.RestoreCheckpointState(projectAssistantCheckpointState{})
	if legacy.CodexPOCEnabled() {
		t.Fatal("legacy checkpoint unexpectedly opted into the POC")
	}
}

func TestProjectAssistantAuditV2RestartedDuplicateUsageIsIdempotent(t *testing.T) {
	run := &store.AssistantRun{ID: "run-audit-restart"}
	first := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	if err := first.recordModelCall(context.Background(), 1, 0, 0, nil, nil, nil, 100); err != nil {
		t.Fatal(err)
	}
	response := schema.AssistantMessage("done", nil)
	response.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14,
	}}
	if err := first.recordModelResult(context.Background(), 1, response); err != nil {
		t.Fatal(err)
	}
	restarted := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	if err := restarted.recordModelResult(context.Background(), 1, response); err != nil {
		t.Fatal(err)
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.ModelCallStats == nil || audit.ModelCallStats.PromptTokens != 10 || audit.ModelCallStats.CompletionTokens != 4 || audit.ModelCallStats.TotalTokens != 14 {
		t.Fatalf("duplicate restart usage was counted again: %#v", audit.ModelCallStats)
	}
}

func TestProjectEinoAssistantToolSearchReducesSchemasAndLoadsSelectedCapability(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "poc-schema-ab")
	h.req.TurnProfile = projectAssistantTurnProfileImplementation
	h.req.TurnPolicy = projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
	discovery := projectEinoAssistantToolDiscovery{IncludeCommitBridge: true, MCPTools: projectEinoAssistantPOCTestDynamicTools(nil)}

	legacyState := newProjectEinoAssistantRunState()
	legacyState.SetTurnPolicy(h.req.TurnPolicy)
	legacyState.SetToolDiscovery(discovery)
	legacyLifecycle := projectEinoAssistantLifecycleMiddleware(h.req, legacyState, h.server).(*projectEinoAssistantLifecycle)
	legacyModel := &adk.ChatModelAgentState{}
	if err := legacyLifecycle.refreshExecutableToolContext(context.Background(), legacyModel, &adk.ModelContext{}); err != nil {
		t.Fatal(err)
	}
	assertProjectEinoAssistantToolInfoPresence(t, legacyModel.ToolInfos, "mcp_database_query", true)
	assertProjectEinoAssistantToolInfoPresence(t, legacyModel.ToolInfos, projectEinoAssistantToolSearchTool, false)
	assertProjectEinoAssistantToolInfoPresence(t, legacyModel.ToolInfos, projectToolReadFile, true)
	assertProjectEinoAssistantToolInfoPresence(t, legacyModel.ToolInfos, projectToolCommitProjectFiles, true)

	pocState := newProjectEinoAssistantRunState()
	pocState.SetTurnPolicy(h.req.TurnPolicy)
	pocState.SetAgentOptimizationMode(projectEinoAssistantOptimizationCodexPOC)
	pocState.SetToolDiscovery(discovery)
	pocLifecycle := projectEinoAssistantLifecycleMiddleware(h.req, pocState, h.server).(*projectEinoAssistantLifecycle)
	pocModel := &adk.ChatModelAgentState{}
	if err := pocLifecycle.refreshExecutableToolContext(context.Background(), pocModel, &adk.ModelContext{}); err != nil {
		t.Fatal(err)
	}
	assertProjectEinoAssistantToolInfoPresence(t, pocModel.ToolInfos, "mcp_database_query", false)
	assertProjectEinoAssistantToolInfoPresence(t, pocModel.ToolInfos, projectEinoAssistantToolSearchTool, true)
	assertProjectEinoAssistantToolInfoPresence(t, pocModel.ToolInfos, projectToolReadFile, true)
	assertProjectEinoAssistantToolInfoPresence(t, pocModel.ToolInfos, projectToolCommitProjectFiles, false)
	legacyTokens := projectEinoAssistantToolSchemasTokenEstimate(legacyModel.ToolInfos)
	pocTokens := projectEinoAssistantToolSchemasTokenEstimate(pocModel.ToolInfos)
	if len(pocModel.ToolInfos) >= len(legacyModel.ToolInfos) || pocTokens >= legacyTokens {
		t.Fatalf("POC schema footprint = %d tools/%d tokens, legacy = %d tools/%d tokens", len(pocModel.ToolInfos), pocTokens, len(legacyModel.ToolInfos), legacyTokens)
	}

	result, err := json.Marshal(projectEinoAssistantToolSearchResult{
		CatalogDigest: projectEinoAssistantDynamicToolCatalogDigest(discovery),
		Matches:       []projectEinoAssistantToolSearchMatch{{Name: "mcp_database_query"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pocState.ApplyDynamicToolSearchResult(string(result)); err != nil {
		t.Fatal(err)
	}
	if err := pocLifecycle.refreshExecutableToolContext(context.Background(), pocModel, &adk.ModelContext{}); err != nil {
		t.Fatal(err)
	}
	assertProjectEinoAssistantToolInfoPresence(t, pocModel.ToolInfos, "mcp_database_query", true)
	assertProjectEinoAssistantToolInfoPresence(t, pocModel.ToolInfos, "mcp_issue_create", false)
	assertProjectEinoAssistantToolInfoPresence(t, pocModel.ToolInfos, projectToolCommitProjectFiles, false)

	commitResult, err := json.Marshal(projectEinoAssistantToolSearchResult{
		CatalogDigest: projectEinoAssistantDynamicToolCatalogDigest(discovery),
		Matches:       []projectEinoAssistantToolSearchMatch{{Name: projectToolCommitProjectFiles}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pocState.ApplyDynamicToolSearchResult(string(commitResult)); err != nil {
		t.Fatal(err)
	}
	if err := pocLifecycle.refreshExecutableToolContext(context.Background(), pocModel, &adk.ModelContext{}); err != nil {
		t.Fatal(err)
	}
	assertProjectEinoAssistantToolInfoPresence(t, pocModel.ToolInfos, projectToolCommitProjectFiles, true)
}

func TestProjectEinoAssistantPOCDefersCommitBeforeBackendAdmission(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "poc-deferred-commit")
	h.req.TurnProfile = projectAssistantTurnProfileImplementation
	h.req.TurnPolicy = projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
	port := &projectAssistantV2CountingCommitPort{}
	h.req.ToolPort = port
	discovery := projectEinoAssistantToolDiscovery{IncludeCommitBridge: true}
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(h.req.TurnPolicy)
	state.SetAgentOptimizationMode(projectEinoAssistantOptimizationCodexPOC)
	state.SetToolDiscovery(discovery)
	node := projectEinoAssistantPOCTestToolsNode(t, h, state, discovery)
	result := projectEinoAssistantPOCInvokeTool(t, node, "call-hidden-commit", projectToolCommitProjectFiles, `{"repositoryRef":"repo","message":"test"}`)
	if port.calls != 0 || !strings.Contains(result, "call tool_search first") {
		t.Fatalf("hidden commit result = %q, backend calls = %d", result, port.calls)
	}
}

func TestProjectEinoAssistantToolSearchUsesDurableLedgerAndReplay(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "poc-tool-search-ledger")
	var backendCalls int
	discovery := projectEinoAssistantToolDiscovery{MCPTools: projectEinoAssistantPOCTestDynamicTools(&backendCalls)}
	state := newProjectEinoAssistantRunState()
	state.SetAgentOptimizationMode(projectEinoAssistantOptimizationCodexPOC)
	state.SetToolDiscovery(discovery)
	node := projectEinoAssistantPOCTestToolsNode(t, h, state, discovery)

	blocked := projectEinoAssistantPOCInvokeTool(t, node, "call-hidden", "mcp_database_query", `{"sql":"select 1"}`)
	if backendCalls != 0 || !strings.Contains(blocked, "call tool_search first") {
		t.Fatalf("unselected call result = %q, backend calls = %d", blocked, backendCalls)
	}
	events, err := h.messages.ListAssistantRunEvents(context.Background(), h.scope, h.req.AssistantRun.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != projectAssistantRunToolRequestEventType || events[1].Type != projectAssistantRunToolResultEventType {
		t.Fatalf("deferred rejection events = %#v, want durable request/result", events)
	}

	searchResult := projectEinoAssistantPOCInvokeTool(t, node, "call-search", projectEinoAssistantToolSearchTool, `{"query":"database query"}`)
	if !strings.Contains(searchResult, "mcp_database_query") || !state.DynamicToolSelected("mcp_database_query") {
		t.Fatalf("search result = %q, selected = %v", searchResult, state.DynamicToolSelected("mcp_database_query"))
	}
	projectEinoAssistantPOCInvokeTool(t, node, "call-database", "mcp_database_query", `{"sql":"select 1"}`)
	projectEinoAssistantPOCInvokeTool(t, node, "call-database", "mcp_database_query", `{"sql":"select 1"}`)
	if backendCalls != 1 {
		t.Fatalf("dynamic backend calls = %d, want exactly one after replay", backendCalls)
	}

	checkpoint := state.CheckpointState()
	checkpoint.SelectedDynamicToolNames = nil // Simulate a checkpoint that lagged the durable search result.
	restarted := newProjectEinoAssistantRunState()
	restarted.RestoreCheckpointState(checkpoint)
	restarted.SetToolDiscovery(discovery)
	restartedReq := h.req
	restartedReq.eventLedger = newProjectAssistantRunEventLedger(h.messages, h.scope, h.req.AssistantRun.ID)
	restartedHarness := h
	restartedHarness.req = restartedReq
	restartedNode := projectEinoAssistantPOCTestToolsNode(t, restartedHarness, restarted, discovery)
	replayedSearch := projectEinoAssistantPOCInvokeTool(t, restartedNode, "call-search", projectEinoAssistantToolSearchTool, `{"query":"database query"}`)
	if replayedSearch != searchResult || !restarted.DynamicToolSelected("mcp_database_query") {
		t.Fatalf("replayed search = %q, selected = %v", replayedSearch, restarted.DynamicToolSelected("mcp_database_query"))
	}
}

func TestProjectEinoAssistantToolSearchMatchesProviderAlias(t *testing.T) {
	matches := projectEinoAssistantSearchDynamicTools([]projectAssistantTool{projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name: projectToolDatabricksListTables, Description: "List imported tables.", Risk: projectAssistantToolRiskRead,
		},
	}}, "databricks list tables", 5)
	if len(matches) != 1 || matches[0].Name != projectToolDatabricksListTables {
		t.Fatalf("provider alias matches = %#v, want list_tables", matches)
	}
}

func projectEinoAssistantPOCTestDynamicTools(calls *int) []projectAssistantTool {
	tool := func(name, description string) projectAssistantTool {
		return projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name: name, Description: description, Risk: projectAssistantToolRiskRead, ParallelSafe: true,
				Parameters: json.RawMessage(`{"type":"object","properties":{"sql":{"type":"string"},"resource":{"type":"string"}},"additionalProperties":false}`),
			},
			call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
				if calls != nil {
					*calls++
				}
				return `{"status":"succeeded"}`, nil
			},
		}
	}
	return []projectAssistantTool{
		tool("mcp_database_query", "Run a read-only database query and return matching application records."),
		tool("mcp_issue_create", "Create a tracked issue in the connected project-management provider."),
		tool("mcp_observability_search", "Search application traces and structured runtime logs in the observability provider."),
	}
}

func projectEinoAssistantPOCTestToolsNode(
	t *testing.T,
	h projectAssistantV2ToolHarness,
	state *projectEinoAssistantRunState,
	discovery projectEinoAssistantToolDiscovery,
) *compose.ToolsNode {
	t.Helper()
	baseTools, err := projectEinoAssistantToolsForDiscovery(context.Background(), h.server, h.req, state, discovery)
	if err != nil {
		t.Fatal(err)
	}
	tools := make([]einotool.BaseTool, 0, len(baseTools))
	for _, tool := range baseTools {
		info, infoErr := tool.Info(context.Background())
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info != nil && (info.Name == projectEinoAssistantToolSearchTool || info.Name == projectToolCommitProjectFiles || strings.HasPrefix(info.Name, "mcp_")) {
			tools = append(tools, tool)
		}
	}
	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func projectEinoAssistantPOCInvokeTool(t *testing.T, node *compose.ToolsNode, callID, name, arguments string) string {
	t.Helper()
	output, err := node.Invoke(context.Background(), schema.AssistantMessage("", []schema.ToolCall{{
		ID: callID, Function: schema.FunctionCall{Name: name, Arguments: arguments},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 1 || output[0] == nil {
		t.Fatalf("tool output = %#v, want one message", output)
	}
	return output[0].Content
}
